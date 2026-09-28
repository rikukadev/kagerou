package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #224 の本体。YAML の整形器はコメントを黙って落とす。記録がコメントに載っていると
// **整形を CI に入れた瞬間に upgrade --check が静かに機能を失う**。
// 記録は kagerou.yaml の外にあり、コメントを全部落としても読めること。
func TestGenRecordSurvivesYamlFormatting(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "web", Region: "ap-northeast-1", Compute: "ecs", Entrypoint: "apigateway"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}

	// 整形器がコメントを全部落とした状況を作る
	cfgPath := filepath.Join(dir, "kagerou.yaml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	stripped := []byte(strings.Join(kept, "\n"))
	if err := os.WriteFile(cfgPath, stripped, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := ParseGenRecord(dir, stripped)
	if !ok {
		t.Fatal("コメントを落とされた後に記録を読めていない(#224 の再発)")
	}
	if got.Compute != "ecs" || got.Entrypoint != "apigateway" {
		t.Errorf("記録の中身が違う: %+v", got)
	}
}

// 記録は kagerou.yaml に書かない。コメントとして書くと、また整形器に消される。
func TestGenRecordNotInKagerouYaml(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "web", Region: "r"}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), genRecordPrefix) {
		t.Errorf("kagerou.yaml に記録が残っている:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, GenRecordPath)); err != nil {
		t.Errorf("%s が無い: %v", GenRecordPath, err)
	}
}

// 既に旧形式で刻んだリポジトリを見捨てない。ファイルが無ければコメントを読む。
func TestGenRecordFallsBackToComment(t *testing.T) {
	dir := t.TempDir() // .kagerou/generated.json は作らない
	body := []byte(`# kagerou:generated {"project":"old","compute":"ecs"}` + "\nproject: old\n")
	got, ok := ParseGenRecord(dir, body)
	if !ok {
		t.Fatal("旧形式の記録を読めていない")
	}
	if got.Compute != "ecs" {
		t.Errorf("Compute = %q", got.Compute)
	}
}

// ファイルが壊れているときにコメントへ落ちない。古い記録で差分を出す方が危ない。
func TestGenRecordBrokenFileDoesNotFallBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".kagerou"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, GenRecordPath), []byte("{ broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte(`# kagerou:generated {"project":"old","compute":"ecs"}`)
	if _, ok := ParseGenRecord(dir, body); ok {
		t.Error("壊れた記録があるのにコメントへ落ちている")
	}
}

// 記録は人が開いて意味が分かること(JSON にコメントが書けないので note を持つ)。
func TestGenRecordIsSelfDescribing(t *testing.T) {
	got := Params{Project: "x", Region: "r"}.GenRecord()
	if !strings.Contains(got, "kagerou") || !strings.Contains(got, "upgrade --check") {
		t.Errorf("何のファイルか読み取れない:\n%s", got)
	}
	// バージョンは入れない(版を上げるたび記録だけ変わると差分が騒がしくなる)
	if strings.Contains(got, "version") {
		t.Errorf("記録にバージョンが入っている:\n%s", got)
	}
}
