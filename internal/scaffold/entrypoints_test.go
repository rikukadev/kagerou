package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/appscan"
)

// recommend の 5 つの入口のうち、scaffold が形を持つのは静的配信・lambda・ecs・
// apigateway・edge(認証)。ここは apigateway と edge の生成物を固定する。

func TestScaffoldComputeAPIGateway(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "relay", Region: "r", Compute: "ecs", Entrypoint: "apigateway", Domain: "relay.example.com",
		Wants: appscan.Wants{DynamoDB: true, SQS: true}}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}

	tp := read(t, dir, "template.yaml")
	for _, want := range []string{
		"resolve:ssm:/kagerou/base/relay/apigw_vpc_link_id",
		"resolve:ssm:/kagerou/base/relay/apigw_namespace_id",
		"resolve:ssm:/kagerou/base/relay/apigw_cluster",
		"resolve:ssm:/kagerou/base/relay/regional_certificate_arn",
		// ecs 形と同じ理由でサブネットだけはリスト型パラメータ
		"AWS::SSM::Parameter::Value<List<AWS::EC2::Subnet::Id>>",
		"Default: /kagerou/base/relay/apigw_subnets",
		"Subnets: !Ref BaseSubnets",
		"AWS::ApiGatewayV2::Api", "AWS::ApiGatewayV2::Integration",
		"ConnectionType: VPC_LINK",
		"AWS::ServiceDiscovery::Service", // タスクの IP は毎回変わるので Cloud Map 経由
		// VPC Link 統合は SRV を要求する。A レコードだと cfn-lint は通るが
		// 実際には繋がらない(kagerou-ecs-demo の実機検証で判明済み)
		"Type: SRV",
		"RoutingPolicy: MULTIVALUE",
		"ContainerName: app", // SRV にはコンテナとポートが要る
		"AWS::ECS::Service", "FARGATE",
		"EnvImageUri:",
		"TABLE_NAME", "QUEUE_URL", "TaskRole", // Wants 配線
	} {
		if !strings.Contains(tp, want) {
			t.Errorf("apigw template missing %q", want)
		}
	}
	// 固定費のある ALB は出てこない(それが ecs 形との唯一の違い)
	for _, notWant := range []string{
		"ElasticLoadBalancingV2", "EnvRulePriority", // ALB 固有
		"Serverless::Function", "Transform:", "AWS_LWA_PORT", // Lambda/SAM
	} {
		if strings.Contains(tp, notWant) {
			t.Errorf("apigw template should not contain %q", notWant)
		}
	}

	ab := read(t, dir, "deploy/apigw-base.yaml")
	for _, want := range []string{
		"AWS::ApiGatewayV2::VpcLink",
		"AWS::ServiceDiscovery::PrivateDnsNamespace",
		"/kagerou/base/${Project}/apigw_vpc_link_id",
		"/kagerou/base/${Project}/apigw_subnets",
	} {
		if !strings.Contains(ab, want) {
			t.Errorf("apigw-base missing %q", want)
		}
	}
	if strings.Contains(ab, "AWS::ElasticLoadBalancingV2::LoadBalancer") {
		t.Error("apigw-base が ALB を作っている(固定費ゼロが選ぶ理由なので致命的)")
	}

	// kagerou.yaml は ecs 形と同じ「コンテナ構成」の書き方になる。
	// ただし入口が環境ごとの HTTP API なので RULE_PRIORITY は要らない
	ky := read(t, dir, "kagerou.yaml")
	for _, want := range []string{"template: template.yaml", "ttl: 24h", "readiness_path: /healthz"} {
		if !strings.Contains(ky, want) {
			t.Errorf("kagerou.yaml missing %q", want)
		}
	}
	if strings.Contains(ky, "packaged.yaml") {
		t.Error("apigateway は sam を使わない")
	}

	// workflow も docker push 側。RULE_PRIORITY は ALB 形だけ
	wf := read(t, dir, ".github/workflows/kagerou-preview.yml")
	if !strings.Contains(wf, "docker push") {
		t.Error("workflow が image を push していない")
	}
	if strings.Contains(wf, "RULE_PRIORITY") {
		t.Error("apigateway に RULE_PRIORITY は無い(共有リスナーを使わない)")
	}
	if !strings.Contains(wf, "IMAGE_URI") {
		t.Error("workflow が IMAGE_URI を渡していない")
	}
}

func TestScaffoldEdgeAuth(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "docs", Region: "r", Driver: "static", Framework: "astro", Auth: true}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}

	// 認証ありでは共有ベースが edge 側になる。両方出ると「どちらを deploy
	// するのか」が読めなくなるので、preview base は出さない
	base := read(t, dir, "deploy/edge-base.yaml")
	if _, err := os.Stat(filepath.Join(dir, "deploy", "preview-base.yaml")); err == nil {
		t.Error("認証ありで preview-base.yaml も出ている")
	}
	for _, want := range []string{
		"Transform: AWS::Serverless-2016-10-31",
		"CodeUri: edge-auth/",
		"EventType: viewer-request",
		"edgelambda.amazonaws.com", // Lambda@Edge の trust policy
		"/kagerou/edge-auth/${DomainName}/",
		"auth.${DomainName}/_kagerou/auth/callback", // IdP に登録する唯一の redirect_uri
	} {
		if !strings.Contains(base, want) {
			t.Errorf("edge-base missing %q", want)
		}
	}
	// 機密はテンプレートに載せない(CFN のコンソールから読めてしまう)
	for _, notWant := range []string{"ClientSecret:", "client_secret: !Ref", "SessionSecret:"} {
		if strings.Contains(base, notWant) {
			t.Errorf("edge-base が機密をテンプレートに持っている: %q", notWant)
		}
	}

	fn := read(t, dir, "deploy/edge-auth/index.mjs")
	for _, want := range []string{
		"GetParametersByPathCommand",   // 設定は SSM から(Lambda@Edge に env は無い)
		"timingSafeEqual",              // セッション署名の比較
		"verifySignature",              // discovery/JWKS で ID token の署名を検証
		"url.searchParams.set('nonce'", // nonce を認可requestへ送り、browser cookieとも結ぶ
		"Secure; HttpOnly",
		"allowed_domain", // 誰でもログインできる IdP で全公開にしない
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("edge-auth missing %q", want)
		}
	}
}
