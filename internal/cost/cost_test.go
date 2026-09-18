package cost

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

type fakeCE struct {
	pages []*costexplorer.GetCostAndUsageOutput
	calls int
	last  *costexplorer.GetCostAndUsageInput
}

func (f *fakeCE) GetCostAndUsage(_ context.Context, in *costexplorer.GetCostAndUsageInput,
	_ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	f.last = in
	out := f.pages[f.calls]
	f.calls++
	return out, nil
}

func group(tag, amount string) cetypes.Group {
	return cetypes.Group{
		Keys: []string{TagName + "$" + tag},
		Metrics: map[string]cetypes.MetricValue{
			"UnblendedCost": {Amount: aws.String(amount), Unit: aws.String("USD")},
		},
	}
}

func page(groups []cetypes.Group, next string) *costexplorer.GetCostAndUsageOutput {
	out := &costexplorer.GetCostAndUsageOutput{
		ResultsByTime: []cetypes.ResultByTime{{Groups: groups}},
	}
	if next != "" {
		out.NextPageToken = aws.String(next)
	}
	return out
}

// 基盤(name タグ無し)は環境に按分せず別枠で持つ。按分すると「環境を 1 つ
// 消したらいくら減るのか」が読めなくなる。
func TestFetchSeparatesBaseFromEnvironments(t *testing.T) {
	api := &fakeCE{pages: []*costexplorer.GetCostAndUsageOutput{
		page([]cetypes.Group{
			group("pr-42", "1.50"),
			group("pr-43", "0.50"),
			group("", "18.00"), // name が付かない = 基盤
		}, ""),
	}}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 10)

	rep, err := Fetch(context.Background(), api, "todo", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Base != 18.00 {
		t.Errorf("Base = %v, want 18.00", rep.Base)
	}
	if got := rep.PerEnvTotal(); got != 2.00 {
		t.Errorf("PerEnvTotal = %v, want 2.00", got)
	}
	if got := rep.Total(); got != 20.00 {
		t.Errorf("Total = %v, want 20.00", got)
	}
	// 金額の降順(大きいものから見たい)
	if rep.PerEnv[0].Name != "pr-42" {
		t.Errorf("降順になっていない: %+v", rep.PerEnv)
	}
	if rep.Calls != 1 {
		t.Errorf("Calls = %d, want 1(1 回 $0.01 なので回数は数える)", rep.Calls)
	}
	// project で絞っていること(他プロジェクトの費用を混ぜない)
	if api.last.Filter == nil || api.last.Filter.Tags == nil ||
		*api.last.Filter.Tags.Key != TagProject {
		t.Errorf("kagerou:project で絞っていない: %+v", api.last.Filter)
	}
}

func TestFetchFollowsPages(t *testing.T) {
	api := &fakeCE{pages: []*costexplorer.GetCostAndUsageOutput{
		page([]cetypes.Group{group("pr-1", "1.00")}, "next"),
		page([]cetypes.Group{group("pr-2", "2.00")}, ""),
	}}
	rep, err := Fetch(context.Background(), api, "", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.PerEnv) != 2 || rep.Calls != 2 {
		t.Errorf("ページを辿っていない: %+v calls=%d", rep.PerEnv, rep.Calls)
	}
	// --all-projects 相当ではフィルタを付けない
	if api.last.Filter != nil {
		t.Errorf("project 未指定でフィルタが付いている: %+v", api.last.Filter)
	}
}

// CE は約 24 時間遅れる。直近日を含めると「今日は 0 円」に見えるので終端を past にする。
func TestWindowEndsInThePast(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
	start, end := Window(now, 7)
	if !end.Before(now.Truncate(24 * time.Hour)) {
		t.Errorf("終端が当日以降: end=%v now=%v", end, now)
	}
	if got := int(end.Sub(start).Hours() / 24); got != 7 {
		t.Errorf("期間 = %d 日, want 7", got)
	}
}

func TestEmptyMeansUnknown(t *testing.T) {
	var r Report
	if !r.Empty() {
		t.Error("空の Report が Empty ではない")
	}
	// 「使っていない」と「タグを有効化していない」を区別できる案内であること
	h := ActivateHint()
	for _, want := range []string{"cost allocation tags", "not retroactive"} {
		if !contains(h, want) {
			t.Errorf("案内に %q が無い: %s", want, h)
		}
	}
}

func TestMonthlyAndFormat(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 10)
	if got := Monthly(10, start, end); got != 30 {
		t.Errorf("Monthly = %v, want 30", got)
	}
	if got := Monthly(10, start, start); got != 0 {
		t.Errorf("期間 0 で %v を返した", got)
	}
	if got := FormatUSD(0.0001); got != "<$0.01" {
		t.Errorf("微小額の表示 = %q", got)
	}
	if got := FormatUSD(18); got != "$18.00" {
		t.Errorf("FormatUSD(18) = %q", got)
	}
	if got := FormatUSD(0); got != "$0.00" {
		t.Errorf("FormatUSD(0) = %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
