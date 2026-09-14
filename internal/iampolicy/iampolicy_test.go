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

// 1 本のデプロイロールを複数構成で共有している場合、単独の構成と突き合わせると
// 「他の構成にだけ要る権限」が全部 extra になって使えない。和集合で見る(#135)。
func TestCheckDriftActionsUnion(t *testing.T) {
	withQueue := Policy{Statement: []Statement{
		{Effect: "Allow", Action: []string{"sqs:CreateQueue", "cloudformation:CreateStack"}},
	}}
	withBucket := Policy{Statement: []Statement{
		{Effect: "Allow", Action: []string{"s3:PutObject", "cloudformation:CreateStack"}},
	}}
	attached := []byte(`{"Statement":[{"Effect":"Allow","Action":["sqs:CreateQueue","s3:PutObject","cloudformation:CreateStack"]}]}`)

	// 単独で見ると、もう片方の構成に要る権限が over-permission に見える
	extra, _, err := CheckDrift(withQueue, attached)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) == 0 {
		t.Fatal("前提が崩れている: 単独比較では extra が出るはず")
	}

	gen := PolicyAllowActions(withQueue)
	for a := range PolicyAllowActions(withBucket) {
		gen[a] = true
	}
	extra, missing, err := CheckDriftActions(gen, attached)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 || len(missing) != 0 {
		t.Errorf("和集合ならノイズゼロのはず: extra=%v missing=%v", extra, missing)
	}
}

// 手で足した権限(生成器が知らない)は和集合にしても extra として出る。
// これを検出できることが #135 の目的。
func TestCheckDriftActionsFindsHandAddedAction(t *testing.T) {
	gen := PolicyAllowActions(Policy{Statement: []Statement{
		{Effect: "Allow", Action: []string{"cloudformation:CreateStack"}},
	}})
	attached := []byte(`{"Statement":[{"Effect":"Allow","Action":["cloudformation:CreateStack","iam:PassRole"]}]}`)
	extra, missing, err := CheckDriftActions(gen, attached)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 1 || extra[0] != "iam:PassRole" {
		t.Errorf("手で足した iam:PassRole を検出していない: %v", extra)
	}
	if len(missing) != 0 {
		t.Errorf("missing は空のはず: %v", missing)
	}
}

// DynamoDB テーブルを作るだけで CFN が読む 2 つ。欠けると CreateStack が 403 で落ちる。
func TestDynamoDBLifecycleIncludesDescribeCalls(t *testing.T) {
	facts := TemplateFacts{Counts: map[string]int{"AWS::DynamoDB::Table": 1}}
	pol, err := Build(Options{Prefix: "kge2e-", Template: &facts})
	if err != nil {
		t.Fatal(err)
	}
	got := PolicyAllowActions(pol)
	for _, want := range []string{"dynamodb:DescribeContinuousBackups", "dynamodb:DescribeTimeToLive"} {
		if !got[want] {
			t.Errorf("%s が生成されていない", want)
		}
	}
}

// #118: immutable subject(新しい org の既定)のトークンは sub に数値 id を含む。
// 古典形式だけを StringEquals に並べると一生マッチせず、症状は
// AssumeRoleWithWebIdentity の Not authorized だけ。
func TestBuildTrustImmutableSubject(t *testing.T) {
	tp, err := BuildTrust(TrustOptions{
		Repo: "rikukadev/todo", Account: "123456789012",
		OwnerID: "328009292", RepoID: "1368452126",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := tp.JSON()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		// 古典形式(immutable subject を切っているリポジトリ)
		"repo:rikukadev/todo:pull_request",
		"repo:rikukadev/todo:ref:refs/heads/main",
		// id 形式(既定の新しい org)
		"repo:rikukadev@328009292/todo@1368452126:pull_request",
		"repo:rikukadev@328009292/todo@1368452126:ref:refs/heads/main",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("trust policy missing sub %q", want)
		}
	}
	// 許す context は増やさない。* に広げると他の workflow も assume できる
	if strings.Contains(s, `"repo:rikukadev/todo:*"`) {
		t.Error("context をワイルドカードに広げている")
	}
}

func TestBuildTrustWithoutIDsStaysClassic(t *testing.T) {
	// id が引けなかったときに中途半端な sub を作らない(片方だけでは形が作れない)
	for _, o := range []TrustOptions{
		{Repo: "a/b", OwnerID: "1"},
		{Repo: "a/b", RepoID: "2"},
		{Repo: "a/b"},
	} {
		tp, err := BuildTrust(o)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := tp.JSON()
		if strings.Contains(string(b), "@") {
			t.Errorf("id が揃っていないのに id 形式を出した: %+v", o)
		}
	}
}

func TestBuildCoversAlbEcsAndDynamicRefs(t *testing.T) {
	// 既定構成(lambda × 共有 ALB)と compute: ecs は kagerou init が生成する
	// 一次対応の形。警告を出して手書きさせるのではなく、権限を出す(#170)
	tf, err := ScanTemplate([]byte(`
Resources:
  Fn:
    Type: AWS::Serverless::Function
    Properties:
      PackageType: Image
  Tg:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
  Rule:
    Type: AWS::ElasticLoadBalancingV2::ListenerRule
    Properties:
      ListenerArn: "{{resolve:ssm:/kagerou/base/relay/alb_listener_arn}}"
`))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(Options{Prefix: "relay-", Template: &tf})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Statement{}
	for _, s := range p.Statement {
		byID[s.Sid] = s
	}

	alb, ok := byID["SharedAlbEntrypoint"]
	if !ok {
		t.Fatal("ALB 入口の statement が無い")
	}
	for _, want := range []string{
		"elasticloadbalancing:CreateTargetGroup",
		"elasticloadbalancing:CreateRule",
		"elasticloadbalancing:RegisterTargets", // lambda ターゲットでも要る
		"elasticloadbalancing:DescribeRules",
	} {
		if !hasAction(alb, want) {
			t.Errorf("ALB statement missing %q", want)
		}
	}

	// 動的参照は読むパスまで絞る
	ssm, ok := byID["ResolveSsmDynamicReferences"]
	if !ok {
		t.Fatal("ssm:GetParameters が無い(警告も出ないまま足りないポリシーになる)")
	}
	if got := ssm.Resource; got != "arn:aws:ssm:*:*:parameter/kagerou/base/relay/alb_listener_arn" {
		t.Fatalf("Resource = %v(読むパスに絞るべき)", got)
	}
	if _, ok := byID["DecryptSecureSsm"]; ok {
		t.Error("ssm-secure を使っていないのに kms:Decrypt を出している")
	}
	if _, ok := byID["EcsService"]; ok {
		t.Error("ECS を使っていないのに ECS の権限を出している")
	}
}

func TestBuildEcsService(t *testing.T) {
	tf, err := ScanTemplate([]byte("Resources:\n  S:\n    Type: AWS::ECS::Service\n  T:\n    Type: AWS::ECS::TaskDefinition\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(Options{Prefix: "relay-", Template: &tf})
	if err != nil {
		t.Fatal(err)
	}
	var ecs *Statement
	for i, s := range p.Statement {
		if s.Sid == "EcsService" {
			ecs = &p.Statement[i]
		}
	}
	if ecs == nil {
		t.Fatal("ECS の statement が無い")
	}
	for _, want := range []string{"ecs:RegisterTaskDefinition", "ecs:CreateService", "ecs:DeleteService"} {
		if !hasAction(*ecs, want) {
			t.Errorf("ECS statement missing %q", want)
		}
	}
}

func hasAction(s Statement, want string) bool {
	for _, a := range s.Action {
		if a == want {
			return true
		}
	}
	return false
}

// #105: compute が ECS の構成。Lambda 前提の導出では 1 つも出ない。
func TestECSStatementsDerived(t *testing.T) {
	f, err := ScanTemplate([]byte(`
Resources:
  Cluster:
    Type: AWS::ECS::Cluster
  Svc:
    Type: AWS::ECS::Service
  Task:
    Type: AWS::ECS::TaskDefinition
  Ns:
    Type: AWS::ServiceDiscovery::PrivateDnsNamespace
  Disco:
    Type: AWS::ServiceDiscovery::Service
  Sg:
    Type: AWS::EC2::SecurityGroup
  Link:
    Type: AWS::ApiGatewayV2::VpcLink
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Unknown) != 0 {
		t.Errorf("未知型として警告している: %v", f.Unknown)
	}
	p, err := Build(Options{Prefix: "p-", Template: &f})
	if err != nil {
		t.Fatal(err)
	}
	// タスク定義の登録はリソース単位の権限に対応していない。ARN で絞ると
	// 「絞ったつもりで 403」になる(EventSourceMapping と同じ穴)
	td := findSid(t, p, "EcsTaskDefinition")
	if got, ok := td.Resource.(string); !ok || got != "*" {
		t.Errorf("RegisterTaskDefinition は Resource:* のはず: %#v", td.Resource)
	}
	// 名前空間の作成は非同期。GetOperation が無いとスタックが待ち続ける
	cm := findSid(t, p, "CloudMapDiscovery")
	if !strings.Contains(strings.Join(cm.Action, ","), "servicediscovery:GetOperation") {
		t.Error("非同期完了待ちの GetOperation が無い")
	}
	// クラスタとサービスは prefix で絞れる(絞れるものは絞る)
	cs := findSid(t, p, "EcsClusterAndService")
	rs, ok := cs.Resource.([]string)
	if !ok || len(rs) == 0 || !strings.Contains(rs[0], "p-") {
		t.Errorf("prefix でスコープしていない: %#v", cs.Resource)
	}
	// コンテナのログは Lambda の /aws/lambda/… とは別の名前空間
	lg := findSid(t, p, "ContainerLogGroups")
	if got, _ := lg.Resource.(string); !strings.Contains(got, "/kagerou/") {
		t.Errorf("ログの名前空間が雛形の規約と違う: %#v", lg.Resource)
	}
}

// Lambda だけの構成に ECS 権限を出さない(テンプレートが真実の源)。
func TestNoECSStatementsWithoutECS(t *testing.T) {
	f, err := ScanTemplate([]byte("Resources:\n  Fn:\n    Type: AWS::Serverless::Function\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := mustJSON(t, Options{Prefix: "p-", Template: &f})
	for _, notWant := range []string{"ecs:", "servicediscovery:", "ec2:CreateSecurityGroup"} {
		if strings.Contains(s, notWant) {
			t.Errorf("ECS 構成でないのに %q が出ている", notWant)
		}
	}
}
