package recommend

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/rikukadev/kagerou/internal/appscan"
)

// 構成図(#136)。「lambda」「apigateway」という入口の名前だけでは、それが AWS 上で
// どういう形になるのかが分からない。diagnose は他人のリポジトリで読み取り専用に
// 走らせるものなので、名前ではなく **形** を出せないと答えになっていない。
//
// 描く内容は Facts 連動: DB ドライバを検出したら DB の箱が付き、Wants の
// DynamoDB / SQS などは environment ごとの箱として付き、URLShape=cross なら
// web と api を別オリジンに割る。
//
// 描くのは **init が実際に生成するテンプレートの形**(internal/scaffold/templates)で、
// 一般論の AWS 構成ではない。ここが食い違うと図を出す意味が無くなる。
// 生成物のファイル名のほうは scaffold.PlannedFiles が受け持つ。

// Node は構成図の 1 要素。Children があればそこで分岐する。
type Node struct {
	Label    string
	Note     string
	Children []Node
}

// Diagram は Facts と入口から構成図を組み立てる。
func Diagram(f appscan.Facts, e Entrypoint) Node {
	root := Node{Label: "Browser"}
	switch {
	case e == Static:
		root.Children = []Node{webChain(f)}
	case e == EdgeAuth:
		// 認証は 1 本の CloudFront が S3 も compute もまとめて守る形。ここで
		// web を別オリジンに割ると、割ったほうが無防備になる(apiChain の中で分岐する)
		root.Children = []Node{apiChain(f, e)}
	case f.URLShape == "cross":
		// フロントと API が別オリジン。web は共有ベースから配り、API は自分の入口を持つ
		root.Children = []Node{webChain(f), apiChain(f, e)}
	default:
		root.Children = []Node{apiChain(f, e)}
	}
	return root
}

// webChain は静的成果物の配信経路。共有ベース(deploy/preview-base.yaml)の
// CloudFront + S3 で、環境ごとに作られるのはプレフィックスだけ。
func webChain(f appscan.Facts) Node {
	return Node{
		Label:    "CloudFront",
		Note:     "共有ベース。全環境で 1 本(環境ごとには作らない)",
		Children: []Node{staticOrigin(f)},
	}
}

func staticOrigin(f appscan.Facts) Node {
	dist := "静的成果物"
	// 名前を出すのはフロントエンドのフレームワークのときだけ。モノレポでは
	// Framework にバックエンド(go など)が勝つことがあり、それを web 側の
	// 成果物の説明に使うと嘘になる
	if isFrontend(f.Framework) {
		dist = f.Framework + " の静的成果物"
	}
	return Node{Label: "S3  <name>/", Note: dist + "。環境ぶんは S3 のプレフィックスだけ"}
}

func isFrontend(fw string) bool {
	switch fw {
	case "next", "remix-run", "react-router", "astro", "nuxt", "sveltejs", "node":
		return true
	}
	return false
}

// apiChain は compute を持つ入口の経路。末端の compute に周辺リソースをぶら下げる。
func apiChain(f appscan.Facts, e Entrypoint) Node {
	app := Node{Children: attachments(f)}
	switch e {
	case Lambda:
		app.Label = "Lambda (Web Adapter)"
		app.Note = lwaNote(f)
		return Node{
			// init は独自ドメインがあれば共有 ALB に載せる(#131)。
			// ドメインが無ければ生の execute-api URL にフォールバックする
			Label:    "共有 ALB",
			Note:     "独自ドメインで配る。ドメインが無ければ生の execute-api URL になる",
			Children: []Node{app},
		}
	case APIGateway:
		app.Label = "ECS Fargate"
		app.Note = ecsNote(f)
		return Node{
			Label: "HTTP API",
			Note:  "環境ごとに作る。固定費ゼロ(リクエストは 30 秒まで)",
			Children: []Node{{
				Label:    "VPC Link → Cloud Map",
				Note:     "タスクの IP は毎回変わるのでサービス名で引く",
				Children: []Node{app},
			}},
		}
	case ALB:
		app.Label = "ECS Fargate"
		app.Note = ecsNote(f)
		return Node{
			Label:    "ALB",
			Note:     "共有ベース(固定費・月 $18 前後)。環境はリスナールールだけ足す",
			Children: []Node{app},
		}
	case EdgeAuth:
		// S3 は常に守る対象。compute があるときだけ、その後ろに足す
		// (静的のみで edge を薦めるのは「ALB は S3 を守れない」からで、
		//  ここに Lambda を描くと在りもしないサーバを描くことになる)
		kids := []Node{staticOrigin(f)}
		if HasCompute(f) {
			app.Label = "Lambda (Web Adapter)"
			app.Note = lwaNote(f)
			kids = append(kids, app)
		}
		return Node{
			Label:    "CloudFront + Lambda@Edge",
			Note:     "認証はここで自前実装。固定費ゼロ(オリジン応答は 〜60 秒)",
			Children: kids,
		}
	}
	return app
}

func lwaNote(f appscan.Facts) string {
	if f.HasLWA {
		return "既存の Dockerfile(LWA 注入済み)をそのまま動かす"
	}
	if f.HasDockerfile {
		return "既存の Dockerfile に LWA を 1 行足して包む"
	}
	return "コンテナを LWA で包んで動かす(Dockerfile は init が雛形を出す)"
}

func ecsNote(f appscan.Facts) string {
	if f.Services > 1 {
		return "常駐プロセス向け。応答時間の上限が無い"
	}
	return "Fargate タスク 1 つ。応答時間の上限が無い"
}

// attachments は compute にぶら下がる周辺リソース。per-env で持てるもの
// (アイドル $0)と、共有ベース側に置くもの(常時課金)を区別して書く。
func attachments(f appscan.Facts) []Node {
	var out []Node
	if f.DBDriver != "" {
		out = append(out, Node{
			Label: "DB (RDS / sashiki)",
			Note:  f.DBDriver + " を検出。接続先は env で注入する",
		})
	}
	perEnv := "環境ごとに作る(on-demand = アイドル $0)"
	for _, x := range []struct {
		on    bool
		label string
	}{
		{f.Wants.DynamoDB, "DynamoDB"}, {f.Wants.SNS, "SNS"},
		{f.Wants.SQS, "SQS"}, {f.Wants.S3, "S3 (アプリ用)"},
	} {
		if x.on {
			out = append(out, Node{Label: x.label, Note: perEnv})
		}
	}
	shared := "共有ベース側(常時課金なので環境ごとには作らない)"
	for _, x := range []struct {
		on    bool
		label string
	}{
		{f.Wants.Redis, "Redis"}, {f.Wants.OpenSearch, "OpenSearch"},
	} {
		if x.on {
			out = append(out, Node{Label: x.label, Note: shared})
		}
	}
	return out
}

// Render は図を行の並びにする。子が 1 つなら縦に続け、複数なら枝分かれする。
// 注記は行をまたいで同じ列に揃える(幅は表示幅で数える。日本語が混ざるため)。
func (n Node) Render() []string {
	ls := lines(n, "")
	w := 0
	for _, l := range ls {
		if l.note == "" {
			continue
		}
		if x := runewidth.StringWidth(l.text); x > w {
			w = x
		}
	}
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		if l.note == "" {
			out = append(out, strings.TrimRight(l.text, " "))
			continue
		}
		out = append(out, pad(l.text, w+3)+l.note)
	}
	return out
}

type line struct{ text, note string }

func lines(n Node, indent string) []line {
	out := []line{{indent + n.Label, n.Note}}
	switch len(n.Children) {
	case 0:
	case 1:
		out = append(out, line{indent + "│", ""}, line{indent + "▼", ""})
		out = append(out, lines(n.Children[0], indent)...)
	default:
		for i, c := range n.Children {
			branch, sub := "├─▶ ", indent+"│   "
			if i == len(n.Children)-1 {
				branch, sub = "└─▶ ", indent+"    "
			}
			cl := lines(c, sub)
			cl[0].text = indent + branch + c.Label // 枝の頭だけ接続記号に差し替える
			out = append(out, cl...)
		}
	}
	return out
}

// pad は表示幅で右詰めする(%-16s はバイト数で数えるので日本語で崩れる)。
func pad(s string, w int) string {
	if n := w - runewidth.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
