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
	Cost     []CostLine
	Estimate string   // 全体の目安(1 行)
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

	plan := AWSPlan{Account: account, Region: p.Region}

	// CI が引き受けるロールと、イメージの置き場。どちらも環境より長生きする。
	plan.Shared = append(plan.Shared,
		AWSResource{"IAM role", repo + "-github-actions", "GitHub Actions が OIDC で引き受ける"},
		AWSResource{"ECR repository", repo, p.Region},
	)

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
	plan.Teardown = append(plan.Teardown,
		"aws iam delete-role --role-name "+repo+"-github-actions",
		"aws ecr delete-repository --repository-name "+repo+" --region "+p.Region+" --force")

	// 費用。**固定の月額が無い**ことが要点なので、それが伝わる並びにする。
	plan.Cost = []CostLine{
		{"IAM role / OIDC", "無料", "—"},
		{"ACM certificate", "無料", "—"},
		{"Route53 レコード", "無料", "ホストゾーンは既存のものを使う"},
		{"S3 保管", "$0.023 /GB・月", "SPA は数 MB なので実質ゼロ"},
		{"ECR 保管", "$0.10 /GB・月", "イメージ 1 個 200MB で約 $0.02/月"},
		{"CloudFront", "無料枠内", "毎月 1TB 転送・1000 万リクエストまで"},
	}
	plan.Estimate = "プレビュー 10 個で 月 $0.1 未満。固定の月額はどれにも無い"

	return plan
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
	b.WriteString("\n  環境ごと(kagerou が PR ごとに作って壊す): Lambda / HTTP API / 上のバケットの <name>/ 配下\n")

	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "\n  ! %s\n", w)
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
