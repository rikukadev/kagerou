package scaffold

// リポジトリと環境を読んで init の入力を半自動で埋める。
// すべてベストエフォート: 検出に失敗した項目は空のままで、従来の
// プレースホルダ表示に落ちるだけ(init が失敗する理由にはしない)。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Detection struct {
	Owner, Repo   string // git remote origin から
	Region        string // samconfig.toml / AWS_REGION から
	AccountID     string // aws sts get-caller-identity から
	Framework     string // next / remix / react-router / astro / go など
	DBDriver      string // mysql2 / pg / go-sql-driver/mysql など(空 = DB 依存なし)
	HasDockerfile bool
	HasLWA        bool   // Dockerfile に Lambda Web Adapter が入っているか
	AppPort       string // Dockerfile の EXPOSE / compose の ports から検出した listen ポート
	HasTemplate   bool
	VarsSet       map[string]bool     // 設定済みの GitHub Variables
	Zones         []string            // Route53 の公開ホストゾーン(ドメイン選択肢)
	Bases         map[string]BaseInfo // 既存 preview base の Exports。key = project(旧アカウント単位 base は key "")
}

// BaseInfo は既存 preview base の Exports(kagerou-preview-base:<project>:*)。
type BaseInfo struct {
	Domain string
	Bucket string
}

// applyBaseKV は base 検出の 1 キーを Bases に畳み込む。
func applyBaseKV(bases map[string]BaseInfo, project, field, value string) {
	b := bases[project]
	switch field {
	case "domain":
		b.Domain = value
	case "bucket":
		b.Bucket = value
	}
	bases[project] = b
}

// Base は project の preview base を返す。project 用が無ければ、旧アカウント
// 単位 base(Export に project セグメントが無い頃のもの)に fallback する。
func (d Detection) Base(project string) (BaseInfo, bool) {
	if b, ok := d.Bases[project]; ok {
		return b, true
	}
	b, ok := d.Bases[""]
	return b, ok
}

// SuggestSashiki は DB 依存が見えたときに sashiki 構成を提案する。
func (d Detection) SuggestSashiki() bool {
	return d.DBDriver != "" && strings.Contains(d.DBDriver, "mysql")
}

// execCommand はテストで差し替えるためのフック。
var execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Detect は dir を走査する。外部コマンド(aws / gh)は 1 つ 5 秒で諦める。
func Detect(dir string) Detection {
	d := Detection{VarsSet: map[string]bool{}}

	d.Owner, d.Repo = detectRemote(filepath.Join(dir, ".git", "config"))
	d.Region = detectRegion(dir)
	d.Framework, d.DBDriver = detectStack(dir)
	d.HasDockerfile = exists(filepath.Join(dir, "Dockerfile"))
	if d.HasDockerfile {
		if b, err := os.ReadFile(filepath.Join(dir, "Dockerfile")); err == nil {
			d.HasLWA = strings.Contains(string(b), "lambda-adapter")
			if m := exposeRe.FindAllStringSubmatch(string(b), -1); len(m) > 0 {
				d.AppPort = m[len(m)-1][1] // マルチステージなら最後の EXPOSE
			}
		}
	}
	if d.AppPort == "" {
		d.AppPort = detectComposePort(dir)
	}
	d.HasTemplate = exists(filepath.Join(dir, "template.yaml"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := execCommand(ctx, "aws", "sts", "get-caller-identity", "--query", "Account", "--output", "text"); err == nil {
		if s := strings.TrimSpace(string(out)); regexp.MustCompile(`^\d{12}$`).MatchString(s) {
			d.AccountID = s
		}
	}
	// samconfig / 環境変数に無ければ ~/.aws/config(プロファイル解決込み)から。
	// `aws configure get region` は env を見ないので、この順で env が勝つ
	if d.Region == "" {
		if out, err := execCommand(ctx, "aws", "configure", "get", "region"); err == nil {
			if r := strings.TrimSpace(string(out)); r != "" {
				d.Region = r
			}
		}
	}
	if out, err := execCommand(ctx, "aws", "route53", "list-hosted-zones",
		"--query", "HostedZones[?Config.PrivateZone==`false`].Name", "--output", "json"); err == nil {
		var zones []string
		if json.Unmarshal(out, &zones) == nil {
			for _, z := range zones {
				d.Zones = append(d.Zones, strings.TrimSuffix(z, "."))
			}
		}
	}
	// preview base(us-east-1 固定)の検出。真実の源は SSM のデータ契約
	// /kagerou/base/<project>/{domain,bucket,...}(CONTRACT §9)。ベースの IaC が
	// CFN でも Terraform でも同じキーを書けばよい(ツール非依存、#75)。
	// SSM を書かない頃の CFN Exports(kagerou-preview-base:*)には fallback する。
	d.Bases = map[string]BaseInfo{}
	if out, err := execCommand(ctx, "aws", "cloudformation", "list-exports", "--region", "us-east-1",
		"--query", "Exports[?starts_with(Name, `kagerou-preview-base`)].[Name,Value]", "--output", "json"); err == nil {
		var kv [][]string
		if json.Unmarshal(out, &kv) == nil {
			for _, e := range kv {
				if len(e) != 2 {
					continue
				}
				parts := strings.Split(e[0], ":")
				switch len(parts) {
				case 2: // 旧: kagerou-preview-base:domain(アカウント単位、key "")
					applyBaseKV(d.Bases, "", parts[1], e[1])
				case 3: // per-app: kagerou-preview-base:todo:domain
					applyBaseKV(d.Bases, parts[1], parts[2], e[1])
				}
			}
		}
	}
	// SSM は Exports より後に読む = 同じ project では SSM が勝つ(契約が真実の源)
	if out, err := execCommand(ctx, "aws", "ssm", "get-parameters-by-path", "--path", "/kagerou/base",
		"--recursive", "--region", "us-east-1",
		"--query", "Parameters[].[Name,Value]", "--output", "json"); err == nil {
		var kv [][]string
		if json.Unmarshal(out, &kv) == nil {
			for _, e := range kv {
				if len(e) != 2 {
					continue
				}
				// /kagerou/base/<project>/<field>
				parts := strings.Split(strings.TrimPrefix(e[0], "/"), "/")
				if len(parts) != 4 || parts[0] != "kagerou" || parts[1] != "base" {
					continue
				}
				applyBaseKV(d.Bases, parts[2], parts[3], e[1])
			}
		}
	}
	if out, err := execCommand(ctx, "gh", "variable", "list", "--json", "name"); err == nil {
		var vars []struct{ Name string }
		if json.Unmarshal(out, &vars) == nil {
			for _, v := range vars {
				d.VarsSet[v.Name] = true
			}
		}
	}
	return d
}

var remoteURLRe = regexp.MustCompile(`(?:github\.com[:/])([^/\s]+)/([^/\s]+?)(?:\.git)?\s*$`)

// detectRemote は .git/config の [remote "origin"] url を読む(exec なしで済ませる)。
func detectRemote(gitConfig string) (owner, repo string) {
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

func detectRegion(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "samconfig.toml")); err == nil {
		if m := regexp.MustCompile(`(?m)^\s*region\s*=\s*"([^"]+)"`).FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	if r := os.Getenv("AWS_REGION"); r != "" {
		return r
	}
	return ""
}

// detectStack は package.json / go.mod / compose から
// フレームワークと DB 依存を推定する。
func detectStack(dir string) (framework, dbDriver string) {
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
		for _, fw := range []string{"next", "@remix-run/node", "react-router", "astro", "nuxt", "@sveltejs/kit"} {
			if deps[fw] {
				framework = strings.TrimPrefix(strings.Split(fw, "/")[0], "@")
				break
			}
		}
		if framework == "" && len(deps) > 0 {
			framework = "node"
		}
		for _, db := range []string{"mysql2", "mysql", "pg", "postgres", "@prisma/client", "drizzle-orm"} {
			if deps[db] {
				dbDriver = db
				break
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		if framework == "" {
			framework = "go"
		}
		s := string(b)
		for _, db := range []string{"go-sql-driver/mysql", "jackc/pgx", "lib/pq"} {
			if strings.Contains(s, db) {
				dbDriver = db
				break
			}
		}
	}
	// compose に mysql/postgres サービスがあれば DB 利用の傍証にする
	if dbDriver == "" {
		for _, f := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
			if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
				s := string(b)
				if strings.Contains(s, "image: mysql") || strings.Contains(s, "image: mariadb") {
					dbDriver = "mysql (compose)"
					break
				}
				if strings.Contains(s, "image: postgres") {
					dbDriver = "postgres (compose)"
					break
				}
			}
		}
	}
	return framework, dbDriver
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var (
	exposeRe      = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)
	composePortRe = regexp.MustCompile(`(?m)^\s*-\s*"?(?:\d+:)?(\d+)"?\s*$`)
)

// detectComposePort は compose の ports("8080:3000" の右側 = コンテナ側)を拾う。
func detectComposePort(dir string) string {
	for _, f := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
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
