package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestKVFlag(t *testing.T) {
	f := kvFlag{}
	if err := f.Set("DB_USER=dev@pr-42"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("EMPTY="); err != nil { // 空値は許す
		t.Fatal(err)
	}
	if f["DB_USER"] != "dev@pr-42" || f["EMPTY"] != "" {
		t.Fatalf("unexpected: %v", f)
	}
	for _, bad := range []string{"NOEQ", "=v"} {
		if err := f.Set(bad); err == nil {
			t.Errorf("Set(%q): want error", bad)
		}
	}
}

func TestMergeKV(t *testing.T) {
	base := map[string]string{"A": "1", "B": "2"}
	got := mergeKV(base, map[string]string{"B": "9", "C": "3"})
	if got["A"] != "1" || got["B"] != "9" || got["C"] != "3" {
		t.Fatalf("merge broken: %v", got)
	}
	if base["B"] != "2" {
		t.Fatal("base mutated")
	}
}

func TestParseUpFlagsAndConfigMerge(t *testing.T) {
	f, err := parseUpFlags("up", []string{
		"--name", "pr-42",
		"--env", "DB_USER=x",
		"--param", "SubnetIds=subnet-1",
		"--ttl", "1h",
		"--config", "does-not-exist.yaml", // 無ければデフォルトで動く
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigFor(f)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TTL != "1h" {
		t.Fatalf("flag ttl should win: %+v", cfg)
	}
	if cfg.Template != "template.yaml" {
		t.Fatalf("default template expected: %+v", cfg)
	}
}

// #144: 設定を別ディレクトリに置き、template が CI 生成物(手元には無い)を
// 指す構成。落ち先がカレント直下固定だと素のテンプレートに辿り着けず、
// テンプレート由来の導出がまるごと効かないポリシーを黙って出す。
func TestLoadTemplateFactsFallsBackNextToConfig(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "e2e", "ssr")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl := "Resources:\n  Fn:\n    Type: AWS::Serverless::Function\n    Properties:\n      PackageType: Image\n"
	if err := os.WriteFile(filepath.Join(sub, "template.yaml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	// packaged.yaml は存在しない(CI で作られる)
	facts := loadTemplateFacts(filepath.Join(dir, "packaged.yaml"), filepath.Join(sub, "kagerou.yaml"))
	if facts == nil {
		t.Fatal("設定の隣の template.yaml に落ちていない")
	}
	if !facts.HasImage {
		t.Error("落ちた先のテンプレートを読めていない")
	}
}

func TestTemplateCandidatesOrder(t *testing.T) {
	got := templateCandidates("packaged.yaml", "e2e/ssr/kagerou.yaml")
	want := []string{"packaged.yaml", "e2e/ssr/template.yaml", "template.yaml"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("候補 %d = %q (want %q)", i, got[i], want[i])
		}
	}
	// 設定がカレント直下なら重複しない
	if got := templateCandidates("", "kagerou.yaml"); len(got) != 1 || got[0] != "template.yaml" {
		t.Errorf("カレント直下で重複している: %v", got)
	}
}

func TestRegionFlagBeatsDetection(t *testing.T) {
	// 既定値と同じ値を明示したケースがこのバグの本体(#161)。
	// `-region ap-northeast-1` は検出値に負けてはいけない
	parse := func(args ...string) (map[string]bool, string) {
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		region := fs.String("region", "ap-northeast-1", "AWS region")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return explicitFlags(fs), *region
	}

	cases := []struct {
		name     string
		args     []string
		detected string
		want     string
	}{
		{"既定値と同じ値を明示", []string{"-region", "ap-northeast-1"}, "us-west-2", "ap-northeast-1"},
		{"既定値と違う値を明示", []string{"-region", "eu-west-1"}, "us-west-2", "eu-west-1"},
		{"未指定なら検出値", nil, "us-west-2", "us-west-2"},
		{"未指定で検出もなし", nil, "", "ap-northeast-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			explicit, flagValue := parse(tc.args...)
			if got := resolveRegion(explicit["region"], flagValue, tc.detected); got != tc.want {
				t.Fatalf("region = %q, want %q", got, tc.want)
			}
		})
	}
}
