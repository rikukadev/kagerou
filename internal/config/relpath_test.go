package config

import (
	"os"
	"path/filepath"
	"testing"
)

// モノレポでルートから `--config services/api/kagerou.yaml` と呼んだとき、
// 設定に書かれた `template: template.yaml` は**設定の隣**を指す(#225)。
// cwd 基準だとルートの template.yaml を探して落ちる。
func TestRelResolvesFromConfigDir(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "services", "api")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "project: api\nregion: ap-northeast-1\nttl: 2h\ntemplate: template.yaml\n"
	cfgPath := filepath.Join(app, "kagerou.yaml")
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rel は実在するほうを選ぶので、設定の隣に実体を置く
	if err := os.WriteFile(filepath.Join(app, "template.yaml"), []byte("Resources: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(app, "template.yaml")
	if got := cfg.Rel(cfg.Template); got != want {
		t.Errorf("Rel = %q, want %q", got, want)
	}
}

// 設定と同じ場所で動かす従来の使い方では挙動が変わらないこと。
func TestRelIsIdentityAtConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "kagerou.yaml")
	if err := os.WriteFile(cfgPath, []byte("project: a\nregion: r\nttl: 2h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cfg, err := Load("kagerou.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Rel("template.yaml"); got != "template.yaml" {
		t.Errorf("同じ場所なのにパスが変わった: %q", got)
	}
	if cfg.BaseDir != "." {
		t.Errorf("BaseDir = %q, want \".\"", cfg.BaseDir)
	}
}

// 絶対パス(--template で渡された値)は触らない。
func TestRelLeavesAbsoluteAlone(t *testing.T) {
	cfg := Config{BaseDir: "services/api"}
	abs := filepath.Join(string(filepath.Separator), "tmp", "packaged.yaml")
	if got := cfg.Rel(abs); got != abs {
		t.Errorf("絶対パスを書き換えた: %q", got)
	}
	if got := cfg.Rel(""); got != "" {
		t.Errorf("空を埋めた: %q", got)
	}
}

// リポジトリルート基準で書かれた既存の設定を壊さない(E2E の fixture がこの形)。
//
// `template: e2e/aws/3tier/template.yaml` のように、**設定ファイルの場所ではなく
// 実行場所からのパス**で書かれているものがある。設定基準へ素直に寄せるとパスが
// 二重になって落ちる。実際に E2E 4 本がこれで落ちた。
func TestRelKeepsPathsWrittenFromTheRunDir(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "e2e", "aws", "3tier")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("e2e", "aws", "3tier", "template.yaml")
	if err := os.WriteFile(filepath.Join(root, rel), []byte("Resources: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cfg := Config{BaseDir: filepath.Join("e2e", "aws", "3tier")}
	if got := cfg.Rel(rel); got != rel {
		t.Errorf("実行場所基準のパスを書き換えた: %q (want %q)", got, rel)
	}
}

// どちらにも無いときは、利用者が書いた値をそのまま返す(エラーに出るのはその文字列)。
func TestRelKeepsUnknownPathAsWritten(t *testing.T) {
	cfg := Config{BaseDir: "services/api"}
	if got := cfg.Rel("nope.yaml"); got != "nope.yaml" {
		t.Errorf("見つからないパスを書き換えた: %q", got)
	}
}
