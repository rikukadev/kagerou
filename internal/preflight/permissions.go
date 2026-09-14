// Package preflight は「このまま setup を走らせて通るか」を事前に確かめる。
// ログインの有無だけ見ても、IAM ロールを作る権限が無い人は最後の最後で
// AccessDenied になり、何が足りないのか分からない。
package preflight

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Identity は呼び出し元(どのアカウントに作ろうとしているか)。
type Identity struct {
	Account string
	ARN     string
	Region  string
}

func (i Identity) String() string {
	if i.ARN == "" {
		return ""
	}
	return fmt.Sprintf("%s (%s)", i.ARN, i.Region)
}

// Check は 1 アクションの判定結果。Unknown はシミュレーション自体ができなかった
// (権限不足・SSO 等)ことを表し、失敗とは区別する。
type Check struct {
	Action  string
	Allowed bool
	Unknown bool
	Why     string // 何のために要るか(人間向け)
	// Teardown は「壊すために要る」権限であることを示す(#158)。
	// 作れるが消せない権限セット(PowerUser 等)は実在し、そのときの被害は
	// 「作った後に down / reap が落ち続けて課金が残る」— 作る前に分けて言う
	Teardown bool
}

// Report は事前検査の結果。
type Report struct {
	Identity  Identity
	Checks    []Check
	Simulated bool // シミュレーション API 自体が使えたか
	Note      string
}

// Denied は明確に拒否されたものだけを返す(Unknown は含めない)。
func (r Report) Denied() []Check {
	var out []Check
	for _, c := range r.Checks {
		if !c.Allowed && !c.Unknown {
			out = append(out, c)
		}
	}
	return out
}

// DeniedTeardown は拒否のうち「壊す側」だけを返す。
// 作る側が全部通っていてここだけ落ちている状態が一番危ない
// (環境は作れてしまい、消せないことは TTL 切れまで表に出ない)。
func (r Report) DeniedTeardown() []Check {
	var out []Check
	for _, c := range r.Denied() {
		if c.Teardown {
			out = append(out, c)
		}
	}
	return out
}

// DeniedSetup は拒否のうち「作る側」だけを返す。
func (r Report) DeniedSetup() []Check {
	var out []Check
	for _, c := range r.Denied() {
		if !c.Teardown {
			out = append(out, c)
		}
	}
	return out
}

// Plan は選択した構成で setup が必要とするアクション。
type Plan struct {
	Role       bool // OIDC ロールを作る
	ECR        bool // ECR リポジトリを作る
	Base       bool // preview base(CFN + Route53 + ACM)をデプロイする
	StaticSync bool // S3 へ同期する(static driver)
}

// actionsFor は構成から必要アクションを組む。過不足があると
// 「通ると言われたのに落ちる/落ちると言われたのに通る」になるので、
// setup スクリプトが実際に叩くものだけを列挙する。
//
// **作る側と壊す側を両方入れる(#158)。** 見るのは setup 自身が作るもの
// (ロール / ECR / base)の撤収で、環境スタックの削除は CI ロールの権限=
// iam-policy の担当。「作れるが消せない」権限セットは実在し、そのときの被害は
// 作った後にしか出ない — preflight の存在意義は作る前に分かることなので、
// ここで分けて言う。
func actionsFor(p Plan) []Check {
	var cs []Check
	if p.Role {
		cs = append(cs,
			Check{Action: "iam:CreateRole", Why: "GitHub Actions の OIDC ロール"},
			Check{Action: "iam:AttachRolePolicy", Why: "ロールへのポリシー付与"},
			Check{Action: "iam:UpdateAssumeRolePolicy", Why: "信頼ポリシーの更新(再実行時)"},
			// IAM は「作れるが消せない」が起きやすい代表格(PowerUser 等)
			Check{Action: "iam:DetachRolePolicy", Why: "撤収時のポリシー剥がし", Teardown: true},
			Check{Action: "iam:DeleteRole", Why: "撤収時のロール削除", Teardown: true},
		)
	}
	if p.ECR {
		cs = append(cs,
			Check{Action: "ecr:CreateRepository", Why: "イメージの push 先"},
			Check{Action: "ecr:DeleteRepository", Why: "撤収時のリポジトリ削除", Teardown: true},
		)
	}
	if p.Base {
		cs = append(cs,
			Check{Action: "cloudformation:CreateStack", Why: "preview base スタック"},
			Check{Action: "acm:RequestCertificate", Why: "ワイルドカード証明書"},
			Check{Action: "route53:ChangeResourceRecordSets", Why: "DNS レコード(証明書検証と alias)"},
			Check{Action: "cloudfront:CreateDistribution", Why: "共有 CloudFront"},
			Check{Action: "s3:CreateBucket", Why: "配信元バケット"},
			Check{Action: "cloudformation:DeleteStack", Why: "撤収時の base 削除", Teardown: true},
			Check{Action: "cloudfront:DeleteDistribution", Why: "撤収時の base 削除", Teardown: true},
			Check{Action: "s3:DeleteBucket", Why: "撤収時の base 削除", Teardown: true},
		)
	}
	if p.StaticSync {
		cs = append(cs,
			Check{Action: "s3:PutObject", Why: "成果物の配置"},
			Check{Action: "s3:GetBucketLocation", Why: "バケット region の解決"},
			// static の down はプレフィックス配下を消す。stack 構成でも
			// 中身の入ったバケットは DeleteStack が消せないので pre_down で使う
			Check{Action: "s3:DeleteObject", Why: "環境の削除(プレフィックス / pre_down)", Teardown: true},
		)
	}
	return cs
}

// CheckPermissions は呼び出し元の素性を取り、必要アクションを
// SimulatePrincipalPolicy で判定する。シミュレーションできない場合も
// 止めない(Unknown として返す)。
func CheckPermissions(ctx context.Context, region string, p Plan) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Checks: actionsFor(p)}
	rep.Identity.Region = cfg.Region

	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return rep, err
	}
	rep.Identity.Account = aws.ToString(id.Account)
	rep.Identity.ARN = aws.ToString(id.Arn)
	if len(rep.Checks) == 0 {
		return rep, nil
	}

	names := make([]string, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		names = append(names, c.Action)
	}
	// 評価対象は「ロール/ユーザー本体」。assumed-role の session ARN は
	// SimulatePrincipalPolicy が受け付けないのでロール ARN に正規化する
	out, err := iam.NewFromConfig(cfg).SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(principalARN(rep.Identity.ARN, rep.Identity.Account)),
		ActionNames:     names,
	})
	if err != nil {
		// iam:SimulatePrincipalPolicy 自体が無いケースが普通にある
		for i := range rep.Checks {
			rep.Checks[i].Unknown = true
		}
		rep.Note = "permission check skipped: " + shortErr(err)
		return rep, nil
	}
	rep.Simulated = true
	allowed := map[string]bool{}
	for _, r := range out.EvaluationResults {
		allowed[aws.ToString(r.EvalActionName)] = r.EvalDecision == iamtypes.PolicyEvaluationDecisionTypeAllowed
	}
	for i, c := range rep.Checks {
		rep.Checks[i].Allowed = allowed[c.Action]
	}
	return rep, nil
}

// principalARN は assumed-role の session ARN をロール ARN に直す。
// arn:aws:sts::123:assumed-role/Role/session → arn:aws:iam::123:role/Role
func principalARN(arn, account string) string {
	const marker = ":assumed-role/"
	i := strings.Index(arn, marker)
	if i < 0 {
		return arn
	}
	rest := arn[i+len(marker):]
	role := rest
	if j := strings.Index(rest, "/"); j >= 0 {
		role = rest[:j]
	}
	return fmt.Sprintf("arn:aws:iam::%s:role/%s", account, role)
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
