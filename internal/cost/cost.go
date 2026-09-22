// Package cost はプレビュー環境の費用を Cost Explorer から引く(#208)。
//
// タグ(`kagerou:name` / `kagerou:project`、CONTRACT §1)が最初から付いているので、
// CE の GroupBy: TAG でそのまま割れる。ALB ベースが per-app 既定 =
// アプリ数 × 月 18 ドル前後になるため、ここが見えると「共有するか分けるか」を
// 数字で決められる。
//
// 正直さのために守っていること:
//
//   - **環境ごとの直接費と基盤の固定費を分けて出す。** 基盤(ALB / CloudFront /
//     NAT)は環境に按分しない。按分して 1 つの数字にすると、環境を 1 つ消したら
//     いくら減るのかが読めなくなる
//   - **CE は約 24 時間遅れる。** 「今いくら」は出せないので、集計できた日付を必ず出す
//   - **1 リクエスト $0.01。** 費用を見るのに費用がかかるので、呼ぶ回数を明示的に持つ
//   - **コスト配分タグは有効化した後の利用分からしか集計されない**(遡らない)。
//     ゼロが返ったときに「使っていない」と「有効化していない」を区別して伝える
package cost

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// TagName / TagProject は CONTRACT §1 のタグ。CE ではコスト配分タグとして
// 有効化されている必要がある。
const (
	TagName    = "kagerou:name"
	TagProject = "kagerou:project"
)

// Line は 1 つの環境の費用。
type Line struct {
	Name   string
	Amount float64
}

// Report は集計結果。
type Report struct {
	Project  string
	Start    time.Time // 集計開始(含む)
	End      time.Time // 集計終了(含まない)。CE の遅延ぶん過去で止まる
	Currency string

	// PerEnv は kagerou:name が付いている費用 = 環境ごとの直接費。金額の降順。
	PerEnv []Line
	// Base は kagerou:project は付くが kagerou:name が付かない費用 = 基盤の固定費。
	// **環境には按分しない。**
	Base float64
	// Calls は Cost Explorer を呼んだ回数(1 回 $0.01)。
	Calls int
}

// PerEnvTotal は環境ごとの直接費の合計。
func (r Report) PerEnvTotal() float64 {
	var t float64
	for _, l := range r.PerEnv {
		t += l.Amount
	}
	return t
}

// Total は基盤を含めた合計。
func (r Report) Total() float64 { return r.PerEnvTotal() + r.Base }

// PerEnvAverage は環境 1 つあたりの直接費。環境が無ければ 0。
func (r Report) PerEnvAverage() float64 {
	if len(r.PerEnv) == 0 {
		return 0
	}
	return r.PerEnvTotal() / float64(len(r.PerEnv))
}

// Empty は「1 件も費用が返らなかった」。使っていないのか、コスト配分タグを
// 有効化していないのかを呼び出し側が区別して伝えるために使う。
func (r Report) Empty() bool { return len(r.PerEnv) == 0 && r.Base == 0 }

// API は Cost Explorer のうち使う部分。テストで差し替える。
type API interface {
	GetCostAndUsage(ctx context.Context, in *costexplorer.GetCostAndUsageInput,
		optFns ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error)
}

// Window は集計期間を決める。end は CE の遅延を見込んで past で止める。
//
// now から 2 日前を終端にするのは、CE がおよそ 24 時間遅れるため。直近日を
// 含めると「今日は 0 円」に見えてしまう。
func Window(now time.Time, days int) (start, end time.Time) {
	end = now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	start = end.AddDate(0, 0, -days)
	return start, end
}

// Fetch は期間の費用をタグで割って返す。API 呼び出しは 1 回。
func Fetch(ctx context.Context, api API, project string, start, end time.Time) (Report, error) {
	rep := Report{Project: project, Start: start, End: end, Currency: "USD"}

	in := &costexplorer.GetCostAndUsageInput{
		TimePeriod: &cetypes.DateInterval{
			Start: aws.String(start.Format("2006-01-02")),
			End:   aws.String(end.Format("2006-01-02")),
		},
		Granularity: cetypes.GranularityDaily,
		Metrics:     []string{"UnblendedCost"},
		GroupBy: []cetypes.GroupDefinition{
			{Type: cetypes.GroupDefinitionTypeTag, Key: aws.String(TagName)},
		},
	}
	if project != "" {
		in.Filter = &cetypes.Expression{
			Tags: &cetypes.TagValues{
				Key:          aws.String(TagProject),
				Values:       []string{project},
				MatchOptions: []cetypes.MatchOption{cetypes.MatchOptionEquals},
			},
		}
	} else {
		// --all-projects でも「kagerou:project が付いているもの」には絞る。
		// 無フィルタだと、name の付かないグループ = **アカウントの kagerou と
		// 無関係な支出すべて**が Base に流れ込み、「基盤 $8,000」のような
		// 数字を平然と出してしまう
		in.Filter = &cetypes.Expression{
			Not: &cetypes.Expression{
				Tags: &cetypes.TagValues{
					Key:          aws.String(TagProject),
					MatchOptions: []cetypes.MatchOption{cetypes.MatchOptionAbsent},
				},
			},
		}
	}

	byName := map[string]float64{}
	for {
		out, err := api.GetCostAndUsage(ctx, in)
		rep.Calls++
		if err != nil {
			return rep, err
		}
		for _, r := range out.ResultsByTime {
			for _, g := range r.Groups {
				amt, cur := groupAmount(g)
				if cur != "" {
					rep.Currency = cur
				}
				name := tagValue(g.Keys)
				if name == "" {
					// kagerou:project は付くが name が付かない = 基盤側。
					// 按分せずそのまま基盤の固定費として持つ
					rep.Base += amt
					continue
				}
				byName[name] += amt
			}
		}
		if out.NextPageToken == nil || *out.NextPageToken == "" {
			break
		}
		in.NextPageToken = out.NextPageToken
	}

	for n, a := range byName {
		rep.PerEnv = append(rep.PerEnv, Line{Name: n, Amount: a})
	}
	sort.Slice(rep.PerEnv, func(i, j int) bool {
		if rep.PerEnv[i].Amount != rep.PerEnv[j].Amount {
			return rep.PerEnv[i].Amount > rep.PerEnv[j].Amount
		}
		return rep.PerEnv[i].Name < rep.PerEnv[j].Name
	})
	return rep, nil
}

// tagValue は CE が返すグループキー("kagerou:name$pr-42")から値を取る。
// 付いていない場合は "kagerou:name$" が返るので空になる。
func tagValue(keys []string) string {
	for _, k := range keys {
		for i := 0; i < len(k); i++ {
			if k[i] == '$' {
				return k[i+1:]
			}
		}
	}
	return ""
}

func groupAmount(g cetypes.Group) (float64, string) {
	m, ok := g.Metrics["UnblendedCost"]
	if !ok || m.Amount == nil {
		return 0, ""
	}
	v, err := strconv.ParseFloat(*m.Amount, 64)
	if err != nil {
		return 0, ""
	}
	cur := ""
	if m.Unit != nil {
		cur = *m.Unit
	}
	return v, cur
}

// Monthly は期間の実績から月額を見積もる。日数が 0 なら 0。
func Monthly(amount float64, start, end time.Time) float64 {
	days := end.Sub(start).Hours() / 24
	if days <= 0 {
		return 0
	}
	return amount / days * 30
}

// FormatUSD は表示用。CE は小数が長いので 2 桁に丸める(0 でない微小額は "<0.01")。
func FormatUSD(v float64) string {
	if v > 0 && v < 0.01 {
		return "<$0.01"
	}
	return "$" + strconv.FormatFloat(v, 'f', 2, 64)
}

// ActivateHint はコスト配分タグが有効化されていないときに出す案内。
// **有効化しても遡らない**のが要点なので、そこを必ず書く。
func ActivateHint() string {
	return fmt.Sprintf(
		"no tagged cost found. activate the cost allocation tags %q and %q in Billing → Cost allocation tags.\n"+
			"they only apply to usage AFTER activation (not retroactive), so allow a day before re-running",
		TagProject, TagName)
}
