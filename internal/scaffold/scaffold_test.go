package scaffold

import (
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
		"resolve:ssm:/kagerou/base/_shared-alb/listener_arn",
		"resolve:ssm:/kagerou/base/_shared-alb/cluster",
		"resolve:ssm:/kagerou/base/_shared-alb/vpc_id",
		"resolve:ssm:/kagerou/base/_shared-alb/subnets",
		"resolve:ssm:/kagerou/base/_shared-alb/task_security_group",
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
	if !strings.Contains(ab, "/kagerou/base/_shared-alb/listener_arn") {
		t.Error("alb-base missing SSM contract")
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
	// preview workflow: docker push + IMAGE_URI/RULE_PRIORITY を kagerou に渡す
	pv := read(t, dir, ".github/workflows/kagerou-preview.yml")
	for _, want := range []string{"docker push", "IMAGE_URI=", "RULE_PRIORITY="} {
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
