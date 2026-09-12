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
