package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfiles(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	creds := filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfg, []byte(`[default]
region = ap-northeast-1

[profile coco]
region = us-east-1
sso_start_url = https://example.awsapps.com/start

[profile no-region]
output = json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("[legacy-keys]\naws_access_key_id = AKIA...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", creds)

	got := Profiles()
	want := []Profile{
		{Name: "default", Region: "ap-northeast-1"},
		{Name: "coco", Region: "us-east-1"},
		{Name: "no-region"},
		{Name: "legacy-keys"}, // credentials 側だけのプロファイルも拾う
	}
	if len(got) != len(want) {
		t.Fatalf("profiles = %+v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("profiles[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestActive(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	if Active() != "default" {
		t.Fatalf("Active() = %q, want default", Active())
	}
	t.Setenv("AWS_PROFILE", "coco")
	if Active() != "coco" {
		t.Fatalf("Active() = %q, want coco", Active())
	}
}

func TestPrincipalARN(t *testing.T) {
	cases := map[string]string{
		// assumed-role の session ARN はシミュレーションに使えないのでロール ARN へ
		"arn:aws:sts::123456789012:assumed-role/MyRole/session-name": "arn:aws:iam::123456789012:role/MyRole",
		"arn:aws:iam::123456789012:user/rikuka":                      "arn:aws:iam::123456789012:user/rikuka",
		"arn:aws:iam::123456789012:role/Direct":                      "arn:aws:iam::123456789012:role/Direct",
	}
	for in, want := range cases {
		if got := principalARN(in, "123456789012"); got != want {
			t.Errorf("principalARN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActionsForPlan(t *testing.T) {
	if len(actionsFor(Plan{})) != 0 {
		t.Error("何も選ばなければ検査もしない")
	}
	role := actionsFor(Plan{Role: true})
	if len(role) == 0 || !strings.HasPrefix(role[0].Action, "iam:") {
		t.Fatalf("role plan = %+v", role)
	}
	full := actionsFor(Plan{Role: true, ECR: true, Base: true, StaticSync: true})
	has := func(a string) bool {
		for _, c := range full {
			if c.Action == a {
				return true
			}
		}
		return false
	}
	for _, a := range []string{
		"iam:CreateRole", "ecr:CreateRepository",
		"route53:ChangeResourceRecordSets", // base の DNS(証明書検証と alias)
		"acm:RequestCertificate", "cloudfront:CreateDistribution",
		"s3:PutObject", "s3:GetBucketLocation",
	} {
		if !has(a) {
			t.Errorf("full plan missing %q", a)
		}
	}
	// 全チェックに「何のために要るか」が付いている(名指しで直せるように)
	for _, c := range full {
		if c.Why == "" {
			t.Errorf("%s has no reason", c.Action)
		}
	}
}

func TestReportDenied(t *testing.T) {
	r := Report{Checks: []Check{
		{Action: "a", Allowed: true},
		{Action: "b", Allowed: false},
		{Action: "c", Allowed: false, Unknown: true}, // シミュレーション不可は失敗扱いにしない
	}}
	d := r.Denied()
	if len(d) != 1 || d[0].Action != "b" {
		t.Fatalf("Denied() = %+v", d)
	}
}

// #158: 使い捨て環境の道具なので「作れる」だけ確かめても検査として足りない。
// 作れるが消せない権限セット(PowerUser 等)は実在し、そのときの被害は
// down / reap が落ち続けて課金が残ること — 作った後にしか表に出ない。
func TestActionsForIncludesTeardown(t *testing.T) {
	full := actionsFor(Plan{Role: true, ECR: true, Base: true, StaticSync: true})
	has := func(a string) bool {
		for _, c := range full {
			if c.Action == a {
				return true
			}
		}
		return false
	}
	for _, a := range []string{
		// IAM は「作れるが消せない」の代表格
		"iam:DeleteRole", "iam:DetachRolePolicy",
		"ecr:DeleteRepository",
		"cloudformation:DeleteStack", "cloudfront:DeleteDistribution", "s3:DeleteBucket",
		"s3:DeleteObject",
	} {
		if !has(a) {
			t.Errorf("撤収に要る %q が検査対象に入っていない", a)
		}
	}
	// 壊す側には印が付いていること(出力で作る側と分けるため)
	for _, c := range full {
		wantTeardown := strings.Contains(c.Action, ":Delete") || strings.Contains(c.Action, ":Detach")
		if c.Teardown != wantTeardown {
			t.Errorf("%s: Teardown = %v (want %v)", c.Action, c.Teardown, wantTeardown)
		}
	}
}

// 何も選ばなければ検査もしない、は撤収権限を足しても変わらない。
func TestEmptyPlanStillChecksNothing(t *testing.T) {
	if got := actionsFor(Plan{}); len(got) != 0 {
		t.Errorf("Plan{} で %d 件検査している: %+v", len(got), got)
	}
}

func TestDeniedSplitByPhase(t *testing.T) {
	r := Report{Checks: []Check{
		{Action: "iam:CreateRole", Allowed: true},
		{Action: "iam:DeleteRole", Teardown: true},                 // 拒否
		{Action: "ecr:CreateRepository"},                           // 拒否
		{Action: "s3:DeleteBucket", Teardown: true, Unknown: true}, // 判定不能は混ぜない
	}}
	if got := len(r.Denied()); got != 2 {
		t.Errorf("Denied = %d (want 2)", got)
	}
	if got := r.DeniedTeardown(); len(got) != 1 || got[0].Action != "iam:DeleteRole" {
		t.Errorf("DeniedTeardown = %+v", got)
	}
	if got := r.DeniedSetup(); len(got) != 1 || got[0].Action != "ecr:CreateRepository" {
		t.Errorf("DeniedSetup = %+v", got)
	}
}
