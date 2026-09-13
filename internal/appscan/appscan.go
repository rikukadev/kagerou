// Package appscan は既存アプリのリポジトリを読み、「このアプリは何か」を
// 事実(Facts)として返す。
//
// 境界の約束(独立コンポーネントとして切り出せる形を保つ):
//   - リポジトリの **ファイルしか読まない**。exec も AWS も環境変数も見ない
//   - kagerou の他パッケージに依存しない(標準ライブラリのみ)
//   - ベストエフォート: 読めない/無いものは零値のまま。エラーで止まらない
//
// モノレポ対応: マーカー(package.json / go.mod / Dockerfile / compose)は
// ルートに無いことがある(例: api/ と web/ に分かれた 3 層構成)。走査は
// 再帰で、見つかった事実を 1 つの Facts にマージする(規則は merge 参照)。
//
// 環境側の検出(AWS アカウント・Route53・gh Variables 等)は scaffold.Detect が
// この Facts に重ねる。
package appscan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Facts はリポジトリから読み取れた事実。
type Facts struct {
	Owner, Repo   string // .git/config の remote origin(ルートのみ)
	Region        string // samconfig.toml(ルートのみ・ファイル由来)
	Framework     string // next / remix / react-router / astro / nuxt / sveltejs / node / go
	DBDriver      string // mysql2 / pg / go-sql-driver/mysql など(空 = DB 依存なし)
	HasDockerfile bool
	HasLWA        bool   // その Dockerfile に Lambda Web Adapter が入っているか
	DockerfileDir string // Dockerfile のあるディレクトリ(ルートからの相対。ルート直下なら "")
	AppPort       string // Dockerfile の EXPOSE / compose の ports から検出した listen ポート
	HasTemplate   bool   // ルートに template.yaml があるか
	Services      int    // サービス数(compose services / cmd/*/main.go / Dockerfile の最大)
	Realtime      bool   // WebSocket / SSE の痕跡(30 秒上限のある入口を避ける根拠)
	Wants         Wants  // 依存から推定した「アプリが使うもの」(全ディレクトリの OR)
}

// Wants は依存関係(package.json / go.mod / compose)から推定した、アプリが
// 使う周辺リソース。preview 環境の形の推論に使う: DynamoDB / SNS / SQS / S3 は
// per-env に置いてもアイドル $0 なのでテンプレートに同梱でき、Redis /
// OpenSearch は常時課金なので共有ベース側に置く(利用側が判断する材料)。
type Wants struct {
	DynamoDB   bool
	SNS        bool
	SQS        bool
	S3         bool
	Redis      bool
	OpenSearch bool
}

// Any はどれか 1 つでも検出されたか。
func (w Wants) Any() bool {
	return w.DynamoDB || w.SNS || w.SQS || w.S3 || w.Redis || w.OpenSearch
}

func (w Wants) or(o Wants) Wants {
	return Wants{
		DynamoDB:   w.DynamoDB || o.DynamoDB,
		SNS:        w.SNS || o.SNS,
		SQS:        w.SQS || o.SQS,
		S3:         w.S3 || o.S3,
		Redis:      w.Redis || o.Redis,
		OpenSearch: w.OpenSearch || o.OpenSearch,
	}
}

// 再帰走査の安全弁。preview 対象のアプリで踏み抜くことはまず無い値にしてある。
const (
	maxDepth = 4   // ルートを 0 としてこの深さまで
	maxDirs  = 512 // 訪問ディレクトリ総数の上限
)

// skipDirs は中を見ないディレクトリ。依存の実体や生成物は「このアプリの事実」ではない。
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "out": true,
	"coverage": true, "testdata": true, "tmp": true, "target": true,
}

// Scan は dir を再帰的に走査して Facts を返す。
func Scan(dir string) Facts {
	f := Facts{}
	f.Owner, f.Repo = gitRemote(filepath.Join(dir, ".git", "config"))
	f.Region = samconfigRegion(dir)
	f.HasTemplate = exists(filepath.Join(dir, "template.yaml"))
	visited := 0
	walk(dir, "", 0, &f, &visited)
	return f
}

// walk はルート優先の深さ優先でディレクトリを回り、事実をマージしていく。
func walk(root, rel string, depth int, f *Facts, visited *int) {
	scanDir(filepath.Join(root, rel), rel, f)
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || skipDirs[name] || strings.HasPrefix(name, ".") {
			continue
		}
		if *visited >= maxDirs {
			return
		}
		*visited++
		walk(root, filepath.Join(rel, name), depth+1, f, visited)
	}
}

// frameworkRank はマージ時の優先度。具体的なフレームワーク > go > 素の node。
// モノレポ(web=素の React SPA + api=Go)ではコンテナ化対象の Go が勝つ。
func frameworkRank(fw string) int {
	switch fw {
	case "":
		return 0
	case "node":
		return 1
	case "go":
		return 2
	default: // next / remix-run / react-router / astro / nuxt / sveltejs
		return 3
	}
}

// scanDir は 1 ディレクトリの事実を f にマージする。
// - Framework: frameworkRank が高い方が勝つ(同点は先着 = ルート優先)
// - DBDriver / AppPort: 先着(ルート優先)
// - Wants: OR
// - Dockerfile: 先着。場所を DockerfileDir に覚える(LWA 注入先になる)
func scanDir(dir, rel string, f *Facts) {
	fw, db, wants := depsOf(dir)
	if frameworkRank(fw) > frameworkRank(f.Framework) {
		f.Framework = fw
	}
	if f.DBDriver == "" {
		f.DBDriver = db
	}
	f.Wants = f.Wants.or(wants)

	if !f.HasDockerfile && exists(filepath.Join(dir, "Dockerfile")) {
		f.HasDockerfile = true
		f.DockerfileDir = rel
		if b, err := os.ReadFile(filepath.Join(dir, "Dockerfile")); err == nil {
			f.HasLWA = strings.Contains(string(b), "lambda-adapter")
			if m := exposeRe.FindAllStringSubmatch(string(b), -1); len(m) > 0 {
				f.AppPort = m[len(m)-1][1] // マルチステージなら最後の EXPOSE
			}
		}
	}
	if f.AppPort == "" {
		f.AppPort = composePort(dir)
	}
	if n := serviceCount(dir); n > f.Services {
		f.Services = n
	}
	if !f.Realtime {
		f.Realtime = realtimeUsed(dir)
	}
}

// serviceCount は「このディレクトリにいくつサービスがあるか」を数える。
// compose の services、cmd/*/main.go(Go の複数バイナリ)、Dockerfile の数の最大を取る。
// 正確な数より「1 つか、複数か」が判定に効く。
func serviceCount(dir string) int {
	n := composeServiceCount(dir)
	if m := cmdMainCount(dir); m > n {
		n = m
	}
	if n == 0 && exists(filepath.Join(dir, "Dockerfile")) {
		n = 1
	}
	return n
}

var composeServiceEntryRe = regexp.MustCompile(`(?m)^  ([a-zA-Z0-9_.-]+):\s*$`)

func composeServiceCount(dir string) int {
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.Index(s, "\nservices:")
		if i < 0 && !strings.HasPrefix(s, "services:") {
			continue
		}
		if i < 0 {
			i = 0
		}
		rest := s[i:]
		// 次のトップレベルキー(volumes: など)まで
		if j := regexp.MustCompile(`(?m)^[a-zA-Z]`).FindStringIndex(rest[1:]); j != nil {
			if k := regexp.MustCompile(`(?m)^(volumes|networks|configs|secrets):`).FindStringIndex(rest); k != nil && k[0] > 0 {
				rest = rest[:k[0]]
			}
		}
		if m := composeServiceEntryRe.FindAllString(rest, -1); len(m) > 0 {
			return len(m)
		}
	}
	return 0
}

// cmdMainCount は Go の「cmd/<name>/main.go」の数(複数バイナリ = 複数サービス)。
func cmdMainCount(dir string) int {
	entries, err := os.ReadDir(filepath.Join(dir, "cmd"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && exists(filepath.Join(dir, "cmd", e.Name(), "main.go")) {
			n++
		}
	}
	return n
}

// realtimeUsed は WebSocket / SSE の痕跡を探す。これがあると、オリジン応答に
// 上限のある入口(API Gateway の 30 秒、CloudFront の ~60 秒)では動かない。
func realtimeUsed(dir string) bool {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		s := string(b)
		for _, dep := range []string{"socket.io", "ws\"", "@fastify/websocket", "sockjs"} {
			if strings.Contains(s, dep) {
				return true
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		s := string(b)
		for _, dep := range []string{"gorilla/websocket", "nhooyr.io/websocket", "coder/websocket", "olahol/melody"} {
			if strings.Contains(s, dep) {
				return true
			}
		}
	}
	return false
}

var remoteURLRe = regexp.MustCompile(`(?:github\.com[:/])([^/\s]+)/([^/\s]+?)(?:\.git)?\s*$`)

// gitRemote は .git/config の [remote "origin"] url を読む(exec なしで済ませる)。
func gitRemote(gitConfig string) (owner, repo string) {
	b, err := os.ReadFile(gitConfig)
	if err != nil {
		return "", ""
	}
	inOrigin := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inOrigin = t == `[remote "origin"]`
			continue
		}
		if inOrigin && strings.HasPrefix(t, "url") {
			if m := remoteURLRe.FindStringSubmatch(t); m != nil {
				return m[1], m[2]
			}
		}
	}
	return "", ""
}

var samconfigRegionRe = regexp.MustCompile(`(?m)^\s*region\s*=\s*"([^"]+)"`)

func samconfigRegion(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "samconfig.toml")); err == nil {
		if m := samconfigRegionRe.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// 依存名 → 何を使うか。フレームワーク・DB と違い「あるだけ全部」拾う。
var (
	nodeFrameworks = []string{"next", "@remix-run/node", "react-router", "astro", "nuxt", "@sveltejs/kit"}
	nodeDBs        = []string{"mysql2", "mysql", "pg", "postgres", "@prisma/client", "drizzle-orm"}
	goDBs          = []string{"go-sql-driver/mysql", "jackc/pgx", "lib/pq"}

	nodeWants = map[string]func(*Wants){
		"@aws-sdk/client-dynamodb":       func(w *Wants) { w.DynamoDB = true },
		"dynamoose":                      func(w *Wants) { w.DynamoDB = true },
		"@aws-sdk/client-sns":            func(w *Wants) { w.SNS = true },
		"@aws-sdk/client-sqs":            func(w *Wants) { w.SQS = true },
		"sqs-consumer":                   func(w *Wants) { w.SQS = true },
		"@aws-sdk/client-s3":             func(w *Wants) { w.S3 = true },
		"redis":                          func(w *Wants) { w.Redis = true },
		"ioredis":                        func(w *Wants) { w.Redis = true },
		"bullmq":                         func(w *Wants) { w.Redis = true },
		"@opensearch-project/opensearch": func(w *Wants) { w.OpenSearch = true },
		"@elastic/elasticsearch":         func(w *Wants) { w.OpenSearch = true },
	}
	goWants = map[string]func(*Wants){
		"service/dynamodb": func(w *Wants) { w.DynamoDB = true },
		"service/sns":      func(w *Wants) { w.SNS = true },
		"service/sqs":      func(w *Wants) { w.SQS = true },
		"service/s3":       func(w *Wants) { w.S3 = true },
		"go-redis":         func(w *Wants) { w.Redis = true },
		"gomodule/redigo":  func(w *Wants) { w.Redis = true },
		"opensearch-go":    func(w *Wants) { w.OpenSearch = true },
		"go-elasticsearch": func(w *Wants) { w.OpenSearch = true },
	}
)

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// depsOf は 1 ディレクトリの package.json / go.mod / compose から
// Framework / DBDriver / Wants を読む。
func depsOf(dir string) (framework, dbDriver string, wants Wants) {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		deps := map[string]bool{}
		if json.Unmarshal(b, &pkg) == nil {
			for k := range pkg.Dependencies {
				deps[k] = true
			}
			for k := range pkg.DevDependencies {
				deps[k] = true
			}
		}
		for _, fw := range nodeFrameworks {
			if deps[fw] {
				framework = strings.TrimPrefix(strings.Split(fw, "/")[0], "@")
				break
			}
		}
		if framework == "" && len(deps) > 0 {
			framework = "node"
		}
		for _, db := range nodeDBs {
			if deps[db] {
				dbDriver = db
				break
			}
		}
		for name, mark := range nodeWants {
			if deps[name] {
				mark(&wants)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		if frameworkRank("go") > frameworkRank(framework) {
			framework = "go"
		}
		s := string(b)
		for _, db := range goDBs {
			if strings.Contains(s, db) {
				if dbDriver == "" {
					dbDriver = db
				}
				break
			}
		}
		for frag, mark := range goWants {
			if strings.Contains(s, frag) {
				mark(&wants)
			}
		}
	}
	// compose のサービスは傍証: DB 種別と、redis / opensearch の利用
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		if dbDriver == "" {
			if strings.Contains(s, "image: mysql") || strings.Contains(s, "image: mariadb") {
				dbDriver = "mysql (compose)"
			} else if strings.Contains(s, "image: postgres") {
				dbDriver = "postgres (compose)"
			}
		}
		if strings.Contains(s, "image: redis") || strings.Contains(s, "image: valkey") {
			wants.Redis = true
		}
		if strings.Contains(s, "opensearchproject/opensearch") || strings.Contains(s, "image: elasticsearch") ||
			strings.Contains(s, "docker.elastic.co") {
			wants.OpenSearch = true
		}
		break // 最初に見つかった compose だけ見る(ポート検出と同じ流儀)
	}
	return framework, dbDriver, wants
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var (
	exposeRe      = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)
	composePortRe = regexp.MustCompile(`(?m)^\s*-\s*"?(?:\d+:)?(\d+)"?\s*$`)
)

// composePort は compose の ports("8080:3000" の右側 = コンテナ側)を拾う。
func composePort(dir string) string {
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.Index(s, "ports:")
		if i < 0 {
			continue
		}
		if m := composePortRe.FindStringSubmatch(s[i:]); m != nil {
			return m[1]
		}
	}
	return ""
}
