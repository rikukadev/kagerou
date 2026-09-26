// Package iampolicy は CI ロール用の最小権限ポリシーを構成別に生成する(#27)。
// 内容は kagerou-3tier-demo の deploy/iam/ci-policy.json(実デプロイを回して
// 足りない権限を 1 つずつ足した実証セット)を土台に、name_prefix で機械的に
// スコープする。ドキュメントで配ると各リポジトリで書き直されるので、コードにする。
//
// 足りない権限はローカル(admin)では絶対に再現せず、CI の絞ったロールでだけ
// 403 として出る。踏むたびに同じ形を繰り返したので、原因を 3 つに整理した:
//
//  1. 書く前に読む。CFN のハンドラは更新前に現状を読むものがあり、
//     Change 系だけ足すと落ちる(route53:GetHostedZone が典型)。
//  2. 作った直後にタグを打つ。タグは本体と別の ARN で評価されることがあり、
//     本体の ARN で絞ると通らない(EventSourceMapping が典型)。
//  3. SAM が展開して初めて現れる型がある。テンプレート本文を型で数えるだけでは
//     永久に見えないので、元のプロパティから導く(template.go 側で処理)。
//
// 新しい型を足すときは、この 3 つを順に当ててから knownTypes に入れること。
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
	Prefix string // kagerou.yaml の name_prefix。全 ARN のスコープ

	// Template があれば「テンプレートが作るもの」はそこから導出する(#71):
	// Lambda / API / VPC / S3 バケット / DynamoDB / SQS。S3・VPC のフラグは
	// このとき無視される(テンプレートが真実の源)。nil なら従来のフラグ挙動。
	Template *TemplateFacts

	// 以下は「CI 自身がやること」でテンプレートからは導けない。フラグのまま。
	ECR          bool   // Lambda コンテナイメージ構成(SSR 単体など)
	EcrRepo      string // --with-ecr のリポジトリ名
	SashikiSSM   bool   // sashiki action の transport=ssm
	InstanceID   string // --with-sashiki-ssm の宛先インスタンス(id 直指定)
	InstanceTag  string // 同上を "Key=Value" のタグで絞る(id 固定を避けたいとき)
	CloudFront   bool   // 共有 CloudFront のキャッシュ無効化
	Route53      bool   // カスタムドメインのレコード操作
	HostedZoneID string // --with-route53 のゾーン
	BaseBucket   string // 共有 preview base バケットへの成果物 sync(post_up の aws s3 sync)
	BaseDomain   bool   // url_template 等で {base_domain} を使う(SSM 契約からドメインを引く)

	// Template == nil のときだけ効く従来フラグ(テンプレートがあれば導出が勝つ)。
	S3  bool // S3 静的配信つき(3 層など)
	VPC bool // VPC 内リソース(sashiki 等)へ繋ぐ Lambda
}

// Build は選択された構成の最小権限ポリシーを組む。
// ssmParamARNs は {{resolve:ssm}} のパスを ARN にする。
//
// リージョン/アカウントはワイルドカードのまま。1 本のロールを複数リージョンで
// 使う運用があり、ここを固定すると「動いていたロールが別リージョンで落ちる」に
// なる(名前空間の絞りはパス側で効いている)。
func ssmParamARNs(paths []string) any {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, "arn:aws:ssm:*:*:parameter"+ensureLeadingSlash(p))
	}
	if len(out) == 1 {
		return out[0]
	}
	return out
}

// secretARNs はシークレットの識別子を ARN にする。
//
// 名前で書かれていたときに末尾へ "-*" を足すのは、Secrets Manager の ARN が
// 名前 + 6 文字のランダム接尾辞になるため。ここを付けないと、名前で指定した
// 構成で必ず AccessDenied になる。
func secretARNs(ids []string) any {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, "arn:") {
			out = append(out, id)
			continue
		}
		out = append(out, "arn:aws:secretsmanager:*:*:secret:"+id+"-*")
	}
	if len(out) == 1 {
		return out[0]
	}
	return out
}

func ensureLeadingSlash(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

func Build(o Options) (Policy, error) {
	if o.Prefix == "" {
		return Policy{}, errors.New("prefix is required (name_prefix in kagerou.yaml, or --prefix)")
	}
	if o.SashikiSSM {
		switch {
		case o.InstanceID == "" && o.InstanceTag == "":
			return Policy{}, errors.New("--with-sashiki-ssm requires --instance-id or --instance-tag Key=Value")
		case o.InstanceID != "" && o.InstanceTag != "":
			return Policy{}, errors.New("--instance-id and --instance-tag are mutually exclusive")
		}
		if k, _, ok := strings.Cut(o.InstanceTag, "="); o.InstanceTag != "" && (!ok || k == "") {
			return Policy{}, fmt.Errorf("--instance-tag must be Key=Value, got %q", o.InstanceTag)
		}
	}
	if o.ECR && o.EcrRepo == "" {
		return Policy{}, errors.New("--with-ecr requires --ecr-repo (or project in kagerou.yaml)")
	}
	p := o.Prefix

	// テンプレートがあれば「テンプレートが作るもの」はそこから導出。
	// 無ければ従来どおり(Lambda/API は常時、S3/VPC はフラグ)。
	wantLambda, wantAPI, wantRoles := true, true, true
	wantS3, wantVPC := o.S3, o.VPC
	// Route53 だけはフラグとの OR。テンプレートが RecordSet を持たなくても、
	// 共有 base 側のゾーンに CI がレコードを足す構成があり得る
	wantRoute53 := o.Route53
	if t := o.Template; t != nil {
		wantRoute53 = wantRoute53 || t.HasRoute53
		wantLambda = t.has("AWS::Serverless::Function", "AWS::Lambda::Function")
		wantAPI = t.has("AWS::Serverless::HttpApi", "AWS::Serverless::Api",
			"AWS::ApiGatewayV2::Api", "AWS::ApiGateway::RestApi")
		wantRoles = wantLambda || t.has("AWS::IAM::Role")
		wantS3 = t.has("AWS::S3::Bucket")
		wantVPC = t.HasVPC
	}
	if wantRoute53 && o.HostedZoneID == "" {
		// テンプレート由来のときはフラグを立てた覚えが無いので、理由まで書く
		return Policy{}, errors.New("route53 records need --hosted-zone-id " +
			"(the template has AWS::Route53::RecordSet, or --with-route53 was given)")
	}

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
	}

	if wantLambda {
		sts = append(sts, Statement{
			Sid: "LambdaFunction", Effect: "Allow",
			Action: []string{
				"lambda:CreateFunction", "lambda:DeleteFunction", "lambda:UpdateFunctionCode", "lambda:UpdateFunctionConfiguration",
				"lambda:GetFunction", "lambda:GetFunctionConfiguration", "lambda:GetPolicy", "lambda:ListVersionsByFunction",
				"lambda:AddPermission", "lambda:RemovePermission",
				"lambda:TagResource", "lambda:UntagResource", "lambda:ListTags",
			},
			Resource: fmt.Sprintf("arn:aws:lambda:*:*:function:%s*", p),
		})
	}
	if wantRoles {
		sts = append(sts, Statement{
			// Lambda の暗黙 role と、ECS 等の明示 AWS::IAM::Role の両方。
			// PassRole に加え、boundary を CreateRole に載せる権限も要る。
			Sid: "ExecutionRole", Effect: "Allow",
			Action: []string{
				"iam:CreateRole", "iam:DeleteRole", "iam:GetRole", "iam:PassRole",
				"iam:AttachRolePolicy", "iam:DetachRolePolicy", "iam:ListAttachedRolePolicies",
				"iam:PutRolePolicy", "iam:DeleteRolePolicy", "iam:GetRolePolicy", "iam:ListRolePolicies",
				"iam:PutRolePermissionsBoundary", "iam:DeleteRolePermissionsBoundary",
				"iam:TagRole", "iam:UntagRole",
			},
			Resource: fmt.Sprintf("arn:aws:iam::*:role/%s*", p),
		})
	}
	if wantLambda {
		sts = append(sts, Statement{
			Sid: "LogGroups", Effect: "Allow",
			Action: []string{
				"logs:CreateLogGroup", "logs:DeleteLogGroup", "logs:DescribeLogGroups", "logs:DescribeLogStreams",
				"logs:PutRetentionPolicy", "logs:TagResource", "logs:UntagResource", "logs:ListTagsForResource",
			},
			Resource: fmt.Sprintf("arn:aws:logs:*:*:log-group:/aws/lambda/%s*", p),
		})
	}
	if wantAPI {
		sts = append(sts, Statement{
			// API Gateway は ARN でスタック単位に絞れない(id は作るまで不明、タグは別パス)。
			// サービス全体 × apigateway:* の妥協。本番権限として写さないこと
			Sid: "HttpApiCompromise", Effect: "Allow",
			Action:   []string{"apigateway:*"},
			Resource: "arn:aws:apigateway:*::*",
		})
	}
	if o.Template != nil && o.Template.has(
		"AWS::ElasticLoadBalancingV2::TargetGroup", "AWS::ElasticLoadBalancingV2::ListenerRule") {
		sts = append(sts, Statement{
			// 共有 ALB 入口。ALB 本体(固定費)はベースの持ち物で、環境が作るのは
			// ターゲットグループとリスナールールだけ。どちらも作る前は ARN が
			// 分からず、ルールは共有リスナー配下に付くので Resource は絞れない。
			// Describe* はロールバック時に CFN が読む(作成だけでも要る)。
			Sid: "SharedAlbEntrypoint", Effect: "Allow",
			Action: []string{
				"elasticloadbalancing:CreateTargetGroup", "elasticloadbalancing:DeleteTargetGroup",
				"elasticloadbalancing:ModifyTargetGroupAttributes",
				"elasticloadbalancing:RegisterTargets", "elasticloadbalancing:DeregisterTargets",
				"elasticloadbalancing:CreateRule", "elasticloadbalancing:DeleteRule", "elasticloadbalancing:ModifyRule",
				"elasticloadbalancing:AddTags", "elasticloadbalancing:RemoveTags",
				"elasticloadbalancing:DescribeTargetGroups", "elasticloadbalancing:DescribeTargetGroupAttributes",
				"elasticloadbalancing:DescribeRules", "elasticloadbalancing:DescribeListeners",
				"elasticloadbalancing:DescribeTags", "elasticloadbalancing:DescribeTargetHealth",
			},
			Resource: "*",
		})
	}
	if o.Template != nil && o.Template.has("AWS::ECS::Service", "AWS::ECS::TaskDefinition") {
		sts = append(sts, Statement{
			// compute: ecs。クラスタは共有ベースの持ち物なので作らない。
			// タスク定義はリビジョンが増えるので ARN を事前に絞れない。
			Sid: "EcsService", Effect: "Allow",
			Action: []string{
				"ecs:RegisterTaskDefinition", "ecs:DeregisterTaskDefinition", "ecs:DescribeTaskDefinition",
				"ecs:CreateService", "ecs:UpdateService", "ecs:DeleteService", "ecs:DescribeServices",
				"ecs:TagResource", "ecs:UntagResource", "ecs:ListTagsForResource",
			},
			Resource: "*",
		})
	}
	if o.Template != nil && len(o.Template.Secrets) > 0 {
		// SSM と同じ理屈: CloudFormation は {{resolve:secretsmanager:…}} を
		// **このロールの資格情報で** 解決する。ALB の authenticate-oidc が
		// client_secret をここから読む(#112)。
		sts = append(sts, Statement{
			Sid: "ResolveSecretsManagerReferences", Effect: "Allow",
			Action:   []string{"secretsmanager:GetSecretValue"},
			Resource: secretARNs(o.Template.Secrets),
		})
	}
	if o.Template != nil && len(o.Template.SSMParams) > 0 {
		// CloudFormation は {{resolve:ssm:…}} を **このロールの資格情報で** 解決する。
		// 読むパスはテンプレートに書いてあるので、そこまで絞れる。
		sts = append(sts, Statement{
			Sid: "ResolveSsmDynamicReferences", Effect: "Allow",
			Action:   []string{"ssm:GetParameter", "ssm:GetParameters"},
			Resource: ssmParamARNs(o.Template.SSMParams),
		})
		if o.Template.HasSecureSSM {
			sts = append(sts, Statement{
				// ssm-secure は SecureString。既定キーでも復号の許可が要る
				Sid: "DecryptSecureSsm", Effect: "Allow",
				Action:   []string{"kms:Decrypt"},
				Resource: "*",
			})
		}
	}
	if o.Template != nil && o.Template.has("AWS::DynamoDB::Table") {
		sts = append(sts, Statement{
			// デプロイロールに要るのはテーブルのライフサイクル(データプレーンは実行ロール)
			Sid: "DynamoDBTableLifecycle", Effect: "Allow",
			Action: []string{
				"dynamodb:CreateTable", "dynamodb:DeleteTable", "dynamodb:DescribeTable", "dynamodb:UpdateTable",
				"dynamodb:TagResource", "dynamodb:UntagResource", "dynamodb:ListTagsOfResource",
				// CFN が作成・更新のたびに読む(テーブルを作るだけでも要る)
				"dynamodb:DescribeContinuousBackups", "dynamodb:DescribeTimeToLive",
			},
			Resource: fmt.Sprintf("arn:aws:dynamodb:*:*:table/%s*", p),
		})
	}
	if o.Template != nil && o.Template.has("AWS::SNS::Topic", "AWS::SNS::Subscription") {
		sts = append(sts, Statement{
			// Subscribe/Unsubscribe は AWS::SNS::Subscription のデプロイ用
			Sid: "SNSTopicLifecycle", Effect: "Allow",
			Action: []string{
				"sns:CreateTopic", "sns:DeleteTopic", "sns:GetTopicAttributes", "sns:SetTopicAttributes",
				"sns:Subscribe", "sns:Unsubscribe", "sns:TagResource", "sns:UntagResource", "sns:ListTagsForResource",
			},
			Resource: fmt.Sprintf("arn:aws:sns:*:*:%s*", p),
		})
	}
	if o.Template != nil && o.Template.has("AWS::SQS::Queue") {
		sts = append(sts, Statement{
			Sid: "SQSQueueLifecycle", Effect: "Allow",
			Action: []string{
				"sqs:CreateQueue", "sqs:DeleteQueue", "sqs:GetQueueAttributes", "sqs:SetQueueAttributes",
				"sqs:TagQueue", "sqs:UntagQueue", "sqs:ListQueueTags",
			},
			Resource: fmt.Sprintf("arn:aws:sqs:*:*:%s*", p),
		})
	}
	if o.BaseBucket != "" {
		sts = append(sts, Statement{
			// 共有 preview base バケットへの成果物 sync(post_up の aws s3 sync)。
			// バケットの作成・削除・ポリシー変更は含めない(base は別スタックの持ち物)
			Sid: "SharedWebBucketSync", Effect: "Allow",
			Action: []string{"s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
			Resource: []string{
				fmt.Sprintf("arn:aws:s3:::%s", o.BaseBucket),
				fmt.Sprintf("arn:aws:s3:::%s/*", o.BaseBucket),
			},
		})
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
	if o.Template != nil && o.Template.HasEventSourceMapping {
		sts = append(sts,
			Statement{
				// マッピングの CRUD は ARN で絞れない。uuid は作るまで決まらず、
				// Lambda は Create/Delete/Update いずれも `Resource: *` として評価する
				// (関数 ARN では通らない。絞ったつもりで 403 になる)
				Sid: "EventSourceMapping", Effect: "Allow",
				Action: []string{
					"lambda:CreateEventSourceMapping", "lambda:DeleteEventSourceMapping",
					"lambda:UpdateEventSourceMapping", "lambda:GetEventSourceMapping",
					"lambda:ListEventSourceMappings",
				},
				Resource: "*",
			},
			Statement{
				// タグ操作だけはマッピング ARN で評価される。CFN は作成直後に
				// TagResource を呼ぶので、これが無いと作成が丸ごと巻き戻る
				Sid: "EventSourceMappingTags", Effect: "Allow",
				Action:   []string{"lambda:TagResource", "lambda:UntagResource", "lambda:ListTags"},
				Resource: "arn:aws:lambda:*:*:event-source-mapping:*",
			})
	}
	hasCloudMap := o.Template != nil && o.Template.has("AWS::ECS::Cluster",
		"AWS::ServiceDiscovery::PrivateDnsNamespace", "AWS::ServiceDiscovery::Service")
	if hasCloudMap {
		sts = append(sts, cloudMapStatements(p)...)
	}
	if o.Template != nil && o.Template.has("AWS::Logs::LogGroup") && !hasCloudMap {
		sts = append(sts, containerLogStatement())
	}
	if o.Template != nil && o.Template.has("AWS::SSM::Parameter") {
		sts = append(sts, Statement{
			// ベースが SSM に書く契約値(CONTRACT §9)。**読む権限とは別**で、
			// ResolveSsmDynamicReferences(GetParameter)があっても作成はできない。
			// これが無いと base スタックが PutParameter の AccessDenied で
			// CREATE_FAILED になり、巻き戻しの削除も落ちてスタックが residue になる
			Sid: "BaseSsmParameters", Effect: "Allow",
			Action: []string{
				"ssm:PutParameter", "ssm:DeleteParameter", "ssm:DeleteParameters",
				"ssm:GetParameter", "ssm:GetParameters",
				"ssm:AddTagsToResource", "ssm:RemoveTagsFromResource", "ssm:ListTagsForResource",
				"ssm:LabelParameterVersion",
			},
			Resource: "arn:aws:ssm:*:*:parameter/kagerou/base/*",
		})
	}
	if wantS3 {
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
	if wantVPC {
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
		// 宛先インスタンスとドキュメントの両方で絞る。片方だけだと
		// 「任意の EC2 で任意のシェルを実行できるロール」になる。
		//
		// ただし **2 つの statement に分ける**。SendCommand は宛先ごとに権限を
		// 評価するので、1 つにまとめて条件を付けるとドキュメント ARN 側にも
		// 同じ条件が掛かり、タグを持たないドキュメントが落ちて 403 になる。
		target := Statement{
			Sid: "SashikiSendCommandTarget", Effect: "Allow",
			Action:   []string{"ssm:SendCommand"},
			Resource: fmt.Sprintf("arn:aws:ec2:*:*:instance/%s", o.InstanceID),
		}
		if o.InstanceTag != "" {
			// タグで絞れば、ホストを作り直して id が変わっても権限が追従する
			k, v, _ := strings.Cut(o.InstanceTag, "=")
			target.Resource = "arn:aws:ec2:*:*:instance/*"
			target.Condition = map[string]any{
				"StringEquals": map[string]string{"ssm:resourceTag/" + k: v},
			}
		}
		sts = append(sts,
			target,
			Statement{
				Sid: "SashikiSendCommandDocument", Effect: "Allow",
				Action:   []string{"ssm:SendCommand"},
				Resource: "arn:aws:ssm:*::document/AWS-RunShellScript",
			},
			Statement{
				// GetCommandInvocation はリソース単位で絞れない(読めるのは自分の command のみ)
				Sid: "SashikiReadCommandResult", Effect: "Allow",
				Action: []string{"ssm:GetCommandInvocation"}, Resource: "*",
			})
	}
	if o.BaseDomain {
		sts = append(sts, Statement{
			// {base_domain} の解決(CONTRACT §9)。ベースが書いたキーを読むだけ。
			// 書き込みは含めない — ベースは別スタックの持ち物
			Sid: "BaseDomainLookup", Effect: "Allow",
			Action:   []string{"ssm:GetParameter"},
			Resource: "arn:aws:ssm:*:*:parameter/kagerou/base/*",
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
	if wantRoute53 {
		sts = append(sts,
			Statement{
				// GetHostedZone は CFN の RecordSet ハンドラが書く前に必ず読む。
				// Change 系だけ足して 403 になるのが定番の踏み方
				Sid: "Route53Records", Effect: "Allow",
				Action: []string{
					"route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets",
					"route53:GetHostedZone",
				},
				Resource: fmt.Sprintf("arn:aws:route53:::hostedzone/%s", o.HostedZoneID),
			},
			Statement{
				// 反映待ちのポーリング。change id は作るまで不明でゾーンにも紐付かない
				Sid: "Route53ChangeStatus", Effect: "Allow",
				Action:   []string{"route53:GetChange"},
				Resource: "arn:aws:route53:::change/*",
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
	// OwnerID / RepoID は immutable subject(新しい org の既定)の sub 形式
	//   repo:<owner>@<ownerID>/<repo>@<repoID>:<context>
	// を組むための数値 id。両方そろったときだけ、その形式も許可に加える(#118)。
	OwnerID string
	RepoID  string
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
	// 許す context は 2 つ(preview の pull_request と、reap の schedule / 手動実行)。
	// immutable subject が有効なリポジトリではトークンの sub が id 入りの形で来るので、
	// 両方の形式を並べる。**古典形式だけだと StringEquals は一生マッチせず**、
	// 症状は sts:AssumeRoleWithWebIdentity の Not authorized — trust の JSON は
	// 正しく見えるので原因が遠い
	contexts := []string{"pull_request", "ref:refs/heads/" + branch}
	repos := []string{o.Repo}
	if o.OwnerID != "" && o.RepoID != "" {
		owner, name, _ := strings.Cut(o.Repo, "/")
		repos = append(repos, fmt.Sprintf("%s@%s/%s@%s", owner, o.OwnerID, name, o.RepoID))
	}
	var subs []string
	for _, r := range repos {
		for _, c := range contexts {
			subs = append(subs, fmt.Sprintf("repo:%s:%s", r, c))
		}
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
			// region ロック。Deny + StringNotEqualsIfExists はキーが無い場合も
			// true になり、IAM/CloudFront/Route53 等のグローバル操作まで落とす。
			// Null=false を AND して「キーが存在し、かつ許可外」だけ拒否する。
			Sid: "DenyOutsideRegions", Effect: "Deny",
			Action:   []string{"*"},
			Resource: "*",
			Condition: map[string]any{
				"StringNotEquals": map[string]any{"aws:RequestedRegion": o.Regions},
				"Null":            map[string]any{"aws:RequestedRegion": "false"},
			},
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
	return CheckDriftActions(PolicyAllowActions(generated), attached)
}

// CheckDriftActions は生成側の Allow アクション集合と attach ポリシーを突き合わせる。
// 1 本のロールを複数構成で共有している場合に和集合を渡せるよう、Policy ではなく
// 集合を受ける(#135)。単独の構成と比べると、他の構成にだけ要る権限が全部
// over-permission に見えてしまい、本当に見たい missing が埋もれる。
func CheckDriftActions(gen map[string]bool, attached []byte) (extra, missing []string, err error) {
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

// cloudMapStatements は Cloud Map 経由の apigw 構成に足りるぶん(#105)。
//
// main の EcsService(#170)はクラスタと Cloud Map を**共有ベースの持ち物**として
// 扱う。E2E fixture と、ベースをまだ作っていない構成は自前で作るので、その差を
// ここで埋める。EcsService と重ならない範囲だけを持つ。
//
// パッケージ冒頭の 3 パターンのうち 2 つがここに出る:
//   - 名前空間の作成は非同期で、CFN は GetOperation で完了を待つ(書く前後に読む)
//   - タグは本体と別の ARN で評価される。**タスク定義にも打たれる** —
//     CI で実際に踏んだ(CreateTaskDefinition は通るのに直後のタグで 403)
//
// もう 1 つ、型からは見えない形がある: **リソースが別サービスを連れてくる**。
// PrivateDnsNamespace の実体は Route53 のプライベートホストゾーンで、作成には
// route53:CreateHostedZone が要る。servicediscovery の権限を全部与えても通らない。
func cloudMapStatements(p string) []Statement {
	return []Statement{
		{
			// クラスタ。共有ベースを使う構成では要らないが、作る構成では要る
			Sid: "EcsCluster", Effect: "Allow",
			Action: []string{
				"ecs:CreateCluster", "ecs:DeleteCluster", "ecs:DescribeClusters",
			},
			Resource: fmt.Sprintf("arn:aws:ecs:*:*:cluster/%s*", p),
		},
		{
			// タグの対象にタスク定義を含める。EcsService 側は Resource:* なので
			// 通るが、将来そこを絞ったときにここが抜けないよう明示しておく
			Sid: "EcsTags", Effect: "Allow",
			Action: []string{"ecs:TagResource", "ecs:UntagResource", "ecs:ListTagsForResource"},
			Resource: []string{
				fmt.Sprintf("arn:aws:ecs:*:*:cluster/%s*", p),
				fmt.Sprintf("arn:aws:ecs:*:*:service/%s*/*", p),
				fmt.Sprintf("arn:aws:ecs:*:*:task-definition/%s*:*", p),
			},
		},
		{
			// Cloud Map。作成系は ARN で絞れず、名前空間の作成は非同期なので
			// GetOperation で待つ。これが無いとスタックが固まる
			Sid: "CloudMapDiscovery", Effect: "Allow",
			Action: []string{
				"servicediscovery:CreatePrivateDnsNamespace", "servicediscovery:DeleteNamespace",
				"servicediscovery:GetNamespace", "servicediscovery:ListNamespaces",
				"servicediscovery:CreateService", "servicediscovery:DeleteService",
				"servicediscovery:GetService", "servicediscovery:ListServices",
				"servicediscovery:ListInstances", "servicediscovery:GetOperation",
				"servicediscovery:TagResource", "servicediscovery:UntagResource", "servicediscovery:ListTagsForResource",
			},
			Resource: "*",
		},
		{
			// PrivateDnsNamespace は **Route53 のプライベートホストゾーンを作る**。
			// リソース型は ServiceDiscovery なので、型を見ているだけでは絶対に
			// 出てこない権限。ゾーンは作る前なので ARN で絞れない
			Sid: "CloudMapPrivateZone", Effect: "Allow",
			Action: []string{
				"route53:CreateHostedZone", "route53:DeleteHostedZone", "route53:GetHostedZone",
				"route53:ListHostedZonesByName", "route53:ListHostedZones",
				"route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets",
				"route53:GetChange", "route53:ChangeTagsForResource",
				// 名前空間の削除時に健全性チェックの後始末が走ることがある
				"route53:CreateHealthCheck", "route53:DeleteHealthCheck", "route53:GetHealthCheck",
			},
			Resource: "*",
		},
		{
			// タスクと VPC Link のセキュリティグループ。作成時点では ARN が無い
			Sid: "TaskNetworking", Effect: "Allow",
			Action: []string{
				"ec2:CreateSecurityGroup", "ec2:DeleteSecurityGroup", "ec2:DescribeSecurityGroups",
				"ec2:AuthorizeSecurityGroupIngress", "ec2:RevokeSecurityGroupIngress",
				"ec2:AuthorizeSecurityGroupEgress", "ec2:RevokeSecurityGroupEgress",
				"ec2:CreateTags", "ec2:DescribeVpcs", "ec2:DescribeSubnets",
			},
			Resource: "*",
		},
		containerLogStatement(),
	}
}

// containerLogStatement は生成テンプレートの明示 AWS::Logs::LogGroup 用。
// 雛形の規約は /kagerou/<env>(Lambda の /aws/lambda/... とは別の名前空間)。
func containerLogStatement() Statement {
	return Statement{
		Sid: "ContainerLogGroups", Effect: "Allow",
		Action: []string{
			"logs:CreateLogGroup", "logs:DeleteLogGroup", "logs:DescribeLogGroups",
			"logs:PutRetentionPolicy", "logs:TagResource", "logs:UntagResource", "logs:ListTagsForResource",
		},
		Resource: "arn:aws:logs:*:*:log-group:/kagerou/*",
	}
}

// Union は複数構成のポリシーを 1 本にまとめる。
//
// 1 つの CI ロールを複数の構成が共有するとき(E2E の 5 fixture がそれ)、
// **アタッチするポリシーはその和集合そのもの**。生成できないと結局手で書くことに
// なり、#135 が止めたかった「手で足して生成器が知らない」に戻る。
//
// 同じ Sid・同じ Condition の statement は Action と Resource を足して 1 つにする。
// Condition が違うものは別物として残す(緩い方に飲ませると権限が広がる)。
func Union(ps ...Policy) Policy {
	type key struct{ sid, cond string }
	var order []key
	merged := map[key]*Statement{}
	for _, p := range ps {
		for _, s := range p.Statement {
			c := ""
			if s.Condition != nil {
				b, _ := marshal(s.Condition)
				c = string(b)
			}
			k := key{s.Sid, c}
			cur, ok := merged[k]
			if !ok {
				cp := s
				cp.Action = append([]string(nil), s.Action...)
				cp.Resource = resourceList(s.Resource)
				merged[k] = &cp
				order = append(order, k)
				continue
			}
			cur.Action = addAll(cur.Action, s.Action)
			cur.Resource = addAll(resourceList(cur.Resource), resourceList(s.Resource))
		}
	}
	out := Policy{Version: "2012-10-17"}
	for _, k := range order {
		s := *merged[k]
		sort.Strings(s.Action)
		if rs, ok := s.Resource.([]string); ok {
			sort.Strings(rs)
			if len(rs) == 1 {
				s.Resource = rs[0] // 1 本なら文字列に戻す(単一構成の出力と揃える)
			} else {
				s.Resource = rs
			}
		}
		out.Statement = append(out.Statement, s)
	}
	return out
}

// resourceList は Resource(文字列 or 配列)を []string に正規化する。
func resourceList(r any) []string {
	switch v := r.(type) {
	case nil:
		return nil
	case string:
		return []string{v}
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// addAll は重複を作らずに足す。
func addAll(dst, src []string) []string {
	seen := map[string]bool{}
	for _, s := range dst {
		seen[s] = true
	}
	for _, s := range src {
		if !seen[s] {
			seen[s] = true
			dst = append(dst, s)
		}
	}
	return dst
}
