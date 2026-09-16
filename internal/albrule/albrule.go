// Package albrule は共有 ALB のリスナールール優先度を up のたびに確保する(#189)。
//
// 以前は PR 番号を優先度に写していた(#187)。写す限り次の 3 つが消えない:
//
//	5000 番台で 1〜50000 の上限を超える(複数サービスでは桁を食うのでもっと早い)
//	1 環境あたりのサービスが 10 個まで(枝番を 1 桁に保つ必要がある)
//	同時に開いた環境の PR 番号がちょうど 4999 離れると衝突する
//
// 優先度に要るのは「**そのリスナー上で、そのとき** 一意」であることだけで、
// 未来永劫の一意性ではない。だから番号から導かず、その場で空きを取る。
//
// 競合(2 つの up が同じ空きを掴む)は ALB が PriorityInUse で返すので、
// 失敗として観測できる。取り直して再試行すれば足りる。
package albrule

import (
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
)

// 優先度の範囲(ALB の仕様)。
const (
	Min = 1
	Max = 50000
)

// Client は使う API だけの口(テストで差し替える)。
type Client interface {
	DescribeRules(ctx context.Context, in *elasticloadbalancingv2.DescribeRulesInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error)
}

// newClient は既定の資格情報で elbv2 クライアントを作る。テストで差し替える。
var newClient = func(ctx context.Context, region string) (Client, error) {
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

// Reserve は listener 上の空き優先度を n 個返す。
//
// seed には環境名を渡す。**同じ環境は毎回同じあたりを掴む**ので、続けて up しても
// 優先度が動かない(= 差分の無い update になる)。別の環境とは開始点が散るので、
// 同時に走る up どうしが同じ空きを掴みにくい。
func Reserve(ctx context.Context, region, listenerARN string, n int, seed string) ([]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	c, err := newClient(ctx, region)
	if err != nil {
		return nil, err
	}
	used, err := Used(ctx, c, listenerARN)
	if err != nil {
		return nil, err
	}
	return Pick(used, n, seed)
}

// Used は listener 上で使用中の優先度を集める。
func Used(ctx context.Context, c Client, listenerARN string) (map[int]bool, error) {
	used := map[int]bool{}
	var marker *string
	for {
		out, err := c.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{
			ListenerArn: aws.String(listenerARN),
			Marker:      marker,
		})
		if err != nil {
			return nil, fmt.Errorf("describe rules: %w", err)
		}
		for _, r := range out.Rules {
			// 既定ルール(priority "default")は数値を占有しない
			if v, err := strconv.Atoi(aws.ToString(r.Priority)); err == nil {
				used[v] = true
			}
		}
		if out.NextMarker == nil || *out.NextMarker == "" {
			return used, nil
		}
		marker = out.NextMarker
	}
}

// Pick は used に無い優先度を n 個返す。seed から開始点を決め、そこから
// 上に向かって探して端で折り返す。
func Pick(used map[int]bool, n int, seed string) ([]int, error) {
	if n <= 0 {
		return nil, nil
	}
	span := Max - Min + 1
	if len(used)+n > span {
		return nil, fmt.Errorf("no room for %d more listener rules: %d of %d priorities are taken",
			n, len(used), span)
	}
	out := make([]int, 0, n)
	taken := make(map[int]bool, n)
	p := start(seed, span)
	for i := 0; i < span && len(out) < n; i++ {
		v := Min + (p+i)%span
		if used[v] || taken[v] {
			continue
		}
		taken[v] = true
		out = append(out, v)
	}
	if len(out) < n {
		// 上の残量チェックを通ったので起きないはずだが、黙って足りない数を返さない
		return nil, fmt.Errorf("could not find %d free priorities (found %d)", n, len(out))
	}
	return out, nil
}

// start は seed から探索の開始位置を決める。空なら先頭から。
func start(seed string, span int) int {
	if seed == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return int(h.Sum32() % uint32(span))
}
