// Package quota は「共有 ALB にあと何面置けるか」を AWS に問い合わせる(#160)。
//
// 環境を 1 つ増やすと、ALB にはリスナールールが 1 本とターゲットグループが 1 つ
// 増える。どちらも ALB 単位の上限があり、**上限に当たるのは作ろうとした瞬間**で、
// そのとき出るのは CFN の TooManyRules / TooManyTargetGroups だけ。事前に見えれば
// 「ALB を分ける」判断ができる。
//
// 上限は Service Quotas ではなく elbv2 の DescribeAccountLimits から取る。
// こちらは **引き上げ済みの実効値**が返り、quota コードを覚えなくてよい。
//
// preflight と同じ約束: 読み取りだけ・短い時間で諦める・失敗は判定不能として
// 残し零値で進む。ゲートにはしない。
package quota

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
)

// 上限の名前(DescribeAccountLimits が返す文字列。AWS 側の契約)。
const (
	limitRulesPerALB        = "rules-per-application-load-balancer"
	limitTargetGroupsPerALB = "target-groups-per-application-load-balancer"
	limitCertsPerALB        = "certificates-per-application-load-balancer"
	limitTargetGroups       = "target-groups"
)

// Limit は 1 つの上限と、分かるなら現在の使用数。
type Limit struct {
	Name string
	Max  int
	// Used / HasUsed は使用数。listener を渡さなければ数えない。
	Used    int
	HasUsed bool
	// PerEnv は環境 1 つあたりこの上限をいくつ消費するか(0 = 消費しない)。
	// 「あと何面」を出すのに要る。
	PerEnv int
}

// Remaining は残り環境数。使用数が分からなければ (0,false)。
func (l Limit) Remaining() (int, bool) {
	if !l.HasUsed || l.PerEnv <= 0 {
		return 0, false
	}
	n := (l.Max - l.Used) / l.PerEnv
	if n < 0 {
		n = 0
	}
	return n, true
}

// Report は照会結果。Unknown なら AWS に届かなかった(失敗ではない)。
type Report struct {
	Region  string
	Limits  []Limit
	Unknown bool
	Note    string
}

// Headroom は「あと何面置けるか」= 環境を消費する上限のうち最小の残り。
// どれも使用数が分からなければ (0,false)。
func (r Report) Headroom() (int, string, bool) {
	best, name, ok := 0, "", false
	for _, l := range r.Limits {
		n, has := l.Remaining()
		if !has {
			continue
		}
		if !ok || n < best {
			best, name, ok = n, l.Name, true
		}
	}
	return best, name, ok
}

// client は使う API だけの口(テストで差し替える)。
type client interface {
	DescribeAccountLimits(ctx context.Context, in *elasticloadbalancingv2.DescribeAccountLimitsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeAccountLimitsOutput, error)
	DescribeRules(ctx context.Context, in *elasticloadbalancingv2.DescribeRulesInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error)
}

// newClient は既定の資格情報で elbv2 クライアントを作る。テストで差し替える。
var newClient = func(ctx context.Context, region string) (client, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return elasticloadbalancingv2.NewFromConfig(cfg), nil
}

// Lookup は ALB の上限を引く。listenerARN があれば使用中のルール数も数える。
// 届かなければ Unknown を立てて返す(エラーにはしない)。
func Lookup(ctx context.Context, region, listenerARN string) Report {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rep := Report{Region: region}
	c, err := newClient(ctx, region)
	if err != nil {
		rep.Unknown, rep.Note = true, err.Error()
		return rep
	}
	out, err := c.DescribeAccountLimits(ctx, &elasticloadbalancingv2.DescribeAccountLimitsInput{})
	if err != nil {
		rep.Unknown, rep.Note = true, err.Error()
		return rep
	}

	// 環境 1 つ = ルール 1 本 + ターゲットグループ 1 つ。証明書はワイルドカードを
	// 共有するので環境ごとには増えない(上限だけ出す)
	perEnv := map[string]int{limitRulesPerALB: 1, limitTargetGroupsPerALB: 1}
	want := []string{limitRulesPerALB, limitTargetGroupsPerALB, limitCertsPerALB, limitTargetGroups}
	max := map[string]int{}
	for _, l := range out.Limits {
		if l.Name == nil || l.Max == nil {
			continue
		}
		// Max は文字列。"Unlimited" が返ることがあるので数値でなければ飛ばす
		if v, err := strconv.Atoi(aws.ToString(l.Max)); err == nil {
			max[aws.ToString(l.Name)] = v
		}
	}
	for _, name := range want {
		v, ok := max[name]
		if !ok {
			continue
		}
		rep.Limits = append(rep.Limits, Limit{Name: name, Max: v, PerEnv: perEnv[name]})
	}

	if listenerARN != "" {
		if used, ok := countRules(ctx, c, listenerARN); ok {
			for i := range rep.Limits {
				// ルールとターゲットグループは環境ごとに 1 対 1 で増えるので、
				// ルール数を両方の使用数として使う。**推定であることは
				// 名前を見れば分かる**(TG を直接数えるには LB ARN が要る)
				if rep.Limits[i].PerEnv > 0 {
					rep.Limits[i].Used, rep.Limits[i].HasUsed = used, true
				}
			}
		}
	}
	return rep
}

// countRules は listener の既定ルールを除いたルール数を数える。
func countRules(ctx context.Context, c client, listenerARN string) (int, bool) {
	n := 0
	var marker *string
	for {
		out, err := c.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{
			ListenerArn: aws.String(listenerARN),
			Marker:      marker,
		})
		if err != nil {
			return 0, false
		}
		for _, r := range out.Rules {
			// 既定ルール(priority "default")は環境のものではない
			if aws.ToString(r.Priority) == "default" {
				continue
			}
			n++
		}
		if out.NextMarker == nil || *out.NextMarker == "" {
			return n, true
		}
		marker = out.NextMarker
	}
}
