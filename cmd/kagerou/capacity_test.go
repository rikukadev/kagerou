package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/quota"
	"github.com/rikukadev/kagerou/internal/scaffold"
)

// TestPerEnvMatchesGeneratedTemplate は、capacity の割り算の分母が
// **init の生成物と一致する**ことを固定する。
//
// #188 は分母が固定値 1 だったので、複数サービスの環境(サービスの数だけ
// ルールとターゲットグループを作る)で残り面数が N 倍に出ていた。分母は
// 推定ではなく、環境の実体である template.yaml から数える。
func TestPerEnvMatchesGeneratedTemplate(t *testing.T) {
	cases := []struct {
		name        string
		p           scaffold.Params
		wantRules   int
		wantTGs     int
		wantSource  string
		wantUsesALB bool
	}{
		{name: "単一サービス(既定)", wantRules: 1, wantTGs: 1, wantSource: "template", wantUsesALB: true,
			p: scaffold.Params{Project: "app", Region: "ap-northeast-1", Framework: "go",
				Compute: "lambda", Entrypoint: "alb", Domain: "app.example.com"}},
		{name: "3 サービス", wantRules: 3, wantTGs: 3, wantSource: "template", wantUsesALB: true,
			p: scaffold.Params{Project: "app", Region: "ap-northeast-1", Framework: "go",
				Compute: "lambda", Entrypoint: "alb", Domain: "app.example.com",
				Services: []string{"api", "web", "admin"}}},
		{name: "HTTP API 入口(ALB を使わない)", wantRules: 0, wantTGs: 0, wantSource: "template",
			p: scaffold.Params{Project: "app", Region: "ap-northeast-1", Framework: "go",
				Compute: "lambda", Entrypoint: "apigateway"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := scaffold.Run(dir, tc.p, scaffold.AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			got := perEnvFromTemplate(filepath.Join(dir, "template.yaml"), filepath.Join(dir, "kagerou.yaml"))
			if got.Rules != tc.wantRules || got.TargetGroups != tc.wantTGs {
				t.Errorf("1 環境あたり %d rules / %d TGs (want %d / %d)",
					got.Rules, got.TargetGroups, tc.wantRules, tc.wantTGs)
			}
			if got.Source != tc.wantSource {
				t.Errorf("出どころ = %q (want %q)", got.Source, tc.wantSource)
			}
			if got.UsesALB() != tc.wantUsesALB {
				t.Errorf("UsesALB = %v (want %v)", got.UsesALB(), tc.wantUsesALB)
			}
		})
	}
}

// TestPerEnvFallsBackToAssumed は、テンプレートが無いときに黙って 1 としない
// ことを固定する。仮定したまま数字だけ読まれるのが一番危ない。
func TestPerEnvFallsBackToAssumed(t *testing.T) {
	dir := t.TempDir()
	got := perEnvFromTemplate(filepath.Join(dir, "packaged.yaml"), filepath.Join(dir, "kagerou.yaml"))
	if got.Rules != 1 || got.TargetGroups != 1 {
		t.Errorf("仮定は単一サービスのはず: %+v", got)
	}
	if got.Source != "assumed" {
		t.Errorf("仮定であることが残っていない: %+v", got)
	}
}

func TestCapacityTextShowsTheDenominator(t *testing.T) {
	rep := quota.Report{
		Region: "ap-northeast-1",
		PerEnv: quota.PerEnv{Rules: 3, TargetGroups: 3, Source: "template"},
		Limits: []quota.Limit{
			{Name: "rules-per-application-load-balancer", Max: 100, Used: 12, HasUsed: true, PerEnv: 3},
		},
	}
	var b bytes.Buffer
	if err := writeCapacityText(&b, rep, "arn:listener"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"1 environment = 3 rules + 3 target groups", // 分母
		"counted in template.yaml",                  // その出どころ
		"room for 29 more environments",             // 割った後の数
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q が出ていない:\n%s", want, out)
		}
	}
}

func TestCapacityTextSaysWhenItAssumed(t *testing.T) {
	rep := quota.Report{
		Region: "ap-northeast-1",
		PerEnv: quota.Assumed(),
		Limits: []quota.Limit{
			{Name: "rules-per-application-load-balancer", Max: 100, Used: 12, HasUsed: true, PerEnv: 1},
		},
	}
	var b bytes.Buffer
	if err := writeCapacityText(&b, rep, "arn:listener"); err != nil {
		t.Fatal(err)
	}
	if out := b.String(); !strings.Contains(out, "assumed") {
		t.Errorf("仮定したことを言っていない:\n%s", out)
	}
}

// TestCapacityTextNoALB は ALB を使わない構成で「あと何面」を出さないことを見る。
func TestCapacityTextNoALB(t *testing.T) {
	rep := quota.Report{
		Region: "ap-northeast-1",
		PerEnv: quota.PerEnv{Source: "template"},
		Limits: []quota.Limit{{Name: "rules-per-application-load-balancer", Max: 100}},
	}
	var b bytes.Buffer
	if err := writeCapacityText(&b, rep, ""); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "does not use the shared ALB") {
		t.Errorf("ALB を使わないことを言っていない:\n%s", out)
	}
	if strings.Contains(out, "room for") {
		t.Errorf("枠を取らないのに残り面数を出している:\n%s", out)
	}
}

// 生成物を読む経路が壊れていないことの保険(テンプレートが読めれば nil にならない)。
func TestLoadTemplateFactsFindsGeneratedTemplate(t *testing.T) {
	dir := t.TempDir()
	p := scaffold.Params{Project: "app", Region: "ap-northeast-1", Framework: "go",
		Compute: "lambda", Entrypoint: "alb", Domain: "app.example.com"}
	if _, err := scaffold.Run(dir, p, scaffold.AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "template.yaml")); err != nil {
		t.Fatal(err)
	}
	// template には packaged.yaml(CI 生成物・ここには無い)を渡し、隣の
	// template.yaml に落ちることを見る
	if facts := loadTemplateFacts(filepath.Join(dir, "packaged.yaml"), filepath.Join(dir, "kagerou.yaml")); facts == nil {
		t.Fatal("設定の隣の template.yaml に落ちていない")
	}
}
