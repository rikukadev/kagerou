package quota

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

type fakeClient struct {
	limits map[string]string
	rules  []string // priority の一覧("default" を含めてよい)
	err    error
	ruleer error
}

func (f fakeClient) DescribeAccountLimits(context.Context, *elb.DescribeAccountLimitsInput, ...func(*elb.Options)) (*elb.DescribeAccountLimitsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	var ls []elbtypes.Limit
	for k, v := range f.limits {
		ls = append(ls, elbtypes.Limit{Name: aws.String(k), Max: aws.String(v)})
	}
	return &elb.DescribeAccountLimitsOutput{Limits: ls}, nil
}

func (f fakeClient) DescribeRules(context.Context, *elb.DescribeRulesInput, ...func(*elb.Options)) (*elb.DescribeRulesOutput, error) {
	if f.ruleer != nil {
		return nil, f.ruleer
	}
	var rs []elbtypes.Rule
	for _, p := range f.rules {
		rs = append(rs, elbtypes.Rule{Priority: aws.String(p)})
	}
	return &elb.DescribeRulesOutput{Rules: rs}, nil
}

func stub(t *testing.T, c client) {
	t.Helper()
	orig := newClient
	t.Cleanup(func() { newClient = orig })
	newClient = func(context.Context, string) (client, error) { return c, nil }
}

func defaultLimits() map[string]string {
	return map[string]string{
		"rules-per-application-load-balancer":         "100",
		"target-groups-per-application-load-balancer": "100",
		"certificates-per-application-load-balancer":  "25",
		"target-groups": "3000",
	}
}

func TestHeadroomFromRuleCount(t *testing.T) {
	// 既定ルールは環境のものではないので数えない。環境 12 面 = ルール 12 本
	rules := []string{"default"}
	for i := 0; i < 12; i++ {
		rules = append(rules, "1")
	}
	stub(t, fakeClient{limits: defaultLimits(), rules: rules})

	rep := Lookup(context.Background(), "ap-northeast-1", "arn:listener")
	n, by, ok := rep.Headroom()
	if !ok {
		t.Fatal("使用数が取れているのに残りが出ていない")
	}
	if n != 88 {
		t.Errorf("残り = %d (want 88)", n)
	}
	if by != "rules-per-application-load-balancer" && by != "target-groups-per-application-load-balancer" {
		t.Errorf("bound by %q", by)
	}
}

func TestHeadroomBoundByTighterLimit(t *testing.T) {
	// 上限緩和でルールだけ 500 に上げた組織。ターゲットグループ側が縛りになる
	// (DescribeAccountLimits は引き上げ済みの実効値を返すので、こう出せる)
	l := defaultLimits()
	l["rules-per-application-load-balancer"] = "500"
	stub(t, fakeClient{limits: l, rules: []string{"default", "1", "2"}})

	rep := Lookup(context.Background(), "ap-northeast-1", "arn:listener")
	n, by, _ := rep.Headroom()
	if n != 98 || by != "target-groups-per-application-load-balancer" {
		t.Errorf("残り = %d bound by %q (want 98 / target-groups-per-...)", n, by)
	}
}

func TestNoListenerStillReportsLimits(t *testing.T) {
	// listener が分からなくても上限は出す価値がある
	stub(t, fakeClient{limits: defaultLimits()})
	rep := Lookup(context.Background(), "ap-northeast-1", "")
	if rep.Unknown || len(rep.Limits) != 4 {
		t.Fatalf("%+v", rep)
	}
	if _, _, ok := rep.Headroom(); ok {
		t.Error("使用数が無いのに残りを出している")
	}
}

func TestUnreachableIsUnknownNotFailure(t *testing.T) {
	// 権限不足・資格情報なしでも「上限ゼロ」とは言わない
	stub(t, fakeClient{err: errors.New("AccessDenied")})
	rep := Lookup(context.Background(), "ap-northeast-1", "arn:listener")
	if !rep.Unknown {
		t.Error("届かなかったのに Unknown が立っていない")
	}
	if len(rep.Limits) != 0 {
		t.Error("推測した上限を返している")
	}
}

func TestRuleCountFailureKeepsLimits(t *testing.T) {
	// ルールが数えられなくても上限は出す(片方の失敗で全部捨てない)
	stub(t, fakeClient{limits: defaultLimits(), ruleer: errors.New("AccessDenied")})
	rep := Lookup(context.Background(), "ap-northeast-1", "arn:listener")
	if rep.Unknown || len(rep.Limits) == 0 {
		t.Fatalf("%+v", rep)
	}
	if _, _, ok := rep.Headroom(); ok {
		t.Error("数えられていないのに残りを出している")
	}
}

func TestNonNumericMaxIsSkipped(t *testing.T) {
	// Max は文字列で、"Unlimited" が返ることがある
	stub(t, fakeClient{limits: map[string]string{
		"rules-per-application-load-balancer":         "Unlimited",
		"target-groups-per-application-load-balancer": "100",
	}})
	rep := Lookup(context.Background(), "ap-northeast-1", "")
	for _, l := range rep.Limits {
		if l.Name == "rules-per-application-load-balancer" {
			t.Error("数値でない上限を採っている")
		}
	}
}

func TestRemainingNeverNegative(t *testing.T) {
	l := Limit{Name: "x", Max: 10, Used: 25, HasUsed: true, PerEnv: 1}
	if n, _ := l.Remaining(); n != 0 {
		t.Errorf("残り = %d (負にしない)", n)
	}
}
