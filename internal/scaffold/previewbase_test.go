package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// previewbase.yaml.tmpl の CloudFront Function を **テンプレートから取り出して
// 実際に実行する**。ルーティングは文字列一致では確かめられない(壊れ方が
// 「/about が 403」という配信時の挙動でしか出ない)ので、node で動かす。
//
// #86: SPA のディープリンクが 403 になっていた。directory モードが
// /about → /about/index.html を探し、SPA では実体が無いため。
func TestPreviewBaseFunctionRouting(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node が無い")
	}
	src, err := tmplFS.ReadFile("templates/previewbase.yaml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	body := extractFunctionCode(t, string(src))

	cases := []struct {
		mode, routing, host, uri, want string
	}{
		// 共通: ルートと実ファイルはどちらのモードでも同じ
		{"per-app", "directory", "pr-42.app.example.com", "/", "/pr-42/index.html"},
		{"per-app", "spa", "pr-42.app.example.com", "/", "/pr-42/index.html"},
		{"per-app", "directory", "pr-42.app.example.com", "/assets/x.js", "/pr-42/assets/x.js"},
		{"per-app", "spa", "pr-42.app.example.com", "/assets/x.js", "/pr-42/assets/x.js"},
		{"per-app", "spa", "pr-42.app.example.com", "/config.json", "/pr-42/config.json"},

		// ここが #86。拡張子の無いパスの扱いだけが違う
		{"per-app", "directory", "pr-42.app.example.com", "/about", "/pr-42/about/index.html"},
		{"per-app", "spa", "pr-42.app.example.com", "/about", "/pr-42/index.html"},
		{"per-app", "directory", "pr-42.app.example.com", "/a/b/c", "/pr-42/a/b/c/index.html"},
		{"per-app", "spa", "pr-42.app.example.com", "/a/b/c", "/pr-42/index.html"},

		// 環境名は host の先頭ラベルから。別の環境のプレフィックスを読まない
		{"per-app", "spa", "pr-7.app.example.com", "/about", "/pr-7/index.html"},

		// shared: <project>--<name> を /<project>/<name>/ に畳む。
		// SPA の index.html も **その環境のプレフィックス配下**でなければ、
		// 別アプリの画面が出てしまう。
		{"shared", "directory", "todo--pr-42.example.com", "/", "/todo/pr-42/index.html"},
		{"shared", "spa", "todo--pr-42.example.com", "/about", "/todo/pr-42/index.html"},
		{"shared", "directory", "todo--pr-42.example.com", "/about", "/todo/pr-42/about/index.html"},
		{"shared", "spa", "todo--pr-42.example.com", "/assets/x.js", "/todo/pr-42/assets/x.js"},
		// 区切りが無いホストは畳まない(誤って上位を読まない)
		{"shared", "spa", "plain.example.com", "/about", "/plain/index.html"},
	}
	for _, c := range cases {
		got := runFunction(t, body, c.mode, c.routing, c.host, c.uri)
		if got != c.want {
			t.Errorf("mode=%s routing=%s %s%s → %s, want %s", c.mode, c.routing, c.host, c.uri, got, c.want)
		}
	}
}

// extractFunctionCode は FunctionCode: !Sub | ブロックの中身を取り出す。
func extractFunctionCode(t *testing.T, tmpl string) string {
	t.Helper()
	i := strings.Index(tmpl, "FunctionCode: !Sub |")
	if i < 0 {
		t.Fatal("FunctionCode: !Sub | が見つからない(テンプレートの形が変わった)")
	}
	var out []string
	for _, line := range strings.Split(tmpl[i:], "\n")[1:] {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		// ブロックのインデント(8 スペース)を外れたら終わり
		if !strings.HasPrefix(line, "        ") {
			break
		}
		out = append(out, strings.TrimPrefix(line, "        "))
	}
	body := strings.Join(out, "\n")
	if !strings.Contains(body, "function handler") {
		t.Fatalf("handler を取り出せていない:\n%s", body)
	}
	return body
}

var subVar = regexp.MustCompile(`\$\{(\w+)\}`)

func runFunction(t *testing.T, body, mode, routing, host, uri string) string {
	t.Helper()
	// CFN の !Sub と同じ置換をする(${Mode} / ${Routing} → 値)。
	code := subVar.ReplaceAllStringFunc(body, func(m string) string {
		switch subVar.FindStringSubmatch(m)[1] {
		case "Routing":
			return routing
		case "Mode":
			return mode
		}
		t.Fatalf("未知の !Sub 変数: %s", m)
		return m
	})
	script := code + `
const e = {request: {headers: {host: {value: ` + jsString(host) + `}}, uri: ` + jsString(uri) + `}};
console.log(handler(e).uri);
`
	f := filepath.Join(t.TempDir(), "fn.js")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func jsString(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func TestRoutingFor(t *testing.T) {
	// クライアントルーターを持つものだけ spa。
	for _, f := range []string{"react-router", "remix-run", "vite"} {
		if got := RoutingFor(f); got != "spa" {
			t.Errorf("RoutingFor(%q) = %q, want spa", f, got)
		}
	}
	// 静的サイト生成器は /about/index.html を出すので directory が正しい。
	// ここを spa にすると、今度は個別ページが出せなくなる(逆向きの壊れ方)。
	for _, f := range []string{"next", "astro", "nuxt", "sveltejs", "go", ""} {
		if got := RoutingFor(f); got != "directory" {
			t.Errorf("RoutingFor(%q) = %q, want directory(判別できないものは現状維持へ倒す)", f, got)
		}
	}
}

// 生成される kagerou.yaml と、base に渡す値がずれないこと。
// ずれると「設定には spa と書いてあるのにベースは directory」になり、
// 症状(403)から設定を見ても原因に辿り着けない。
func TestGeneratedConfigMatchesBaseParameter(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "demo", Region: "ap-northeast-1", Domain: "demo.example.com",
		SetupBase: true, Routing: "spa"}
	if _, err := Run(dir, p, Targets{KagerouYaml: true}, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "routing: spa") {
		t.Errorf("kagerou.yaml に routing: spa が無い:\n%s", cfg)
	}
	if _, err := WriteSetupScript(dir, p, Detection{Owner: "acme", Repo: "demo"}); err != nil {
		t.Fatal(err)
	}
	sh, err := os.ReadFile(filepath.Join(dir, SetupScriptName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sh), `Routing="spa"`) {
		t.Errorf("setup スクリプトが base に spa を渡していない")
	}
}

// directory(既定)のときは static セクションを出さない。既定値を書き下すと
// 「変えられる設定」に見えて、ベース再デプロイが要ることが伝わらない。
func TestGeneratedConfigOmitsDefaultRouting(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "demo", Region: "ap-northeast-1", Domain: "demo.example.com"}
	if _, err := Run(dir, p, Targets{KagerouYaml: true}, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if strings.Contains(string(cfg), "routing:") {
		t.Errorf("既定なのに routing が書かれている:\n%s", cfg)
	}
}
