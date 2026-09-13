package scaffold

import (
	"strings"
	"testing"
)

func TestBuildAWSPlan(t *testing.T) {
	det := Detection{AccountID: "123456789012", Owner: "acme", Repo: "todo-app"}

	t.Run("base なしなら CloudFront を作らない", func(t *testing.T) {
		p := BuildAWSPlan(Params{Project: "todo", Region: "ap-northeast-1"}, det)
		if kinds := kindsOf(p); contains(kinds, "CloudFront") {
			t.Errorf("SetupBase=false なのに CloudFront が入っている: %v", kinds)
		}
		if !contains(kindsOf(p), "IAM role") {
			t.Error("ロールは常に作る")
		}
	})

	t.Run("base ありなら配信一式が入る", func(t *testing.T) {
		p := BuildAWSPlan(Params{
			Project: "todo", Region: "ap-northeast-1",
			Domain: "todo.example.com", SetupBase: true,
		}, det)
		for _, want := range []string{"CloudFront", "ACM certificate", "S3 bucket", "Route53 record"} {
			if !contains(kindsOf(p), want) {
				t.Errorf("%s が入っていない", want)
			}
		}
		// 15 分かかることは押す前に知りたい。
		if len(p.Warnings) == 0 {
			t.Error("時間がかかることを伝えていない")
		}
	})

	// 作るものには必ず消し方が要る。ここが抜けると「作ったが消せない」が
	// ドキュメントにも出ないまま残る。
	t.Run("作るもの全部に取り壊し手順がある", func(t *testing.T) {
		p := BuildAWSPlan(Params{
			Project: "todo", Region: "ap-northeast-1",
			Domain: "todo.example.com", SetupBase: true,
		}, det)
		teardown := strings.Join(p.Teardown, "\n")
		// CloudFront / 証明書 / DNS は preview base スタックごと消える。
		// バケットは中身が残ると消せないので個別の rm が要る。
		for _, want := range []string{
			"delete-stack",                   // CloudFront / ACM / Route53
			"kagerou-base-todo-123456789012", // バケットの中身
			"todo-app-github-actions",        // ロール
			"delete-repository",              // ECR
		} {
			if !strings.Contains(teardown, want) {
				t.Errorf("取り壊し手順に %q が無い:\n%s", want, teardown)
			}
		}
	})

	t.Run("アカウントが取れなくても落ちない", func(t *testing.T) {
		p := BuildAWSPlan(Params{Project: "todo", Region: "ap-northeast-1"}, Detection{})
		if p.Account == "" {
			t.Error("Account が空のまま表示に出ると何も分からない")
		}
		// repo 未検出なら project で代替する(名前が空になるのを避ける)
		if !strings.Contains(strings.Join(namesOf(p), " "), "todo") {
			t.Errorf("repo 未検出時に project で代替していない: %v", namesOf(p))
		}
	})
}

func TestAWSPlanRender(t *testing.T) {
	p := BuildAWSPlan(Params{
		Project: "todo", Region: "ap-northeast-1",
		Domain: "todo.example.com", SetupBase: true,
	}, Detection{AccountID: "123456789012", Repo: "todo-app"})
	out := p.Render()

	// 値段を出す以上、いつ時点のものかを必ず添える。
	if !strings.Contains(out, PricesAsOf) {
		t.Errorf("価格の時点(%s)が出ていない", PricesAsOf)
	}
	for _, want := range []string{"費用の目安", "取り壊すとき", "account 123456789012"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q が出ていない", want)
		}
	}
	t.Log("\n" + out)
}

// sashiki ホストは preview base と同じ共有側の住人なのに init が作らないので、
// 何もしないとプランから消える。利用者が気づくのが「最初のプレビューが DB に
// 繋がらない時点」になるのを防ぐ(#123)。
func TestSashikiHostAppearsAsPrereq(t *testing.T) {
	det := Detection{AccountID: "123456789012", Repo: "todo-app"}
	base := Params{Project: "todo", Region: "ap-northeast-1"}

	t.Run("sashiki なしでは前提を出さない", func(t *testing.T) {
		p := BuildAWSPlan(base, det)
		if len(p.Prereqs) != 0 {
			t.Errorf("関係ない前提が出ている: %+v", p.Prereqs)
		}
		if strings.Contains(p.Render(), "前提") {
			t.Error("sashiki を使わない構成に前提の節が出ている")
		}
	})

	t.Run("sashiki を選ぶとホストが前提に出る", func(t *testing.T) {
		sp := base
		sp.Sashiki = true
		p := BuildAWSPlan(sp, det)

		if len(p.Prereqs) == 0 {
			t.Fatal("sashiki ホストが前提に出ていない")
		}
		// 作るものと混ざってはいけない。作らないのだから取り壊し手順にも出ない
		for _, r := range p.Shared {
			if strings.Contains(r.Kind, "sashiki") {
				t.Errorf("作らないものが「作るもの」に入っている: %+v", r)
			}
		}
		if strings.Contains(strings.Join(p.Teardown, "\n"), "sashiki") {
			t.Error("作らないものが取り壊し手順に入っている")
		}

		out := p.Render()
		for _, want := range []string{
			"前提(作らない。無ければ先に用意)",
			"sashiki host",
			"Terraform", // 立て方が分かること
			"baseline",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%q が出ていない:\n%s", want, out)
			}
		}
		// 常駐費があるのに「固定の月額はどれにも無い」だけ読まれると誤解になる
		if !strings.Contains(out, "常駐費") {
			t.Errorf("常駐費に触れていない:\n%s", out)
		}
		t.Log("\n" + out)
	})
}

func kindsOf(p AWSPlan) []string {
	var out []string
	for _, r := range p.Shared {
		out = append(out, r.Kind)
	}
	return out
}

func namesOf(p AWSPlan) []string {
	var out []string
	for _, r := range p.Shared {
		out = append(out, r.Name)
	}
	return out
}
