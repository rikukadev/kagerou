package recommend

import (
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/appscan"
)

// joined は図を 1 つの文字列にする(部分一致で形を確かめるため)。
func joined(f appscan.Facts, e Entrypoint) string {
	return strings.Join(Diagram(f, e).Render(), "\n")
}

func TestDiagramShapePerEntrypoint(t *testing.T) {
	server := appscan.Facts{Framework: "go", HasDockerfile: true, AppPort: "8080", Services: 1}

	cases := []struct {
		name string
		f    appscan.Facts
		e    Entrypoint
		want []string
		deny []string
	}{
		{
			name: "static は CloudFront + S3 だけで compute を描かない",
			f:    appscan.Facts{Framework: "astro"},
			e:    Static,
			want: []string{"Browser", "CloudFront", "S3  <name>/", "astro の静的成果物"},
			deny: []string{"Lambda", "ECS", "ALB"},
		},
		{
			// init は独自ドメインがあれば lambda も共有 ALB に載せる(#131)
			name: "lambda は共有 ALB の後ろに LWA",
			f:    server,
			e:    Lambda,
			want: []string{"共有 ALB", "Lambda (Web Adapter)", "execute-api"},
			deny: []string{"ECS", "VPC Link"},
		},
		{
			name: "apigateway は VPC Link 経由で ECS",
			f:    server,
			e:    APIGateway,
			want: []string{"HTTP API", "VPC Link", "Cloud Map", "ECS Fargate"},
			deny: []string{"Lambda"},
		},
		{
			name: "alb は共有 ALB の後ろに Fargate",
			f:    server,
			e:    ALB,
			want: []string{"ALB", "ECS Fargate", "共有ベース"},
			deny: []string{"HTTP API", "VPC Link"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joined(tc.f, tc.e)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("%q が無い:\n%s", w, got)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Errorf("%q は描くべきでない:\n%s", d, got)
				}
			}
		})
	}
}

func TestDiagramIsFactsDriven(t *testing.T) {
	// 周辺リソースは依存から。per-env で持てるものと共有側のものを区別して描く
	f := appscan.Facts{
		Framework: "go", HasDockerfile: true, Services: 1,
		DBDriver: "go-sql-driver/mysql",
		Wants:    appscan.Wants{DynamoDB: true, SQS: true, Redis: true},
	}
	got := joined(f, Lambda)
	for _, w := range []string{"go-sql-driver/mysql を検出", "DynamoDB", "SQS", "Redis"} {
		if !strings.Contains(got, w) {
			t.Errorf("%q が無い:\n%s", w, got)
		}
	}
	if !strings.Contains(got, "Redis") || !strings.Contains(got, "共有ベース側") {
		t.Errorf("常時課金のものは共有側と書くべき:\n%s", got)
	}
	if strings.Contains(got, "SNS") || strings.Contains(got, "OpenSearch") {
		t.Errorf("検出していないものを描いている:\n%s", got)
	}

	// 依存が無ければ箱も出ない
	bare := joined(appscan.Facts{Framework: "go", HasDockerfile: true, Services: 1}, Lambda)
	for _, d := range []string{"DynamoDB", "SQS", "Redis", "RDS"} {
		if strings.Contains(bare, d) {
			t.Errorf("%q を描いている:\n%s", d, bare)
		}
	}
}

func TestDiagramCrossOriginSplitsWebAndAPI(t *testing.T) {
	f := appscan.Facts{
		Framework: "react-router", HasDockerfile: true, Services: 3,
		URLShape: "cross",
	}
	got := joined(f, APIGateway)
	if !strings.Contains(got, "├─▶ CloudFront") || !strings.Contains(got, "└─▶ HTTP API") {
		t.Fatalf("cross は Browser の下で web と api に分岐するはず:\n%s", got)
	}
	if !strings.Contains(got, "S3  <name>/") {
		t.Fatalf("web 側の配信先が無い:\n%s", got)
	}

	// cross でなければ分岐しない(1 本道)
	f.URLShape = ""
	if strings.Contains(joined(f, APIGateway), "├─▶ CloudFront") {
		t.Error("cross でないのに web を別オリジンに割っている")
	}
}

func TestDiagramEdgeWithoutComputeProtectsS3(t *testing.T) {
	// 静的のみで edge を薦めるのは「ALB は S3 を守れない」から。
	// ここに Lambda を描くと、在りもしないサーバを描くことになる
	got := joined(appscan.Facts{Framework: "astro"}, EdgeAuth)
	if !strings.Contains(got, "CloudFront + Lambda@Edge") || !strings.Contains(got, "S3  <name>/") {
		t.Fatalf("edge は CloudFront@Edge → S3 のはず:\n%s", got)
	}
	if strings.Contains(got, "Lambda (Web Adapter)") {
		t.Fatalf("compute が無いのに Lambda を描いている:\n%s", got)
	}

	// compute があれば S3 と両方を 1 本の CloudFront が守る
	withApp := joined(appscan.Facts{Framework: "go", HasDockerfile: true, Services: 1}, EdgeAuth)
	if !strings.Contains(withApp, "S3  <name>/") || !strings.Contains(withApp, "Lambda (Web Adapter)") {
		t.Fatalf("compute ありの edge は両方を守るはず:\n%s", withApp)
	}
}

func TestDiagramRenderAlignsNotes(t *testing.T) {
	// 注記は同じ列に揃う(表示幅で数える。日本語混じりでずれない)
	f := appscan.Facts{Framework: "go", HasDockerfile: true, Services: 1, DBDriver: "pg"}
	var cols []int
	for _, l := range Diagram(f, Lambda).Render() {
		if i := strings.Index(l, "  "); i > 0 && strings.TrimSpace(l[i:]) != "" {
			cols = append(cols, len([]rune(strings.TrimRight(l[:i], " "))))
		}
	}
	if len(cols) < 2 {
		t.Fatalf("注記付きの行が足りない: %v", cols)
	}
	// 末尾に空白を残さない(行末の空白は差分やコピペで邪魔になる)
	for _, l := range Diagram(f, Lambda).Render() {
		if l != strings.TrimRight(l, " ") {
			t.Errorf("行末に空白が残っている: %q", l)
		}
	}
}

func TestHasComputeEvidence(t *testing.T) {
	// コンテナ化していなくても、DB / WebSocket 依存はサーバがある証拠
	cases := []struct {
		name string
		f    appscan.Facts
		want bool
	}{
		{"何も無い", appscan.Facts{Framework: "astro"}, false},
		{"Dockerfile", appscan.Facts{HasDockerfile: true}, true},
		{"サービス数", appscan.Facts{Services: 1}, true},
		{"ポート", appscan.Facts{AppPort: "8080"}, true},
		{"DB ドライバだけ", appscan.Facts{DBDriver: "pg"}, true},
		{"WebSocket 依存だけ", appscan.Facts{Realtime: true}, true},
	}
	for _, tc := range cases {
		if got := HasCompute(tc.f); got != tc.want {
			t.Errorf("%s: HasCompute = %v, want %v", tc.name, got, tc.want)
		}
	}
}
