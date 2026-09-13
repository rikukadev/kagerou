package scaffold

// 環境(AWS アカウント・Route53・gh Variables・preview base)を読んで、
// リポジトリ側の事実(internal/appscan)に重ね、init の入力を半自動で埋める。
// リポジトリの走査そのものは appscan が担う(ファイルのみ・独立コンポーネント)。
// すべてベストエフォート: 検出に失敗した項目は空のままで、従来の
// プレースホルダ表示に落ちるだけ(init が失敗する理由にはしない)。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/rikukadev/kagerou/internal/appscan"
)

type Detection struct {
	// リポジトリ側の事実(appscan.Scan から写す。走査の実体は internal/appscan)
	Owner, Repo   string
	Region        string
	Framework     string
	DBDriver      string
	HasDockerfile bool
	HasLWA        bool
	DockerfileDir string // Dockerfile のあるディレクトリ(ルート相対。LWA 注入先)
	AppPort       string
	HasTemplate   bool
	Wants         appscan.Wants // 依存から推定した周辺リソース(DynamoDB/SQS/S3/Redis/OpenSearch)

	// 環境側の事実(aws / gh の exec から)
	AccountID string              // aws sts get-caller-identity から
	VarsSet   map[string]bool     // 設定済みの GitHub Variables
	Zones     []string            // Route53 の公開ホストゾーン(ドメイン選択肢)
	Bases     map[string]BaseInfo // 既存 preview base。key = project(旧アカウント単位 base は key "")
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

// SharedBaseKey は 1 ドメインを複数アプリで共有する base の名前空間(#94)。
// project 名には使えない文字(_)を先頭に置いて、実在の project と衝突させない。
const SharedBaseKey = "_shared"

// Base は project の preview base を返す。解決順は
// project 専用 → 共有(_shared)→ 旧アカウント単位(project セグメントが
// 無い頃の Export)。共有かどうかは 2 つ目の戻り値で分かる。
func (d Detection) Base(project string) (BaseInfo, bool) {
	b, shared, ok := d.BaseFor(project)
	_ = shared
	return b, ok
}

// BaseFor は Base に加えて「共有 base か」を返す。URL 規約と
// static のプレフィックスが共有かどうかで変わるため。
func (d Detection) BaseFor(project string) (info BaseInfo, shared, ok bool) {
	if b, hit := d.Bases[project]; hit {
		return b, false, true
	}
	if b, hit := d.Bases[SharedBaseKey]; hit {
		return b, true, true
	}
	b, hit := d.Bases[""]
	return b, false, hit
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
	f := appscan.Scan(dir)
	d := Detection{
		Owner: f.Owner, Repo: f.Repo, Region: f.Region,
		Framework: f.Framework, DBDriver: f.DBDriver,
		HasDockerfile: f.HasDockerfile, HasLWA: f.HasLWA, DockerfileDir: f.DockerfileDir,
		AppPort: f.AppPort, HasTemplate: f.HasTemplate, Wants: f.Wants,
		VarsSet: map[string]bool{},
	}
	// Region の優先順位: samconfig.toml(Facts)> AWS_REGION > ~/.aws/config
	if d.Region == "" {
		d.Region = os.Getenv("AWS_REGION")
	}

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
