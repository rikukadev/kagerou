// Package appscan は既存アプリのリポジトリを読み、「このアプリは何か」を
// 事実(Facts)として返す。
//
// 境界の約束(独立コンポーネントとして切り出せる形を保つ):
//   - リポジトリの **ファイルしか読まない**。exec も AWS も環境変数も見ない
//   - kagerou の他パッケージに依存しない(標準ライブラリのみ)
//   - ベストエフォート: 読めない/無いものは零値のまま。エラーで止まらない
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
	Owner, Repo   string // .git/config の remote origin
	Region        string // samconfig.toml(ファイル由来のみ)
	Framework     string // next / remix / react-router / astro / nuxt / sveltejs / node / go
	DBDriver      string // mysql2 / pg / go-sql-driver/mysql など(空 = DB 依存なし)
	HasDockerfile bool
	HasLWA        bool   // Dockerfile に Lambda Web Adapter が入っているか
	AppPort       string // Dockerfile の EXPOSE / compose の ports から検出した listen ポート
	HasTemplate   bool   // template.yaml があるか
	Wants         Wants  // 依存から推定した「アプリが使うもの」
}

// Wants は依存関係(package.json / go.mod / compose)から推定した、アプリが
// 使う周辺リソース。preview 環境の形の推論に使う: DynamoDB / SQS / S3 は
// per-env に置いてもアイドル $0 なのでテンプレートに同梱でき、Redis /
// OpenSearch は常時課金なので共有ベース側に置く(利用側が判断する材料)。
type Wants struct {
	DynamoDB   bool
	SQS        bool
	S3         bool
	Redis      bool
	OpenSearch bool
}

// Any はどれか 1 つでも検出されたか。
func (w Wants) Any() bool { return w.DynamoDB || w.SQS || w.S3 || w.Redis || w.OpenSearch }

// Scan は dir を走査して Facts を返す。
func Scan(dir string) Facts {
	f := Facts{}
	f.Owner, f.Repo = gitRemote(filepath.Join(dir, ".git", "config"))
	f.Region = samconfigRegion(dir)
	scanDeps(dir, &f)
	f.HasDockerfile = exists(filepath.Join(dir, "Dockerfile"))
	if f.HasDockerfile {
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
	f.HasTemplate = exists(filepath.Join(dir, "template.yaml"))
	return f
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
		"service/sqs":      func(w *Wants) { w.SQS = true },
		"service/s3":       func(w *Wants) { w.S3 = true },
		"go-redis":         func(w *Wants) { w.Redis = true },
		"gomodule/redigo":  func(w *Wants) { w.Redis = true },
		"opensearch-go":    func(w *Wants) { w.OpenSearch = true },
		"go-elasticsearch": func(w *Wants) { w.OpenSearch = true },
	}
)

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// scanDeps は package.json / go.mod / compose から Framework / DBDriver / Wants を埋める。
func scanDeps(dir string, f *Facts) {
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
				f.Framework = strings.TrimPrefix(strings.Split(fw, "/")[0], "@")
				break
			}
		}
		if f.Framework == "" && len(deps) > 0 {
			f.Framework = "node"
		}
		for _, db := range nodeDBs {
			if deps[db] {
				f.DBDriver = db
				break
			}
		}
		for name, mark := range nodeWants {
			if deps[name] {
				mark(&f.Wants)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		if f.Framework == "" {
			f.Framework = "go"
		}
		s := string(b)
		for _, db := range goDBs {
			if strings.Contains(s, db) {
				f.DBDriver = db
				break
			}
		}
		for frag, mark := range goWants {
			if strings.Contains(s, frag) {
				mark(&f.Wants)
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
		if f.DBDriver == "" {
			if strings.Contains(s, "image: mysql") || strings.Contains(s, "image: mariadb") {
				f.DBDriver = "mysql (compose)"
			} else if strings.Contains(s, "image: postgres") {
				f.DBDriver = "postgres (compose)"
			}
		}
		if strings.Contains(s, "image: redis") || strings.Contains(s, "image: valkey") {
			f.Wants.Redis = true
		}
		if strings.Contains(s, "opensearchproject/opensearch") || strings.Contains(s, "image: elasticsearch") ||
			strings.Contains(s, "docker.elastic.co") {
			f.Wants.OpenSearch = true
		}
		break // 最初に見つかった compose だけ見る(ポート検出と同じ流儀)
	}
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
