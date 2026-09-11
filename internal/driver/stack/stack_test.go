package stack

// moto server に対する結合テスト(DESIGN.md §9 の第2層)。
// AWS_ENDPOINT_URL 未設定時は skip する。ローカルでは `make test-aws` で実行。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

const testTemplate = `
AWSTemplateFormatVersion: "2010-09-09"
Parameters:
  Greeting:
    Type: String
    Default: hello
  EnvDbUser:
    Type: String
    Default: ""
  EnvDbHost:
    Type: String
    Default: ""
Resources:
  Topic:
    Type: AWS::SNS::Topic
Outputs:
  KagerouUrl:
    Value: !Sub "https://${AWS::StackName}.example.test/"
`

func testDriver(t *testing.T) (*Driver, context.Context) {
	t.Helper()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		t.Skip("AWS_ENDPOINT_URL 未設定(moto なし)のため skip — make test-aws で実行する")
	}
	ctx := context.Background()
	d, err := New(ctx, os.Getenv("AWS_REGION"))
	if err != nil {
		t.Fatal(err)
	}
	return d, ctx
}

func TestUpDownLifecycle(t *testing.T) {
	d, ctx := testDriver(t)
	stackName := "kagerou-test-pr-1"
	expires := time.Now().Add(72 * time.Hour)

	in := UpInput{
		StackName:    stackName,
		Name:         "pr-1",
		Project:      "todo",
		TemplateBody: testTemplate,
		Params:       map[string]string{"Greeting": "hi"},
		Env:          map[string]string{"DB_USER": "dev@pr-1", "DB_HOST": "db.test"},
		ExpiresAt:    &expires,
		Source:       `{"type":"github_pr","repo":"rikukadev/todo","ref":"1"}`,
		Version:      "test",
		Tags:         map[string]string{"team": "rikuka"},
	}

	// create
	info, err := d.Up(ctx, in)
	if err != nil {
		t.Fatalf("up (create): %v", err)
	}
	if url, ok := URL(info.Outputs); !ok || url == "" {
		t.Fatalf("URL output not found: %v", info.Outputs)
	}
	if info.State() != "ready" {
		t.Fatalf("state = %q (status %s), want ready", info.State(), info.Status)
	}

	// 契約タグ(CONTRACT §1)が付いている
	for k, want := range map[string]string{
		TagManaged: "true",
		TagName:    "pr-1",
		TagProject: "todo",
		TagDriver:  DriverName,
		"team":     "rikuka",
	} {
		if info.Tags[k] != want {
			t.Errorf("tag %s = %q, want %q", k, info.Tags[k], want)
		}
	}
	if info.Tags[TagExpiresAt] == "" || info.Tags[TagExpiresAt] == TTLNoneTagValue {
		t.Errorf("expires-at tag missing: %q", info.Tags[TagExpiresAt])
	}

	// env が Env<Key> パラメータとして届いている(CONTRACT §4)
	out, err := d.cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: &stackName})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range out.Stacks[0].Parameters {
		got[aws.ToString(p.ParameterKey)] = aws.ToString(p.ParameterValue)
	}
	if got["EnvDbUser"] != "dev@pr-1" || got["EnvDbHost"] != "db.test" || got["Greeting"] != "hi" {
		t.Fatalf("params not delivered: %v", got)
	}

	// 2 回目の up は冪等(no-change update でも Info が返る)
	info2, err := d.Up(ctx, in)
	if err != nil {
		t.Fatalf("up (idempotent): %v", err)
	}
	if _, ok := URL(info2.Outputs); !ok {
		t.Fatalf("URL output lost on second up: %v", info2.Outputs)
	}

	// down → 2 回目も成功(冪等)
	if err := d.Down(ctx, stackName); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := d.Down(ctx, stackName); err != nil {
		t.Fatalf("down (idempotent): %v", err)
	}
	if status, err := d.stackStatus(ctx, stackName); err != nil || status != "" {
		t.Fatalf("stack still exists after down: status=%q err=%v", status, err)
	}
}

func TestUpRejectsUndeclaredEnv(t *testing.T) {
	d, ctx := testDriver(t)
	stackName := "kagerou-test-noenv"
	t.Cleanup(func() { _ = d.Down(ctx, stackName) })
	_, err := d.Up(ctx, UpInput{
		StackName:    stackName,
		Name:         "noenv",
		TemplateBody: testTemplate,
		Env:          map[string]string{"UNDECLARED_KEY": "x"},
	})
	if err == nil {
		t.Fatal("want error: env key without declared Env<Key> parameter")
	}
}

func TestEnvParamName(t *testing.T) {
	cases := map[string]string{
		"DB_HOST":  "EnvDbHost",
		"DB_USER":  "EnvDbUser",
		"PORT":     "EnvPort",
		"API_KEY2": "EnvApiKey2",
	}
	for in, want := range cases {
		got, err := EnvParamName(in)
		if err != nil || got != want {
			t.Errorf("EnvParamName(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "DB-HOST", "DB.HOST", "日本語"} {
		if _, err := EnvParamName(bad); err == nil {
			t.Errorf("EnvParamName(%q): want error", bad)
		}
	}
}

func TestURLFallback(t *testing.T) {
	if _, ok := URL(map[string]string{"Other": "x"}); ok {
		t.Fatal("URL should not match unrelated outputs")
	}
	if u, ok := URL(map[string]string{"PreviewUrl": "https://p"}); !ok || u != "https://p" {
		t.Fatal("PreviewUrl fallback broken")
	}
	if u, ok := URL(map[string]string{"PreviewUrl": "https://p", "KagerouUrl": "https://k"}); !ok || u != "https://k" {
		t.Fatal("KagerouUrl should win")
	}
}

func TestInfoState(t *testing.T) {
	cases := map[string]string{
		"CREATE_COMPLETE":                    "ready",
		"UPDATE_COMPLETE":                    "ready",
		"CREATE_IN_PROGRESS":                 "creating",
		"UPDATE_IN_PROGRESS":                 "updating",
		"DELETE_IN_PROGRESS":                 "deleting",
		"ROLLBACK_COMPLETE":                  "failed",
		"UPDATE_ROLLBACK_COMPLETE":           "failed",
		"CREATE_FAILED":                      "failed",
		"UPDATE_COMPLETE_CLEANUP_IN_PROGRESS": "updating",
	}
	for status, want := range cases {
		i := &Info{Status: status}
		if got := i.State(); got != want {
			t.Errorf("State(%s) = %q, want %q", status, got, want)
		}
	}
}
