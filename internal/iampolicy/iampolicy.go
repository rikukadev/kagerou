// Package iampolicy は CI ロール用の最小権限ポリシーを構成別に生成する(#27)。
// 内容は kagerou-3tier-demo の deploy/iam/ci-policy.json(実デプロイを回して
// 足りない権限を 1 つずつ足した実証セット)を土台に、name_prefix で機械的に
// スコープする。ドキュメントで配ると各リポジトリで書き直されるので、コードにする。
package iampolicy

import (
	"encoding/json"
	"errors"
	"fmt"
)

type Statement struct {
	Sid      string   `json:"Sid"`
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource any      `json:"Resource"` // string または []string
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
	return json.MarshalIndent(p, "", "  ")
}
