package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runDiagnose は diagnose の出力を取る(out に *os.File が要るので一時ファイル経由)。
func runDiagnose(t *testing.T, args ...string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := cmdDiagnose(args, f); err != nil {
		t.Fatalf("diagnose(%v): %v", args, err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDiagnoseIsReadOnly(t *testing.T) {
	dir := repo(t, map[string]string{"Dockerfile": "FROM alpine\nEXPOSE 8080\n"})
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	runDiagnose(t, "--dir", dir)
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("diagnose がファイルを作った: %d → %d", len(before), len(after))
	}
}

func TestDiagnoseRecommendation(t *testing.T) {
	multi := repo(t, map[string]string{
		"Dockerfile": "FROM alpine\nEXPOSE 8080\n",
		"compose.yaml": `services:
  gateway:
    build: .
  api:
    build: .
  worker:
    build: .
`,
	})
	out := runDiagnose(t, "--dir", multi)
	if !strings.Contains(out, "❯ apigateway") {
		t.Fatalf("複数サービスは API Gateway が既定のはず:\n%s", out)
	}
	if !strings.Contains(out, "no AWS calls were made") {
		t.Fatalf("読み取り専用であることの明示が無い:\n%s", out)
	}

	// 認証ありなら ALB か edge に寄り、API Gateway は使用不可として出る
	authed := runDiagnose(t, "--dir", multi, "--auth")
	if !strings.Contains(authed, "❯ edge") {
		t.Fatalf("認証あり(固定費 NG)は edge が既定のはず:\n%s", authed)
	}
	fixed := runDiagnose(t, "--dir", multi, "--auth", "--allow-fixed-cost")
	if !strings.Contains(fixed, "❯ alb") {
		t.Fatalf("認証あり(固定費 OK)は alb が既定のはず:\n%s", fixed)
	}
}

func TestDiagnoseJSON(t *testing.T) {
	dir := repo(t, map[string]string{
		"go.mod":              "module x\n\nrequire github.com/gorilla/websocket v1.5.0\n",
		"cmd/gateway/main.go": "package main\nfunc main() {}\n",
		"Dockerfile":          "FROM alpine\nEXPOSE 8080\n",
	})
	var d diagnosis
	if err := json.Unmarshal([]byte(runDiagnose(t, "--dir", dir, "--json")), &d); err != nil {
		t.Fatal(err)
	}
	if !d.Facts.Realtime {
		t.Fatal("WebSocket を検出できていない")
	}
	if d.Recommend != "alb" {
		t.Fatalf("realtime は alb が既定のはず: %q", d.Recommend)
	}
	if len(d.Reasons) == 0 {
		t.Fatal("理由が空")
	}
	for _, r := range d.Reasons {
		if r.Reason == "" {
			t.Fatalf("%s に理由が無い", r.Entrypoint)
		}
	}
}
