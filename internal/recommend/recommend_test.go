package recommend

import (
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/appscan"
)

func TestEntry(t *testing.T) {
	server := appscan.Facts{HasDockerfile: true, AppPort: "8080", Services: 1}
	multi := appscan.Facts{HasDockerfile: true, AppPort: "8080", Services: 3}
	ws := appscan.Facts{HasDockerfile: true, AppPort: "8080", Services: 1, Realtime: true}
	staticOnly := appscan.Facts{Framework: "astro"}

	cases := []struct {
		name string
		f    appscan.Facts
		o    Options
		want Entrypoint
	}{
		// 認証なし: 安くて早い順に降りる
		{"静的のみ", staticOnly, Options{}, Static},
		{"単一サービス", server, Options{}, Lambda},
		{"複数サービス", multi, Options{}, APIGateway},
		{"WebSocket は上限のある入口を避ける", ws, Options{}, ALB},

		// 認証あり: ALB か Lambda@Edge のどちらか
		{"認証 + 静的のみ(ALB は S3 を守れない)", staticOnly, Options{Auth: true}, EdgeAuth},
		{"認証 + 固定費 NG", multi, Options{Auth: true}, EdgeAuth},
		{"認証 + 固定費 OK", multi, Options{Auth: true, AllowFixedCost: true}, ALB},
		{"認証 + 既存 ALB は固定費指定によらず相乗り", multi, Options{Auth: true, ExistingALB: true}, ALB},
		{"認証 + WebSocket", ws, Options{Auth: true}, ALB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entry(tc.f, tc.o)
			if got.Default != tc.want {
				t.Fatalf("Default = %q, want %q", got.Default, tc.want)
			}
			// 既定は必ず先頭・使用可能で、理由が付く
			if len(got.Candidates) == 0 {
				t.Fatal("候補が空")
			}
			head := got.Candidates[0]
			if head.Entrypoint != got.Default || !head.Usable || head.Reason == "" {
				t.Fatalf("先頭は理由付きの既定であるべき: %+v", head)
			}
			for _, c := range got.Candidates {
				if c.Reason == "" {
					t.Errorf("%s に理由が無い", c.Entrypoint)
				}
			}
		})
	}
}

func TestAuthNeverPicksAPIGateway(t *testing.T) {
	// HTTP API の JWT オーソライザーはトークン検証のみで、ブラウザの
	// ログインリダイレクトができない。認証ありでは既定にも使用可にもしない
	facts := []appscan.Facts{
		{HasDockerfile: true, Services: 1},
		{HasDockerfile: true, Services: 5},
		{HasDockerfile: true, Services: 2, Realtime: true},
		{Framework: "astro"},
	}
	for _, f := range facts {
		for _, o := range []Options{{Auth: true}, {Auth: true, AllowFixedCost: true}, {Auth: true, ExistingALB: true}} {
			got := Entry(f, o)
			if got.Default == APIGateway {
				t.Fatalf("認証ありで API Gateway が既定になった: %+v %+v", f, o)
			}
			for _, c := range got.Candidates {
				if c.Entrypoint == APIGateway && c.Usable {
					t.Fatalf("認証ありで API Gateway が使用可になった: %+v", c)
				}
			}
		}
	}
}

func TestRealtimeNeverPicksCappedEntrypoints(t *testing.T) {
	// WebSocket/SSE は応答時間に上限のある入口(API Gateway 30 秒 /
	// CloudFront ~60 秒)では動かない
	ws := appscan.Facts{HasDockerfile: true, Services: 2, Realtime: true}
	for _, o := range []Options{{}, {Auth: true}, {Auth: true, AllowFixedCost: true}} {
		got := Entry(ws, o)
		if got.Default != ALB {
			t.Fatalf("realtime の既定は ALB のはず: %q (%+v)", got.Default, o)
		}
		for _, c := range got.Candidates {
			if c.Usable && (c.Entrypoint == APIGateway || c.Entrypoint == EdgeAuth) {
				t.Fatalf("上限のある入口が使用可になった: %+v", c)
			}
		}
	}
}

func TestReasonLookup(t *testing.T) {
	got := Entry(appscan.Facts{HasDockerfile: true, Services: 3}, Options{})
	if r := got.Reason(ALB); !strings.Contains(r, "固定費") {
		t.Fatalf("ALB を選ばない理由に費用の説明が無い: %q", r)
	}
	if got.Reason("nope") != "" {
		t.Fatal("未知の入口には空を返すべき")
	}
}
