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
		"arn:aws:iam::*:role/myapp-*",       // PassRole のスコープ
		"iam:PassRole",                      // 一番分かりにくい必須権限
		"transform/Serverless-2016-10-31",   // SAM Transform
		"aws-sam-cli-managed",               // 成果物バケット
		"cloudformation:GetTemplateSummary", // スタック存在前に呼ばれる
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

func TestBuildTrust(t *testing.T) {
	tp, err := BuildTrust(TrustOptions{Repo: "rikukadev/todo", Account: "123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := tp.JSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"sts:AssumeRoleWithWebIdentity",
		"oidc-provider/token.actions.githubusercontent.com",
		"arn:aws:iam::123456789012:oidc-provider", // account が ARN に入る
		"repo:rikukadev/todo:pull_request",        // preview
		"repo:rikukadev/todo:ref:refs/heads/main", // reap の既定ブランチ
		"sts.amazonaws.com",                       // aud 固定
	} {
		if !strings.Contains(s, want) {
			t.Errorf("trust policy missing %q\n%s", want, s)
		}
	}
	// fork ガード: 別リポジトリの sub は出てこない
	if strings.Contains(s, "repo:attacker/") {
		t.Error("trust policy should be scoped to the given repo only")
	}
}

func TestBuildTrustDefaultsAndValidation(t *testing.T) {
	// account 省略時は placeholder、branch は main
	tp, _ := BuildTrust(TrustOptions{Repo: "o/n"})
	b, _ := tp.JSON()
	if !strings.Contains(string(b), AccountPlaceholder) {
		t.Errorf("account 省略時は %s を埋めるはず", AccountPlaceholder)
	}
	if _, err := BuildTrust(TrustOptions{}); err == nil {
		t.Error("repo なしはエラーのはず")
	}
	if _, err := BuildTrust(TrustOptions{Repo: "no-slash"}); err == nil {
		t.Error("owner/name 形式でないとエラーのはず")
	}
}

func TestBuildBoundary(t *testing.T) {
	bp, err := BuildBoundary(BoundaryOptions{Prefix: "myapp-", Regions: []string{"ap-northeast-1"}, Account: "123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := bp.JSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"StringNotEqualsIfExists", // region ロックはグローバルサービスを誤爆させない
		"ap-northeast-1",          // 許可 region
		"DenyIamPrivilegeEscalation",
		"iam:CreateUser",
		"NotResource",                 // 名前空間外の IAM 書込を落とす
		"arn:aws:iam::*:role/myapp-*", // name_prefix 名前空間
		"RequireBoundaryOnNewRoles",   // 作成ロールに boundary を強制
		"iam:PermissionsBoundary",
		"arn:aws:iam::123456789012:policy/myapp-boundary", // boundary 自身の ARN
		"organizations:*",                                 // 組織は管轄外
	} {
		if !strings.Contains(s, want) {
			t.Errorf("boundary missing %q\n%s", want, s)
		}
	}
}

func TestBuildBoundaryValidation(t *testing.T) {
	if _, err := BuildBoundary(BoundaryOptions{Regions: []string{"us-east-1"}}); err == nil {
		t.Error("prefix なしはエラーのはず")
	}
	if _, err := BuildBoundary(BoundaryOptions{Prefix: "p-"}); err == nil {
		t.Error("region なしはエラーのはず")
	}
}

func TestBuildExecution(t *testing.T) {
	p, err := BuildExecution(ExecutionOptions{
		Prefix: "myapp-", VPC: true,
		Allow: []AllowRule{{
			Actions:   []string{"secretsmanager:GetSecretValue"},
			Resources: []string{"arn:aws:secretsmanager:*:*:secret:myapp-*"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.JSON()
	s := string(b)
	for _, want := range []string{
		"logs:CreateLogGroup",
		"log-group:/aws/lambda/myapp-*",             // ロググループを name_prefix に絞る
		"log-group:/aws/lambda/myapp-*:*",           // ストリームまで
		"ec2:CreateNetworkInterface",                // --with-vpc
		"secretsmanager:GetSecretValue",             // 宣言した allow
		"arn:aws:secretsmanager:*:*:secret:myapp-*", // 宣言した resource
	} {
		if !strings.Contains(s, want) {
			t.Errorf("execution policy missing %q", want)
		}
	}
	// VPC 無し・allow 無しでも logs は出る
	base, err := BuildExecution(ExecutionOptions{Prefix: "x-"})
	if err != nil {
		t.Fatal(err)
	}
	bs, _ := base.JSON()
	if strings.Contains(string(bs), "ec2:CreateNetworkInterface") {
		t.Error("VPC 無しで ENI 権限が出ている")
	}
}

func TestBuildExecutionValidation(t *testing.T) {
	if _, err := BuildExecution(ExecutionOptions{}); err == nil {
		t.Error("prefix なしはエラーのはず")
	}
	if _, err := BuildExecution(ExecutionOptions{Prefix: "p-", Allow: []AllowRule{{Actions: []string{"s3:GetObject"}}}}); err == nil {
		t.Error("resource なしの allow はエラーのはず")
	}
}

func TestCheckDrift(t *testing.T) {
	gen, _ := BuildExecution(ExecutionOptions{Prefix: "myapp-"}) // logs のみ
	// attach 側が s3:* を余計に持ち、logs:PutLogEvents を欠く。Action は string / 配列混在。
	attached := []byte(`{
	  "Version": "2012-10-17",
	  "Statement": [
	    {"Effect": "Allow", "Action": ["logs:CreateLogGroup", "logs:CreateLogStream"], "Resource": "*"},
	    {"Effect": "Allow", "Action": "s3:*", "Resource": "*"},
	    {"Effect": "Deny",  "Action": "iam:*", "Resource": "*"}
	  ]
	}`)
	extra, missing, err := CheckDrift(gen, attached)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 1 || extra[0] != "s3:*" {
		t.Errorf("過剰権限 s3:* を検出するはず: %v", extra)
	}
	if !containsStr(missing, "logs:PutLogEvents") {
		t.Errorf("欠落 logs:PutLogEvents を検出するはず: %v", missing)
	}
	// Deny の iam:* は Allow 集合に入らない(過剰権限扱いしない)
	if containsStr(extra, "iam:*") {
		t.Error("Deny のアクションを過剰権限に数えてはいけない")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
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

// ここから下は「実 AWS で 403 を踏んでから足した権限」の回帰。どれも
// ローカル(admin)では再現せず、CI の絞ったロールでしか出ない類なので、
// 落ちたら「また同じ穴を開けた」と読むこと。

func TestRoute53NeedsGetHostedZone(t *testing.T) {
	s := mustJSON(t, Options{Prefix: "p-", Route53: true, HostedZoneID: "Z123"})
	for _, want := range []string{
		// CFN の RecordSet ハンドラは書く前にゾーンを読む
		"route53:GetHostedZone",
		// 反映待ちのポーリング。change id はゾーンに紐付かないので別 statement
		"route53:GetChange",
		"arn:aws:route53:::change/*",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("route53 policy missing %q", want)
		}
	}
}

func TestRoute53DerivedFromTemplate(t *testing.T) {
	f, err := ScanTemplate([]byte(`
Resources:
  Rec:
    Type: AWS::Route53::RecordSet
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Unknown) != 0 {
		t.Errorf("RecordSet を未知型として警告している: %v", f.Unknown)
	}
	// テンプレートが RecordSet を持てば --with-route53 無しでも権限が要る
	if _, err := Build(Options{Prefix: "p-", Template: &f}); err == nil {
		t.Error("RecordSet があるのに hosted-zone-id 無しで通ってしまった")
	}
	s := mustJSON(t, Options{Prefix: "p-", Template: &f, HostedZoneID: "Z9"})
	if !strings.Contains(s, "route53:ChangeResourceRecordSets") {
		t.Error("テンプレート由来の route53 権限が出ていない")
	}
}

func TestEventSourceMappingScoping(t *testing.T) {
	f, err := ScanTemplate([]byte(`
Resources:
  Fn:
    Type: AWS::Serverless::Function
  Map:
    Type: AWS::Lambda::EventSourceMapping
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Unknown) != 0 {
		t.Errorf("EventSourceMapping を未知型として警告している: %v", f.Unknown)
	}
	p, err := Build(Options{Prefix: "p-", Template: &f})
	if err != nil {
		t.Fatal(err)
	}
	crud := findSid(t, p, "EventSourceMapping")
	// CRUD を関数 ARN に絞ると 403 になる(Lambda は Resource:* で評価する)。
	// 「絞ったつもり」を防ぐため、ここは * であることを明示的に固定する
	if got, ok := crud.Resource.(string); !ok || got != "*" {
		t.Errorf("mapping の CRUD は Resource:* のはず: %#v", crud.Resource)
	}
	tags := findSid(t, p, "EventSourceMappingTags")
	// 逆にタグはマッピング ARN で評価される。CFN が作成直後に呼ぶので必須
	if got, _ := tags.Resource.(string); got != "arn:aws:lambda:*:*:event-source-mapping:*" {
		t.Errorf("タグはマッピング ARN で絞るはず: %#v", tags.Resource)
	}
	if !strings.Contains(strings.Join(tags.Action, ","), "lambda:TagResource") {
		t.Errorf("lambda:TagResource が無い: %v", tags.Action)
	}
}

func TestSendCommandTagConditionOnlyOnInstance(t *testing.T) {
	p, err := Build(Options{Prefix: "p-", SashikiSSM: true, InstanceTag: "Role=sashiki"})
	if err != nil {
		t.Fatal(err)
	}
	target := findSid(t, p, "SashikiSendCommandTarget")
	if target.Condition == nil {
		t.Error("宛先インスタンスにタグ条件が無い(任意の EC2 を叩けてしまう)")
	}
	// 条件をドキュメント側にも掛けると、タグを持たない AWS-RunShellScript が
	// 落ちて SendCommand 全体が 403 になる。statement を分ける理由がこれ
	doc := findSid(t, p, "SashikiSendCommandDocument")
	if doc.Condition != nil {
		t.Error("ドキュメント側にタグ条件が掛かっている(SendCommand が 403 になる)")
	}
}

func TestSendCommandValidation(t *testing.T) {
	if _, err := Build(Options{Prefix: "p-", SashikiSSM: true, InstanceID: "i-1", InstanceTag: "K=V"}); err == nil {
		t.Error("id とタグの併用はエラーのはず")
	}
	if _, err := Build(Options{Prefix: "p-", SashikiSSM: true, InstanceTag: "nope"}); err == nil {
		t.Error("Key=Value でないタグはエラーのはず")
	}
}

func findSid(t *testing.T, p Policy, sid string) Statement {
	t.Helper()
	for _, s := range p.Statement {
		if s.Sid == sid {
			return s
		}
	}
	t.Fatalf("statement %q が無い", sid)
	return Statement{}
}
