package appscan

// プレビューに載せる前に**アプリ側**で直す必要がある箇所を見つける(#195)。
//
// kagerou が作る層(テンプレート / workflow / ベース)は生成できるが、
// 時間を食うのはたいていアプリ側の /tmp 化・起動時の secrets 取得・cookie の
// 向き先といった作業で、これらは kagerou の非スコープ(接続情報を env で配るだけ)。
// 非スコープなのは設計として正しいが、**踏むまで分からない**と移行の見積もりが
// 立たない。直すのは利用者、言うのは diagnose、という分担にする。
//
// 方針は「**偽陽性を出すくらいなら黙る**」。この種の検出は正規表現で当てずっぽうを
// やりやすく(#154 の実例)、疑わしい指摘が並ぶと全部読まれなくなる。なので:
//
//   - Go は go/ast で構文を見る(標準ライブラリなので appscan の境界は崩れない)
//   - 他の言語は**リテラルが明確なものだけ**を narrow なパターンで見る
//   - 対応していない言語では何も言わない
//   - テスト・ベンダ・生成物は最初から見ない

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AppWorkKind は「アプリ側に残る作業」の種類。
type AppWorkKind string

const (
	// WorkWritableFS は /tmp 以外への書き込み。Lambda の rootfs は read-only。
	// compute: ecs なら問題にならないので、重みは構成で変わる。
	WorkWritableFS AppWorkKind = "writable-fs"
	// WorkSecretsAtInit は起動時(パッケージ初期化)の secrets 取得。
	// コールドスタートのたびに API を呼ぶ。
	WorkSecretsAtInit AppWorkKind = "secrets-at-init"
	// WorkFixedHost は cookie / リダイレクト先の固定ホスト。
	// 環境ごとにホストが変わるので認証の戻り先が壊れる。
	WorkFixedHost AppWorkKind = "fixed-host"
	// WorkHardcodedResource はバケット名 / ARN の直書き。環境ごとに差し替わらず、
	// **共有物を踏む**。
	WorkHardcodedResource AppWorkKind = "hardcoded-resource"
)

// AppWork は 1 件の指摘。どこで何を見つけたかだけを持ち、直し方は diagnose が出す。
type AppWork struct {
	Kind AppWorkKind
	File string // リポジトリルートからの相対
	Line int
	// Snippet は見つけた根拠(リテラルなど)。長いものは切り詰める。
	Snippet string
}

// ScanAppWork はアプリ側に残る作業を探す。見つからなければ空を返す。
//
// **確信が持てるものだけ**を返す。曖昧なものは出さない。
func ScanAppWork(dir string) []AppWork {
	var out []AppWork
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		switch {
		case strings.HasSuffix(name, "_test.go"):
			return nil
		case strings.HasSuffix(name, ".go"):
			out = append(out, scanGo(path, filepath.ToSlash(rel))...)
		case strings.HasSuffix(name, ".js"), strings.HasSuffix(name, ".mjs"),
			strings.HasSuffix(name, ".ts"), strings.HasSuffix(name, ".tsx"):
			if strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") {
				return nil
			}
			out = append(out, scanJS(path, filepath.ToSlash(rel))...)
		}
		return nil
	})
	return out
}

// --- Go(go/ast で見る) ---

// goWriters は書き込みを起こす標準ライブラリの呼び出し。第 1 引数がパス。
var goWriters = map[string]bool{
	"os.Create": true, "os.WriteFile": true, "os.MkdirAll": true, "os.Mkdir": true,
	"os.OpenFile": true, "ioutil.WriteFile": true,
}

// goSecretGetters は secrets を取りに行く呼び出し(SDK v1 / v2 共通の動詞)。
var goSecretGetters = []string{"GetSecretValue", "GetParameter", "GetParameters", "GetParametersByPath"}

func scanGo(path, rel string) []AppWork {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil // 読めないものは黙る(生成途中・方言など)
	}
	var out []AppWork
	line := func(p token.Pos) int { return fset.Position(p).Line }

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if goWriters[name] && len(call.Args) > 0 {
			if lit, ok := stringLit(call.Args[0]); ok && !underTmp(lit) {
				out = append(out, AppWork{WorkWritableFS, rel, line(call.Pos()), name + "(" + lit + ")"})
			}
		}
		return true
	})

	// 起動時の secrets 取得。パッケージ初期化で走る範囲だけを見る
	// (func init と、パッケージ変数の初期化式)。ハンドラ内は正常なので見ない。
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.Name == "init" && d.Recv == nil {
				out = append(out, secretCalls(d.Body, rel, line)...)
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, v := range vs.Values {
					out = append(out, secretCalls(v, rel, line)...)
				}
			}
		}
	}

	out = append(out, scanLiterals(string(src), rel)...)
	return out
}

// secretCalls は「パッケージ初期化で実際に走る」呼び出しだけを拾う。
//
// **関数リテラルの中には入らない。** `var get = func(...) { ...GetParameter... }` は
// 定義しているだけで、起動時には走らない。ここを見落として kagerou 自身の
// basedomain.go / capacity.go を誤検出した(実測で判明)。
func secretCalls(n ast.Node, rel string, line func(token.Pos) int) []AppWork {
	var out []AppWork
	ast.Inspect(n, func(x ast.Node) bool {
		if _, isLit := x.(*ast.FuncLit); isLit {
			return false
		}
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		for _, g := range goSecretGetters {
			if strings.HasSuffix(name, "."+g) || name == g {
				out = append(out, AppWork{WorkSecretsAtInit, rel, line(call.Pos()), name})
			}
		}
		return true
	})
	return out
}

// calleeName は pkg.Fn / recv.Fn の形を文字列にする。分からなければ空。
func calleeName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return "." + f.Sel.Name
	}
	return ""
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// underTmp は書き込み先が一時領域か。相対パスは判断できないので「一時扱い」に倒す
// (確信が持てないものは出さない、の方針)。
func underTmp(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return true
	}
	return strings.HasPrefix(p, "/tmp/") || p == "/tmp" || strings.HasPrefix(p, "/var/tmp/")
}

// --- JavaScript / TypeScript(リテラルが明確なものだけ) ---

var (
	jsWriteRe  = regexp.MustCompile(`fs\.(?:promises\.)?(?:writeFile|writeFileSync|mkdir|mkdirSync|appendFile|appendFileSync)\(\s*['"](/[^'"]*)['"]`)
	cookieRe   = regexp.MustCompile(`(?i)domain\s*[:=]\s*['"]([A-Za-z0-9.-]+\.[A-Za-z]{2,})['"]`)
	redirectRe = regexp.MustCompile(`(?i)redirect_?uri\s*[:=]\s*['"](https?://[^'"]+)['"]`)
)

func scanJS(path, rel string) []AppWork {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []AppWork
	for i, l := range strings.Split(string(src), "\n") {
		if m := jsWriteRe.FindStringSubmatch(l); m != nil && !underTmp(m[1]) {
			out = append(out, AppWork{WorkWritableFS, rel, i + 1, trim(m[0])})
		}
	}
	out = append(out, scanLiterals(string(src), rel)...)
	return out
}

// --- 言語によらないリテラル ---

var (
	arnRe    = regexp.MustCompile(`arn:aws:[a-z0-9-]+:[a-z0-9-]*:\d{12}:[^\s'"` + "`" + `]+`)
	bucketRe = regexp.MustCompile(`s3://[a-z0-9][a-z0-9.-]{2,62}`)
)

// scanLiterals は言語を問わず「環境ごとに差し替わらない値」を探す。
//
// **アカウント ID を含む ARN** と **s3:// の実バケット**に限る。エンドポイントの
// ホスト名まで拾うと、ドキュメントの例やコメントを大量に拾って読まれなくなる。
func scanLiterals(src, rel string) []AppWork {
	var out []AppWork
	for i, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "*") {
			continue // コメントの例示は指摘しない
		}
		if m := arnRe.FindString(l); m != "" {
			out = append(out, AppWork{WorkHardcodedResource, rel, i + 1, trim(m)})
			continue
		}
		// "s3://kagerou-base-" + project のような**連結の途中**はバケット名ではない。
		// 末尾が区切り文字なら、続きが変数で埋まる形とみなして黙る(実測で判明)
		if m := bucketRe.FindString(l); m != "" && !strings.HasSuffix(m, "-") && !strings.HasSuffix(m, ".") {
			out = append(out, AppWork{WorkHardcodedResource, rel, i + 1, trim(m)})
			continue
		}
		if m := redirectRe.FindStringSubmatch(l); m != nil && !strings.Contains(m[1], "localhost") {
			out = append(out, AppWork{WorkFixedHost, rel, i + 1, trim(m[0])})
			continue
		}
		// domain: "x.y" は設定のフィールド既定値でも普通に出る(sashiki の
		// Domain: "sashiki.internal" を誤検出した)。cookie の文脈でだけ見る
		if !strings.Contains(strings.ToLower(l), "cookie") {
			continue
		}
		if m := cookieRe.FindStringSubmatch(l); m != nil && !strings.Contains(m[1], "localhost") {
			out = append(out, AppWork{WorkFixedHost, rel, i + 1, trim(m[0])})
		}
	}
	return out
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
