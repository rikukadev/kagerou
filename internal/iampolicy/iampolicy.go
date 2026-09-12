// Package iampolicy は CI ロール用の最小権限ポリシーを構成別に生成する(#27)。
// 内容は kagerou-3tier-demo の deploy/iam/ci-policy.json(実デプロイを回して
// 足りない権限を 1 つずつ足した実証セット)を土台に、name_prefix で機械的に
// スコープする。ドキュメントで配ると各リポジトリで書き直されるので、コードにする。
package iampolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// marshal は整形済み JSON にする。SetEscapeHTML(false) で <ACCOUNT_ID> 等の
// 山括弧が < にならないようにする(ポリシー JSON は HTML ではない)。
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

type Statement struct {
	Sid         string   `json:"Sid"`
	Effect      string   `json:"Effect"`
	Action      []string `json:"Action"`
	Resource    any      `json:"Resource,omitempty"`    // string または []string
	NotResource any      `json:"NotResource,omitempty"` // Deny を名前空間外に絞るとき
	Condition   any      `json:"Condition,omitempty"`
}

type Policy struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

type Options struct {
	Prefix       string // kagerou.yaml の name_prefix。全 ARN のスコープ
	ECR          bool   // Lambda コンテナイメージ構成(SSR 単体など)
	EcrRepo      string // --with-ecr のリポジトリ名
	S3           bool   // S3 静的配信つき(3 層など)
	VPC          bool   // VPC 内リソース(sashiki 等)へ繋ぐ Lambda
	SashikiSSM   bool   // sashiki action の transport=ssm
	InstanceID   string // --with-sashiki-ssm の宛先インスタンス
	CloudFront   bool   // 共有 CloudFront のキャッシュ無効化
	Route53      bool   // カスタムドメインのレコード操作
	HostedZoneID string // --with-route53 のゾーン
}

// Build は選択された構成の最小権限ポリシーを組む。
func Build(o Options) (Policy, error) {
	if o.Prefix == "" {
		return Policy{}, errors.New("prefix is required (name_prefix in kagerou.yaml, or --prefix)")
	}
	if o.SashikiSSM && o.InstanceID == "" {
		return Policy{}, errors.New("--with-sashiki-ssm requires --instance-id")
	}
	if o.Route53 && o.HostedZoneID == "" {
		return Policy{}, errors.New("--with-route53 requires --hosted-zone-id")
	}
	if o.ECR && o.EcrRepo == "" {
		return Policy{}, errors.New("--with-ecr requires --ecr-repo (or project in kagerou.yaml)")
	}
	p := o.Prefix

	sts := []Statement{
		{
			Sid: "CloudFormationStack", Effect: "Allow",
			Action: []string{
				"cloudformation:CreateStack", "cloudformation:UpdateStack", "cloudformation:DeleteStack",
				"cloudformation:DescribeStacks", "cloudformation:DescribeStackEvents", "cloudformation:DescribeStackResources",
				"cloudformation:ListStackResources",
				"cloudformation:CreateChangeSet", "cloudformation:DescribeChangeSet", "cloudformation:ExecuteChangeSet", "cloudformation:DeleteChangeSet",
				"cloudformation:TagResource", "cloudformation:UntagResource",
			},
			Resource: fmt.Sprintf("arn:aws:cloudformation:*:*:stack/%s*/*", p),
		},
		{
			// GetTemplateSummary はスタック存在前に呼ばれる。DescribeStacks(無名)と
			// ListStacks は list / reap の走査用。いずれも読み取りのみ
			Sid: "CloudFormationGlobalReads", Effect: "Allow",
			Action:   []string{"cloudformation:GetTemplateSummary", "cloudformation:ListStacks", "cloudformation:DescribeStacks"},
			Resource: "*",
		},
		{
			// sam の --resolve-s3 が作る成果物バケット用スタック(名前固定)
			Sid: "SamManagedStack", Effect: "Allow",
			Action: []string{
				"cloudformation:CreateStack", "cloudformation:UpdateStack", "cloudformation:DescribeStacks",
				"cloudformation:DescribeStackEvents", "cloudformation:CreateChangeSet", "cloudformation:DescribeChangeSet",
				"cloudformation:ExecuteChangeSet", "cloudformation:DeleteChangeSet",
			},
			Resource: "arn:aws:cloudformation:*:*:stack/aws-sam-cli-managed-default/*",
		},
		{
			// SAM の Transform 自体が CreateChangeSet の対象リソースとして評価される
			Sid: "SamTransform", Effect: "Allow",
			Action:   []string{"cloudformation:CreateChangeSet"},
			Resource: "arn:aws:cloudformation:*:aws:transform/Serverless-2016-10-31",
		},
		{
			Sid: "SamArtifactBucket", Effect: "Allow",
			Action: []string{
				"s3:CreateBucket", "s3:GetBucketLocation", "s3:PutObject", "s3:GetObject", "s3:ListBucket",
				"s3:PutBucketPolicy", "s3:PutBucketVersioning", "s3:PutEncryptionConfiguration",
			},
			Resource: []string{"arn:aws:s3:::aws-sam-cli-managed-*", "arn:aws:s3:::aws-sam-cli-managed-*/*"},
		},
		{
			Sid: "LambdaFunction", Effect: "Allow",
			Action: []string{
				"lambda:CreateFunction", "lambda:DeleteFunction", "lambda:UpdateFunctionCode", "lambda:UpdateFunctionConfiguration",
				"lambda:GetFunction", "lambda:GetFunctionConfiguration", "lambda:GetPolicy", "lambda:ListVersionsByFunction",
				"lambda:AddPermission", "lambda:RemovePermission",
				"lambda:TagResource", "lambda:UntagResource", "lambda:ListTags",
			},
			Resource: fmt.Sprintf("arn:aws:lambda:*:*:function:%s*", p),
		},
		{
			// API Gateway は ARN でスタック単位に絞れない(id は作るまで不明、タグは別パス)。
			// サービス全体 × apigateway:* の妥協。本番権限として写さないこと
			Sid: "HttpApiCompromise", Effect: "Allow",
			Action:   []string{"apigateway:*"},
			Resource: "arn:aws:apigateway:*::*",
		},
		{
			// PassRole が無いと Lambda を作れない(最も分かりにくい失敗)
			Sid: "ExecutionRole", Effect: "Allow",
			Action: []string{
				"iam:CreateRole", "iam:DeleteRole", "iam:GetRole", "iam:PassRole",
				"iam:AttachRolePolicy", "iam:DetachRolePolicy", "iam:ListAttachedRolePolicies",
				"iam:PutRolePolicy", "iam:DeleteRolePolicy", "iam:GetRolePolicy", "iam:ListRolePolicies",
				"iam:TagRole", "iam:UntagRole",
			},
			Resource: fmt.Sprintf("arn:aws:iam::*:role/%s*", p),
		},
		{
			Sid: "LogGroups", Effect: "Allow",
			Action: []string{
				"logs:CreateLogGroup", "logs:DeleteLogGroup", "logs:DescribeLogGroups", "logs:DescribeLogStreams",
				"logs:PutRetentionPolicy", "logs:TagResource", "logs:UntagResource", "logs:ListTagsForResource",
			},
			Resource: fmt.Sprintf("arn:aws:logs:*:*:log-group:/aws/lambda/%s*", p),
		},
	}

	if o.ECR {
		sts = append(sts,
			Statement{Sid: "EcrAuth", Effect: "Allow", Action: []string{"ecr:GetAuthorizationToken"}, Resource: "*"},
			Statement{
				Sid: "EcrPush", Effect: "Allow",
				Action: []string{
					"ecr:BatchCheckLayerAvailability", "ecr:InitiateLayerUpload", "ecr:UploadLayerPart",
					"ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
					"ecr:DescribeRepositories", "ecr:DescribeImages",
				},
				Resource: fmt.Sprintf("arn:aws:ecr:*:*:repository/%s", o.EcrRepo),
			})
	}
	if o.S3 {
		sts = append(sts, Statement{
			// SPA 配布バケット。Tagging 系は kagerou がタグで環境を識別するため(CONTRACT §1)
			Sid: "WebBucketLifecycle", Effect: "Allow",
			Action: []string{
				"s3:CreateBucket", "s3:DeleteBucket",
				"s3:PutBucketWebsite", "s3:DeleteBucketWebsite", "s3:GetBucketWebsite",
				"s3:PutBucketPolicy", "s3:GetBucketPolicy", "s3:DeleteBucketPolicy",
				"s3:PutBucketPublicAccessBlock", "s3:GetBucketPublicAccessBlock",
				"s3:PutBucketTagging", "s3:GetBucketTagging", "s3:DeleteBucketTagging",
				"s3:GetBucketLocation", "s3:ListBucket", "s3:PutObject", "s3:GetObject", "s3:DeleteObject",
			},
			Resource: []string{fmt.Sprintf("arn:aws:s3:::%s*", p), fmt.Sprintf("arn:aws:s3:::%s*/*", p)},
		})
	}
	if o.VPC {
		sts = append(sts, Statement{
			// VPC 内へ繋ぐ Lambda の ENI 管理。ENI 系は ARN で絞れない
			Sid: "VpcAccess", Effect: "Allow",
			Action: []string{
				"ec2:CreateNetworkInterface", "ec2:DeleteNetworkInterface", "ec2:DescribeNetworkInterfaces",
				"ec2:DescribeSubnets", "ec2:DescribeSecurityGroups", "ec2:DescribeVpcs",
			},
			Resource: "*",
		})
	}
	if o.SashikiSSM {
		sts = append(sts,
			Statement{
				// 宛先インスタンスとドキュメントの両方で絞る。片方だけだと
				// 「任意の EC2 で任意のシェルを実行できるロール」になる
				Sid: "SashikiSendCommand", Effect: "Allow",
				Action: []string{"ssm:SendCommand"},
				Resource: []string{
					fmt.Sprintf("arn:aws:ec2:*:*:instance/%s", o.InstanceID),
					"arn:aws:ssm:*::document/AWS-RunShellScript",
				},
			},
			Statement{
				// GetCommandInvocation はリソース単位で絞れない(読めるのは自分の command のみ)
				Sid: "SashikiReadCommandResult", Effect: "Allow",
				Action: []string{"ssm:GetCommandInvocation"}, Resource: "*",
			})
	}
	if o.CloudFront {
		sts = append(sts, Statement{
			// CloudFront はディストリビューション名で絞れないため id 不明の段階では *
			Sid: "CloudFrontInvalidation", Effect: "Allow",
			Action:   []string{"cloudfront:CreateInvalidation", "cloudfront:GetInvalidation"},
			Resource: "*",
		})
	}
	if o.Route53 {
		sts = append(sts, Statement{
			Sid: "Route53Records", Effect: "Allow",
			Action:   []string{"route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets"},
			Resource: fmt.Sprintf("arn:aws:route53:::hostedzone/%s", o.HostedZoneID),
		})
	}

	return Policy{Version: "2012-10-17", Statement: sts}, nil
}

// JSON はポリシーを整形済み JSON にする。
func (p Policy) JSON() ([]byte, error) {
	return marshal(p)
}

// --- trust policy(デプロイロールを誰が assume できるか)------------------------
//
// Build が出すのは「何ができるか」の権限ポリシー。ロールを実際に立てるには
// 「誰が assume できるか」の trust policy が対になって要る(#53 ②)。
// GitHub Actions の OIDC を前提に、この 1 リポジトリの pull_request と既定ブランチ
// だけに絞る。fork の PR は sub が repo:FORK/... になり一致しない(かつ GitHub は
// fork PR の workflow に OIDC トークンを既定で渡さない)ので二重に閉じる。

// AccountPlaceholder は --account 未指定時に trust / boundary の ARN へ埋める。
// そのままでは使えないので、利用者に置換を促す(コマンド側が stderr で注意する)。
const AccountPlaceholder = "<ACCOUNT_ID>"

const githubOIDCHost = "token.actions.githubusercontent.com"

type TrustStatement struct {
	Effect    string         `json:"Effect"`
	Principal map[string]any `json:"Principal"`
	Action    string         `json:"Action"`
	Condition map[string]any `json:"Condition"`
}

type TrustPolicy struct {
	Version   string           `json:"Version"`
	Statement []TrustStatement `json:"Statement"`
}

type TrustOptions struct {
	Repo    string // owner/name(必須)。OIDC の sub を repo:owner/name:... に固定する
	Account string // AWS アカウント ID。空なら AccountPlaceholder を埋める
	Branch  string // 既定ブランチ(schedule の reap 等が assume する)。空なら main
}

// BuildTrust は GitHub Actions OIDC 用の trust policy を組む。
func BuildTrust(o TrustOptions) (TrustPolicy, error) {
	if o.Repo == "" {
		return TrustPolicy{}, errors.New("repo is required (--repo owner/name)")
	}
	if !strings.Contains(o.Repo, "/") || strings.HasPrefix(o.Repo, "/") || strings.HasSuffix(o.Repo, "/") {
		return TrustPolicy{}, fmt.Errorf("repo must be owner/name: %q", o.Repo)
	}
	acct := o.Account
	if acct == "" {
		acct = AccountPlaceholder
	}
	branch := o.Branch
	if branch == "" {
		branch = "main"
	}
	providerArn := fmt.Sprintf("arn:aws:iam::%s:oidc-provider/%s", acct, githubOIDCHost)
	subs := []string{
		fmt.Sprintf("repo:%s:pull_request", o.Repo),              // preview は pull_request イベント
		fmt.Sprintf("repo:%s:ref:refs/heads/%s", o.Repo, branch), // reap の schedule / 手動実行
	}
	return TrustPolicy{
		Version: "2012-10-17",
		Statement: []TrustStatement{{
			Effect:    "Allow",
			Principal: map[string]any{"Federated": providerArn},
			Action:    "sts:AssumeRoleWithWebIdentity",
			Condition: map[string]any{
				"StringEquals": map[string]any{
					// aud を固定しないと別 workflow の混入を許す
					githubOIDCHost + ":aud": "sts.amazonaws.com",
					// sub をこのリポジトリの 2 コンテキストだけに固定 = fork ガード
					githubOIDCHost + ":sub": subs,
				},
			},
		}},
	}, nil
}

// JSON はポリシーを整形済み JSON にする。
func (t TrustPolicy) JSON() ([]byte, error) {
	return marshal(t)
}

// --- permissions boundary(自己サーブの天井)----------------------------------
//
// boundary は「上限」。実効権限 = アイデンティティポリシー(Build の出力)∩ boundary。
// なので base は Allow *:* で広く取り、Deny のカーブアウトで sandbox を封じる:
// region 外・IAM 昇格・name_prefix 名前空間外の IAM 書込・組織/課金 を落とす。
// デプロイロールにも、それが作る preview ロールにも同じ boundary を付ける前提
// (作成ロールへの boundary 付与を Deny で強制 = 再帰的に封じる)(#53 ③)。

type BoundaryOptions struct {
	Prefix      string   // name_prefix。IAM リソースを絞る名前空間(必須)
	Regions     []string // 許可する region(必須)。ここ以外の regional アクションを落とす
	BoundaryArn string   // この boundary 自身の ARN。空なら Account から組む/placeholder
	Account     string   // BoundaryArn 未指定時に ARN を組むためのアカウント ID
}

// BuildBoundary は自己サーブ用の permissions boundary を組む。
func BuildBoundary(o BoundaryOptions) (Policy, error) {
	if o.Prefix == "" {
		return Policy{}, errors.New("prefix is required (name_prefix in kagerou.yaml, or --prefix)")
	}
	if len(o.Regions) == 0 {
		return Policy{}, errors.New("at least one region is required (region in kagerou.yaml, or --region)")
	}
	boundaryArn := o.BoundaryArn
	if boundaryArn == "" {
		acct := o.Account
		if acct == "" {
			acct = AccountPlaceholder
		}
		boundaryArn = fmt.Sprintf("arn:aws:iam::%s:policy/%sboundary", acct, o.Prefix)
	}
	roleNamespace := fmt.Sprintf("arn:aws:iam::*:role/%s*", o.Prefix)

	sts := []Statement{
		{
			// 天井の base。実際の絞り込みは Build のアイデンティティポリシー側。
			Sid: "PermissiveBase", Effect: "Allow",
			Action: []string{"*"}, Resource: "*",
		},
		{
			// region ロック。IfExists にしないと region キーを持たない
			// グローバルサービス(IAM/CloudFront/Route53 等)まで落ちる。
			Sid: "DenyOutsideRegions", Effect: "Deny",
			Action:    []string{"*"},
			Resource:  "*",
			Condition: map[string]any{"StringNotEqualsIfExists": map[string]any{"aws:RequestedRegion": o.Regions}},
		},
		{
			// IAM 昇格の定番経路を塞ぐ(ユーザ/グループ/ポリシー版/IdP)。
			Sid: "DenyIamPrivilegeEscalation", Effect: "Deny",
			Action: []string{
				"iam:CreateUser", "iam:CreateAccessKey", "iam:UpdateAccessKey",
				"iam:CreateLoginProfile", "iam:UpdateLoginProfile",
				"iam:AttachUserPolicy", "iam:PutUserPolicy", "iam:AddUserToGroup",
				"iam:CreateGroup", "iam:AttachGroupPolicy", "iam:PutGroupPolicy",
				"iam:CreatePolicyVersion", "iam:SetDefaultPolicyVersion",
				"iam:CreateOpenIDConnectProvider", "iam:DeleteOpenIDConnectProvider",
				"iam:CreateSAMLProvider", "iam:UpdateSAMLProvider",
			},
			Resource: "*",
		},
		{
			// IAM の書込は name_prefix のロールだけ。PassRole も同様(admin ロールを
			// Lambda に渡す経路を塞ぐ)。boundary の外し(Delete...PermissionsBoundary)も含む。
			Sid: "DenyRoleWritesOutsideNamespace", Effect: "Deny",
			Action: []string{
				"iam:CreateRole", "iam:DeleteRole", "iam:UpdateRole", "iam:UpdateAssumeRolePolicy",
				"iam:PutRolePolicy", "iam:DeleteRolePolicy", "iam:AttachRolePolicy", "iam:DetachRolePolicy",
				"iam:PutRolePermissionsBoundary", "iam:DeleteRolePermissionsBoundary", "iam:PassRole",
			},
			NotResource: roleNamespace,
		},
		{
			// 作る role には必ずこの boundary を付けさせる = 子ロールも同じ天井に。
			// これが無いと「boundary 無しの namespace ロール」を作って昇格できる。
			Sid: "RequireBoundaryOnNewRoles", Effect: "Deny",
			Action:    []string{"iam:CreateRole"},
			Resource:  "*",
			Condition: map[string]any{"StringNotEquals": map[string]any{"iam:PermissionsBoundary": boundaryArn}},
		},
		{
			// 組織・アカウント・課金は preview の管轄外。触らせない。
			Sid: "DenyOrgAccountBilling", Effect: "Deny",
			Action:   []string{"organizations:*", "account:*", "aws-portal:*", "budgets:*", "ce:*"},
			Resource: "*",
		},
	}
	return Policy{Version: "2012-10-17", Statement: sts}, nil
}

// --- execution role(preview の Lambda がランタイムで使う)-------------------
//
// deploy ロールとは別。触る ARN はアプリが宣言できる(#53 ①)。Policy Sentry の
// 発想を軽量に持ち込み、宣言(--allow 'actions=resources')から最小ポリシーを組む。
// ベースは Lambda 必須の logs、VPC 内なら ENI 管理。

// AllowRule は「これらのアクションを、これらの ARN にだけ許す」1 文。
type AllowRule struct {
	Actions   []string
	Resources []string
}

type ExecutionOptions struct {
	Prefix string      // name_prefix。ロググループ ARN を絞る(必須)
	VPC    bool        // VPC 内 Lambda(ENI 管理を足す)
	Allow  []AllowRule // アプリが宣言した ARN + アクション
}

// BuildExecution は Lambda 実行ロールの最小権限ポリシーを組む。
func BuildExecution(o ExecutionOptions) (Policy, error) {
	if o.Prefix == "" {
		return Policy{}, errors.New("prefix is required (name_prefix in kagerou.yaml, or --prefix)")
	}
	sts := []Statement{
		{
			// Lambda 必須。CreateLogGroup はグループ ARN、Put/CreateStream は
			// ストリーム(:*)まで要るので両方を対象にする。
			Sid: "Logs", Effect: "Allow",
			Action: []string{"logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"},
			Resource: []string{
				fmt.Sprintf("arn:aws:logs:*:*:log-group:/aws/lambda/%s*", o.Prefix),
				fmt.Sprintf("arn:aws:logs:*:*:log-group:/aws/lambda/%s*:*", o.Prefix),
			},
		},
	}
	if o.VPC {
		sts = append(sts, Statement{
			// VPC 内へ繋ぐ Lambda の ENI 管理。ENI 系は ARN で絞れない。
			Sid: "VpcAccess", Effect: "Allow",
			Action: []string{
				"ec2:CreateNetworkInterface", "ec2:DeleteNetworkInterface", "ec2:DescribeNetworkInterfaces",
			},
			Resource: "*",
		})
	}
	for i, a := range o.Allow {
		if len(a.Actions) == 0 || len(a.Resources) == 0 {
			return Policy{}, fmt.Errorf("--allow rule %d: actions and resources are both required", i+1)
		}
		sts = append(sts, Statement{
			Sid: fmt.Sprintf("Allow%d", i+1), Effect: "Allow",
			Action: a.Actions, Resource: a.Resources,
		})
	}
	return Policy{Version: "2012-10-17", Statement: sts}, nil
}

// --- drift 検出(生成ポリシー vs 実際に attach されたポリシー)------------------
//
// #53 ④。CI で「生成される最小ポリシー」を基準に、実 attach ポリシーの
// アクション差分を出す。**アクション集合の比較**で resource スコープの緩さは見ない
// (`s3:*` のような広いアクションの混入を掴む用途)。Access Analyzer / Cloudsplaining の
// 前段の軽いガードとして CI に置く。

// PolicyAllowActions は Effect=Allow の Action 集合を返す(生成側)。
func PolicyAllowActions(p Policy) map[string]bool {
	set := map[string]bool{}
	for _, s := range p.Statement {
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		for _, a := range s.Action {
			set[a] = true
		}
	}
	return set
}

// attachedPolicy は実 attach ポリシー JSON をゆるく読む(Action は string でも配列でも可、
// Statement 単体でも配列でも可)。
type attachedPolicy struct {
	Statement stmtList `json:"Statement"`
}

type stmtList []struct {
	Effect string     `json:"Effect"`
	Action stringList `json:"Action"`
}

func (l *stmtList) UnmarshalJSON(b []byte) error {
	// Statement は配列が普通だが、単体オブジェクトも許容する。
	trimmed := strings.TrimSpace(string(b))
	if strings.HasPrefix(trimmed, "{") {
		var one struct {
			Effect string     `json:"Effect"`
			Action stringList `json:"Action"`
		}
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*l = stmtList{one}
		return nil
	}
	type raw stmtList
	return json.Unmarshal(b, (*raw)(l))
}

// stringList は "s3:*" と ["s3:Get","s3:Put"] の両方を受ける。
type stringList []string

func (s *stringList) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if strings.HasPrefix(trimmed, "[") {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*s = []string{one}
	return nil
}

// CheckDrift は生成ポリシー generated を基準に、attached の Allow アクションを比べる。
// extra = attached にあって generated に無い(過剰権限の疑い)、
// missing = generated が必要とするが attached に無い(壊れる恐れ)。いずれもソート済み。
func CheckDrift(generated Policy, attached []byte) (extra, missing []string, err error) {
	var ap attachedPolicy
	if err := json.Unmarshal(attached, &ap); err != nil {
		return nil, nil, fmt.Errorf("attached policy JSON: %w", err)
	}
	att := map[string]bool{}
	for _, s := range ap.Statement {
		if !strings.EqualFold(s.Effect, "Allow") {
			continue
		}
		for _, a := range s.Action {
			att[a] = true
		}
	}
	gen := PolicyAllowActions(generated)
	for a := range att {
		if !gen[a] {
			extra = append(extra, a)
		}
	}
	for a := range gen {
		if !att[a] {
			missing = append(missing, a)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing, nil
}
