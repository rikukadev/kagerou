package stack

// リスナールール優先度の確保(#189)。
//
// テンプレートが `EnvRulePriority…` を宣言していて、その値が --env でも --param でも
// 来ていないとき、kagerou が共有リスナーの空きから埋める。**明示が常に勝つ** ので、
// PR 番号を渡す既存の運用はそのまま動く。

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// priorityPrefix は優先度パラメータの目印。env キーで言うと RULE_PRIORITY /
// RULE_PRIORITY_API …(CONTRACT §4 の Env<Key> 変換をかけた形)。
const priorityPrefix = "EnvRulePriority"

// unsetPriorityParams は「宣言されているのに値が決まっていない」優先度パラメータを
// 名前順で返す。名前順なのは、同じ環境を続けて up したときに **同じサービスへ同じ
// 値が行く** ようにするため(順序が揺れると毎回ルールが書き換わる)。
func unsetPriorityParams(declared map[string]bool, have map[string]string) []string {
	var out []string
	for name := range declared {
		if !strings.HasPrefix(name, priorityPrefix) {
			continue
		}
		if _, ok := have[name]; ok {
			continue // 明示が勝つ
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// fillPriorities は優先度パラメータの値を決める。
//
// 既存スタックの更新では **取り直さない**(UsePreviousValue)。今使っている優先度は
// 自分のルールが握っているので、取り直すと自分自身と衝突するうえ、毎回ルールが
// 書き換わって差分の無い update にならなくなる。
//
// 新規で、かつリスナーが分かるときだけ確保する。分からなければ何も足さない —
// テンプレートの Default に任せるのが従来の挙動で、ここで失敗させる理由は無い。
func (d *Driver) fillPriorities(ctx context.Context, names []string, in UpInput, exists bool) ([]cfntypes.Parameter, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if exists {
		out := make([]cfntypes.Parameter, 0, len(names))
		for _, n := range names {
			out = append(out, cfntypes.Parameter{ParameterKey: aws.String(n), UsePreviousValue: aws.Bool(true)})
		}
		return out, nil
	}
	if in.RuleListener == "" || d.reserve == nil {
		return nil, nil
	}
	got, err := d.reserve(ctx, in.RuleListener, len(names), in.Name)
	if err != nil {
		return nil, err
	}
	out := make([]cfntypes.Parameter, 0, len(names))
	for i, n := range names {
		out = append(out, cfntypes.Parameter{
			ParameterKey:   aws.String(n),
			ParameterValue: aws.String(strconv.Itoa(got[i])),
		})
	}
	return out, nil
}

// dropPriorities は確保済みの優先度パラメータを落とす。PriorityInUse で落ちた後に
// 取り直すため(前の値をそのまま再送すると同じ理由で落ちる)。
func dropPriorities(params []cfntypes.Parameter, names []string) []cfntypes.Parameter {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := params[:0:0]
	for _, p := range params {
		if drop[aws.ToString(p.ParameterKey)] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// isPriorityTaken は「優先度が埋まっていた」ことによる失敗か。
// 同時に走る up が同じ空きを掴むと ALB がこれを返す。
func isPriorityTaken(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "PriorityInUse") ||
		strings.Contains(s, "Priority '") && strings.Contains(s, "is currently in use")
}
