// Package scaffold は kagerou init の生成物(kagerou.yaml / workflows /
// テンプレート雛形)を書き出す。既存ファイルは既定で触らない。
package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"github.com/rikukadev/kagerou/internal/genstamp"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/rikukadev/kagerou/internal/appscan"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

// Version は生成物に埋めるバージョン(#207)。main が起動時に設定する。
// init が書いたファイルは利用者のものになるので、こちらでバグを直しても
// 届かない。せめて「いつの kagerou が書いたか」は残す。
var Version = "dev"

// parseTmpl はテンプレートを関数つきで読む。印を関数にしてあるのは、
// テンプレートごとに渡すデータ型が違うため(メソッドにすると全部に生やす必要がある)。
func parseTmpl(name string) (*template.Template, error) {
	return template.New(name).Funcs(template.FuncMap{
		"generatedBy": func(prefix string) string { return genstamp.Line(prefix, Version) },
	}).ParseFS(tmplFS, "templates/"+name)
}

type Params struct {
	Project       string `json:"project,omitempty"`
	Region        string `json:"region,omitempty"`
	Sashiki       bool   `json:"sashiki,omitempty"`       // sashiki 併用の hooks / DB env を含める
	Port          string `json:"port,omitempty"`          // アプリの listen ポート(検出値。空なら framework 既定)
	HasDockerfile bool   `json:"hasDockerfile,omitempty"` // 既存 Dockerfile を使う(template の TODO 文言が変わる)
	// DockerfileName は template.yaml がビルドに使うファイル名(既定 "Dockerfile")。
	// LWA を `Dockerfile.lambda` に分ける構成があるので、検出した名前をそのまま
	// 生成物に流す。ここを固定にすると、LWA の無いイメージが Lambda に載る(#160)。
	DockerfileName string `json:"dockerfileName,omitempty"`
	// DockerfileDir は Dockerfile のあるディレクトリ(ルートからの相対)。
	// モノレポでは api/ 等に置かれるので、ビルドコンテキストもそこを指す。
	DockerfileDir string `json:"dockerfileDir,omitempty"`
	Framework     string `json:"framework,omitempty"` // 検出フレームワーク(Dockerfile 雛形の選択に使う。#61)
	Driver        string `json:"driver,omitempty"`    // "stack"(既定)/ "static"
	// Compute は stack driver の実行形。"lambda"(既定: LWA で包む、アイドル $0)
	// または "ecs"(Fargate + 共有 ALB。常駐プロセスやサイドカーが要るアプリ向け)。
	Compute string `json:"compute,omitempty"`
	// Entrypoint は環境を公開する入口。"alb"(既定: 共有 ALB。独自ドメインで
	// 配る)または "apigateway"。
	//
	// apigateway の中身は compute で変わる:
	//   lambda … 生の execute-api URL(ドメインが無いときのフォールバック)
	//   ecs    … HTTP API + VPC Link + Cloud Map(固定費ゼロの ALB 代替)
	// どちらも「HTTP API から公開する」という点で同じで、ALB の固定費を
	// 持たない代わりにリクエストが 30 秒で切れる。
	Entrypoint string `json:"entrypoint,omitempty"`
	// Services は appscan が見つけたサービス名(cmd/<name>/main.go 等)。
	// ALB 入口では 1 環境の中で **ホストで分ける**(<service>-<env>.<domain>)。
	// パス分割を採らないのは DESIGN §10/§13 の決定。
	Services []string `json:"services,omitempty"`
	// ServiceFacts は「サービスごとにディレクトリと Dockerfile が分かれている」
	// 構成でだけ埋まる(#184)。Services と**どちらか一方ではない**: 名前の並びは
	// Services が持ち、その名前にビルド元があるかをここで引く。
	//
	// 空のままなら 1 イメージ複数バイナリ(cmd/<name>/main.go)の構成として
	// 扱い、従来どおり全サービスが同じイメージを ImageConfig.Command で
	// 使い分ける。両方の形が実在するので、どちらかに寄せない。
	ServiceFacts []appscan.ServiceFact `json:"serviceFacts,omitempty"`
	// Dist は static のときに同期する成果物ディレクトリ。
	Dist string `json:"dist,omitempty"`
	// BaseBucket は検出済み preview base のバケット。空なら TODO を書き出す。
	BaseBucket string `json:"baseBucket,omitempty"`
	Domain     string `json:"domain,omitempty"` // プレビュードメイン(例 preview.example.com)。空なら生 AWS URL 運用
	// DomainFromSSM は Domain が既存ベースの SSM キー(§9)由来であることを示す。
	// このときだけ kagerou.yaml に {base_domain} を書ける — 書いた先が実在する
	// と分かっているため(#139)。
	DomainFromSSM bool `json:"domainFromSSM,omitempty"`
	// HealthPath はアプリのヘルスチェック用パス(appscan 検出、#165)。
	// readiness の既定 "/" はルートが重い SSR で無駄に遅く、リダイレクトする
	// アプリでは誤判定する。**検出できたときだけ** readiness_path に書く。
	HealthPath string `json:"healthPath,omitempty"`
	SetupBase  bool   `json:"setupBase,omitempty"` // preview base をこれから作る(deploy/preview-base.yaml を書き出す)
	// Auth はプレビューにログインを必須にするか。ALB の authenticate-oidc は
	// S3 を守れないので、共有ベースは CloudFront + Lambda@Edge の形
	// (deploy/preview-base.yaml の代わりに deploy/edge-base.yaml)になる。
	Auth bool `json:"auth,omitempty"`
	// AuthDomain は通す組織のドメイン。ALB には hd として渡すが、hd は
	// ヒントでしかないので、生成物にはアプリ側で検証する手順も書き出す。
	AuthDomain string `json:"authDomain,omitempty"`
	// AuthSecretArn は client_id / client_secret を収めた Secrets Manager の
	// シークレット。テンプレートは {{resolve:secretsmanager:…}} で読むので、
	// 秘密はテンプレートにも kagerou.yaml にも載らない(#112)。
	AuthSecretArn string `json:"authSecretArn,omitempty"`
	// Memory は Lambda の MemorySize(MB)。0 なら既定(512)。
	// 適正値はリポジトリのファイルからは分からないので検出しない。宣言で受ける。
	Memory int `json:"memory,omitempty"`
	// Timeout は Lambda のタイムアウト(秒)。0 なら入口ごとの既定。
	//
	// **入口によって上限が違う**: HTTP API は応答 30 秒で切れるので、それ以上を
	// 設定しても無駄(Lambda は動き続けるが応答は返らない)。ALB 入口は
	// アイドルタイムアウトまで伸ばせる。
	Timeout int `json:"timeout,omitempty"`

	// Routing は拡張子の無いパスの解決方法(directory | spa)。preview base を
	// 作るときに決まる。SPA を directory で配るとディープリンクが 403 になる(#86)。
	Routing string `json:"routing,omitempty"`

	// Wants は appscan が依存から推定した周辺リソース。DynamoDB / SQS / S3 は
	// per-env でもアイドル $0 なので template.yaml に同梱し、Redis / OpenSearch は
	// 常時課金なので env の TODO(共有ベース前提)として kagerou.yaml に出す。
	Wants appscan.Wants
	// URLShape は appscan の URL 構成推定("" | "path" | "cross")。cross のとき
	// kagerou.yaml に peer: の雛形コメントを出す(#99/#109)。
	URLShape string `json:"uRLShape,omitempty"`
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
			if !p.hasExistingDockerfile(dir) {
				p.Port = defaultPort(variant)
			}
		}
	}

	for _, f := range fileSpecs(p, sel) {
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
			name := p.DockerfileOrDefault()
			dst := filepath.Join(dir, name)
			if p.hasExistingDockerfile(dir) {
				res.Skipped = append(res.Skipped, name)
			} else {
				dp := p
				if dp.Port == "" {
					dp.Port = defaultPort(variant)
				}
				if err := renderDockerfile(dst, variant, dp); err != nil {
					return res, err
				}
				res.Created = append(res.Created, name)
			}
		}
	}
	return res, nil
}

// hasExistingDockerfile は「このリポジトリは既に Dockerfile を持っているか」。
//
// リテラルの "Dockerfile" を stat してはいけない。LWA を Dockerfile.lambda に
// 分けている構成では素の Dockerfile が無く、**既に LWA 入りを持っているのに
// 雛形をもう 1 つ生やす**(#192)。どちらがビルドされるかは workflow 次第なので、
// 利用者が気づかないまま意図しないほうが使われる。
//
// 検出結果(HasDockerfile / DockerfileName)を先に信じ、ディスクは
// その名前でだけ確かめる。Params が検出を経ていない呼び出し(テスト等)でも
// 落ちないよう、名前が空なら既定の "Dockerfile" を見る。
func (p Params) hasExistingDockerfile(dir string) bool {
	if p.HasDockerfile {
		return true
	}
	_, err := os.Stat(filepath.Join(dir, p.DockerfileOrDefault()))
	return err == nil
}

// MemoryOrDefault は Lambda の MemorySize(MB)。
func (p Params) MemoryOrDefault() int {
	if p.Memory > 0 {
		return p.Memory
	}
	return 512
}

// TimeoutOrDefault は Lambda のタイムアウト(秒)。
//
// 既定を入口で変える。HTTP API 入口は応答が 30 秒で切れるので、そこを超える値を
// 既定にすると「Lambda はまだ動いているのに 504 が返る」状態を標準にしてしまう。
// ALB 入口にはその上限が無いので、起動の遅いアプリに合わせて伸ばせる。
func (p Params) TimeoutOrDefault() int {
	if p.Timeout > 0 {
		return p.Timeout
	}
	if p.ALB() {
		return 60
	}
	return 30
}

// TimeoutExceedsEntrypoint は、指定したタイムアウトが入口の上限を超えているか。
// 超えていても動きはするが、応答は入口で切れるので警告する値になる。
func (p Params) TimeoutExceedsEntrypoint() bool {
	return p.Timeout > 30 && !p.ALB() && !p.Static()
}

// fileSpec は init が書き出すファイル 1 件。
//
// Run(実際に書く)と PlannedFiles(名前だけ列挙する)が同じ表を引く。別々に
// 持つと「diagnose が予告したファイル」と「init が書いたファイル」が静かにずれる。
type fileSpec struct {
	enabled   bool
	path      string
	tmpl      string
	overwrite bool   // force 時に上書きしてよいか
	note      string // 何のためのファイルか(1 行)
}

func fileSpecs(p Params, sel Targets) []fileSpec {
	return []fileSpec{
		{sel.KagerouYaml, "kagerou.yaml", "kagerou.yaml.tmpl", true,
			"環境の設定(driver / TTL / URL / hooks)"},
		{sel.Preview, filepath.Join(".github", "workflows", "kagerou-preview.yml"), "preview.yml.tmpl", true,
			"PR を開くと環境が生え、閉じると消える"},
		{sel.Reap, filepath.Join(".github", "workflows", "kagerou-reap.yml"), "reap.yml.tmpl", true,
			"TTL を過ぎた環境を回収する(削除の取りこぼし対策)"},
		// static には compute が無いので template.yaml も Dockerfile も要らない。
		// ここで落とさないと「消してから手で workflow を書く」ことになる(#81)。
		{sel.Template && !p.Static() && !p.ECS() && !p.MultiService(), "template.yaml", "template.yaml.tmpl", false,
			"環境 1 個ぶんの CloudFormation(Lambda)"},
		// 複数サービスの環境は ALB のホストで分ける(1 環境 = 複数ホスト。DESIGN §13)
		{sel.Template && p.MultiService(), "template.yaml", "template.multi.yaml.tmpl", false,
			"環境 1 個ぶんの CloudFormation(サービスごとにホストを分ける)"},
		// compute: ecs は Lambda/LWA で包まず Fargate を動かす。入口は 2 通り:
		// 共有 ALB(固定費あり・上限なし)か HTTP API + VPC Link(固定費なし・30 秒)。
		{sel.Template && p.ECS() && !p.APIGatewayVPCLink(), "template.yaml", "template.ecs.yaml.tmpl", false,
			"環境 1 個ぶんの CloudFormation(共有 ALB + Fargate)"},
		{sel.Template && p.APIGatewayVPCLink(), "template.yaml", "template.apigw.yaml.tmpl", false,
			"環境 1 個ぶんの CloudFormation(HTTP API + VPC Link + Fargate)"},
		// 共有 ALB を入口にするなら(ecs / lambda どちらでも)ベースを同梱する
		{p.ALB(), filepath.Join("deploy", "alb-base.yaml"), "albbase.yaml.tmpl", true,
			"共有 ALB + ECS クラスタ。一度だけ deploy する(既にあれば不要)"},
		{p.APIGatewayVPCLink(), filepath.Join("deploy", "apigw-base.yaml"), "apigwbase.yaml.tmpl", true,
			"共有 VPC Link + Cloud Map + ECS クラスタ。一度だけ deploy する(固定費なし)"},
		{p.SetupBase && !p.Auth, filepath.Join("deploy", "preview-base.yaml"), "previewbase.yaml.tmpl", true,
			"共有 CloudFront + S3。一度だけ deploy する(既にあれば不要)"},
		// 認証ありの共有ベース。preview base と同じ SSM キーを書くので、環境側から
		// 見た契約は変わらない(入口に認証が挟まるだけ)。
		//
		// preview base と違い SetupBase で条件しない: 認証を求めた時点でこの 2 つが
		// 無いと認証が成立しないので、「既にあるかも」で省いてよいものではない。
		{p.Auth, filepath.Join("deploy", "edge-base.yaml"), "edgebase.yaml.tmpl", true,
			"共有 CloudFront + Lambda@Edge 認証。us-east-1 に一度だけ deploy する"},
		{p.Auth, filepath.Join("deploy", "edge-auth", "index.mjs"), "edgeauth.mjs.tmpl", true,
			"Lambda@Edge の認証本体(OIDC。client_secret は SSM に置く)"},
	}
}

// PlannedFile は init が書き出す(または書き換える)ファイル 1 件。
type PlannedFile struct {
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
}

// PlannedFiles は init の生成物を **1 つも書かずに** 列挙する。diagnose(他人の
// リポジトリでも走らせる読み取り専用の診断)が「何が生えるのか」を出すために使う。
//
// Dockerfile の扱いだけ Run と判断材料が違う: Run はディスクを stat するが、
// ここは走査も書き込みもしない立場なので Params.HasDockerfile を信じる。
func PlannedFiles(p Params, sel Targets) []PlannedFile {
	var out []PlannedFile
	for _, f := range fileSpecs(p, sel) {
		if f.enabled {
			out = append(out, PlannedFile{Path: filepath.ToSlash(f.path), Note: f.note})
		}
	}
	// Dockerfile。既存があれば init は上書きしない。Lambda 形のときだけ
	// LWA の 1 行を足す(ecs は素のコンテナをそのまま動かす)。
	if sel.Dockerfile && !p.Static() {
		if _, ok := dockerfileVariant(p.Framework); ok {
			note := p.Framework + " 向けの雛形を新規生成する"
			switch {
			case p.HasDockerfile && p.ECS():
				note = "既存をそのまま使う(生成も変更もしない)"
			case p.HasDockerfile:
				note = "既存。Lambda Web Adapter の 1 行だけ足す(上書きしない)"
			}
			// 予告するファイル名も検出値に合わせる。ここを "Dockerfile" 固定に
			// すると、Dockerfile.lambda の構成で「触らない」と言いながら
			// 別名のファイルが生えたように読める(#192)
			out = append(out, PlannedFile{Path: p.DockerfileOrDefault(), Note: note})
		}
	}
	return out
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
	t, err := parseTmpl(name)
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
	body, err := render(name, p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o644)
}

// render は書き出さずにテンプレートを描画する。upgrade --check が
// 「いま生成するとどうなるか」を作るのに使う(Run と同じ経路を通す)。
func render(name string, p Params) ([]byte, error) {
	t, err := parseTmpl(name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return buf.Bytes(), nil
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
		// Auth のとき配信ベースは preview-base ではなく edge-base になる。
		// これを渡さないと、生成されていない preview-base.yaml を deploy
		// しようとするスクリプトが出る(#194)。
		Auth       bool
		AuthDomain string
		// 入口のベース。deploy はしない(VPC / サブネットの id が要るし、
		// 認証は client_secret を人が置く)が、**要ることは言う**。
		// 黙ると、role と ECR だけ出来た状態で最初のプレビューが繋がらない。
		ALB        bool
		APIGateway bool
		Base       bool // 上のどれかがあり、ドメイン/ゾーンの解決が要る
		// BaseReady は「この時点でベースが在る」と言ってよいか。deploy を人に
		// 任せた直後に "base https://…" と出すと、まだ開かない URL を出来たものと
		// して見せることになる。SSM 由来のドメインなら既に在るので出してよい。
		BaseReady bool
	}{d.Owner, d.Repo, or(p.Region, d.Region), p.Domain, p.Project, p.SetupBase, p.RoutingOrDefault(),
		p.Auth, p.AuthDomain, p.ALB(), p.APIGatewayVPCLink(),
		p.SetupBase || handOff(p), !handOff(p) || p.DomainFromSSM}
	t, err := parseTmpl("setup.sh.tmpl")
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

// handOff は「ベースの deploy を人に渡す」構成か。
//
// セットアップが deploy できるのは preview base だけ。ALB / VPC Link は VPC と
// サブネットの id が要り(チームが既に持っているものに乗る)、認証ベースは
// コードの同梱に sam が要るうえ client_secret を人が置く。どれも勝手に決めて
// 作れるものではないので、手順を出して渡す(#194)。
func handOff(p Params) bool { return p.Auth || p.ALB() || p.APIGatewayVPCLink() }

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
	if p.Auth {
		// 認証ベースだけはセットアップが deploy しない(コードの同梱に sam が要り、
		// client_secret は人が置くもの)。ここに出さないと、認証を選んだ人に
		// 「やること」が 1 つも見えないまま最初のプレビューが 403 になる(#194)。
		steps = append(steps, Step{
			Title: "Deploy the auth base (deploy/edge-base.yaml) and put the OIDC values in SSM",
			Detail: "sam deploy --template-file deploy/edge-base.yaml --region us-east-1 --capabilities CAPABILITY_IAM\n" +
				"secrets: /kagerou/edge-auth/<domain>/{issuer,client_id,client_secret,session_secret}\n" +
				"IdP redirect_uri: https://auth.<domain>/_kagerou/auth/callback (1 本だけ)",
		})
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
		// ecs も入口を選べる。apigateway なら固定費のある ALB は要らない
		return p.Entrypoint != "apigateway"
	}
	return p.Entrypoint == "alb" && p.Domain != ""
}

// APIGatewayVPCLink は compute: ecs を HTTP API + VPC Link で公開する構成か。
// 動かすコンテナは ALB 版と同じで、違うのは入口だけ(固定費が消える代わりに
// リクエストが 30 秒で切れる)。lambda の apigateway は生の execute-api なので
// ここには含めない。
func (p Params) APIGatewayVPCLink() bool { return p.ECS() && p.Entrypoint == "apigateway" }

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

// DockerfileOrDefault は template.yaml の `Dockerfile:` に書く名前。
// 検出できなかったときだけ "Dockerfile" に落とす。
func (p Params) DockerfileOrDefault() string {
	if p.DockerfileName != "" {
		return p.DockerfileName
	}
	return "Dockerfile"
}

// DockerContext は template.yaml の `DockerContext:` に書くビルドコンテキスト。
//
// SAM は DockerContext をテンプレートからの相対、Dockerfile をその
// DockerContext からの相対で解決する。モノレポで api/Dockerfile を検出したなら
// コンテキストも api/ を指さないと、ビルドが別の場所を見る。
func (p Params) DockerContext() string { return dockerContextFor(p.DockerfileDir) }

// HealthPathOrDefault は LWA の readiness チェック先。検出できなければ
// 従来どおり /healthz(#182)。
func (p Params) HealthPathOrDefault() string {
	if p.HealthPath != "" {
		return p.HealthPath
	}
	return "/healthz"
}

// ServiceSpec はテンプレートに渡す 1 サービスぶんの値。
type ServiceSpec struct {
	Name    string // api
	Logical string // Api — CFN の論理 ID 接頭辞
	Index   int    // 0,1,2 — リスナールール優先度の枝番
	Host    string // api-${EnvKagerouEnv}.example.com(!Sub の中で使う)
	EnvKey  string // API_URL — 他サービスの URL を届ける環境変数名
	// Upper は env キーに使うサービス名(API)。優先度を手で固定したい人が
	// --env RULE_PRIORITY_API と書けるように、生成物へそのまま出す(#189)
	Upper   string
	Primary bool // 環境の代表(url_template が指す先)

	// ここから下はサービスごとのビルド元(#184)。ServiceFacts に対応する
	// 事実があればそれ、無ければルートの検出値に落ちる。
	DockerContext string // ./api — SAM はテンプレートからの相対で解決する
	Dockerfile    string // Dockerfile.lambda — DockerContext からの相対
	// SharedImage は全サービスが 1 つのイメージを共有する構成か。
	// true のときだけ ImageConfig.Command でバイナリを使い分ける。
	// サービスごとに Dockerfile があるならイメージ側の entrypoint が正しく、
	// Command を被せると**そちらが無視される**ので出さない。
	SharedImage bool
	Port        string // このサービスの listen ポート
	HealthPath  string // LWA の readiness チェック先
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
	svcs := p.Services
	out := make([]ServiceSpec, 0, len(svcs))
	for i, name := range svcs {
		s := ServiceSpec{
			Name:    name,
			Logical: logicalID(name),
			Index:   i,
			Host:    name + "-${EnvKagerouEnv}." + p.Domain,
			EnvKey:  envKeyFor(name),
			Upper:   strings.TrimSuffix(envKeyFor(name), "_URL"),
			Primary: name == primary,
		}
		s.applySource(p, p.serviceFact(name))
		out = append(out, s)
	}
	return out
}

// serviceFact は名前でサービスの事実を引く。無ければ nil。
func (p Params) serviceFact(name string) *appscan.ServiceFact {
	for i := range p.ServiceFacts {
		if p.ServiceFacts[i].Name == name {
			return &p.ServiceFacts[i]
		}
	}
	return nil
}

// applySource はビルド元と検出値を埋める。サービスごとの事実が無い、または
// あっても Dockerfile が無いなら、ルートの値に落として SharedImage にする。
//
// 「事実があるのに Dockerfile が無い」は起こりうる(compose の image: 指定など)。
// そのときにサービスのディレクトリだけを DockerContext にすると、存在しない
// Dockerfile をビルドしにいくので、ルートごと落とす。
func (s *ServiceSpec) applySource(p Params, f *appscan.ServiceFact) {
	s.Port, s.HealthPath = p.PortOrDefault(), p.HealthPathOrDefault()
	if f != nil {
		if f.Port != "" {
			s.Port = f.Port
		}
		if f.HealthPath != "" {
			s.HealthPath = f.HealthPath
		}
	}
	if f == nil || f.Dockerfile == "" {
		s.DockerContext, s.Dockerfile = p.DockerContext(), p.DockerfileOrDefault()
		s.SharedImage = true
		return
	}
	s.DockerContext, s.Dockerfile = dockerContextFor(f.Dir), f.Dockerfile
}

// dockerContextFor はリポジトリ相対のディレクトリを SAM の DockerContext に直す。
func dockerContextFor(dir string) string {
	if dir == "" {
		return "."
	}
	return "./" + filepath.ToSlash(dir)
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
