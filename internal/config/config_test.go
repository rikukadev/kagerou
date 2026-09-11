package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeYAML(t, `
driver: stack
template: infra/template.yaml
region: ap-northeast-1
name_prefix: todo-
ttl: 24h
tags:
  team: rikuka
env:
  DB_USER: dev@{name}
hooks:
  pre_up: sashiki create {name}
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Template != "infra/template.yaml" || cfg.NamePrefix != "todo-" || cfg.TTL != "24h" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Tags["team"] != "rikuka" {
		t.Fatalf("tags not loaded: %+v", cfg.Tags)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeYAML(t, `region: ap-northeast-1`))
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if cfg.Driver != def.Driver || cfg.Template != def.Template || cfg.TTL != def.TTL {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load(writeYAML(t, `templete: oops.yaml`)) // タイポ
	if err == nil || !strings.Contains(err.Error(), "templete") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
}

func TestLoadRejectsUnknownDriver(t *testing.T) {
	_, err := Load(writeYAML(t, `driver: warp`))
	if err == nil || !strings.Contains(err.Error(), "warp") {
		t.Fatalf("want unknown-driver error, got %v", err)
	}
}

func TestLoadOrDefault(t *testing.T) {
	cfg, err := LoadOrDefault(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	def := Default()
	if cfg.Driver != def.Driver || cfg.Template != def.Template || cfg.TTL != def.TTL || cfg.Env != nil {
		t.Fatalf("want defaults, got %+v", cfg)
	}
}

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		hasTTL  bool
		wantErr bool
	}{
		{"72h", 72 * time.Hour, true, false},
		{"30m", 30 * time.Minute, true, false},
		{"none", 0, false, false},
		{"0s", 0, false, true},
		{"-1h", 0, false, true},
		{"3days", 0, false, true},
	}
	for _, tc := range cases {
		d, hasTTL, err := ParseTTL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTTL(%q): want error", tc.in)
			}
			continue
		}
		if err != nil || d != tc.want || hasTTL != tc.hasTTL {
			t.Errorf("ParseTTL(%q) = (%v, %v, %v), want (%v, %v, nil)", tc.in, d, hasTTL, err, tc.want, tc.hasTTL)
		}
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"pr-42", "main", "a", "Feature-X2"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", ok, err)
		}
	}
	bad := []string{"", "42pr", "-lead", "pr_42", "pr.42", strings.Repeat("a", 65)}
	for _, ng := range bad {
		if err := ValidateName(ng); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", ng)
		}
	}
}

func TestExpandName(t *testing.T) {
	cfg := Config{
		Env:   map[string]string{"DB_USER": "dev@{name}", "STATIC": "x"},
		Hooks: Hooks{PreUp: "sashiki create {name}", PostDown: "sashiki delete {name}"},
	}
	got := cfg.ExpandName("pr-42")
	if got.Env["DB_USER"] != "dev@pr-42" || got.Env["STATIC"] != "x" {
		t.Fatalf("env not expanded: %+v", got.Env)
	}
	if got.Hooks.PreUp != "sashiki create pr-42" || got.Hooks.PostDown != "sashiki delete pr-42" {
		t.Fatalf("hooks not expanded: %+v", got.Hooks)
	}
	// 元の Config は不変であること
	if cfg.Env["DB_USER"] != "dev@{name}" || cfg.Hooks.PreUp != "sashiki create {name}" {
		t.Fatalf("original mutated: %+v", cfg)
	}
}

func TestStackName(t *testing.T) {
	c := Config{NamePrefix: "todo-"}
	if got := c.StackName("pr-42"); got != "todo-pr-42" {
		t.Fatalf("StackName = %q", got)
	}
}
