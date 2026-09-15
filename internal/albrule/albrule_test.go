package albrule

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

func TestPickSkipsUsed(t *testing.T) {
	used := map[int]bool{}
	// seed "pr-42" の開始点から 3 つ塞いでおく
	first, err := Pick(map[int]bool{}, 3, "pr-42")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range first {
		used[v] = true
	}
	got, err := Pick(used, 2, "pr-42")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got {
		if used[v] {
			t.Errorf("使用中の %d を返した (used=%v)", v, first)
		}
	}
}

// 同じ環境は同じ優先度を掴む。ここが動くと、続けて up するたびにルールの
// 優先度が書き換わる(差分の無い update にならない)。
func TestPickIsStableForTheSameEnvironment(t *testing.T) {
	a, _ := Pick(map[int]bool{}, 3, "pr-42")
	b, _ := Pick(map[int]bool{}, 3, "pr-42")
	if len(a) != 3 || len(a) != len(b) {
		t.Fatalf("%v / %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("同じ環境なのに違う値: %v / %v", a, b)
		}
	}
}

// 別の環境は別のあたりを掴む。同時に走る up が同じ空きを掴みにくくするため。
func TestPickSpreadsAcrossEnvironments(t *testing.T) {
	a, _ := Pick(map[int]bool{}, 1, "pr-42")
	b, _ := Pick(map[int]bool{}, 1, "pr-43")
	if a[0] == b[0] {
		t.Errorf("別の環境が同じ優先度から始まっている: %v", a)
	}
}

func TestPickStaysInRange(t *testing.T) {
	for _, seed := range []string{"", "pr-1", "very-long-environment-name-xyz"} {
		got, err := Pick(map[int]bool{}, 12, seed)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 12 {
			t.Fatalf("%d 個しか返っていない", len(got))
		}
		seen := map[int]bool{}
		for _, v := range got {
			if v < Min || v > Max {
				t.Errorf("範囲外の優先度 %d (seed=%q)", v, seed)
			}
			if seen[v] {
				t.Errorf("同じ値を 2 回返した: %d", v)
			}
			seen[v] = true
		}
	}
}

// サービスが 11 個以上でも衝突しない(#187 の 10 個上限が外れることの根拠)。
func TestPickManyServices(t *testing.T) {
	got, err := Pick(map[int]bool{}, 25, "pr-42")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 {
		t.Fatalf("%d 個", len(got))
	}
}

func TestPickFailsWhenFull(t *testing.T) {
	used := map[int]bool{}
	for i := Min; i <= Max; i++ {
		used[i] = true
	}
	if _, err := Pick(used, 1, "pr-1"); err == nil {
		t.Fatal("満杯なのに空きを返した")
	}
}

type fakeELB struct {
	pages [][]string // ページごとの優先度
	err   error
	calls int
}

func (f *fakeELB) DescribeRules(ctx context.Context, in *elasticloadbalancingv2.DescribeRulesInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	i := f.calls
	f.calls++
	out := &elasticloadbalancingv2.DescribeRulesOutput{}
	for _, p := range f.pages[i] {
		out.Rules = append(out.Rules, elbtypes.Rule{Priority: aws.String(p)})
	}
	if i+1 < len(f.pages) {
		out.NextMarker = aws.String("next")
	}
	return out, nil
}

func TestUsedReadsEveryPage(t *testing.T) {
	c := &fakeELB{pages: [][]string{{"1", "2", "default"}, {"3"}}}
	used, err := Used(context.Background(), c, "arn:listener")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{1, 2, 3} {
		if !used[want] {
			t.Errorf("%d を拾えていない: %v", want, used)
		}
	}
	if len(used) != 3 {
		t.Errorf("default を数えている: %v", used)
	}
}

func TestUsedSurfacesTheError(t *testing.T) {
	// 読めないときに「空き」と答えてしまうと、使用中の優先度を掴んで up が落ちる。
	// 分からないことは分からないと返す
	c := &fakeELB{err: errors.New("denied")}
	if _, err := Used(context.Background(), c, "arn:listener"); err == nil {
		t.Fatal("読めなかったのに成功している")
	}
}
