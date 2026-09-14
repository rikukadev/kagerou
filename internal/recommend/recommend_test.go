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

// #152: ExistingALB を読んでいたのは認証ありの経路だけで、認証なしの既定経路
// (単一コンテナ)には届いていなかった。フラグを付けても出力が 1 文字も
// 変わらないので、効いていないのか効いた上で同じ結論なのかが読めなかった。
func TestExistingALBChangesNonAuthPath(t *testing.T) {
	f := appscan.Facts{HasDockerfile: true, AppPort: "3000", Services: 1}

	plain := Entry(f, Options{})
	withALB := Entry(f, Options{ExistingALB: true})

	// compute の選択は変わらない(単一コンテナ = lambda)
	if plain.Default != Lambda || withALB.Default != Lambda {
		t.Fatalf("compute が変わっている: %q → %q", plain.Default, withALB.Default)
	}
	// 変わるのは入口
	if withALB.EntryNote == "" {
		t.Error("共有 ALB があるのに入口の注記が出ていない")
	}
	if plain.EntryNote != "" {
		t.Error("ALB が無いのに入口の注記が出ている")
	}
	// ALB を落とす理由だった固定費が、相乗りなら発生しない
	if !usable(withALB, ALB) {
		t.Error("既存 ALB があるのに alb が使えない扱いのまま")
	}
	if usable(plain, ALB) {
		t.Error("ALB が無いのに使える扱いになっている")
	}
	if strings.Contains(withALB.Reason(ALB), "固定費もかかる") {
		t.Errorf("固定費の理由が残っている: %q", withALB.Reason(ALB))
	}
}

// 複数サービスは ALB があるなら ALB が勝つ。30 秒上限を背負う理由が無くなる。
func TestExistingALBWinsForMultiService(t *testing.T) {
	f := appscan.Facts{HasDockerfile: true, Services: 3}
	if got := Entry(f, Options{}).Default; got != APIGateway {
		t.Errorf("ALB 無しでは apigateway のはず: %q", got)
	}
	if got := Entry(f, Options{ExistingALB: true}).Default; got != ALB {
		t.Errorf("ALB があれば alb のはず: %q", got)
	}
}

func usable(c Choice, e Entrypoint) bool {
	for _, x := range c.Candidates {
		if x.Entrypoint == e {
			return x.Usable
		}
	}
	return false
}

// #172: compute の値段と入口の値段は別勘定。#131 以降、独自ドメインがあれば
// init は共有 ALB を立てるので、「lambda = 固定費ゼロ」と読んだまま init すると
// 月 $18 の ALB が生える。推薦の時点でそれが読めないといけない。
func TestCustomDomainSurfacesTheALBCost(t *testing.T) {
	f := appscan.Facts{HasDockerfile: true, AppPort: "3000", Services: 1}

	plain := Entry(f, Options{})
	domain := Entry(f, Options{CustomDomain: true})

	// compute の選択は変わらない
	if plain.Default != Lambda || domain.Default != Lambda {
		t.Fatalf("compute が変わっている: %q → %q", plain.Default, domain.Default)
	}
	// ドメインが無ければ「アイドル $0」は構成全体について本当。そのままでよい
	if !strings.Contains(plain.Reason(Lambda), "アイドル $0") {
		t.Errorf("ドメイン無しの説明を変えている: %q", plain.Reason(Lambda))
	}
	if plain.EntryNote != "" {
		t.Errorf("ドメインが無いのに入口の注記が出ている: %q", plain.EntryNote)
	}
	// ドメインがあれば、固定費が発生することが読めなければならない
	if domain.EntryNote == "" {
		t.Fatal("ドメインがあるのに入口の注記が無い")
	}
	for _, want := range []string{"alb", "$18"} {
		if !strings.Contains(domain.EntryNote, want) {
			t.Errorf("入口の注記に %q が無い: %q", want, domain.EntryNote)
		}
	}
	// lambda の行が「固定費ゼロ」と読めたままだと、注記があっても誤解が残る
	if strings.Contains(domain.Reason(Lambda), "アイドル $0 で、環境も") {
		t.Errorf("入口の固定費を隠したままの説明: %q", domain.Reason(Lambda))
	}
	// 固定費を避ける道(生の execute-api)が示されていること
	if !strings.Contains(domain.EntryNote, "apigateway") {
		t.Errorf("固定費を避ける選択肢が示されていない: %q", domain.EntryNote)
	}
}

// 既存 ALB があるなら固定費は増えない。#156 の文言と矛盾させない。
func TestExistingALBWinsOverCustomDomainWording(t *testing.T) {
	f := appscan.Facts{HasDockerfile: true, AppPort: "3000", Services: 1}
	c := Entry(f, Options{CustomDomain: true, ExistingALB: true})
	if strings.Contains(c.EntryNote, "$18") {
		t.Errorf("既にある ALB について固定費が増えると言っている: %q", c.EntryNote)
	}
	if !strings.Contains(c.EntryNote, "増えない") {
		t.Errorf("相乗りであることが読めない: %q", c.EntryNote)
	}
}
