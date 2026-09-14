package iampolicy

import (
	"strings"
	"testing"
)

const scanFixture = `
AWSTemplateFormatVersion: "2010-09-09"
Transform: AWS::Serverless-2016-10-31
Resources:
  Api:
    Type: AWS::Serverless::HttpApi
  Fn:
    Type: AWS::Serverless::Function
    Properties:
      ImageUri: !Sub "${EcrRepo}:latest"
      VpcConfig:
        SubnetIds: [subnet-1]
  Queue:
    Type: AWS::SQS::Queue
  Mystery:
    Type: AWS::SecretsManager::Secret
`

func TestScanTemplate(t *testing.T) {
	f, err := ScanTemplate([]byte(scanFixture)) // !Sub 入りでも読める
	if err != nil {
		t.Fatal(err)
	}
	if f.Counts["AWS::Serverless::Function"] != 1 || f.Counts["AWS::SQS::Queue"] != 1 {
		t.Fatalf("counts broken: %v", f.Counts)
	}
	if !f.HasVPC {
		t.Error("VpcConfig を検出するはず")
	}
	// 知らない型は黙らず Unknown に入る(既知の型は入らない)
	if len(f.Unknown) != 1 || f.Unknown[0] != "AWS::SecretsManager::Secret" {
		t.Fatalf("unknown = %v", f.Unknown)
	}
}

func TestBuildFromTemplate(t *testing.T) {
	f, err := ScanTemplate([]byte(scanFixture))
	if err != nil {
		t.Fatal(err)
	}
	// --with-s3 を主張してもテンプレートに Bucket が無ければ S3 statement は出ない
	p, err := Build(Options{Prefix: "tier3-", Template: &f, S3: true})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.JSON()
	s := string(b)
	if strings.Contains(s, "s3:CreateBucket") && strings.Contains(s, "tier3-") && strings.Contains(s, "WebBucketLifecycle") {
		t.Error("テンプレートに無い S3 バケット権限を出してはいけない")
	}
	for _, want := range []string{
		"lambda:CreateFunction",      // Function がある
		"apigateway:*",               // HttpApi がある
		"ec2:CreateNetworkInterface", // VpcConfig から導出
		"sqs:CreateQueue",            // Queue から導出
		"arn:aws:sqs:*:*:tier3-*",    // prefix スコープ
	} {
		if !strings.Contains(s, want) {
			t.Errorf("template-derived policy missing %q", want)
		}
	}
}

func TestBuildTemplateWithoutLambda(t *testing.T) {
	// static 系: Bucket だけのテンプレートなら Lambda / API / logs は出ない
	f, err := ScanTemplate([]byte(`
Resources:
  Web:
    Type: AWS::S3::Bucket
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(Options{Prefix: "web-", Template: &f})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.JSON()
	s := string(b)
	for _, notWant := range []string{"lambda:", "apigateway:", "iam:PassRole"} {
		if strings.Contains(s, notWant) {
			t.Errorf("bucket-only template should not contain %q", notWant)
		}
	}
	if !strings.Contains(s, "s3:PutBucketWebsite") {
		t.Error("Bucket があるので WebBucketLifecycle は出るはず")
	}
}

func TestBuildBaseBucketSync(t *testing.T) {
	p, err := Build(Options{Prefix: "x-", BaseBucket: "kagerou-base-todo-123"})
	if err != nil {
		t.Fatal(err)
	}
	var sync *Statement
	for i := range p.Statement {
		if p.Statement[i].Sid == "SharedWebBucketSync" {
			sync = &p.Statement[i]
		}
	}
	if sync == nil {
		t.Fatal("SharedWebBucketSync statement missing")
	}
	actions := strings.Join(sync.Action, ",")
	for _, want := range []string{"s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject"} {
		if !strings.Contains(actions, want) {
			t.Errorf("sync actions missing %q: %s", want, actions)
		}
	}
	// sync に要らない破壊系はこの statement に含めない(base は別スタックの持ち物)
	for _, notWant := range []string{"s3:CreateBucket", "s3:DeleteBucket", "s3:PutBucketPolicy"} {
		if strings.Contains(actions, notWant) {
			t.Errorf("sync should not contain %q", notWant)
		}
	}
	res, _ := sync.Resource.([]string)
	if len(res) != 2 || res[0] != "arn:aws:s3:::kagerou-base-todo-123" || res[1] != "arn:aws:s3:::kagerou-base-todo-123/*" {
		t.Fatalf("sync resources = %v", sync.Resource)
	}
}

// コンテナイメージで配る Function は ECR が要る。--with-ecr を付け忘れても
// 足りないポリシーを出さないよう、テンプレートから導出する(#71/#135)。
func TestScanTemplateDetectsImagePackaging(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"PackageType: Image", "Resources:\n  F:\n    Type: AWS::Serverless::Function\n    Properties:\n      PackageType: Image\n", true},
		{"sam package 後の ImageUri", "Resources:\n  F:\n    Type: AWS::Serverless::Function\n    Properties:\n      ImageUri: 1.dkr.ecr.ap-northeast-1.amazonaws.com/x:y\n", true},
		{"zip", "Resources:\n  F:\n    Type: AWS::Serverless::Function\n    Properties:\n      Handler: index.handler\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ScanTemplate([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if f.HasImage != tc.want {
				t.Errorf("HasImage = %v, want %v", f.HasImage, tc.want)
			}
		})
	}
}

func TestScanTemplateDynamicRefs(t *testing.T) {
	// 動的参照はプロパティの「値」なので、型の数え上げには現れない。
	// 拾わないと警告も出ないまま足りないポリシーになる(#170)
	body := []byte(`
Resources:
  Rule:
    Type: AWS::ElasticLoadBalancingV2::ListenerRule
    Properties:
      ListenerArn: "{{resolve:ssm:/kagerou/base/relay/alb_listener_arn}}"
  Svc:
    Type: AWS::ECS::Service
    Properties:
      Cluster: "{{resolve:ssm:/kagerou/base/relay/alb_cluster}}"
      Dup: "{{resolve:ssm:/kagerou/base/relay/alb_cluster}}"
      Pinned: "{{resolve:ssm:/kagerou/base/relay/alb_vpc_id:3}}"
`)
	f, err := ScanTemplate(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/kagerou/base/relay/alb_cluster",
		"/kagerou/base/relay/alb_listener_arn",
		"/kagerou/base/relay/alb_vpc_id", // バージョン指定は落とす
	}
	if strings.Join(f.SSMParams, ",") != strings.Join(want, ",") {
		t.Fatalf("SSMParams = %v, want %v", f.SSMParams, want)
	}
	if f.HasSecureSSM {
		t.Error("ssm-secure は使っていない")
	}
	// ALB / ECS の型は既知になったので警告しない
	if len(f.Unknown) != 0 {
		t.Errorf("Unknown = %v, want empty", f.Unknown)
	}
}

func TestScanTemplateSecureSSM(t *testing.T) {
	f, err := ScanTemplate([]byte(`
Resources:
  X:
    Type: AWS::ECS::Service
    Properties:
      Secret: "{{resolve:ssm-secure:/app/db/password}}"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !f.HasSecureSSM {
		t.Error("ssm-secure を検出できていない")
	}
	if len(f.SSMParams) != 1 || f.SSMParams[0] != "/app/db/password" {
		t.Fatalf("SSMParams = %v", f.SSMParams)
	}
}

func TestScanTemplateNoDynamicRefs(t *testing.T) {
	// 使っていないテンプレートで ssm: を増やさない
	f, err := ScanTemplate([]byte("Resources:\n  F:\n    Type: AWS::Serverless::Function\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.SSMParams) != 0 || f.HasSecureSSM {
		t.Fatalf("動的参照なしで拾っている: %+v", f.SSMParams)
	}
}
