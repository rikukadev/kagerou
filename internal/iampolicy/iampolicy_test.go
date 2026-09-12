package iampolicy

import (
	"strings"
	"testing"
)

func mustJSON(t *testing.T, o Options) string {
	t.Helper()
	p, err := Build(o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuildBaseScopesByPrefix(t *testing.T) {
	s := mustJSON(t, Options{Prefix: "myapp-"})
	for _, want := range []string{
		"arn:aws:cloudformation:*:*:stack/myapp-*/*",
		"arn:aws:lambda:*:*:function:myapp-*",
		"arn:aws:iam::*:role/myapp-*",          // PassRole のスコープ
		"iam:PassRole",                          // 一番分かりにくい必須権限
		"transform/Serverless-2016-10-31",       // SAM Transform
		"aws-sam-cli-managed",                   // 成果物バケット
		"cloudformation:GetTemplateSummary",     // スタック存在前に呼ばれる
	} {
		if !strings.Contains(s, want) {
			t.Errorf("base policy missing %q", want)
		}
	}
	for _, notWant := range []string{"ecr:", "PutBucketWebsite", "ssm:", "cloudfront:", "route53:"} {
		if strings.Contains(s, notWant) {
			t.Errorf("base policy should not contain %q", notWant)
		}
	}
}

func TestBuildModules(t *testing.T) {
	s := mustJSON(t, Options{
		Prefix: "myapp-", ECR: true, EcrRepo: "myapp", S3: true, VPC: true,
		SashikiSSM: true, InstanceID: "i-0123", CloudFront: true, Route53: true, HostedZoneID: "Z123",
	})
	for _, want := range []string{
		"ecr:GetAuthorizationToken",
		"arn:aws:ecr:*:*:repository/myapp",
		"s3:PutBucketWebsite",
		"arn:aws:s3:::myapp-*",
		"ec2:CreateNetworkInterface",
		"arn:aws:ec2:*:*:instance/i-0123",
		"document/AWS-RunShellScript", // 宛先とドキュメント両方で絞る
		"cloudfront:CreateInvalidation",
		"hostedzone/Z123",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("modules policy missing %q", want)
		}
	}
}

func TestBuildValidation(t *testing.T) {
	if _, err := Build(Options{}); err == nil {
		t.Error("prefix なしはエラーのはず")
	}
	if _, err := Build(Options{Prefix: "p-", SashikiSSM: true}); err == nil {
		t.Error("--with-sashiki-ssm は instance-id 必須のはず")
	}
	if _, err := Build(Options{Prefix: "p-", Route53: true}); err == nil {
		t.Error("--with-route53 は hosted-zone-id 必須のはず")
	}
	if _, err := Build(Options{Prefix: "p-", ECR: true}); err == nil {
		t.Error("--with-ecr は ecr-repo 必須のはず")
	}
}
