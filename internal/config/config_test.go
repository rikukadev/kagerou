package config

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestMaxLifetimeValidation(t *testing.T) {
	if _, err := Load(writeYAML(t, "max_lifetime: 6h")); err != nil {
		t.Fatalf("正の duration は通るはず: %v", err)
	}
	for _, bad := range []string{"nope", "-1h", "0h"} {
		if _, err := Load(writeYAML(t, "max_lifetime: "+bad)); err == nil || !strings.Contains(err.Error(), "max_lifetime") {
			t.Errorf("max_lifetime %q は拒否のはず: %v", bad, err)
		}
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
	for _, ok := range []string{"pr-42", "main", "a", "feature-x2"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", ok, err)
		}
	}
	bad := []string{"", "42pr", "-lead", "pr_42", "pr.42", "pr-", "Feature-X2", strings.Repeat("a", 64)}
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

	// フックを足したときに ExpandName の更新を忘れると、{name} が展開されない
	// まま実行される(「pr-42 のつもりで {name} という名前のリソースを消す」)。
	// フィールドを列挙して、全部が展開対象になっていることを機械的に確かめる。
	t.Run("すべてのフックが展開される", func(t *testing.T) {
		all := Config{Hooks: Hooks{
			PreUp: "{name}", PostUp: "{name}", PreDown: "{name}", PostDown: "{name}",
		}}
		h := reflect.ValueOf(all.ExpandName("pr-1").Hooks)
		for i := range h.NumField() {
			f := h.Type().Field(i)
			if v := h.Field(i).String(); v != "pr-1" {
				t.Errorf("Hooks.%s = %q、{name} が展開されていない(ExpandName に足し忘れ)", f.Name, v)
			}
		}
	})
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

func TestExpandNameProject(t *testing.T) {
	// {project} は共有 base の URL 規約 <project>--<name>.<domain> で使う
	c := Config{
		Project:     "todo",
		URLTemplate: "https://{project}--{name}.example.com",
		Env:         map[string]string{"APP": "{project}-{name}"},
		Hooks:       Hooks{PreUp: "echo {project}/{name}"},
	}
	got := c.ExpandName("pr-42")
	if got.URLTemplate != "https://todo--pr-42.example.com" {
		t.Fatalf("url = %q", got.URLTemplate)
	}
	if got.Env["APP"] != "todo-pr-42" || got.Hooks.PreUp != "echo todo/pr-42" {
		t.Fatalf("expansion broken: %+v %q", got.Env, got.Hooks.PreUp)
	}
}

func TestStaticPrefix(t *testing.T) {
	perApp := Config{Project: "todo"}
	if got := perApp.StaticPrefix("pr-42"); got != "pr-42" {
		t.Fatalf("per-app prefix = %q, want pr-42", got)
	}
	shared := Config{Project: "todo", Static: Static{Prefix: "{project}/{name}"}}
	if got := shared.StaticPrefix("pr-42"); got != "todo/pr-42" {
		t.Fatalf("shared prefix = %q, want todo/pr-42", got)
	}
	trimmed := Config{Project: "todo", Static: Static{Prefix: "/{project}/{name}/"}}
	if got := trimmed.StaticPrefix("pr-42"); got != "todo/pr-42" {
		t.Fatalf("prefix should be trimmed: %q", got)
	}
}

func TestStaticRouting(t *testing.T) {
	base := "project: p\ndriver: stack\nregion: ap-northeast-1\n"

	mustLoad := func(t *testing.T, body string) Config {
		t.Helper()
		cfg, err := Load(writeYAML(t, body))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	t.Run("未指定は directory", func(t *testing.T) {
		if got := mustLoad(t, base).StaticRouting(); got != RoutingDirectory {
			t.Errorf("StaticRouting() = %q, want %q", got, RoutingDirectory)
		}
	})

	t.Run("spa を受け付ける", func(t *testing.T) {
		if got := mustLoad(t, base+"static:\n  routing: spa\n").StaticRouting(); got != RoutingSPA {
			t.Errorf("StaticRouting() = %q, want spa", got)
		}
	})

	// 綴り違いを黙って directory に倒すと、症状は「デプロイ後にディープリンクが
	// 403」になり、設定を見ても原因が分からない(#86)。読み込みで止める。
	t.Run("知らない値は拒否する", func(t *testing.T) {
		if _, err := Load(writeYAML(t, base+"static:\n  routing: SPA\n")); err == nil {
			t.Error("routing: SPA は拒否されるべき")
		}
	})

	// stack driver でも SPA を preview base から配る構成がある(3 層デモ)。
	// static 専用の検証にすると、そこで綴り違いを拾えなくなる。
	t.Run("driver: stack でも検証する", func(t *testing.T) {
		if _, err := Load(writeYAML(t, base+"static:\n  routing: nope\n")); err == nil {
			t.Error("driver: stack でも不正な routing は拒否されるべき")
		}
	})
}

func TestPeerConfig(t *testing.T) {
	cfg, err := Load(writeYAML(t, "peer:\n  project: pub-demo\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Peer.Project != "pub-demo" || cfg.Peer.FallbackName() != "main" {
		t.Fatalf("peer = %+v", cfg.Peer)
	}
	if _, err := Load(writeYAML(t, "peer:\n  project: \"Bad_Name\"\n")); err == nil {
		t.Error("不正な project 名は拒否のはず")
	}
	if _, err := Load(writeYAML(t, "peer:\n  fallback: main\n")); err == nil {
		t.Error("project 無しの fallback は拒否のはず")
	}
}
