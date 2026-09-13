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
