package stack

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

func TestUnsetPriorityParamsSkipsExplicitValues(t *testing.T) {
	declared := map[string]bool{
		"EnvKagerouEnv":         true,
		"EnvRulePriority":       true,
		"EnvRulePriorityApi":    true,
		"EnvRulePriorityWorker": true,
	}
	// 明示された値は触らない(PR 番号を渡す既存の運用がそのまま動く)
	have := map[string]string{"EnvRulePriorityApi": "900"}

	got := unsetPriorityParams(declared, have)
	want := []string{"EnvRulePriority", "EnvRulePriorityWorker"}
	if len(got) != len(want) {
		t.Fatalf("%v (want %v)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%v (want %v) — 名前順でないと、同じ環境を up し直すたびに値が入れ替わる", got, want)
		}
	}
}

func TestFillPrioritiesReservesOnCreate(t *testing.T) {
	var gotN int
	var gotSeed, gotListener string
	d := &Driver{reserve: func(_ context.Context, listener string, n int, seed string) ([]int, error) {
		gotListener, gotN, gotSeed = listener, n, seed
		return []int{31, 32}, nil
	}}
	in := UpInput{Name: "pr-42", RuleListener: "arn:listener"}

	got, err := d.fillPriorities(context.Background(), []string{"EnvRulePriorityApi", "EnvRulePriorityWeb"}, in, false)
	if err != nil {
		t.Fatal(err)
	}
	if gotListener != "arn:listener" || gotN != 2 || gotSeed != "pr-42" {
		t.Errorf("確保の引数が違う: listener=%q n=%d seed=%q", gotListener, gotN, gotSeed)
	}
	want := map[string]string{"EnvRulePriorityApi": "31", "EnvRulePriorityWeb": "32"}
	for _, p := range got {
		k := aws.ToString(p.ParameterKey)
		if v := aws.ToString(p.ParameterValue); v != want[k] {
			t.Errorf("%s = %s (want %s)", k, v, want[k])
		}
	}
}

// 既存スタックの更新では取り直さない。今の優先度は自分のルールが握っているので、
// 取り直すと自分自身と衝突するうえ、毎回ルールが書き換わる。
func TestFillPrioritiesKeepsPreviousOnUpdate(t *testing.T) {
	called := false
	d := &Driver{reserve: func(context.Context, string, int, string) ([]int, error) {
		called = true
		return []int{1}, nil
	}}
	in := UpInput{Name: "pr-42", RuleListener: "arn:listener"}

	got, err := d.fillPriorities(context.Background(), []string{"EnvRulePriority"}, in, true)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("update なのに確保し直している")
	}
	if len(got) != 1 || !aws.ToBool(got[0].UsePreviousValue) {
		t.Errorf("UsePreviousValue になっていない: %+v", got)
	}
}

// リスナーが分からないときは何もしない = テンプレートの Default に任せる(従来の挙動)。
func TestFillPrioritiesWithoutListenerDoesNothing(t *testing.T) {
	d := &Driver{reserve: func(context.Context, string, int, string) ([]int, error) {
		t.Fatal("リスナー不明なのに確保しようとしている")
		return nil, nil
	}}
	got, err := d.fillPriorities(context.Background(), []string{"EnvRulePriority"}, UpInput{Name: "x"}, false)
	if err != nil || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestFillPrioritiesSurfacesReserveErrors(t *testing.T) {
	// 空きが読めないのに黙って進むと、使用中の優先度を掴んで up が落ちる
	d := &Driver{reserve: func(context.Context, string, int, string) ([]int, error) {
		return nil, errors.New("denied")
	}}
	in := UpInput{Name: "pr-42", RuleListener: "arn:listener"}
	if _, err := d.fillPriorities(context.Background(), []string{"EnvRulePriority"}, in, false); err == nil {
		t.Fatal("確保に失敗したのに成功している")
	}
}

func TestDropPrioritiesKeepsEverythingElse(t *testing.T) {
	params := []cfntypes.Parameter{
		{ParameterKey: aws.String("EnvKagerouEnv"), ParameterValue: aws.String("pr-42")},
		{ParameterKey: aws.String("EnvRulePriorityApi"), ParameterValue: aws.String("31")},
	}
	got := dropPriorities(params, []string{"EnvRulePriorityApi"})
	if len(got) != 1 || aws.ToString(got[0].ParameterKey) != "EnvKagerouEnv" {
		t.Errorf("落とすものを間違えている: %+v", got)
	}
}

func TestIsPriorityTaken(t *testing.T) {
	yes := []error{
		errors.New("create stack: PriorityInUse: Priority '31' is currently in use"),
		errors.New("ValidationError: Priority '31' is currently in use"),
	}
	for _, err := range yes {
		if !isPriorityTaken(err) {
			t.Errorf("優先度の衝突と見なせていない: %v", err)
		}
	}
	if isPriorityTaken(errors.New("AccessDenied")) {
		t.Error("関係ない失敗を優先度の衝突と見なしている")
	}
	if isPriorityTaken(nil) {
		t.Error("nil を衝突と見なしている")
	}
}
