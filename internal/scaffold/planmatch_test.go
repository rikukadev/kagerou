package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanMatchesGeneratedTemplate は「AWS を作る前に見せるプラン」と「生成物」が
// ずれないことを機械で固定する。
//
// plan.go の冒頭に「見せている内容と実際に作るものがずれる」が一番まずいと
// 書いてあるのに、守っているのは人間の注意力だけだった。実際 #183 では既定構成
// (lambda × 共有 ALB)で
//
//	プラン    「Lambda / HTTP API」「固定の月額はどれにも無い」
//	生成物    ELBv2 が 2 リソース、deploy/alb-base.yaml(月 $18 前後)
//
// とずれ、**費用を少なく見せて** y を押させていた。文言のズレではなく安全側の
// 問題なので、ここで落とす。
func TestPlanMatchesGeneratedTemplate(t *testing.T) {
	det := Detection{AccountID: "123456789012", Owner: "acme", Repo: "todo-app"}
	base := func(p Params) Params {
		p.Project, p.Region = "web", "ap-northeast-1"
		return p
	}
	cases := []struct {
		name string
		p    Params
	}{
		{"lambda × 共有 ALB(独自ドメインの既定)", base(Params{
			Framework: "next", Compute: "lambda", Entrypoint: "alb", Domain: "web.example.com"})},
		{"lambda × 共有 ALB × 複数サービス", base(Params{
			Framework: "go", Compute: "lambda", Entrypoint: "alb", Domain: "web.example.com",
			Services: []string{"api", "web"}})},
		{"lambda × 生の execute-api(ドメイン無し)", base(Params{
			Framework: "go", Compute: "lambda", Entrypoint: "alb"})},
		{"ecs × 共有 ALB", base(Params{
			Framework: "go", Compute: "ecs", Entrypoint: "alb", Domain: "web.example.com"})},
		{"ecs × HTTP API + VPC Link", base(Params{
			Framework: "go", Compute: "ecs", Entrypoint: "apigateway", Domain: "web.example.com"})},
		{"static", base(Params{Framework: "astro", Driver: "static",
			Domain: "web.example.com", SetupBase: true})},
		{"認証あり", base(Params{Framework: "go", Compute: "lambda", Entrypoint: "alb",
			Domain: "web.example.com", Auth: true, AuthDomain: "example.com"})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Run(dir, tc.p, AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			plan := BuildAWSPlan(tc.p, det)
			out := plan.Render()

			// 1. 書き出した共有ベースは、自分で deploy する必要があることが読めること。
			//    テンプレートだけ生えて画面に出ないと、最初のプレビューが繋がらない
			//    時点で初めて気づくことになる。
			for _, f := range PlannedFiles(tc.p, AllTargets()) {
				if !strings.HasSuffix(f.Path, "-base.yaml") {
					continue
				}
				if !strings.Contains(out, f.Path) {
					t.Errorf("%s を書き出すのに、プランから deploy の必要が読めない:\n%s", f.Path, out)
				}
			}

			// 2. 環境ごとに作るものが template.yaml と一致すること。
			tpl := readFile(t, filepath.Join(dir, "template.yaml"))
			hasALB := strings.Contains(tpl, "AWS::ElasticLoadBalancingV2::")
			hasHTTPAPI := strings.Contains(tpl, "AWS::Serverless::HttpApi") ||
				strings.Contains(tpl, "AWS::ApiGatewayV2::Api")

			if hasALB && !strings.Contains(plan.PerEnv, "リスナールール") {
				t.Errorf("ALB のリスナールールを作るのに、環境ごとの説明に出ていない: %q", plan.PerEnv)
			}
			if hasHTTPAPI != strings.Contains(plan.PerEnv, "HTTP API") {
				t.Errorf("HTTP API を作る=%v なのに、環境ごとの説明は %q", hasHTTPAPI, plan.PerEnv)
			}

			// 3. 固定費のある構成で「固定の月額はどれにも無い」と言わないこと。
			//    ALB は作るのが環境側(ルール)でも、金を払うのは共有ベースなので、
			//    生成物に ELBv2 が出たら必ず固定費の話が要る。
			if hasALB {
				for _, want := range []string{"ALB", "$18"} {
					if !strings.Contains(out, want) {
						t.Errorf("ALB 入口なのに %q が出ていない:\n%s", want, out)
					}
				}
			}
			if len(fixedCosts(tc.p)) > 0 && strings.Contains(plan.Estimate, "固定の月額はどれにも無い") {
				t.Errorf("固定費があるのに無いと言っている: %q", plan.Estimate)
			}

			// 4. 作らないものを「作るもの」と取り壊し手順に混ぜないこと。
			//    共有ベースは init が deploy しないので、消し方を書く立場にない。
			for _, r := range plan.Shared {
				if strings.Contains(r.Kind, "ALB") || strings.Contains(r.Kind, "VPC Link") {
					t.Errorf("init が deploy しないものが「作るもの」に入っている: %+v", r)
				}
			}
			if td := strings.Join(plan.Teardown, "\n"); strings.Contains(td, "alb-base") ||
				strings.Contains(td, "apigw-base") {
				t.Errorf("init が作らないものが取り壊し手順に入っている:\n%s", td)
			}
			t.Log("\n" + out)
		})
	}
}

// fixedCosts は「この構成に固定費があるか」をテスト側から独立に判断する。
// plan.go と同じ条件を書くのは重複だが、実装の返り値をそのまま正解にすると
// 何も検証できない。
func fixedCosts(p Params) []string {
	var out []string
	if p.ALB() {
		out = append(out, "alb")
	}
	if p.Sashiki {
		out = append(out, "sashiki")
	}
	if p.Auth && p.ALB() {
		out = append(out, "secret")
	}
	return out
}

// TestPlanALBFixedCost は #183 の再現をそのまま固定する(既定構成での回帰防止)。
func TestPlanALBFixedCost(t *testing.T) {
	p := Params{Project: "web", Region: "ap-northeast-1", Framework: "next",
		Compute: "lambda", Entrypoint: "alb", Domain: "web.example.com"}
	out := BuildAWSPlan(p, Detection{AccountID: "123456789012", Repo: "web"}).Render()

	for _, want := range []string{
		"共有 ALB",               // 入口が画面に出る
		"月 $18 前後",             // いくらかかるか
		"deploy/alb-base.yaml", // 自分で deploy する必要
		"既にあれば不要",              // 2 人目以降は増えないこと
		"リスナールール",              // 環境ごとに作るもの
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q が出ていない:\n%s", want, out)
		}
	}
	// ALB 入口では HTTP API を作らない。作らないものを見せない
	if strings.Contains(out, "HTTP API") {
		t.Errorf("ALB 入口なのに HTTP API が出ている:\n%s", out)
	}
	t.Log("\n" + out)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}
