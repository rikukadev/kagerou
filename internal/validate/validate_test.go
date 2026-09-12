package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/config"
)

const goodTemplate = `
AWSTemplateFormatVersion: "2010-09-09"
Transform: AWS::Serverless-2016-10-31
Parameters:
  EnvKagerouEnv: { Type: String }
  EnvDbHost: { Type: String, Default: "" }
  EnvAllowedOrigins: { Type: String, Default: "" }
Resources:
  Fn:
    Type: AWS::Serverless::Function
    Properties:
      Environment:
        Variables:
          DB_HOST: !Ref EnvDbHost
          URL: !Sub "https://${Fn}"
Outputs:
  KagerouUrl:
    Value: !Sub "https://example.com"
`

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "template.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func msgs(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(string(f.Level) + ": " + f.Msg + "\n")
	}
	return b.String()
}

func TestRunGoodTemplate(t *testing.T) {
	cfg := config.Config{Env: map[string]string{"KAGEROU_ENV": "x", "DB_HOST": "h", "ALLOWED_ORIGINS": "o"}}
	fs, err := Run(cfg, write(t, goodTemplate), "pr-42")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Fatalf("want no findings, got:\n%s", msgs(fs))
	}
}

func TestRunMissingEnvParam(t *testing.T) {
	cfg := config.Config{Env: map[string]string{"DB_USER": "u"}} // EnvDbUser 未宣言
	fs, err := Run(cfg, write(t, goodTemplate), "")
	if err != nil {
		t.Fatal(err)
	}
	if !HasErrors(fs) || !strings.Contains(msgs(fs), "EnvDbUser") {
		t.Fatalf("want EnvDbUser error, got:\n%s", msgs(fs))
	}
}

func TestRunUndeclaredParamWithoutDefaultWarns(t *testing.T) {
	tpl := strings.Replace(goodTemplate, `EnvDbHost: { Type: String, Default: "" }`, `EnvDbHost: { Type: String }`, 1)
	cfg := config.Config{Env: map[string]string{"KAGEROU_ENV": "x"}} // DB_HOST を渡さない
	fs, err := Run(cfg, write(t, tpl), "")
	if err != nil {
		t.Fatal(err)
	}
	if HasErrors(fs) {
		t.Fatalf("should be warn, not error:\n%s", msgs(fs))
	}
	if !strings.Contains(msgs(fs), "EnvDbHost") {
		t.Fatalf("want EnvDbHost warn, got:\n%s", msgs(fs))
	}
}

func TestRunMissingURLOutput(t *testing.T) {
	tpl := strings.Replace(goodTemplate, "KagerouUrl", "Url", 1)
	fs, err := Run(config.Config{}, write(t, tpl), "")
	if err != nil {
		t.Fatal(err)
	}
	if !HasErrors(fs) || !strings.Contains(msgs(fs), "KagerouUrl") {
		t.Fatalf("want URL output error, got:\n%s", msgs(fs))
	}
	// PreviewUrl でも通る
	tpl2 := strings.Replace(goodTemplate, "KagerouUrl", "PreviewUrl", 1)
	fs2, _ := Run(config.Config{}, write(t, tpl2), "")
	if HasErrors(fs2) {
		t.Fatalf("PreviewUrl should be accepted:\n%s", msgs(fs2))
	}
}

func TestRunBadNameSuggests(t *testing.T) {
	fs, err := Run(config.Config{}, write(t, goodTemplate), "feature/Foo_Bar")
	if err != nil {
		t.Fatal(err)
	}
	if !HasErrors(fs) || !strings.Contains(msgs(fs), `"feature-foo-bar"`) {
		t.Fatalf("want normalized suggestion, got:\n%s", msgs(fs))
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"feature/Foo_Bar": "feature-foo-bar",
		"PR-42":           "pr-42",
		"42-abc":          "abc",
		"日本語":             "",
		"a--b__c":         "a-b-c",
	}
	for in, want := range cases {
		if got := config.NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
