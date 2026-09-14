// Package recommend は「このリポジトリならどの入口(配信の形)が良いか」を
// 決める純関数(#107)。appscan の Facts と、リポジトリからは分からない事情
// (認証が要るか・既存ベースがあるか)だけを見て、選択と**選ばなかった理由**を返す。
//
// AWS も設定ファイルも触らない。判定を独立させておくと表駆動テストで固定でき、
// 将来 `kagerou recommend --json` として切り出せる。
package recommend

import "github.com/rikukadev/kagerou/internal/appscan"

// Entrypoint は環境を公開する形。
type Entrypoint string

const (
	// Static は S3 + CloudFront。compute を持たない(SSG / CSR の SPA)。
	Static Entrypoint = "static"
	// Lambda は Lambda + Web Adapter。単一コンテナ、アイドル $0。
	// 入口は Function URL か共有 ALB(ドメインがあれば後者が既定。EntryNote 参照)。
	Lambda Entrypoint = "lambda"
	// APIGateway は HTTP API + VPC Link + ECS。固定費ゼロだがリクエスト 30 秒上限で、
	// ブラウザのログインリダイレクトができない(認証ありでは選べない)。
	APIGateway Entrypoint = "apigateway"
	// ALB は共有 ALB。後ろは ECS でも Lambda でもよい(#131 以降、独自ドメインで
	// 配るなら lambda + ALB が init の既定)。固定費(月 18 ドル前後、共有)と
	// 引き換えに authenticate-oidc がコード 0 行、WebSocket と長い処理に耐える。
	// **既に ALB があるなら固定費は増えない** — 環境が足すのはリスナールールと
	// ターゲットグループだけで、どちらも無料。
	ALB Entrypoint = "alb"
	// EdgeAuth は CloudFront + Lambda@Edge。固定費ゼロで static も守れるが、
	// 認証は自前実装で、オリジン応答は ~60 秒が上限。
	EdgeAuth Entrypoint = "edge"
)

// Options はリポジトリから分からない事情。
type Options struct {
	// Auth はプレビューに認証(Google OIDC 等)が要るか。
	Auth bool
	// ExistingALB は共有 ALB ベースが既にあるか(あれば相乗りが安い)。
	ExistingALB bool
	// CustomDomain は独自ドメインで配るか。#131 以降、ドメインがあれば init の
	// 入口は共有 ALB になる = **固定費が 1 本乗る**。compute(lambda)のアイドル
	// $0 とは別勘定なので、ここを知らないと推薦の理由と生成物が食い違う(#172)。
	CustomDomain bool
	// AllowFixedCost が false なら、固定費のある入口(ALB)を既定にしない。
	// 既に ALB がある場合は固定費が増えないので、この指定に関わらず相乗りできる。
	AllowFixedCost bool
}

// Candidate は 1 つの選択肢と、それが既定になった/ならなかった理由。
type Candidate struct {
	Entrypoint Entrypoint
	Reason     string // なぜ選ばれたか / なぜ選ばれなかったか(1 行)
	Usable     bool   // 要件を満たせるか(満たせないものも理由付きで見せる)
}

// Choice は推薦結果。Default が既定で、Candidates は表示順(既定が先頭)。
type Choice struct {
	Default    Entrypoint
	Candidates []Candidate
	// EntryNote は compute の選択とは別に決まる「入口」の注記(#152)。
	// Default は compute の軸で選ぶので、共有 ALB の相乗りのように
	// **compute を変えずに入口だけ変わる**話はここに出す。空なら注記なし。
	EntryNote string
}

// Entry は Facts と Options から入口を推薦する。
//
// 方針(#107):
//   - URL は常に Host ベース(パス分けはアプリの挙動に影響が大きいので採らない)
//   - 認証が要るなら ALB(コード 0 行)か CloudFront+Lambda@Edge(固定費ゼロ)の
//     どちらかに寄せる。API Gateway はブラウザのログインができないので外す
//   - それ以外は「安くて早い」順に降り、その構成では無理な理由があるときだけ次へ
func Entry(f appscan.Facts, o Options) Choice {
	multi := f.Services > 1

	switch {
	case !HasCompute(f):
		// 静的のみ。認証が要るなら ALB は S3 を守れないので Lambda@Edge しかない
		if o.Auth {
			return choice(EdgeAuth,
				cand(EdgeAuth, true, "静的配信に認証をかけられるのはここだけ(ALB は S3 を守れない)"),
				cand(Static, false, "認証が要るので素の CloudFront 配信は選べない"),
				cand(ALB, false, "ALB は S3 を直接守れない"),
			)
		}
		return choice(Static,
			cand(Static, true, "サーバが見つからない。最安・最速(環境は S3 プレフィックスだけ)"),
			cand(Lambda, false, "compute が要らないので不要"),
		)

	case o.Auth:
		// 認証あり: ALB か Lambda@Edge。realtime / 既存 ALB / 固定費許容で分岐
		switch {
		case f.Realtime:
			return choice(ALB,
				cand(ALB, true, "WebSocket/SSE を検出。authenticate-oidc がコード 0 行で、応答時間の上限も無い"),
				cand(EdgeAuth, false, "固定費ゼロだが、CloudFront のオリジン応答上限(〜60 秒)で WebSocket が切れる"),
				cand(APIGateway, false, "ブラウザのログインリダイレクトができない(JWT 検証のみ)"),
			)
		case o.ExistingALB:
			return choice(ALB,
				cand(ALB, true, "共有 ALB が既にある。相乗りなら固定費は増えず、認証もコード 0 行"),
				cand(EdgeAuth, false, "固定費ゼロだが、認証を自前実装することになる"),
			)
		case !o.AllowFixedCost:
			return choice(EdgeAuth,
				cand(EdgeAuth, true, "固定費ゼロ。static も同じ認証で守れる(認証は自前実装)"),
				cand(ALB, false, "認証はコード 0 行だが、月 18 ドル前後の固定費がかかる"),
				cand(APIGateway, false, "ブラウザのログインリダイレクトができない"),
			)
		default:
			return choice(ALB,
				cand(ALB, true, "authenticate-oidc でコード 0 行。固定費は共有 ALB 1 本ぶん"),
				cand(EdgeAuth, false, "固定費ゼロだが、認証を自前実装することになる"),
				cand(APIGateway, false, "ブラウザのログインリダイレクトができない"),
			)
		}

	case f.Realtime:
		// 認証なし + WebSocket。上限のある入口は使えない
		return choice(ALB,
			cand(ALB, true, "WebSocket/SSE を検出。応答時間に上限のない入口が要る"),
			cand(APIGateway, false, "リクエスト 30 秒上限で WebSocket が切れる"),
			cand(Lambda, false, "同上(Function URL も長時間の双方向通信には向かない)"),
		)

	case multi:
		if o.ExistingALB {
			// 固定費が増えないなら ALB の唯一の欠点が消える。複数サービスは
			// ホストで分けられる(#137)ので、30 秒上限も背負わずに済む
			return choice(ALB,
				cand(ALB, true, "共有 ALB が既にある。相乗りなら固定費は増えず、サービスをホストで分けられる"),
				cand(APIGateway, false, "固定費ゼロだが、リクエストは 30 秒まで"),
				cand(Lambda, false, "単一コンテナ向け(複数サービスを検出)"),
			)
		}
		return choice(APIGateway,
			cand(APIGateway, true, "複数サービスを固定費ゼロで動かせる(リクエストは 30 秒まで)"),
			cand(ALB, false, "上限は無いが、月 18 ドル前後の固定費がかかる"),
			cand(Lambda, false, "単一コンテナ向け(複数サービスを検出)"),
		)

	default:
		// compute の選択はどの枝でも lambda(単一コンテナ)。変わるのは入口と、
		// **入口にかかる金**。compute のアイドル $0 と入口の固定費は別勘定なので、
		// 1 行にまとめない(まとめていたのが #172 の食い違い)
		switch {
		case o.ExistingALB:
			c := choice(Lambda,
				cand(Lambda, true, "単一コンテナ。アイドル $0 で、環境も 1 スタックで済む"),
				cand(ALB, true, "共有 ALB が既にある。相乗りなら固定費は増えず、独自ドメインで出せる"),
				cand(APIGateway, false, "複数サービスではないので ECS を持ち出す必要がない"),
			)
			c.EntryNote = "alb — 共有 ALB に相乗り(固定費は増えない)。Function URL で出すなら entrypoint: apigateway"
			return c

		case o.CustomDomain:
			// ドメインがあれば init は ALB を入口にする(#131)。共有 ALB が
			// まだ無いので、**ここで 1 本立つ** = 固定費が発生する。
			// 「lambda = 固定費ゼロ」と読んだまま init すると話が違う
			c := choice(Lambda,
				cand(Lambda, true, "単一コンテナ。compute はアイドル $0(入口の固定費は別)"),
				cand(ALB, true, "独自ドメインで配るなら共有 ALB を 1 本立てる。月 $18 前後を全環境で共有する"),
				cand(APIGateway, false, "生の execute-api URL でよければ固定費ゼロ(独自ドメインは付けられない)"),
			)
			c.EntryNote = "alb — 共有 ALB を 1 本立てる(deploy/alb-base.yaml、月 $18 前後・全環境で共有)。" +
				"固定費を避けるなら entrypoint: apigateway = 生の execute-api URL"
			return c

		default:
			// ドメインが無ければ入口は生の execute-api / Function URL。
			// このときだけ「アイドル $0」が構成全体について本当になる
			return choice(Lambda,
				cand(Lambda, true, "単一コンテナ。アイドル $0 で、環境も 1 スタックで済む"),
				cand(APIGateway, false, "複数サービスではないので ECS を持ち出す必要がない"),
				cand(ALB, false, "同上。固定費もかかる"),
			)
		}
	}
}

// HasCompute は「サーバがあるか」。入口の形(Dockerfile / ポート / サービス数)
// だけに頼らない: コンテナ化していない構成(zip Lambda 等)ではそのどれも空に
// なることがある。DB ドライバと WebSocket 依存は静的配信では説明が付かないので、
// それ自体が compute の証拠として使える。
func HasCompute(f appscan.Facts) bool {
	return f.HasDockerfile || f.AppPort != "" || f.Services > 0 ||
		f.DBDriver != "" || f.Realtime
}

func cand(e Entrypoint, usable bool, reason string) Candidate {
	return Candidate{Entrypoint: e, Usable: usable, Reason: reason}
}

func choice(def Entrypoint, cs ...Candidate) Choice {
	return Choice{Default: def, Candidates: cs}
}

// Reason は候補の理由を引く(UI で説明を出すため)。
func (c Choice) Reason(e Entrypoint) string {
	for _, x := range c.Candidates {
		if x.Entrypoint == e {
			return x.Reason
		}
	}
	return ""
}
