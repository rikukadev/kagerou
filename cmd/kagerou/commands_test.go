package main

import (
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

// 設定を別ディレクトリに置き、template が CI 生成物(手元には無い)を指す構成。
// 落ち先がカレント直下の template.yaml 固定だと素のテンプレートを見つけられず、
// 権限を静かに取りこぼす(CI だけ落ちる形で実際に踏んだ)。
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
	if !facts.HasContainerImage {
		t.Error("落ちた先のテンプレートを読めていない")
	}
}
