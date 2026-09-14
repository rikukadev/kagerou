// Package scaffold は kagerou init の生成物(kagerou.yaml / workflows /
// テンプレート雛形)を書き出す。既存ファイルは既定で触らない。
package scaffold

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/rikukadev/kagerou/internal/appscan"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

type Params struct {
	Project       string
	Region        string
	Sashiki       bool   // sashiki 併用の hooks / DB env を含める
	Port          string // アプリの listen ポート(検出値。空なら framework 既定)
	HasDockerfile bool   // 既存 Dockerfile を使う(template の TODO 文言が変わる)
	Framework     string // 検出フレームワーク(Dockerfile 雛形の選択に使う。#61)
	Driver        string // "stack"(既定)/ "static"
	// Compute は stack driver の実行形。"lambda"(既定: LWA で包む、アイドル $0)
	// または "ecs"(Fargate + 共有 ALB。常駐プロセスやサイドカーが要るアプリ向け)。
	Compute string
	// Entrypoint は環境を公開する入口。"alb"(既定: 共有 ALB。独自ドメインで
	// 配る)または "apigateway"(生の execute-api URL。ドメインが無いときの
	// フォールバック)。compute: ecs は常に ALB。
	Entrypoint string
	// Services は appscan が見つけたサービス名(cmd/<name>/main.go 等)。
	// ALB 入口では 1 環境の中で **ホストで分ける**(<service>-<env>.<domain>)。
	// パス分割を採らないのは DESIGN §10/§13 の決定。
	Services []string
	// Dist は static のときに同期する成果物ディレクトリ。
	Dist string
	// BaseBucket は検出済み preview base のバケット。空なら TODO を書き出す。
	BaseBucket string
	Domain     string // プレビュードメイン(例 preview.example.com)。空なら生 AWS URL 運用
	// DomainFromSSM は Domain が既存ベースの SSM キー(§9)由来であることを示す。
	// このときだけ kagerou.yaml に {base_domain} を書ける — 書いた先が実在する
	// と分かっているため(#139)。
	DomainFromSSM bool
	// HealthPath はアプリのヘルスチェック用パス(appscan 検出、#165)。
	// readiness の既定 "/" はルートが重い SSR で無駄に遅く、リダイレクトする
	// アプリでは誤判定する。**検出できたときだけ** readiness_path に書く。
	HealthPath string
	SetupBase  bool // preview base をこれから作る(deploy/preview-base.yaml を書き出す)
	// Routing は拡張子の無いパスの解決方法(directory | spa)。preview base を
	// 作るときに決まる。SPA を directory で配るとディープリンクが 403 になる(#86)。
	Routing string

	// Wants は appscan が依存から推定した周辺リソース。DynamoDB / SQS / S3 は
	// per-env でもアイドル $0 なので template.yaml に同梱し、Redis / OpenSearch は
	// 常時課金なので env の TODO(共有ベース前提)として kagerou.yaml に出す。
	Wants appscan.Wants
	// URLShape は appscan の URL 構成推定("" | "path" | "cross")。cross のとき
	// kagerou.yaml に peer: の雛形コメントを出す(#99/#109)。
	URLShape string
}

type Result struct {
	Created []string
	Skipped []string // 既存のため触らなかったもの
}

// Targets は何を生成するか(TUI のチェックがここに落ちる)。
type Targets struct {
	KagerouYaml bool
	Preview     bool
	Reap        bool
	Template    bool
	Dockerfile  bool // Dockerfile が無い人向けの雛形(#61)。既存があれば生成しない
}

// AllTargets は全部入り(非対話モードの既定)。
func AllTargets() Targets {
	return Targets{KagerouYaml: true, Preview: true, Reap: true, Template: true, Dockerfile: true}
}

// Run は dir に選択された生成物を書き出す。force は kagerou.yaml と workflows
// のみ上書きを許す。template.yaml はアプリの実体なので force でも上書きしない。
func Run(dir string, p Params, sel Targets, force bool) (Result, error) {
	var res Result

	// これから Dockerfile 雛形を生成する場合、その listen ポートは自分が決める
	// (variant の既定)。template.yaml の AWS_LWA_PORT にも同じ値を使わないと、
	// 「雛形は 8080 で listen、LWA は 3000 を見にいく」で最初のデプロイから 502 になる。
	// routing / dist は **preview base から配るとき** にだけ意味がある。
	// stack 構成(compute がルーティングを持つ)で書くと嘘になるので埋めない。
	if p.Static() {
		if p.Dist == "" {
			p.Dist = DistFor(p.Framework)
		}
		if p.Routing == "" {
			p.Routing = RoutingFor(p.Framework)
		}
	}
	if sel.Dockerfile && p.Port == "" {
		if variant, ok := dockerfileVariant(p.Framework); ok {
			if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
				p.Port = defaultPort(variant)
			}
		}
	}

	files := []struct {
		enabled   bool
		path      string
		tmpl      string
		overwrite bool // force 時に上書きしてよいか
	}{
		{sel.KagerouYaml, "kagerou.yaml", "kagerou.yaml.tmpl", true},
		{sel.Preview, filepath.Join(".github", "workflows", "kagerou-preview.yml"), "preview.yml.tmpl", true},
		{sel.Reap, filepath.Join(".github", "workflows", "kagerou-reap.yml"), "reap.yml.tmpl", true},
		// static には compute が無いので template.yaml も Dockerfile も要らない。
		// ここで落とさないと「消してから手で workflow を書く」ことになる(#81)。
		{sel.Template && !p.Static() && !p.ECS() && !p.MultiService(), "template.yaml", "template.yaml.tmpl", false},
		// 複数サービスの環境は ALB のホストで分ける(1 環境 = 複数ホスト。DESIGN §13)
		{sel.Template && p.MultiService(), "template.yaml", "template.multi.yaml.tmpl", false},
		// compute: ecs は Lambda/LWA で包まず、Fargate + 共有 ALB のテンプレートを出す。
		// ALB は固定費があるので共有ベース(deploy/alb-base.yaml)が持ち、環境は
		// リスナールールとターゲットグループだけ足す。
		{sel.Template && p.ECS(), "template.yaml", "template.ecs.yaml.tmpl", false},
		// 共有 ALB を入口にするなら(ecs / lambda どちらでも)ベースを同梱する
		{p.ALB(), filepath.Join("deploy", "alb-base.yaml"), "albbase.yaml.tmpl", true},
		{p.SetupBase, filepath.Join("deploy", "preview-base.yaml"), "previewbase.yaml.tmpl", true},
	}
	for _, f := range files {
		if !f.enabled {
			continue
		}
		dst := filepath.Join(dir, f.path)
		if _, err := os.Stat(dst); err == nil {
			if !force || !f.overwrite {
				res.Skipped = append(res.Skipped, f.path)
				continue
			}
		}
		if err := renderTo(dst, f.tmpl, p); err != nil {
			return res, err
		}
		res.Created = append(res.Created, f.path)
	}

	// Dockerfile 雛形は「無い人向け」。既存は(force でも)絶対に上書きしない。
	// 既知フレームワークの雛形が無ければ黙ってスキップ(init は失敗させない)。
	// static はコンテナを作らないので、そもそも出さない(#81)。
	if sel.Dockerfile && !p.Static() {
		if variant, ok := dockerfileVariant(p.Framework); ok {
			dst := filepath.Join(dir, "Dockerfile")
			if _, err := os.Stat(dst); err == nil {
				res.Skipped = append(res.Skipped, "Dockerfile")
			} else {
				dp := p
				if dp.Port == "" {
					dp.Port = defaultPort(variant)
				}
				if err := renderDockerfile(dst, variant, dp); err != nil {
					return res, err
				}
				res.Created = append(res.Created, "Dockerfile")
			}
		}
	}
	return res, nil
}

// dockerfileVariant は検出フレームワークを雛形テンプレート名に割り当てる。
// 割り当てが無ければ雛形を作らない(ok=false)。Node 系 SSR はまとめて node に寄せる。
func dockerfileVariant(framework string) (string, bool) {
	switch framework {
	case "go":
		return "go", true
	case "next":
		return "next", true
	case "node", "remix-run", "react-router", "nuxt", "sveltejs", "astro":
		return "node", true
	default:
		return "", false
	}
}

// defaultPort は Port が検出できなかったときの listen ポート既定。
func defaultPort(variant string) string {
	if variant == "go" {
		return "8080"
	}
	return "3000" // node / next の慣習
}

// dockerfileData は Dockerfile テンプレートに渡す値。LWALine は既存注入(InjectLWA)と
// 同じ 1 行を使い、バージョンの二重管理を避ける。
type dockerfileData struct {
	Params
	LWALine string
}

func renderDockerfile(dst, variant string, p Params) error {
	name := "dockerfile." + variant + ".tmpl"
	t, err := template.ParseFS(tmplFS, "templates/"+name)
	if err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := t.Execute(f, dockerfileData{Params: p, LWALine: LWALine}); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", name, err)
	}
	return f.Close()
}

func renderTo(dst, name string, p Params) error {
	t, err := template.ParseFS(tmplFS, "templates/"+name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := t.Execute(f, p); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", name, err)
	}
	return f.Close()
}

// SetupScriptName は AWS セットアップスクリプトの生成先。
const SetupScriptName = "kagerou-setup.sh"

// WriteSetupScript は AWS セットアップ(OIDC ロール / ECR / Variables)を
// 行うスクリプトを dir に書き出す。実行するかは呼び出し側の選択。
func WriteSetupScript(dir string, p Params, d Detection) (string, error) {
	data := struct {
		Owner, Repo, Region, Domain, Project string
		SetupBase                            bool
		Routing                              string
	}{d.Owner, d.Repo, or(p.Region, d.Region), p.Domain, p.Project, p.SetupBase, p.RoutingOrDefault()}
	t, err := template.ParseFS(tmplFS, "templates/setup.sh.tmpl")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, SetupScriptName)
	if err := os.WriteFile(dst, []byte(b.String()), 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

// SetupMode は AWS セットアップ(role / ECR / variables)をどう扱ったか。
type SetupMode int

const (
	SetupSkip    SetupMode = iota // 手動(コマンドを列挙する)
	SetupScript                   // kagerou-setup.sh を書き出した
	SetupApplied                  // その場で適用済み
)

// Step は生成後に残る手作業の 1 項目(TUI チェックリストの行になる)。
type Step struct {
	Title  string
	Detail string // 補足(コマンド例など)。改行可
	Done   bool   // 検出により最初からチェック済み
}

// or は v が空のとき placeholder を返す(検出できた値を優先して埋める)。
func or(v, placeholder string) string {
	if v != "" {
		return v
	}
	return placeholder
}

// Steps は生成後に残る手作業のチェックリスト。検出できた値(アカウント ID・
// リポジトリ名・設定済み Variables)は実値で埋め、済みの項目は Done にする。
// mode により AWS セットアップ 3 手順は 1 行(スクリプト実行)や済みに畳まれる。
func Steps(p Params, d Detection, mode SetupMode) []Step {
	acct := or(d.AccountID, "<account>")
	repo := or(d.Repo, "<repo>")
	region := or(p.Region, or(d.Region, "<region>"))
	roleArn := "arn:aws:iam::" + acct + ":role/" + repo + "-github-actions"
	ecrURI := acct + ".dkr.ecr." + region + ".amazonaws.com/" + repo

	varsDone := d.VarsSet["AWS_ROLE_ARN"] && d.VarsSet["AWS_REGION"] && d.VarsSet["ECR_REPOSITORY"]

	oidcDetail := "note: newer orgs use ID-style sub claims (repo:org@ID/repo@ID:*)"
	if d.Owner != "" && d.Repo != "" {
		oidcDetail = "trust policy sub: repo:" + d.Owner + "/" + d.Repo + ":* (or the ID-style form)\n" + oidcDetail
	}

	steps := []Step{
		{
			Title:  "Write a Dockerfile and fill the TODOs in template.yaml",
			Detail: "run your app as an HTTP server and wrap it with Lambda Web Adapter",
			// 既存 Dockerfile + LWA 済み(注入含む)なら残作業なし
			Done: d.HasDockerfile && (d.HasLWA || d.HasTemplate),
		},
	}
	switch mode {
	case SetupApplied:
		steps = append(steps, Step{
			Title: "AWS setup (role / ECR / variables)",
			Done:  true,
		})
	case SetupScript:
		steps = append(steps, Step{
			Title:  "Review and run ./" + SetupScriptName,
			Detail: "creates the OIDC role and ECR repository, and sets the 3 GitHub variables",
		})
	default: // SetupSkip: 手動でやる人向けにコマンドを列挙する
		steps = append(steps,
			Step{
				Title:  "Create a GitHub OIDC role (scoped to this repository)",
				Detail: oidcDetail,
				Done:   d.VarsSet["AWS_ROLE_ARN"],
			},
			Step{
				Title:  "Create an ECR repository (push target for sam package)",
				Detail: "aws ecr create-repository --repository-name " + repo,
				Done:   d.VarsSet["ECR_REPOSITORY"],
			},
			Step{
				Title: "Set the three GitHub Variables",
				Detail: `gh variable set AWS_ROLE_ARN   --body "` + roleArn + `"
gh variable set AWS_REGION     --body "` + region + `"
gh variable set ECR_REPOSITORY --body "` + ecrURI + `"`,
				Done: varsDone,
			},
		)
	}
	if p.Sashiki {
		steps = append(steps,
			Step{
				Title:  "Fill the DB_HOST / DB_PASSWORD / DB_NAME TODOs in kagerou.yaml",
				Detail: "match your sashiki proxy host and baseline credentials",
			},
			Step{
				Title:  "Make sure the runner can reach sashikid",
				Detail: "run the workflow where `sashiki create` works (self-hosted / inside the VPC)",
			},
		)
	}
	steps = append(steps, Step{
		Title:  "Open a PR and watch the environment appear",
		Detail: "it vanishes on close or when the TTL (72h) expires",
	})
	return steps
}

// PlainSteps は非 TTY(CI 等)向けのプレーンテキスト版チェックリスト。
func PlainSteps(p Params, d Detection) string {
	var b strings.Builder
	b.WriteString("\nNext steps (once per repository):\n\n")
	for i, s := range Steps(p, d, SetupSkip) {
		mark := " "
		if s.Done {
			mark = "x"
		}
		fmt.Fprintf(&b, "  [%s] %d. %s\n", mark, i+1, s.Title)
		for _, line := range strings.Split(s.Detail, "\n") {
			fmt.Fprintf(&b, "        %s\n", line)
		}
	}
	return b.String()
}

// Static は driver: static 構成か(compute を作らない)。
func (p Params) Static() bool { return p.Driver == "static" }

// ECS は compute: ecs 構成か(Fargate + 共有 ALB。Lambda/LWA で包まない)。
func (p Params) ECS() bool { return p.Compute == "ecs" && !p.Static() }

// ALB は共有 ALB を入口にする構成か。独自ドメインで配るための既定で、
// compute: ecs は常に ALB、compute: lambda はドメインがあるとき ALB
// (無ければ生の execute-api URL = apigateway にフォールバック)。
func (p Params) ALB() bool {
	if p.Static() {
		return false
	}
	if p.ECS() {
		return true
	}
	return p.Entrypoint == "alb" && p.Domain != ""
}

// LambdaALB は「lambda を共有 ALB に載せる」構成か(API Gateway を作らない)。
func (p Params) LambdaALB() bool { return p.ALB() && !p.ECS() }

// MultiService は 1 環境に複数サービスを立てる構成か。ALB 入口のときだけ
// 意味を持つ(ホストで分けられるのが ALB の利点。DESIGN §13)。
func (p Params) MultiService() bool { return p.LambdaALB() && len(p.Services) > 1 }

// PortOrDefault は LWA に渡す listen ポート(検出値、無ければ framework 既定)。
func (p Params) PortOrDefault() string {
	if p.Port != "" {
		return p.Port
	}
	if variant, ok := dockerfileVariant(p.Framework); ok {
		return defaultPort(variant)
	}
	return "3000"
}

// ServiceSpec はテンプレートに渡す 1 サービスぶんの値。
type ServiceSpec struct {
	Name    string // api
	Logical string // Api — CFN の論理 ID 接頭辞
	Index   int    // 0,1,2 — リスナールール優先度の枝番
	Host    string // api-${EnvKagerouEnv}.example.com(!Sub の中で使う)
	EnvKey  string // API_URL — 他サービスの URL を届ける環境変数名
	Primary bool   // 環境の代表(url_template が指す先)
}

// primaryNames は「代表サービス」に選ばれやすい名前(先頭が強い)。
var primaryNames = []string{"gateway", "web", "app", "frontend", "www", "api"}

// PrimaryService は環境の代表サービス名を返す(url_template の宛先)。
func (p Params) PrimaryService() string {
	if len(p.Services) == 0 {
		return ""
	}
	for _, want := range primaryNames {
		for _, s := range p.Services {
			if s == want {
				return s
			}
		}
	}
	return p.Services[0]
}

// ServiceSpecs はテンプレート用にサービス一覧を組む。
func (p Params) ServiceSpecs() []ServiceSpec {
	primary := p.PrimaryService()
	out := make([]ServiceSpec, 0, len(p.Services))
	for i, name := range p.Services {
		out = append(out, ServiceSpec{
			Name:    name,
			Logical: logicalID(name),
			Index:   i,
			Host:    name + "-${EnvKagerouEnv}." + p.Domain,
			EnvKey:  envKeyFor(name),
			Primary: name == primary,
		})
	}
	return out
}

// logicalID は CFN の論理 ID に使える形(英数字のみ・先頭大文字)に直す。
func logicalID(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			s := string(r)
			if upper {
				s = strings.ToUpper(s)
				upper = false
			}
			b.WriteString(s)
		default:
			upper = true // 区切り文字は落として次を大文字に
		}
	}
	return b.String()
}

// envKeyFor は他サービスの URL を渡す環境変数名(api → API_URL)。
func envKeyFor(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("_URL")
	return b.String()
}

// DriverFor は構成を決める。既存 kagerou.yaml の driver が最優先で、
// 無ければ「compute があるか」で決める。
//
// compute があると見なすのは Dockerfile か既存 template.yaml がある場合。
// どちらも無く、フロントエンドのフレームワークだけが見つかるなら静的配信。
// **迷ったら stack**(compute 付き)に倒す — static で作って足りないより、
// 余分な雛形を消すほうが復帰しやすい。
func DriverFor(d Detection) string {
	if d.Driver != "" {
		return d.Driver // 既存の設定が一番強い手掛かり
	}
	if d.HasDockerfile || d.HasTemplate {
		return "stack"
	}
	switch d.Framework {
	case "react-router", "remix-run", "vite", "astro":
		return "static"
	default:
		return "stack"
	}
}

// DistFor はフレームワークから成果物ディレクトリを推定する。
// 外したら kagerou.yaml を直せばよいだけなので、推定は素直に倒す。
func DistFor(framework string) string {
	switch framework {
	case "next":
		return "out" // next export
	default:
		return "dist" // vite / astro / react-router など
	}
}

// RoutingOrDefault は Routing の既定(directory)を埋めて返す。
func (p Params) RoutingOrDefault() string {
	if p.Routing == "" {
		return "directory"
	}
	return p.Routing
}

// RoutingFor はフレームワークから既定の routing を決める。
//
// **クライアントルーターを持つものだけ spa** にする。静的サイト生成器
// (next export / astro / nuxt generate)は /about/index.html を出すので
// directory が正しく、そちらを spa にすると今度は個別ページが出せなくなる。
// 判別できないものは directory(現状維持。壊れ方が小さいほうへ倒す)。
func RoutingFor(framework string) string {
	switch framework {
	case "react-router", "remix-run", "vite":
		return "spa"
	default:
		return "directory"
	}
}

// LWALine は既存 Dockerfile に注入する Lambda Web Adapter の 1 行。
// これだけで通常のコンテナが Lambda で動く(Lambda 外では何もしない)。
const LWALine = "COPY --from=public.ecr.aws/awsguru/aws-lambda-adapter:0.9.1 /lambda-adapter /opt/extensions/lambda-adapter"

// InjectLWA は Dockerfile の最終ステージ(最後の FROM の直後)に LWA を注入する。
// 既に入っていれば何もしない。name が空なら "Dockerfile"。
//
// name を取るのは、`Dockerfile`(ECS / 本番)と `Dockerfile.lambda` を分けている
// リポジトリで**用途の違うイメージ定義を書き換えないため**(#151)。
func InjectLWA(dir, name string) (changed bool, err error) {
	if name == "" {
		name = "Dockerfile"
	}
	path := filepath.Join(dir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	src := string(b)
	if strings.Contains(src, "lambda-adapter") {
		return false, nil
	}
	lines := strings.Split(src, "\n")
	last := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(l)), "FROM ") {
			last = i
		}
	}
	if last < 0 {
		return false, fmt.Errorf("no FROM found in %s", path)
	}
	inject := []string{
		"# Lambda Web Adapter: プレビュー環境(kagerou)で Lambda として動かすための 1 行。",
		"# Lambda の外(ローカル docker run / ECS)では何もしない拡張なので本番イメージに残してよい",
		LWALine,
	}
	out := append(append(append([]string{}, lines[:last+1]...), inject...), lines[last+1:]...)
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
