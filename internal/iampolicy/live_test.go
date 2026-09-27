package iampolicy

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

type fakeIAM struct {
	inline   map[string]string // policy name -> document
	attached map[string]string // policy name -> document
}

func (f fakeIAM) ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	var names []string
	for n := range f.inline {
		names = append(names, n)
	}
	return &iam.ListRolePoliciesOutput{PolicyNames: names}, nil
}

func (f fakeIAM) GetRolePolicy(_ context.Context, in *iam.GetRolePolicyInput, _ ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	return &iam.GetRolePolicyOutput{PolicyDocument: aws.String(f.inline[aws.ToString(in.PolicyName)])}, nil
}

func (f fakeIAM) ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	var out []iamtypes.AttachedPolicy
	for n := range f.attached {
		out = append(out, iamtypes.AttachedPolicy{
			PolicyName: aws.String(n),
			PolicyArn:  aws.String("arn:aws:iam::123456789012:policy/" + n),
		})
	}
	return &iam.ListAttachedRolePoliciesOutput{AttachedPolicies: out}, nil
}

func (f fakeIAM) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	return &iam.GetPolicyOutput{Policy: &iamtypes.Policy{
		Arn: in.PolicyArn, DefaultVersionId: aws.String("v1"),
	}}, nil
}

func (f fakeIAM) GetPolicyVersion(_ context.Context, in *iam.GetPolicyVersionInput, _ ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error) {
	name := aws.ToString(in.PolicyArn)
	name = name[strings.LastIndexByte(name, '/')+1:]
	return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{
		Document: aws.String(f.attached[name]),
	}}, nil
}

const inlineDoc = `{"Version":"2012-10-17","Statement":[
  {"Sid":"A","Effect":"Allow","Action":["s3:GetObject"],"Resource":"*"}]}`

const selfcheckDoc = `{"Version":"2012-10-17","Statement":[
  {"Sid":"B","Effect":"Allow","Action":"iam:GetRolePolicy","Resource":"*"}]}`

const managedDoc = `{"Version":"2012-10-17","Statement":[
  {"Sid":"C","Effect":"Allow","Action":["logs:CreateLogGroup"],"Resource":"*"}]}`

// **inline も managed も全部集めて和集合にする。** 権限は加算されるので、
// 1 つだけ見ると他のポリシーにある権限が「不足」として出る。
func TestLiveRolePolicyUnionsEverything(t *testing.T) {
	api := fakeIAM{
		inline:   map[string]string{"deploy": inlineDoc, "selfcheck": selfcheckDoc},
		attached: map[string]string{"managed": managedDoc},
	}
	doc, sources, err := liveRolePolicy(context.Background(), api, "r")
	if err != nil {
		t.Fatal(err)
	}
	// どれを足し合わせたかが出ること。出ないと「なぜこの権限があるのか」が追えない
	want := "attached:managed inline:deploy inline:selfcheck"
	if got := strings.Join(sources, " "); got != want {
		t.Errorf("sources = %q, want %q", got, want)
	}
	gen := map[string]bool{"s3:GetObject": true, "iam:GetRolePolicy": true, "logs:CreateLogGroup": true}
	extra, missing, err := CheckDriftActions(gen, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 || len(missing) != 0 {
		t.Errorf("和集合が取れていない: extra=%v missing=%v", extra, missing)
	}
}

// IAM は policyDocument を **URL エンコードして返す**ことがある。素で JSON として
// 読むと "invalid character '%'" で落ちる。
func TestLiveRolePolicyDecodesURLEncodedDocument(t *testing.T) {
	api := fakeIAM{inline: map[string]string{"deploy": url.QueryEscape(inlineDoc)}}
	doc, _, err := liveRolePolicy(context.Background(), api, "r")
	if err != nil {
		t.Fatalf("URL エンコードされた document を読めない: %v", err)
	}
	var p struct{ Statement []json.RawMessage }
	if err := json.Unmarshal(doc, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Statement) != 1 {
		t.Errorf("statement 数 = %d, want 1", len(p.Statement))
	}
}

// 不足と過剰が区別されて出ること(#230 の受け入れ条件)。
func TestLiveRolePolicyReportsMissingAndExtra(t *testing.T) {
	api := fakeIAM{inline: map[string]string{"deploy": inlineDoc}}
	doc, _, err := liveRolePolicy(context.Background(), api, "r")
	if err != nil {
		t.Fatal(err)
	}
	// 生成器は logs:CreateLogGroup を要求するが実ロールに無い(= これから 403)。
	// 実ロールの s3:GetObject は生成器に無い(= 権限の化石)。
	gen := map[string]bool{"logs:CreateLogGroup": true}
	extra, missing, err := CheckDriftActions(gen, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "logs:CreateLogGroup" {
		t.Errorf("missing = %v, want [logs:CreateLogGroup]", missing)
	}
	if len(extra) != 1 || extra[0] != "s3:GetObject" {
		t.Errorf("extra = %v, want [s3:GetObject]", extra)
	}
}

// ポリシーが 1 つも無いロールは、差分ゼロではなくエラー。
// 「no drift」と出ると、権限を付け忘れたロールを緑と読んでしまう。
func TestLiveRolePolicyErrorsWhenNoPolicy(t *testing.T) {
	_, _, err := liveRolePolicy(context.Background(), fakeIAM{}, "r")
	if err == nil {
		t.Fatal("ポリシーの無いロールがエラーにならない")
	}
	if !strings.Contains(err.Error(), "no inline or attached policy") {
		t.Errorf("理由が分からないエラー: %v", err)
	}
}
