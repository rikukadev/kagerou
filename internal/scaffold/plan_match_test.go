package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plan.go の冒頭は「見せている内容と実際に作るものがずれるのが一番まずい」と
// 書いているが、それを機械で守るものが無かった。#131 で既定が共有 ALB になった
// のに plan が Lambda + HTTP API のままで、**固定費のある ALB が画面に一度も
// 現れない**状態が残っていた(#183)。ここで固定する。
func TestPlanMatchesGeneratedFiles(t *testing.T) {
	cases := []struct {
		name string
		p    Params
		// wantShared は生成物に対応してプランへ必ず出る語
		wantShared []string
		// denyEstimate は費用のまとめに出てはいけない語
		denyEstimate []string
	}{
		{
			name: "lambda × alb(既定): ALB が画面に出る",
			p:    Params{Compute: "lambda", Entrypoint: "alb", Domain: "relay.example.com"},
			// deploy/alb-base.yaml が生成されるので、プランにも費用にも出るべき
			wantShared:   []string{"ALB"},
			denyEstimate: []string{"固定の月額はどれにも無い"},
		},
		{
			name:         "ecs × apigateway: 固定費は無いが Fargate は課金される",
			p:            Params{Compute: "ecs", Entrypoint: "apigateway", Domain: "relay.example.com"},
			wantShared:   []string{"VPC Link"},
			denyEstimate: []string{"固定の月額はどれにも無い"},
		},
		{
			name:         "lambda × apigateway: 固定費は本当に無い",
			p:            Params{Compute: "lambda", Entrypoint: "apigateway"},
			denyEstimate: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.Project, p.Region = "relay", "ap-northeast-1"
			dir := t.TempDir()
			if _, err := Run(dir, p, AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			plan := BuildAWSPlan(p, Detection{})
			rendered := plan.Render()

			// 生成された共有ベースは、必ずプランのどこかに現れる
			for _, base := range []string{"alb-base.yaml", "apigw-base.yaml"} {
				if _, err := os.Stat(filepath.Join(dir, "deploy", base)); err != nil {
					continue
				}
				if !strings.Contains(rendered, "deploy/"+base) {
					t.Errorf("%s を生成するのにプランに出ていない:\n%s", base, rendered)
				}
			}
			// 固定費のある資源は **費用の表** にも出る。Shared に出るだけだと、
			// 「費用の目安」を読んだ人には見えない(代入で消えていた #183)
			if p.ALB() {
				var found bool
				for _, c := range plan.Cost {
					if strings.Contains(c.Item, "ALB") {
						found = true
					}
				}
				if !found {
					t.Errorf("ALB が費用の表に無い: %+v", plan.Cost)
				}
			}
			for _, want := range tc.wantShared {
				if !strings.Contains(rendered, want) {
					t.Errorf("プランに %q が無い:\n%s", want, rendered)
				}
			}
			for _, deny := range tc.denyEstimate {
				if strings.Contains(plan.Estimate, deny) {
					t.Errorf("固定費のある構成で %q と言っている: %s", deny, plan.Estimate)
				}
			}
		})
	}
}

// PerEnv は「環境ごとに作られるもの」。作られないものを挙げない。
func TestPlanPerEnvDoesNotNameAbsentResources(t *testing.T) {
	p := Params{Project: "relay", Region: "r", Compute: "lambda",
		Entrypoint: "alb", Domain: "relay.example.com"}
	dir := t.TempDir()
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	plan := BuildAWSPlan(p, Detection{})

	// ALB 入口では HTTP API を作らない。テンプレートにも無い
	if strings.Contains(tp, "AWS::Serverless::HttpApi") {
		t.Fatal("前提が崩れている(ALB 入口では HTTP API を作らないはず)")
	}
	if strings.Contains(plan.PerEnv, "HTTP API") {
		t.Errorf("作られない HTTP API をプランが挙げている: %s", plan.PerEnv)
	}
	if !strings.Contains(plan.PerEnv, "リスナールール") {
		t.Errorf("実際に作るリスナールールがプランに無い: %s", plan.PerEnv)
	}
}
