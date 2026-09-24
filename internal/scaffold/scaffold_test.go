package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/appscan"
)

func read(t *testing.T, dir, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestDockerfileScaffoldGo(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, Params{Project: "svc", Region: "r", Framework: "go"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Created, "Dockerfile") {
		t.Fatalf("go では Dockerfile を生成するはず: %v", res.Created)
	}
	df := read(t, dir, "Dockerfile")
	for _, want := range []string{
		LWALine,       // 既存注入と同じ LWA 行を焼き込む
		"EXPOSE 8080", // go の既定ポート
		"ENV PORT=8080",
		"golang:1-alpine", // Go のマルチステージ
	} {
		if !strings.Contains(df, want) {
			t.Errorf("go Dockerfile missing %q", want)
		}
	}
}

func TestDockerfileScaffoldNextUsesDetectedPort(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "web", Region: "r", Framework: "next", Port: "4000"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	df := read(t, dir, "Dockerfile")
	if !strings.Contains(df, "EXPOSE 4000") || !strings.Contains(df, "ENV PORT=4000") {
		t.Errorf("検出ポートを使うはず:\n%s", df)
	}
	if !strings.Contains(df, ".next/standalone") || !strings.Contains(df, "server.js") {
		t.Errorf("Next.js standalone 雛形になっていない:\n%s", df)
	}
}

func TestDockerfileScaffoldNodeSSR(t *testing.T) {
	dir := t.TempDir()
	// react-router などの Node SSR は node 雛形に寄せる。ポート未検出なら 3000。
	if _, err := Run(dir, Params{Project: "app", Region: "r", Framework: "react-router"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	df := read(t, dir, "Dockerfile")
	if !strings.Contains(df, "EXPOSE 3000") || !strings.Contains(df, `CMD ["npm", "start"]`) {
		t.Errorf("node 雛形になっていない:\n%s", df)
	}
}

func TestDockerfileScaffoldUnknownFrameworkSkips(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, Params{Project: "x", Region: "r", Framework: "cobol"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if contains(res.Created, "Dockerfile") {
		t.Error("未知フレームワークでは Dockerfile を作らないはず")
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		t.Error("Dockerfile が書き出されている")
	}
}

func TestDockerfileScaffoldNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	// 既存 Dockerfile は force でも上書きしない(アプリの実体)。
	original := "FROM scratch\n# mine\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Run(dir, Params{Project: "svc", Region: "r", Framework: "go"}, AllTargets(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Skipped, "Dockerfile") {
		t.Fatalf("既存 Dockerfile は skip のはず: %v", res.Skipped)
	}
	if got := read(t, dir, "Dockerfile"); got != original {
		t.Errorf("既存 Dockerfile を上書きした:\n%s", got)
	}
}

func TestRunCreatesAll(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, Params{Project: "myapp", Region: "ap-northeast-1"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 4 || len(res.Skipped) != 0 {
		t.Fatalf("created=%v skipped=%v", res.Created, res.Skipped)
	}

	ky := read(t, dir, "kagerou.yaml")
	if !strings.Contains(ky, "project: myapp") || !strings.Contains(ky, "name_prefix: myapp-") {
		t.Fatalf("kagerou.yaml: %s", ky)
	}
	if strings.Contains(ky, "sashiki") {
		t.Fatal("sashiki block should be absent without --sashiki")
	}

	pv := read(t, dir, ".github/workflows/kagerou-preview.yml")
	if !strings.Contains(pv, "${{ vars.AWS_ROLE_ARN }}") || !strings.Contains(pv, "rikukadev/kagerou/action@v0") {
		t.Fatalf("preview.yml placeholders broken: %s", pv[:200])
	}

	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "EnvKagerouEnv") || !strings.Contains(tp, "KagerouUrl") {
		t.Fatalf("template.yaml: missing contract pieces")
	}
}

func TestRunSashikiBlock(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "myapp", Region: "ap-northeast-1", Sashiki: true}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	ky := read(t, dir, "kagerou.yaml")
	for _, want := range []string{"pre_up: sashiki create {name}", "post_down: sashiki delete {name}", "DB_USER: dev@{name}"} {
		if !strings.Contains(ky, want) {
			t.Errorf("kagerou.yaml missing %q", want)
		}
	}
}

func TestRunSkipsExistingAndForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kagerou.yaml"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template.yaml"), []byte("my template"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(dir, Params{Project: "x", Region: "r"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped=%v", res.Skipped)
	}
	if read(t, dir, "kagerou.yaml") != "mine" {
		t.Fatal("existing kagerou.yaml was overwritten without force")
	}

	// force: kagerou.yaml は上書き、template.yaml は force でも守る
	if _, err := Run(dir, Params{Project: "x", Region: "r"}, AllTargets(), true); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "kagerou.yaml") == "mine" {
		t.Fatal("force should overwrite kagerou.yaml")
	}
	if read(t, dir, "template.yaml") != "my template" {
		t.Fatal("template.yaml must never be overwritten")
	}
}

func TestStepsAndPlain(t *testing.T) {
	det := Detection{VarsSet: map[string]bool{}}
	base := Steps(Params{Region: "r"}, det, SetupSkip)
	withSashiki := Steps(Params{Region: "r", Sashiki: true}, det, SetupSkip)
	if len(withSashiki) != len(base)+2 {
		t.Fatalf("sashiki steps not added: %d vs %d", len(withSashiki), len(base))
	}
	plain := PlainSteps(Params{Region: "ap-northeast-1"}, det)
	if !strings.Contains(plain, "1. ") || !strings.Contains(plain, "ECR") {
		t.Fatalf("plain steps: %s", plain)
	}
}

func TestWriteSetupScript(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteSetupScript(dir, Params{Region: "ap-northeast-1"}, Detection{Owner: "rikukadev", Repo: "myapp"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`OWNER="rikukadev"`, `REPO="myapp"`, `REGION="ap-northeast-1"`,
		"create-open-id-connect-provider",                // provider が無ければ作る
		"repo:${OWNER}/${REPO}:*",                        // 旧形式 sub
		"repo:${OWNER}@${OWNER_ID}/${REPO}@${REPO_ID}:*", // ID 形式 sub(新 org)
		"create-repository",
		"gh variable set AWS_ROLE_ARN",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("setup script missing %q", want)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode()&0o100 == 0 {
		t.Error("setup script should be executable")
	}
}

func TestStepsSetupModes(t *testing.T) {
	det := Detection{VarsSet: map[string]bool{}}
	p := Params{Region: "r"}
	skip := Steps(p, det, SetupSkip)
	script := Steps(p, det, SetupScript)
	applied := Steps(p, det, SetupApplied)
	if len(script) != len(skip)-2 || len(applied) != len(skip)-2 {
		t.Fatalf("script/applied should fold 3 steps into 1: skip=%d script=%d applied=%d", len(skip), len(script), len(applied))
	}
	foundDone := false
	for _, s := range applied {
		if s.Done && strings.Contains(s.Title, "AWS setup") {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatal("applied mode should mark AWS setup as done")
	}
}

func TestScaffoldWithWants(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "shop", Region: "r",
		Wants: appscan.Wants{DynamoDB: true, SQS: true, Redis: true}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		"AppTable:", "AWS::DynamoDB::Table", "PAY_PER_REQUEST", "TABLE_NAME: !Ref AppTable",
		"DynamoDBCrudPolicy",
		"AppQueue:", "AWS::SQS::Queue", "QUEUE_URL: !Ref AppQueue", "SQSSendMessagePolicy",
		"EnvRedisUrl:", "REDIS_URL: !Ref EnvRedisUrl", // redis はリソースでなく env の口
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("template missing %q", want)
		}
	}
	if strings.Contains(tp, "AppBucket") {
		t.Error("S3 未検出なのに AppBucket が出ている")
	}
	ky := read(t, dir, "kagerou.yaml")
	if !strings.Contains(ky, "REDIS_URL:") || !strings.Contains(ky, `prefix "{name}:"`) {
		t.Errorf("kagerou.yaml missing redis env TODO: %s", ky)
	}
	if strings.Contains(ky, "OPENSEARCH_URL") {
		t.Error("opensearch 未検出なのに env が出ている")
	}
}

func TestScaffoldWithoutWantsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "plain", Region: "r"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, notWant := range []string{"AppTable", "AppQueue", "AppBucket", "Policies:", "EnvRedisUrl"} {
		if strings.Contains(tp, notWant) {
			t.Errorf("wants 無しでは %q を出さないはず", notWant)
		}
	}
}

func TestScaffoldedDockerfilePortMatchesTemplate(t *testing.T) {
	// fresh 3tier で発覚: 雛形 Dockerfile(go=8080)と template の AWS_LWA_PORT(3000 TODO)が
	// 食い違い、最初のデプロイから 502 になる。雛形を生成するときは同じポートを両方に使う。
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "svc", Region: "r", Framework: "go"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	df := read(t, dir, "Dockerfile")
	tp := read(t, dir, "template.yaml")
	if !strings.Contains(df, "EXPOSE 8080") {
		t.Fatalf("go 雛形は 8080 のはず:\n%s", df)
	}
	if !strings.Contains(tp, `AWS_LWA_PORT: "8080"`) {
		t.Fatalf("template は雛形と同じ 8080 を使うはず:\n%s", tp)
	}
	if strings.Contains(tp, "TODO: match") {
		t.Fatal("ポートが確定しているのに TODO が残っている")
	}
	// 既存 Dockerfile がある(=雛形を出さない)ときは従来どおり
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir2, Params{Project: "svc", Region: "r", Framework: "go", HasDockerfile: true}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if tp2 := read(t, dir2, "template.yaml"); !strings.Contains(tp2, `AWS_LWA_PORT: "3000"`) {
		t.Fatalf("既存 Dockerfile 時は挙動を変えない:\n%s", tp2)
	}
}

func TestScaffoldWithSNSAndSQS(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "hub", Region: "r", Wants: appscan.Wants{SNS: true, SQS: true}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		"AppTopic:", "AWS::SNS::Topic", "hub-${EnvKagerouEnv}-events", // 連動用に環境名から導ける Topic 名
		"TOPIC_ARN: !Ref AppTopic", "SNSPublishMessagePolicy",
		"AppQueueSubscription:", "AWS::SNS::Subscription", // 両方使うなら fan-out 配線も同梱
		"AWS::SQS::QueuePolicy", "sns.amazonaws.com",
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("template missing %q", want)
		}
	}
	// SNS だけなら Subscription 配線は出ない
	dir2 := t.TempDir()
	if _, err := Run(dir2, Params{Project: "pub", Region: "r", Wants: appscan.Wants{SNS: true}}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if tp2 := read(t, dir2, "template.yaml"); strings.Contains(tp2, "AppQueueSubscription") {
		t.Error("SQS 無しで Subscription を出してはいけない")
	}
}

func TestScaffoldComputeECS(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "relay", Region: "r", Compute: "ecs", Domain: "relay.example.com",
		Wants: appscan.Wants{DynamoDB: true, SQS: true}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		"resolve:ssm:/kagerou/base/relay/alb_listener_arn",
		"resolve:ssm:/kagerou/base/relay/alb_cluster",
		"resolve:ssm:/kagerou/base/relay/alb_vpc_id",
		// サブネットは動的参照では読めない(Fn::Split の中では展開されない)。
		// リスト型の SSM パラメータとして受け取る
		"AWS::SSM::Parameter::Value<List<AWS::EC2::Subnet::Id>>",
		"Default: /kagerou/base/relay/alb_subnets",
		"Subnets: !Ref BaseSubnets",
		"resolve:ssm:/kagerou/base/relay/alb_task_security_group",
		"resolve:ssm:/kagerou/base/relay/alb_assign_public_ip", // ベースの判断に追従
		"AWS::ElasticLoadBalancingV2::ListenerRule",
		"AWS::ECS::Service", "FARGATE",
		"EnvImageUri:", "EnvRulePriority:",
		"TABLE_NAME", "QUEUE_URL", "TaskRole", // Wants 配線
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("ecs template missing %q", want)
		}
	}
	// Lambda/SAM の語彙が混ざっていないこと
	for _, notWant := range []string{"Serverless::Function", "Transform:", "AWS_LWA_PORT", "HttpApi"} {
		if strings.Contains(tp, notWant) {
			t.Errorf("ecs template should not contain %q", notWant)
		}
	}
	// 共有 ALB base が同梱される
	ab := read(t, dir, "deploy/alb-base.yaml")
	for _, want := range []string{
		"alb_listener_arn",
		"TaskSubnetIds",  // 既存プライベートサブネットに乗るノブ
		"HasTaskSubnets", // 指定時のみ DISABLED に切り替わる
		"assign_public_ip",
	} {
		if !strings.Contains(ab, want) {
			t.Errorf("alb-base missing %q", want)
		}
	}
	// kagerou.yaml: sam 不使用・短い TTL・readiness
	ky := read(t, dir, "kagerou.yaml")
	for _, want := range []string{"template: template.yaml", "ttl: 24h", "readiness_path: /healthz",
		`url_template: "https://{name}.relay.example.com"`} {
		if !strings.Contains(ky, want) {
			t.Errorf("ecs kagerou.yaml missing %q", want)
		}
	}
	if strings.Contains(ky, "packaged.yaml") {
		t.Error("ecs では sam package を前提にしてはいけない")
	}
	// preview workflow: docker push + IMAGE_URI を kagerou に渡す
	// (優先度は渡さない。kagerou が up のたびに確保する。#189)
	pv := read(t, dir, ".github/workflows/kagerou-preview.yml")
	for _, want := range []string{"docker push", "IMAGE_URI="} {
		if !strings.Contains(pv, want) {
			t.Errorf("ecs preview.yml missing %q", want)
		}
	}
	if strings.Contains(pv, "sam build") {
		t.Error("ecs の workflow に sam が残っている")
	}
}

func TestScaffoldComputeLambdaUnchanged(t *testing.T) {
	// 既定(compute 未指定 / lambda)は従来の生成物のまま。
	dirA := t.TempDir()
	if _, err := Run(dirA, Params{Project: "x", Region: "r"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	dirB := t.TempDir()
	if _, err := Run(dirB, Params{Project: "x", Region: "r", Compute: "lambda"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"kagerou.yaml", "template.yaml", ".github/workflows/kagerou-preview.yml"} {
		if read(t, dirA, f) != read(t, dirB, f) {
			t.Errorf("%s: compute 未指定と lambda 明示で差分が出てはいけない", f)
		}
	}
	if strings.Contains(read(t, dirA, "template.yaml"), "resolve:ssm") {
		t.Error("lambda 既定に ecs の痕跡が混ざっている")
	}
	if _, err := os.Stat(filepath.Join(dirA, "deploy", "alb-base.yaml")); err == nil {
		t.Error("lambda では alb-base を出さないはず")
	}
}

func TestScaffoldPeerBlockOnCross(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "web", Region: "r", URLShape: "cross"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	ky := read(t, dir, "kagerou.yaml")
	for _, want := range []string{"#peer:", "#  project:", "#  fallback: main", "EnvPeerEnv / EnvPeerUrl"} {
		if !strings.Contains(ky, want) {
			t.Errorf("cross では peer 雛形コメントを出すはず: missing %q", want)
		}
	}
	dir2 := t.TempDir()
	if _, err := Run(dir2, Params{Project: "web", Region: "r"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, dir2, "kagerou.yaml"), "#peer:") {
		t.Error("cross でないときは peer 雛形を出さない")
	}
}

func TestScaffoldLambdaOnALB(t *testing.T) {
	// ドメインがあれば lambda も共有 ALB 入口(独自ドメインが既定)。
	dir := t.TempDir()
	p := Params{Project: "web", Region: "r", Compute: "lambda", Entrypoint: "alb",
		Domain: "web.example.com", Port: "8080"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		"TargetType: lambda",
		"AlbInvokePermission", "elasticloadbalancing.amazonaws.com",
		"AlbListenerRule", "resolve:ssm:/kagerou/base/web/alb_listener_arn",
		"EnvRulePriority", "EnvKagerouUrl",
		"AWS::Serverless::Function", // LWA で包むのは変わらない
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("lambda+alb template missing %q", want)
		}
	}
	// API Gateway は作らない(入口は ALB 一本)
	for _, notWant := range []string{"AWS::Serverless::HttpApi", "execute-api"} {
		if strings.Contains(tp, notWant) {
			t.Errorf("lambda+alb should not contain %q", notWant)
		}
	}
	// 共有 ALB ベースが同梱され、URL は独自ドメイン
	if !strings.Contains(read(t, dir, "deploy/alb-base.yaml"), "alb_listener_arn") {
		t.Error("alb-base should be scaffolded for lambda+alb")
	}
	ky := read(t, dir, "kagerou.yaml")
	if !strings.Contains(ky, `url_template: "https://{name}.web.example.com"`) {
		t.Errorf("custom domain url_template missing: %s", ky)
	}
	if !strings.Contains(ky, "ttl: 72h") {
		t.Error("lambda はアイドル $0 なので TTL は 72h のまま")
	}
	// 優先度を workflow から渡さない(kagerou が確保する。#189)。
	// lambda は CI でイメージを push しないので IMAGE_URI も渡らない
	pv := read(t, dir, ".github/workflows/kagerou-preview.yml")
	if strings.Contains(pv, "RULE_PRIORITY=") || strings.Contains(pv, "IMAGE_URI=") {
		t.Errorf("lambda+alb workflow が env を渡している:\n%s", pv)
	}
	if strings.Contains(pv, "% 4999") {
		t.Errorf("PR 番号から優先度を導く step が残っている:\n%s", pv)
	}
}

func TestScaffoldLambdaFallsBackToApiGatewayWithoutDomain(t *testing.T) {
	// ドメインが取れない人は従来どおり生 URL(挙動不変)。
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "web", Region: "r", Compute: "lambda", Entrypoint: "alb"},
		AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "AWS::Serverless::HttpApi") || !strings.Contains(tp, "execute-api") {
		t.Error("ドメイン無しでは API Gateway 経路のまま")
	}
	for _, notWant := range []string{"TargetType: lambda", "AlbListenerRule"} {
		if strings.Contains(tp, notWant) {
			t.Errorf("ドメイン無しで ALB 経路を出してはいけない: %q", notWant)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy", "alb-base.yaml")); err == nil {
		t.Error("ドメイン無しで alb-base を出さない")
	}
}

func TestScaffoldMultiServiceOnALB(t *testing.T) {
	// 複数サービスの環境は ALB の「ホストで分ける」形で出る(DESIGN §13)。
	dir := t.TempDir()
	p := Params{Project: "shop", Region: "r", Compute: "lambda", Entrypoint: "alb",
		Domain: "shop.example.com", Port: "8080",
		Services: []string{"api", "gateway", "worker"}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		// サービスごとに function / ターゲット / ルール
		"ApiFunction:", "GatewayFunction:", "WorkerFunction:",
		"ApiListenerRule:", "GatewayListenerRule:", "WorkerListenerRule:",
		`Command: ["/api"]`, `Command: ["/gateway"]`,
		// ホストで分ける(パス分割しない)
		`api-${EnvKagerouEnv}.shop.example.com`,
		`gateway-${EnvKagerouEnv}.shop.example.com`,
		// 優先度はサービスごとの独立したパラメータ。kagerou が up のたびに
		// 共有リスナーの空きから確保する(#189。枝番の連結はやめた)
		"Priority: !Ref EnvRulePriorityApi",
		"Priority: !Ref EnvRulePriorityWorker",
		"EnvRulePriorityGateway:",
		"RULE_PRIORITY_GATEWAY", // 手で固定したい人向けの案内
		// 相互に URL が届く(発見のための設定が要らない)
		"API_URL:", "GATEWAY_URL:", "WORKER_URL:",
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("multi-service template missing %q", want)
		}
	}
	if strings.Contains(tp, "PathPattern") || strings.Contains(tp, "path-pattern") {
		t.Error("パス分割は採らない(DESIGN §10/§13)")
	}
	// url_template は代表サービス(gateway が primaryNames で優先される)
	ky := read(t, dir, "kagerou.yaml")
	if !strings.Contains(ky, `url_template: "https://gateway-{name}.shop.example.com"`) {
		t.Errorf("primary service url_template missing: %s", ky)
	}
}

func TestScaffoldSingleServiceUnaffected(t *testing.T) {
	// サービスが 1 つなら従来の単一 function テンプレのまま。
	dir := t.TempDir()
	p := Params{Project: "web", Region: "r", Compute: "lambda", Entrypoint: "alb",
		Domain: "web.example.com", Services: []string{"web"}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "AppFunction:") || strings.Contains(tp, "WebFunction:") {
		t.Error("単一サービスは従来テンプレ(AppFunction)のまま")
	}
	if !strings.Contains(read(t, dir, "kagerou.yaml"), `url_template: "https://{name}.web.example.com"`) {
		t.Error("単一サービスの URL にサービス名は付けない")
	}
}

func TestAlbBaseIsPerApp(t *testing.T) {
	// ALB も per-app(CloudFront base と同じ思想)。チームごとに使うので 1 本では
	// 足りなくなる + リスナールール/証明書の上限もある。
	dir := t.TempDir()
	p := Params{Project: "todo", Region: "r", Compute: "ecs", Domain: "todo.example.com"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	ab := read(t, dir, "deploy/alb-base.yaml")
	for _, want := range []string{
		"Project:", // project をパラメータに取る
		"/kagerou/base/${Project}/domain",
		"/kagerou/base/${Project}/alb_listener_arn",
		"kagerou-preview-${Project}", // クラスタ名も衝突しない
	} {
		if !strings.Contains(ab, want) {
			t.Errorf("alb-base missing %q", want)
		}
	}
	// キーは名前空間つき。共有(_shared-alb)は Project に渡したときだけ
	// = テンプレートにハードコードされていないこと
	if strings.Contains(ab, "Name: /kagerou/base/_shared-alb") {
		t.Error("既定では共有名前空間に書かない(共有はオプトイン)")
	}
	// 環境テンプレは自分の project のキーだけを読む
	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "resolve:ssm:/kagerou/base/todo/alb_listener_arn") {
		t.Errorf("env template should read its own project's keys:\n%s", tp)
	}
	if strings.Contains(tp, "_shared-alb") {
		t.Error("env template が共有名前空間を読んでいる")
	}
}

// #139: {base_domain} を書けるのは、ドメインを SSM のデータ契約から得たときだけ。
// 旧 CFN Exports 由来のベースには SSM キーが無いので、書くと init 直後の
// 最初の up が「解決できない」で落ちる。
func TestScaffoldBaseDomainPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fromSSM bool
		want    string
		notWant string
	}{
		{"SSM 由来なら placeholder", true,
			`url_template: "https://{name}.{base_domain}"`,
			`url_template: "https://{name}.web.example.com"`},
		{"Exports 由来ならリテラル", false,
			`url_template: "https://{name}.web.example.com"`,
			`{base_domain}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := Params{Project: "web", Region: "r", Compute: "lambda", Entrypoint: "alb",
				Domain: "web.example.com", DomainFromSSM: tc.fromSSM, Port: "8080"}
			if _, err := Run(dir, p, AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			ky := read(t, dir, "kagerou.yaml")
			if !strings.Contains(ky, tc.want) {
				t.Errorf("missing %q:\n%s", tc.want, ky)
			}
			if strings.Contains(ky, tc.notWant) {
				t.Errorf("should not contain %q:\n%s", tc.notWant, ky)
			}
		})
	}
}

// #133: ALB ベースが作る証明書は regional。ARN を公開していないと、
// API Gateway のカスタムドメインを使いたい人が 2 枚目を立てることになる
// (preview base のものは us-east-1 固定で受け付けられない)。
func TestAlbBasePublishesRegionalCertificate(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "todo", Region: "r", Compute: "ecs", Domain: "todo.example.com"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	ab := read(t, dir, "deploy/alb-base.yaml")
	if !strings.Contains(ab, "/kagerou/base/${Project}/regional_certificate_arn") {
		t.Error("regional 証明書の ARN を SSM に公開していない")
	}
	// ALB 専用ではないので alb_ 接頭辞は付けない(契約 §9)
	if strings.Contains(ab, "alb_certificate") {
		t.Error("alb_ 接頭辞が付いている(ALB 専用の値ではない)")
	}
}

// #151: 注入先はファイル名まで指定できる。Dockerfile.lambda を使う構成で
// ECS / 本番用の Dockerfile を書き換えてしまわないため。
func TestInjectLWATargetsNamedFile(t *testing.T) {
	dir := t.TempDir()
	plain := "FROM node:22\nEXPOSE 3000\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.lambda"), []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := InjectLWA(dir, "Dockerfile.lambda")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(read(t, dir, "Dockerfile.lambda"), "lambda-adapter") {
		t.Error("指定したファイルに注入されていない")
	}
	if read(t, dir, "Dockerfile") != plain {
		t.Error("指定していない Dockerfile を書き換えた")
	}
}

// #165: 検出できたヘルスチェックのパスを readiness_path に書く。
// 既定の "/" は、ルートが重い SSR で無駄に遅く、リダイレクトするアプリで誤判定する。
func TestScaffoldUsesDetectedHealthPath(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "web", Region: "r", Compute: "lambda", Port: "3000", HealthPath: "/healthz"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if ky := read(t, dir, "kagerou.yaml"); !strings.Contains(ky, "readiness_path: /healthz") {
		t.Errorf("検出したパスを使っていない:\n%s", ky)
	}
}

func TestScaffoldOmitsReadinessWithoutEvidence(t *testing.T) {
	// 検出できなければ書かない。当てずっぽうのパスを readiness にすると
	// up が落ちる(200 が返らない)
	dir := t.TempDir()
	p := Params{Project: "web", Region: "r", Compute: "lambda", Port: "3000"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if ky := read(t, dir, "kagerou.yaml"); strings.Contains(ky, "readiness_path") {
		t.Errorf("根拠が無いのに readiness_path を書いている:\n%s", ky)
	}
}

func TestTemplateUsesDetectedDockerfile(t *testing.T) {
	// LWA を Dockerfile.lambda に分けている構成。ここを "Dockerfile" に固定すると
	// LWA の無いイメージ(ECS/本番用)が Lambda に載る(#160)
	dir := t.TempDir()
	p := Params{Project: "web", Region: "r", HasDockerfile: true,
		DockerfileName: "Dockerfile.lambda"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "Dockerfile: Dockerfile.lambda") {
		t.Errorf("検出したファイル名を使っていない:\n%s", tp)
	}

	// モノレポ: api/ に置かれていればビルドコンテキストもそこを指す。
	// SAM は Dockerfile を DockerContext からの相対で解決する
	dir2 := t.TempDir()
	p2 := Params{Project: "web", Region: "r", HasDockerfile: true,
		DockerfileName: "Dockerfile.lambda", DockerfileDir: "api"}
	if _, err := Run(dir2, p2, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp2 := read(t, dir2, "template.yaml")
	if !strings.Contains(tp2, "DockerContext: ./api") {
		t.Errorf("ビルドコンテキストが Dockerfile の場所を指していない:\n%s", tp2)
	}

	// 検出できなければ従来どおり(Dockerfile 雛形を生成する経路も含む)
	dir3 := t.TempDir()
	if _, err := Run(dir3, Params{Project: "web", Region: "r", Framework: "go"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp3 := read(t, dir3, "template.yaml")
	if !strings.Contains(tp3, "Dockerfile: Dockerfile") || !strings.Contains(tp3, "DockerContext: .") {
		t.Errorf("既定は Dockerfile / . のはず:\n%s", tp3)
	}
}

func TestMultiServiceTemplateUsesDetectedDockerfile(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "relay", Region: "r", Domain: "relay.example.com", Entrypoint: "alb",
		HasDockerfile: true, DockerfileName: "Dockerfile.lambda",
		Services: []string{"gateway", "api"}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if tp := read(t, dir, "template.yaml"); !strings.Contains(tp, "Dockerfile: Dockerfile.lambda") {
		t.Errorf("multi 版も検出したファイル名を使うはず:\n%s", tp)
	}
}

// サービス数に上限は無い(#189)。優先度はサービスごとの独立したパラメータに
// なり、kagerou が up のたびに空きを確保するので、枝番を 1 桁に保つ必要が消えた。
// かつては 10 個で弾いていた(#187)。
func TestManyServicesGetTheirOwnPriorityParameter(t *testing.T) {
	names := make([]string, 12)
	for i := range names {
		names[i] = fmt.Sprintf("svc%d", i)
	}
	dir := t.TempDir()
	p := Params{Project: "relay", Region: "r", Entrypoint: "alb",
		Domain: "relay.example.com", Services: names}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatalf("12 サービスが通らない: %v", err)
	}
	tp := read(t, dir, "template.yaml")
	seen := map[string]bool{}
	for _, spec := range p.ServiceSpecs() {
		param := "EnvRulePriority" + spec.Logical
		if !strings.Contains(tp, param+":") {
			t.Errorf("%s のパラメータが無い", spec.Name)
		}
		if !strings.Contains(tp, "Priority: !Ref "+param) {
			t.Errorf("%s のルールが自分のパラメータを参照していない", spec.Name)
		}
		if seen[param] {
			t.Errorf("パラメータ名が衝突している: %s", param)
		}
		seen[param] = true
	}
	// 連結をやめたので、枝番はテンプレートに出てこない
	if strings.Contains(tp, "${EnvRulePriority}") {
		t.Error("優先度の連結が残っている")
	}
}

// LWA を Dockerfile.lambda に分けている構成に、雛形を生やしてはいけない(#192)。
// 「Dockerfile を持っているか」をリテラル名で見ると、既に LWA 入りを持って
// いるのにもう 1 つ増え、どちらがビルドされるかが workflow 次第になる。
func TestNoScaffoldWhenOnlyNamedDockerfileExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.lambda"),
		[]byte("FROM node:22\nEXPOSE 3000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Params{Project: "web", Region: "r", Framework: "next",
		HasDockerfile: true, DockerfileName: "Dockerfile.lambda"}

	res, err := Run(dir, p, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		t.Error("LWA 入りの Dockerfile.lambda があるのに素の Dockerfile を生やした")
	}
	if !contains(res.Skipped, "Dockerfile.lambda") {
		t.Errorf("既存として skip されるはず: created=%v skipped=%v", res.Created, res.Skipped)
	}

	// 予告も同じ名前を指す
	var got string
	for _, f := range PlannedFiles(p, AllTargets()) {
		if strings.HasPrefix(f.Path, "Dockerfile") {
			got = f.Path
		}
	}
	if got != "Dockerfile.lambda" {
		t.Errorf("PlannedFiles が %q を予告している(検出値と違う)", got)
	}
}

// 何も無いリポジトリでは従来どおり雛形を出す。
func TestScaffoldsDockerfileWhenNoneExists(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, Params{Project: "web", Region: "r", Framework: "next"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Created, "Dockerfile") {
		t.Fatalf("雛形が出ていない: %v", res.Created)
	}
}

// MemorySize / Timeout は固定だったので、生成後に手で直す前提になっていた(#185)。
func TestMemoryAndTimeoutAreConfigurable(t *testing.T) {
	cases := []struct {
		name            string
		p               Params
		wantMem, wantTo string
	}{
		{
			// 既定。ALB 入口は応答時間に上限が無いので少し長めに取れる
			name:    "既定(ALB 入口)",
			p:       Params{Entrypoint: "alb", Domain: "web.example.com"},
			wantMem: "MemorySize: 512", wantTo: "Timeout: 60",
		},
		{
			// HTTP API は応答 30 秒で切れる。それを超える既定を置くと
			// 「Lambda は動いているのに 504」を標準にしてしまう
			name:    "既定(HTTP API 入口)",
			p:       Params{Entrypoint: "apigateway"},
			wantMem: "MemorySize: 512", wantTo: "Timeout: 30",
		},
		{
			name:    "指定あり",
			p:       Params{Entrypoint: "alb", Domain: "web.example.com", Memory: 1024, Timeout: 120},
			wantMem: "MemorySize: 1024", wantTo: "Timeout: 120",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.Project, p.Region, p.Framework = "web", "r", "next"
			dir := t.TempDir()
			if _, err := Run(dir, p, AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			tp := read(t, dir, "template.yaml")
			for _, want := range []string{tc.wantMem, tc.wantTo} {
				if !strings.Contains(tp, want) {
					t.Errorf("template missing %q", want)
				}
			}
		})
	}
}

// 入口の上限を超えた指定は「効かない設定」なので、呼び出し側が警告できるようにする。
func TestTimeoutExceedsEntrypoint(t *testing.T) {
	cases := []struct {
		name string
		p    Params
		want bool
	}{
		{"HTTP API で 120 秒", Params{Entrypoint: "apigateway", Timeout: 120}, true},
		{"HTTP API で 30 秒", Params{Entrypoint: "apigateway", Timeout: 30}, false},
		{"ALB で 120 秒", Params{Entrypoint: "alb", Domain: "x.example.com", Timeout: 120}, false},
		{"未指定", Params{Entrypoint: "apigateway"}, false},
		{"static は compute が無い", Params{Driver: "static", Timeout: 120}, false},
	}
	for _, tc := range cases {
		if got := tc.p.TimeoutExceedsEntrypoint(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// #207: 記録は「実際に生成に使われた値」を持つ。等価な指定(compute 未指定と
// lambda 明示)で記録行が変わると、生成物が同じなのに upgrade --check が
// 差分を報告してしまう。
func TestGenRecordIsCanonical(t *testing.T) {
	a := Params{Project: "x", Region: "r"}.GenRecord()
	b := Params{Project: "x", Region: "r", Compute: "lambda", Driver: "stack"}.GenRecord()
	if a != b {
		t.Errorf("等価な指定で記録が違う:\n  %s\n  %s", a, b)
	}
	if !strings.Contains(a, `"compute":"lambda"`) {
		t.Errorf("既定が埋まっていない: %s", a)
	}
}

// 記録を書いて読み戻すと、生成に使った入力が戻る。
func TestGenRecordRoundTrip(t *testing.T) {
	p := Params{Project: "web", Region: "ap-northeast-1", Compute: "ecs",
		Entrypoint: "apigateway", Domain: "web.example.com", Port: "3000",
		Framework: "next", HealthPath: "/healthz"}
	body := []byte("# generated by kagerou v1.2.3\n" + p.GenRecord() + "\nproject: web\n")
	got, ok := ParseGenRecord(body)
	if !ok {
		t.Fatal("記録を読めていない")
	}
	for _, c := range []struct{ name, want, got string }{
		{"Compute", "ecs", got.Compute},
		{"Entrypoint", "apigateway", got.Entrypoint},
		{"Domain", "web.example.com", got.Domain},
		{"HealthPath", "/healthz", got.HealthPath},
	} {
		if c.want != c.got {
			t.Errorf("%s = %q (want %q)", c.name, c.got, c.want)
		}
	}
}

// 記録が無い生成物(v0.13 以前)でも落ちない。
func TestParseGenRecordAbsent(t *testing.T) {
	if _, ok := ParseGenRecord([]byte("# generated by kagerou v0.11.0\nproject: old\n")); ok {
		t.Error("無い記録を読めたことにしている")
	}
}

// #184 2/4: モノレポでサービスごとに Dockerfile がある構成。
//
// ここが壊れると「複数サービスを検出 → multi 版を生成」まで通ったうえで、
// 出てくるテンプレートが全サービスに同じイメージを指す。生成後に手で書き直す
// ことになり、それなら最初から手で書くのと変わらない。
func TestMultiServiceUsesEachServiceOwnDockerfile(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "shop", Region: "r", Compute: "lambda", Entrypoint: "alb",
		Domain: "shop.example.com",
		// ルートの検出値。サービスごとの事実があるほうが優先される
		DockerfileName: "Dockerfile", Port: "3000",
		Services: []string{"api", "web"},
		ServiceFacts: []appscan.ServiceFact{
			{Name: "api", Dir: "api", Dockerfile: "Dockerfile.lambda", Port: "8080", HealthPath: "/api/health"},
			{Name: "web", Dir: "web", Dockerfile: "Dockerfile"},
		}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		"DockerContext: ./api",
		"Dockerfile: Dockerfile.lambda",
		"DockerContext: ./web",
		// 検出できたぶんはサービスごとの値になる
		`AWS_LWA_PORT: "8080"`,
		"AWS_LWA_READINESS_CHECK_PATH: /api/health",
		// web は自分の値が無いのでルートの検出値に落ちる
		`AWS_LWA_PORT: "3000"`,
		"AWS_LWA_READINESS_CHECK_PATH: /healthz",
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("サービスごとのビルド元が出ていない %q:\n%s", want, tp)
		}
	}
	// サービス専用のイメージなら entrypoint はイメージ側が持つ。Command を
	// 被せるとそちらが無視されるので出さない
	if strings.Contains(tp, "ImageConfig") {
		t.Errorf("Dockerfile が分かれているのに ImageConfig.Command を出している:\n%s", tp)
	}
}

// 1 イメージ複数バイナリ(cmd/<name>/main.go)は従来どおり。#184 の受け入れ条件。
func TestMultiServiceWithoutPerServiceDockerfileKeepsSharedImage(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "shop", Region: "r", Compute: "lambda", Entrypoint: "alb",
		Domain: "shop.example.com", Port: "8080",
		Services: []string{"api", "worker"}} // ServiceFacts は空
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		`Command: ["/api"]`, `Command: ["/worker"]`,
		"DockerContext: .", "Dockerfile: Dockerfile",
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("共有イメージの形が崩れている %q:\n%s", want, tp)
		}
	}
	if strings.Contains(tp, "DockerContext: ./") {
		t.Errorf("サービスごとのコンテキストを出してはいけない:\n%s", tp)
	}
}

// 事実はあるが Dockerfile が無い(compose の image: 指定など)。ディレクトリだけを
// コンテキストにすると、存在しない Dockerfile をビルドしにいく。
func TestServiceFactWithoutDockerfileFallsBackToRoot(t *testing.T) {
	p := Params{Project: "shop", Domain: "shop.example.com", Entrypoint: "alb",
		DockerfileName: "Dockerfile.lambda",
		Services:       []string{"api"},
		ServiceFacts:   []appscan.ServiceFact{{Name: "api", Dir: "api"}}}
	got := p.ServiceSpecs()[0]
	if !got.SharedImage {
		t.Error("Dockerfile の無い事実は共有イメージ扱いにするはず")
	}
	if got.DockerContext != "." || got.Dockerfile != "Dockerfile.lambda" {
		t.Errorf("ルートの値に落ちていない: context=%q dockerfile=%q", got.DockerContext, got.Dockerfile)
	}
}

// 生成記録(#207)は Params の JSON。古い記録には serviceFacts が無いので、
// 読み直したときに共有イメージの形へ落ちること — つまり v0.13 以前に生成した
// リポジトリで upgrade --check が幻の差分を出さないこと。
func TestGenRecordWithoutServiceFactsStaysSharedImage(t *testing.T) {
	body := []byte(`# kagerou:generated {"project":"shop","domain":"shop.example.com","entrypoint":"alb","services":["api","web"]}`)
	p, ok := ParseGenRecord(body)
	if !ok {
		t.Fatal("記録を読めていない")
	}
	for _, s := range p.ServiceSpecs() {
		if !s.SharedImage {
			t.Errorf("%s: 記録に serviceFacts が無いなら共有イメージのはず", s.Name)
		}
	}
}

// ecs × 複数サービスは生成できない(#103)。黙って出すと「単一イメージの
// テンプレート + ルートに無い Dockerfile を build する workflow」という、
// 最初の PR で確実に落ちる形になる(#226)。理由と逃げ道つきで止める。
func TestScaffoldECSMultiServiceRefusesWithReason(t *testing.T) {
	p := Params{Project: "mono", Region: "r", Compute: "ecs", Entrypoint: "alb",
		Domain: "mono.example.com", Services: []string{"api", "worker"}}
	_, err := Run(t.TempDir(), p, AllTargets(), false)
	if err == nil {
		t.Fatal("ecs × 複数サービスを黙って受け入れている")
	}
	for _, want := range []string{"#103", "lambda", "one service"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラーに %q が無い(理由と逃げ道を言う): %v", want, err)
		}
	}
	// 単一サービスの ecs は従来どおり通る
	ok := p
	ok.Services = []string{"api"}
	if _, err := Run(t.TempDir(), ok, AllTargets(), false); err != nil {
		t.Fatalf("単一サービスの ecs が通らない: %v", err)
	}
}

// ecs の workflow は検出した Dockerfile の場所で build する。ルート直書きだと
// モノレポ(services/api/Dockerfile)で最初の PR から落ちる(#226)。
func TestScaffoldECSBuildsFromDetectedContext(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "mono", Region: "r", Compute: "ecs", Entrypoint: "alb",
		Domain: "mono.example.com", Services: []string{"api"},
		HasDockerfile: true, DockerfileName: "Dockerfile", DockerfileDir: "services/api"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	pv := read(t, dir, ".github/workflows/kagerou-preview.yml")
	if !strings.Contains(pv, `-f "./services/api/Dockerfile" "./services/api"`) {
		t.Errorf("検出した場所で build していない:\n%s", pv)
	}

	// ルート直下の従来構成は . のまま
	dir2 := t.TempDir()
	p2 := Params{Project: "web", Region: "r", Compute: "ecs", Entrypoint: "alb", Domain: "web.example.com"}
	if _, err := Run(dir2, p2, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if pv2 := read(t, dir2, ".github/workflows/kagerou-preview.yml"); !strings.Contains(pv2, `-f "./Dockerfile" "."`) {
		t.Errorf("ルート構成の build 先が変わっている:\n%s", pv2)
	}
}
