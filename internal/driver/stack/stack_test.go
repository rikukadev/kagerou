package stack

// moto server に対する結合テスト(DESIGN.md §9 の第2層)。
// AWS_ENDPOINT_URL 未設定時は skip する。ローカルでは `make test-aws` で実行。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
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
		Source:       "github_pr://rikukadev/todo/1",
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

func TestListReturnsOnlyManaged(t *testing.T) {
	d, ctx := testDriver(t)

	// kagerou 管理のスタック
	managed := "kagerou-test-list-managed"
	t.Cleanup(func() { _ = d.Down(ctx, managed) })
	if _, err := d.Up(ctx, UpInput{StackName: managed, Name: "list-managed", TemplateBody: testTemplate}); err != nil {
		t.Fatal(err)
	}

	// kagerou 管理でない素のスタック
	raw := "kagerou-test-list-raw"
	body := testTemplate
	t.Cleanup(func() { _ = d.Down(ctx, raw) })
	if _, err := d.cfn.CreateStack(ctx, &cloudformation.CreateStackInput{StackName: &raw, TemplateBody: &body}); err != nil {
		t.Fatal(err)
	}

	infos, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, info := range infos {
		names[info.StackName] = true
		if info.Tags[TagManaged] != "true" {
			t.Errorf("unmanaged stack in list: %s", info.StackName)
		}
	}
	if !names[managed] {
		t.Errorf("managed stack missing from list: %v", names)
	}
	if names[raw] {
		t.Errorf("raw stack should not be listed: %v", names)
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

func TestExpired(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	mk := func(v string) *Info { return &Info{Tags: map[string]string{TagExpiresAt: v}} }
	cases := []struct {
		info  *Info
		grace time.Duration
		want  bool
	}{
		{mk("2026-09-12T11:00:00Z"), 0, true},          // 期限切れ
		{mk("2026-09-12T13:00:00Z"), 0, false},         // まだ生きてる
		{mk("2026-09-12T11:30:00Z"), time.Hour, false}, // grace 内
		{mk("2026-09-12T10:00:00Z"), time.Hour, true},  // grace を過ぎた
		{mk(TTLNoneTagValue), 0, false},                // 明示無期限
		{mk("broken"), 0, false},                       // 壊れたタグは消さない
		{&Info{Tags: map[string]string{}}, 0, false},   // タグ欠落
	}
	for i, tc := range cases {
		if got := tc.info.Expired(now, tc.grace); got != tc.want {
			t.Errorf("case %d: Expired = %v, want %v", i, got, tc.want)
		}
	}
}

func TestUpClampsTTLToMaxLifetime(t *testing.T) {
	d, ctx := testDriver(t)
	stackName := "kagerou-test-clamp"
	t.Cleanup(func() { _ = d.Down(ctx, stackName) })

	far := time.Now().Add(MaxLifetime + 240*time.Hour) // 上限超え
	info, err := d.Up(ctx, UpInput{StackName: stackName, Name: "clamp", TemplateBody: testTemplate, ExpiresAt: &far})
	if err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, info.Tags[TagExpiresAt])
	if err != nil {
		t.Fatalf("expires-at tag unparsable: %q", info.Tags[TagExpiresAt])
	}
	if exp.After(time.Now().Add(MaxLifetime + time.Minute)) {
		t.Fatalf("expires-at not clamped: %s", exp)
	}
}

func TestReapLifecycle(t *testing.T) {
	d, ctx := testDriver(t)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(24 * time.Hour)

	expired := "kagerou-test-reap-expired"
	alive := "kagerou-test-reap-alive"
	t.Cleanup(func() { _ = d.Down(ctx, expired); _ = d.Down(ctx, alive) })
	if _, err := d.Up(ctx, UpInput{StackName: expired, Name: "reap-expired", TemplateBody: testTemplate, ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Up(ctx, UpInput{StackName: alive, Name: "reap-alive", TemplateBody: testTemplate, ExpiresAt: &future}); err != nil {
		t.Fatal(err)
	}

	infos, err := d.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, info := range infos {
		if info.Expired(now, 0) {
			if err := d.Down(ctx, info.StackName); err != nil {
				t.Fatal(err)
			}
		}
	}

	if status, _ := d.stackStatus(ctx, expired); status != "" {
		t.Errorf("expired stack not reaped: %s", status)
	}
	if status, _ := d.stackStatus(ctx, alive); status == "" {
		t.Error("alive stack was reaped")
	}
}

func TestValidateSource(t *testing.T) {
	for _, ok := range []string{"", "github_pr://rikukadev/todo/42", "manual @rikuka 2026-09-12", "a=b.c:d/e+f-g"} {
		if err := ValidateSource(ok); err != nil {
			t.Errorf("ValidateSource(%q) = %v, want nil", ok, err)
		}
	}
	// JSON は CFN タグ値に入らない(実 AWS で 400。moto は通すので単体で守る)
	for _, ng := range []string{`{"type":"github_pr"}`, "改行\nあり", strings.Repeat("a", 257)} {
		if err := ValidateSource(ng); err == nil {
			t.Errorf("ValidateSource(%q): want error", ng)
		}
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
		"CREATE_COMPLETE":                     "ready",
		"UPDATE_COMPLETE":                     "ready",
		"CREATE_IN_PROGRESS":                  "creating",
		"UPDATE_IN_PROGRESS":                  "updating",
		"DELETE_IN_PROGRESS":                  "deleting",
		"ROLLBACK_COMPLETE":                   "failed",
		"UPDATE_ROLLBACK_COMPLETE":            "failed",
		"CREATE_FAILED":                       "failed",
		"UPDATE_COMPLETE_CLEANUP_IN_PROGRESS": "updating",
	}
	for status, want := range cases {
		i := &Info{Status: status}
		if got := i.State(); got != want {
			t.Errorf("State(%s) = %q, want %q", status, got, want)
		}
	}
}

func TestUpWithPredeterminedURL(t *testing.T) {
	d, ctx := testDriver(t)
	stackName := "kagerou-test-url"
	t.Cleanup(func() { _ = d.Down(ctx, stackName) })

	// Parameters 直下に EnvKagerouUrl を挿す(受け取り口の宣言)
	tpl := strings.Replace(testTemplate, "Parameters:\n", "Parameters:\n  EnvKagerouUrl:\n    Type: String\n    Default: \"\"\n", 1)

	url := "https://pr-9.preview.example.test"
	info, err := d.Up(ctx, UpInput{
		StackName:    stackName,
		Name:         "pr-9",
		TemplateBody: tpl,
		URL:          url,
	})
	if err != nil {
		t.Fatal(err)
	}
	// タグに確定 URL が入り、解決順の先頭になる(Output より優先)
	if info.Tags[TagURL] != url {
		t.Fatalf("kagerou:url tag = %q, want %q", info.Tags[TagURL], url)
	}
	if got, ok := info.EnvironmentURL(); !ok || got != url {
		t.Fatalf("EnvironmentURL = %q, want %q (tag should win over output)", got, url)
	}
	// EnvKagerouUrl パラメータにも届いている
	out, err := d.cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: &stackName})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range out.Stacks[0].Parameters {
		got[aws.ToString(p.ParameterKey)] = aws.ToString(p.ParameterValue)
	}
	if got["EnvKagerouUrl"] != url {
		t.Fatalf("EnvKagerouUrl param = %q, want %q", got["EnvKagerouUrl"], url)
	}
}

func TestEnvironmentURLFallsBackToOutputs(t *testing.T) {
	i := &Info{Tags: map[string]string{}, Outputs: map[string]string{"KagerouUrl": "https://out"}}
	if u, ok := i.EnvironmentURL(); !ok || u != "https://out" {
		t.Fatalf("fallback broken: %q", u)
	}
}

func TestUpOwnership(t *testing.T) {
	d, ctx := testDriver(t)

	// 1) 自分で up → kagerou:owner が付く。同じ呼び出し元の再 up は上書きできる
	mine := UpInput{StackName: "kagerou-test-own-1", Name: "own-1", TemplateBody: testTemplate, Version: "test"}
	info, err := d.Up(ctx, mine)
	if err != nil {
		t.Fatalf("up (create): %v", err)
	}
	if info.Tags[TagOwner] == "" {
		t.Fatalf("kagerou:owner should be stamped: %v", info.Tags)
	}
	if _, err := d.Up(ctx, mine); err != nil {
		t.Fatalf("same-owner re-up should overwrite: %v", err)
	}
	t.Cleanup(func() { _ = d.Down(ctx, mine.StackName) })

	// 2) 他人 owner のスタックを直接作っておく → 同名 up は名前衝突エラー
	foreign := "kagerou-test-own-2"
	_, err = d.cfn.CreateStack(ctx, &cloudformation.CreateStackInput{
		StackName:    aws.String(foreign),
		TemplateBody: aws.String(testTemplate),
		Tags: []cfntypes.Tag{
			{Key: aws.String(TagManaged), Value: aws.String("true")},
			{Key: aws.String(TagName), Value: aws.String("own-2")},
			{Key: aws.String(TagOwner), Value: aws.String("arn:aws:iam::999999999999:user/someone-else")},
		},
	})
	if err != nil {
		t.Fatalf("pre-create foreign stack: %v", err)
	}
	t.Cleanup(func() { _ = d.Down(ctx, foreign) })

	_, err = d.Up(ctx, UpInput{StackName: foreign, Name: "own-2", TemplateBody: testTemplate, Version: "test"})
	if err == nil || !strings.Contains(err.Error(), "someone-else") {
		t.Fatalf("foreign env must be rejected with the owner named, got: %v", err)
	}
}

const peerTemplate = `
AWSTemplateFormatVersion: "2010-09-09"
Parameters:
  EnvPeerEnv: { Type: String, Default: "" }
  EnvPeerUrl: { Type: String, Default: "" }
Resources:
  Wait:
    Type: AWS::CloudFormation::WaitConditionHandle
Outputs:
  KagerouUrl: { Value: "https://peer.example" }
`

func TestUpDeliversPeerParams(t *testing.T) {
	d, ctx := testDriver(t)
	name := "kagerou-test-peer-1"
	_, err := d.Up(ctx, UpInput{
		StackName: name, Name: "peer-1", TemplateBody: peerTemplate, Version: "test",
		PeerEnv: "pr-42", PeerURL: "https://pr-42.pub-demo.example.com",
	})
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	t.Cleanup(func() { _ = d.Down(ctx, name) })
	out, err := d.cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(name)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range out.Stacks[0].Parameters {
		got[aws.ToString(p.ParameterKey)] = aws.ToString(p.ParameterValue)
	}
	if got["EnvPeerEnv"] != "pr-42" || got["EnvPeerUrl"] != "https://pr-42.pub-demo.example.com" {
		t.Fatalf("peer params not delivered: %v", got)
	}
}
