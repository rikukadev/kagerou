package scaffold

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// pad は表示幅で右詰めする。%-16s はバイト数で数えるため、日本語が混ざると
// 列がずれる(「ECR 保管」と「CloudFront」が揃わない)。
func pad(s string, w int) string {
	if n := w - runewidth.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// PricesAsOf は下の単価を確認した時点。価格は変わるので、いつのものかを
// 一緒に出す。これが無いと「ツールが言っていた額と請求が違う」になる。
const PricesAsOf = "2026-09"

// AWSResource は init が AWS に作るもの 1 つ。
type AWSResource struct {
	Kind string // "CloudFront" / "IAM role" など。何のサービスか
	Name string // 実際に付く名前。後で探せるように具体名を出す
	Note string // 補足(リージョンの制約など)
}

// Prereq は「init が作らないが、無いとプレビューが成立しない」共有側の住人。
//
// preview base(配信)と同じ長生きの層に住むのに、init が作らないというだけで
// プランから消えると、利用者は最初のプレビューが繋がらない時点で初めて気づく(#123)。
// **作るものと前提を同じ画面に並べる**のが目的なので、Kind/Name は Shared と揃える。
type Prereq struct {
	Kind string
	Name string
	How  []string // 用意のしかた(1 行ずつ)
	Cost string   // 常駐するので固定費になる。額は「作るもの」と分けて出す
}

// CostLine は費用の内訳 1 行。Price は単価、Est はこの構成での目安。
type CostLine struct {
	Item  string
	Price string
	Est   string
}

// AWSPlan は「y を押したら何が起きるか」を全部書いたもの。
//
// 作る前にこれを見せるのは、init が **課金されるリソースを黙って作らない**
// ため。ファイルの生成と違って、取り消すには手で消す必要がある。
type AWSPlan struct {
	Account  string
	Region   string
	Shared   []AWSResource // 一度だけ作る。環境より寿命が長い
	Prereqs  []Prereq      // 作らないが、無いと動かない(共有側)
	Cost     []CostLine
	Estimate string   // 全体の目安(1 行)
	PerEnv   string   // 環境ごとに作られるもの(構成で変わる)
	Teardown []string // 取り壊すためのコマンド
	Warnings []string // 先に知っておくべきこと(時間がかかる、など)
}

// BuildAWSPlan は init のセットアップが AWS に作るものを組み立てる。
//
// 純粋な関数にしてあるのは、TUI を起動せずに中身を検証できるようにするため
// (「見せている内容と実際に作るものがずれる」が一番まずい)。
func BuildAWSPlan(p Params, d Detection) AWSPlan {
	account := d.AccountID
	if account == "" {
		account = "<account>"
	}
	repo := d.Repo
	if repo == "" {
		repo = p.Project
	}

	plan := AWSPlan{Account: account, Region: p.Region, PerEnv: perEnv(p)}

	// CI が引き受けるロール。環境より長生きする。
	plan.Shared = append(plan.Shared,
		AWSResource{"IAM role", repo + "-github-actions", "GitHub Actions が OIDC で引き受ける"})
	// イメージの置き場は compute 構成だけ。static で出すと、作られないものを
	// 見せることになる(セットアップスクリプトは元から作らない)。
	if !p.Static() {
		plan.Shared = append(plan.Shared, AWSResource{"ECR repository", repo, p.Region})
	}

	if p.SetupBase {
		// preview base。CloudFront の証明書が us-east-1 必須なので、
		// スタックごと us-east-1 に置く。
		plan.Shared = append(plan.Shared,
			AWSResource{"CloudFront", "*." + p.Domain,
				"全環境で共有。routing=" + p.RoutingOrDefault() + "(" + routingNote(p.RoutingOrDefault()) + ")"},
			AWSResource{"ACM certificate", "*." + p.Domain, "us-east-1(CloudFront の制約)。DNS 検証は自動"},
			AWSResource{"S3 bucket", "kagerou-base-" + p.Project + "-" + account, "環境ごとに <name>/ プレフィックス"},
			AWSResource{"Route53 record", "*." + p.Domain, "ワイルドカード 1 本。環境ごとには作らない"},
		)
		plan.Warnings = append(plan.Warnings,
			"証明書の検証と CloudFront の配信開始で 15 分ほどかかる(以降は環境ごとの待ちに乗らない)",
			"routing はここで焼き込まれる。後から変えるには deploy/preview-base.yaml を deploy し直す")
		plan.Teardown = append(plan.Teardown,
			"aws s3 rm s3://kagerou-base-"+p.Project+"-"+account+" --recursive",
			"aws cloudformation delete-stack --region us-east-1 --stack-name kagerou-preview-base-"+p.Project)
	}
	plan.Teardown = append(plan.Teardown, "aws iam delete-role --role-name "+repo+"-github-actions")
	if !p.Static() {
		plan.Teardown = append(plan.Teardown,
			"aws ecr delete-repository --repository-name "+repo+" --region "+p.Region+" --force")
	}

	// 入口の共有ベース(deploy/*-base.yaml)。init はテンプレートを書き出すだけで
	// deploy はしないので、「作るもの」ではなく前提として並べる。
	plan.Prereqs = append(plan.Prereqs, baseStacks(p)...)

	// 費用。**固定の月額が無い**ことが要点なので、それが伝わる並びにする。
	plan.Cost = []CostLine{{"IAM role / OIDC", "無料", "—"}}
	if !p.Static() {
		plan.Cost = append(plan.Cost,
			CostLine{"ECR 保管", "$0.10 /GB・月", "イメージ 1 個 200MB で約 $0.02/月"})
	}
	// 証明書と DNS は、ドメインを持つベース(配信 / ALB / apigw)があるときだけ。
	// 関係ない構成で並べると、作りも使いもしないものの費用を読ませることになる。
	if p.SetupBase || p.Static() || p.ALB() || p.APIGatewayVPCLink() {
		plan.Cost = append(plan.Cost,
			CostLine{"ACM certificate", "無料", "—"},
			CostLine{"Route53 レコード", "無料", "ホストゾーンは既存のものを使う"})
	}
	// S3 と CloudFront は配信ベースのもの。stack driver だけの構成には無い。
	if p.SetupBase || p.Static() {
		plan.Cost = append(plan.Cost,
			CostLine{"S3 保管", "$0.023 /GB・月", "SPA は数 MB なので実質ゼロ"},
			CostLine{"CloudFront", "無料枠内", "毎月 1TB 転送・1000 万リクエストまで"})
	}

	// 固定費(常駐して毎月立つ額)を集める。1 つでもあれば「固定の月額はどれにも
	// 無い」とは書けない。費用を**少なく**見せるのが一番まずい(#183)。
	var fixed []string
	if p.ALB() {
		plan.Cost = append(plan.Cost,
			CostLine{"ALB(共有)", "$0.0225 /時", "+ LCU。月 $18 前後で、環境が増えても同額"})
		fixed = append(fixed, "共有 ALB に 月 $18 前後(既にあれば増えない)")
	}
	if p.ECS() {
		// Fargate はアイドル $0 ではない。Lambda と同じ書き方をすると、
		// 止め忘れた環境の額が画面から消える。
		plan.Cost = append(plan.Cost,
			CostLine{"Fargate", "$0.05 /vCPU・時", "0.25 vCPU の環境を 72 時間(既定 TTL)で約 $1"})
	}
	if p.Auth && p.ALB() {
		plan.Cost = append(plan.Cost,
			CostLine{"Secrets Manager", "$0.40 /月・1 個", "authenticate-oidc が読む client_secret"})
		fixed = append(fixed, "Secrets Manager のシークレット 1 個に 月 $0.40")
	}

	if p.Sashiki {
		// sashiki ホストは preview base と同じ共有側の住人。環境ごとに乗るのは
		// CoW ブランチだけで、ホスト自体は init の対象外(#123)。
		plan.Prereqs = append(plan.Prereqs, Prereq{
			Kind: "sashiki host",
			Name: "EC2 + sashikid + zpool + baseline(共有側に 1 台)",
			How: []string{
				"sashiki の Terraform モジュール(deploy/terraform、RDS 互換の出力)",
				"または手で: deb を入れて sashiki init → sashiki baseline import",
			},
			Cost: "EC2 1 台 + EBS の常駐費。小さめの DB で 月 $30 前後、" +
				"600GB・同時 5 なら 月 $120 前後(sashiki/docs/COSTS.md)",
		})
		plan.Cost = append(plan.Cost,
			CostLine{"sashiki ホスト", "月 $30 前後〜", "前提。kagerou は作らないし消さない"})
		fixed = append(fixed, "前提の sashiki ホストに 月 $30 前後〜")
	}

	plan.Estimate = estimate(p, fixed)
	return plan
}

// perEnv は「環境ごとに作って壊すもの」を構成から 1 行にする。
//
// 入口で中身が変わる: ALB 入口では HTTP API を作らないし、ecs は Lambda を
// 作らない。ここが生成物とずれると、作られないものを見せたまま y を押させる。
func perEnv(p Params) string {
	// バケットは preview base を作るときにだけ存在する。無い構成で書くと、
	// 置き場所が別にあるように読める。
	bucket := ""
	if p.SetupBase {
		bucket = " / 上のバケットの <name>/ 配下"
	}
	switch {
	case p.Static():
		// static は compute を作らない。ここに Lambda と書くと、
		// 「作られないものが作られる」と読める。
		return "上のバケットの <name>/ 配下(compute は作らない)"
	case p.APIGatewayVPCLink():
		return "Fargate タスク / Cloud Map サービス / HTTP API + ドメイン" + bucket
	case p.ECS():
		return "Fargate タスク / ターゲットグループ / リスナールール(後ろ 2 つは無料)" + bucket
	case p.MultiService():
		return "サービスごとに Lambda / ターゲットグループ / リスナールール(後ろ 2 つは無料)" + bucket
	case p.LambdaALB():
		return "Lambda / ターゲットグループ / リスナールール(後ろ 2 つは無料)" + bucket
	default:
		// ドメインの無い lambda。生の execute-api URL で配る
		return "Lambda / HTTP API" + bucket
	}
}

// baseStacks は init が **書き出すが deploy はしない** 共有ベースを前提として並べる。
//
// deploy/*-base.yaml があるのにプランに出ないと、固定費と「自分で deploy する
// 必要があること」の両方が画面から消える(#183)。Prereq に置くのは、init が
// 作らないものを「作るもの」に混ぜないため(sashiki ホストと同じ扱い)。
func baseStacks(p Params) []Prereq {
	domain := p.Domain
	if domain == "" {
		domain = "<domain>"
	}
	var out []Prereq
	if p.ALB() {
		out = append(out, Prereq{
			Kind: "共有 ALB",
			Name: "kagerou-alb-base-" + p.Project + "(*." + domain + " の入口)",
			How: []string{
				"init が deploy/alb-base.yaml を書き出す。アプリのリージョンで 1 回だけ deploy する:",
				"aws cloudformation deploy --stack-name kagerou-alb-base-" + p.Project + " \\",
				"  --template-file deploy/alb-base.yaml \\",
				"  --parameter-overrides Project=" + p.Project + " DomainName=" + domain + " \\",
				"    HostedZoneId=<zone> VpcId=<vpc> SubnetIds=<subnet-a>,<subnet-b>",
				"既にあれば不要。環境は同じ ALB に相乗りする(固定費は増えない)",
			},
			Cost: "ALB 1 本で 月 $18 前後。環境が 1 個でも 30 個でも同額",
		})
	}
	if p.APIGatewayVPCLink() {
		out = append(out, Prereq{
			Kind: "共有 VPC Link",
			Name: "kagerou-apigw-base-" + p.Project + "(VPC Link + Cloud Map + ECS クラスタ)",
			How: []string{
				"init が deploy/apigw-base.yaml を書き出す。アプリのリージョンで 1 回だけ deploy する:",
				"aws cloudformation deploy --stack-name kagerou-apigw-base-" + p.Project + " \\",
				"  --template-file deploy/apigw-base.yaml \\",
				"  --parameter-overrides Project=" + p.Project + " DomainName=" + domain + " \\",
				"    HostedZoneId=<zone> VpcId=<vpc> SubnetIds=<subnet-a>,<subnet-b>",
			},
			Cost: "固定費なし(VPC Link も HTTP API も作成は無料)。" +
				"課金は環境が動いている間の Fargate だけ",
		})
	}
	if p.Auth {
		out = append(out, Prereq{
			Kind: "認証ベース",
			Name: "kagerou-edge-base-" + p.Project + "(CloudFront + Lambda@Edge)",
			How: []string{
				"init が deploy/edge-base.yaml を書き出す。us-east-1 で 1 回だけ deploy する:",
				"sam deploy --template-file deploy/edge-base.yaml --region us-east-1 \\",
				"  --stack-name kagerou-edge-base-" + p.Project + " --capabilities CAPABILITY_NAMED_IAM",
				"OIDC の issuer / client_id / client_secret は SSM に置く(テンプレート冒頭の手順)",
			},
			Cost: "固定費なし(CloudFront は無料枠内、Lambda@Edge はリクエスト課金)",
		})
	}
	return out
}

// estimate は費用の 1 行まとめ。固定費が 1 つでもあるなら「固定の月額はどれにも
// 無い」とは書かない — 安い側に丸めた 1 行だけが読まれるのが一番危ない(#183)。
func estimate(p Params, fixed []string) string {
	est := "環境ごとの費用はプレビュー 10 個で 月 $0.1 未満"
	if p.ECS() {
		// Fargate は動いている間ずっと課金される。プレビュー 10 個で $0.1 は
		// アイドル $0 の lambda の話で、ecs に流用すると額が桁で変わる。
		est = "環境ごとの費用は Fargate の実行時間ぶん(0.25 vCPU なら 72 時間で約 $1)"
	}
	if len(fixed) == 0 {
		return est + "。固定の月額はどれにも無い"
	}
	return est + "。別に固定費: " + strings.Join(fixed, " / ")
}

// routingNote は routing の意味を 1 行で説明する。作る前に見せる値なので、
// 用語ではなく「何が起きるか」で書く。
func routingNote(r string) string {
	if r == "spa" {
		return "/about は index.html に写す。クライアントルーター向け"
	}
	return "/about は /about/index.html を探す。静的サイト生成器向け"
}

// Render はプランを人が読む形にする。TUI と --plain の両方から使う。
func (p AWSPlan) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "AWS に作るもの   account %s · %s\n\n", p.Account, p.Region)
	for _, r := range p.Shared {
		fmt.Fprintf(&b, "  %s %s\n", pad(r.Kind, 16), r.Name)
		if r.Note != "" {
			fmt.Fprintf(&b, "  %s %s\n", pad("", 16), r.Note)
		}
	}
	fmt.Fprintf(&b, "\n  環境ごと(kagerou が PR ごとに作って壊す): %s\n", p.PerEnv)

	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\n  ! %s\n", w)
	}

	if len(p.Prereqs) > 0 {
		b.WriteString("\n前提(作らない。無ければ先に用意)\n\n")
		for _, r := range p.Prereqs {
			fmt.Fprintf(&b, "  %s %s\n", pad(r.Kind, 16), r.Name)
			for _, h := range r.How {
				fmt.Fprintf(&b, "  %s %s\n", pad("", 16), h)
			}
			if r.Cost != "" {
				fmt.Fprintf(&b, "  %s %s\n", pad("", 16), r.Cost)
			}
		}
	}

	fmt.Fprintf(&b, "\n費用の目安   %s 時点\n\n", PricesAsOf)
	for _, c := range p.Cost {
		fmt.Fprintf(&b, "  %s %s %s\n", pad(c.Item, 16), pad(c.Price, 16), c.Est)
	}
	fmt.Fprintf(&b, "\n  %s\n", p.Estimate)
	b.WriteString("  正確な額はリージョンと使い方で変わる。AWS Pricing Calculator で確認できる\n")

	b.WriteString("\n取り壊すとき\n\n")
	for _, t := range p.Teardown {
		fmt.Fprintf(&b, "  %s\n", t)
	}
	return b.String()
}
