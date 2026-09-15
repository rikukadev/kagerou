package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rikukadev/kagerou/internal/cost"
)

func readOut(t *testing.T, f *os.File) string {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tmpOut(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func sample() cost.Report {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return cost.Report{
		Project: "todo", Start: start, End: start.AddDate(0, 0, 10), Currency: "USD",
		PerEnv: []cost.Line{{Name: "pr-42", Amount: 1.5}, {Name: "pr-43", Amount: 0.5}},
		Base:   6.0, Calls: 1,
	}
}

// 基盤を環境に按分していないことが出力から読めること。混ぜると「環境を 1 つ
// 消したらいくら減るか」が分からなくなる。
func TestCostTextSeparatesBase(t *testing.T) {
	f := tmpOut(t)
	if err := writeCostText(f, sample(), false); err != nil {
		t.Fatal(err)
	}
	out := readOut(t, f)
	for _, want := range []string{
		"environments", "base", "NOT split across environments", "$6.00", "$2.00", "$8.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q が出ていない:\n%s", want, out)
		}
	}
	// 数字の意味(遅れ・課金)を必ず添える
	if !strings.Contains(out, "lags") || !strings.Contains(out, "$0.01") {
		t.Errorf("CE の遅延と課金に触れていない:\n%s", out)
	}
	// --by name を付けていないので内訳は出さない
	if strings.Contains(out, "pr-42") {
		t.Errorf("--by name 無しで内訳が出ている:\n%s", out)
	}
	t.Log("\n" + out)
}

func TestCostTextByName(t *testing.T) {
	f := tmpOut(t)
	if err := writeCostText(f, sample(), true); err != nil {
		t.Fatal(err)
	}
	out := readOut(t, f)
	if !strings.Contains(out, "pr-42") || !strings.Contains(out, "pr-43") {
		t.Errorf("内訳が出ていない:\n%s", out)
	}
}

// 0 件のときは「使っていない」と断定せず、タグ有効化を案内する。
func TestCostTextEmptyExplainsActivation(t *testing.T) {
	f := tmpOut(t)
	if err := writeCostText(f, cost.Report{Project: "todo"}, false); err != nil {
		t.Fatal(err)
	}
	out := readOut(t, f)
	if !strings.Contains(out, "cost allocation tags") || !strings.Contains(out, "not retroactive") {
		t.Errorf("有効化の案内が無い:\n%s", out)
	}
}

func TestParseDays(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"7d", 7, true}, {"30", 30, true}, {"365d", 365, true},
		{"0d", 0, false}, {"-1d", 0, false}, {"366d", 0, false}, {"abc", 0, false},
	} {
		got, err := parseDays(tc.in)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseDays(%q) = (%d, %v)", tc.in, got, err)
		}
	}
}
