package main

import "testing"

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
