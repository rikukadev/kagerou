package iampolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// LiveRolePolicy は AWS に **実際に attach されているもの** を 1 つのポリシー
// 文書にまとめて返す(#230)。
//
// なぜ要るか: drift を見る層が 3 つあり、3 つ目だけ穴が空いていた。
//
//	生成ファイル(workflow / base)の古さ        upgrade --check が見る
//	リポジトリの ci-policy.json と生成器の drift  CI の iam-policy-drift が見る
//	**AWS に適用済みのロール**と生成器の drift    誰も見ていなかった
//
// 3 つ目は実行時に 403 を踏むまで分からず、踏むのは schedule の reap
// (人が見ていない時間)。実際に 6 時間おきに 5 日落ち続けたことがある。
//
// **inline も managed も全部集めて和集合にする。** 権限は加算されるので、
// 1 つだけ見ると「他のポリシーにある権限」が不足として出てしまう。
// ロールに複数の inline policy を分けて置く運用は普通にある
// (このリポジトリ自身、CI の自己読み取り権限を別ポリシーに切っている)。
func LiveRolePolicy(ctx context.Context, roleName string) (json.RawMessage, []string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("aws config: %w", err)
	}
	return liveRolePolicy(ctx, iam.NewFromConfig(cfg), roleName)
}

// iamAPI は LiveRolePolicy が使う IAM の口。テストで差し替える。
type iamAPI interface {
	ListRolePolicies(context.Context, *iam.ListRolePoliciesInput, ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error)
	GetRolePolicy(context.Context, *iam.GetRolePolicyInput, ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error)
	ListAttachedRolePolicies(context.Context, *iam.ListAttachedRolePoliciesInput, ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error)
	GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
	GetPolicyVersion(context.Context, *iam.GetPolicyVersionInput, ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
}

func liveRolePolicy(ctx context.Context, api iamAPI, roleName string) (json.RawMessage, []string, error) {
	var stmts []json.RawMessage
	var sources []string

	// --- inline policy ---
	var marker *string
	for {
		out, err := api.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{
			RoleName: &roleName, Marker: marker,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list inline policies of %s: %w", roleName, err)
		}
		for _, name := range out.PolicyNames {
			got, err := api.GetRolePolicy(ctx, &iam.GetRolePolicyInput{
				RoleName: &roleName, PolicyName: aws.String(name),
			})
			if err != nil {
				return nil, nil, fmt.Errorf("get inline policy %s: %w", name, err)
			}
			s, err := statementsOf(aws.ToString(got.PolicyDocument))
			if err != nil {
				return nil, nil, fmt.Errorf("inline policy %s: %w", name, err)
			}
			stmts = append(stmts, s...)
			sources = append(sources, "inline:"+name)
		}
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}

	// --- attached managed policy ---
	marker = nil
	for {
		out, err := api.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
			RoleName: &roleName, Marker: marker,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list attached policies of %s: %w", roleName, err)
		}
		for _, p := range out.AttachedPolicies {
			arn := aws.ToString(p.PolicyArn)
			meta, err := api.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: &arn})
			if err != nil {
				return nil, nil, fmt.Errorf("get policy %s: %w", arn, err)
			}
			if meta.Policy == nil || meta.Policy.DefaultVersionId == nil {
				return nil, nil, fmt.Errorf("policy %s has no default version", arn)
			}
			ver, err := api.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
				PolicyArn: &arn, VersionId: meta.Policy.DefaultVersionId,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("get policy version of %s: %w", arn, err)
			}
			if ver.PolicyVersion == nil {
				return nil, nil, fmt.Errorf("policy %s version is empty", arn)
			}
			s, err := statementsOf(aws.ToString(ver.PolicyVersion.Document))
			if err != nil {
				return nil, nil, fmt.Errorf("policy %s: %w", arn, err)
			}
			stmts = append(stmts, s...)
			sources = append(sources, "attached:"+aws.ToString(p.PolicyName))
		}
		if !out.IsTruncated {
			break
		}
		marker = out.Marker
	}

	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("role %s has no inline or attached policy", roleName)
	}
	sort.Strings(sources)
	doc, err := json.Marshal(struct {
		Version   string            `json:"Version"`
		Statement []json.RawMessage `json:"Statement"`
	}{Version: "2012-10-17", Statement: stmts})
	if err != nil {
		return nil, nil, err
	}
	return doc, sources, nil
}

// statementsOf は policyDocument の Statement を取り出す。
//
// **IAM は policyDocument を URL エンコードして返す**(GetRolePolicy /
// GetPolicyVersion の両方)。SDK がデコードしてくれる場合とそうでない場合が
// あるので、`{` で始まらなければデコードを試す。ここを素で JSON として
// 読むと "invalid character '%'" で落ちる。
func statementsOf(doc string) ([]json.RawMessage, error) {
	if doc == "" {
		return nil, fmt.Errorf("empty policy document")
	}
	if doc[0] != '{' {
		dec, err := url.QueryUnescape(doc)
		if err != nil {
			return nil, fmt.Errorf("url-decode policy document: %w", err)
		}
		doc = dec
	}
	var p struct {
		Statement []json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return nil, fmt.Errorf("policy JSON: %w", err)
	}
	return p.Statement, nil
}
