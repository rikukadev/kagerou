package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectDockerfilePortAndLWA(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", `FROM node:22 AS build
EXPOSE 9999
FROM node:22-slim
EXPOSE 3000
CMD ["node", "server.js"]
`)
	d := Detect(dir)
	if !d.HasDockerfile || d.HasLWA {
		t.Fatalf("dockerfile detection broken: %+v", d)
	}
	if d.AppPort != "3000" {
		t.Fatalf("AppPort = %q, want 3000 (last EXPOSE)", d.AppPort)
	}
}

func TestDetectComposePortFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "FROM node:22\nCMD [\"node\"]\n")
	writeFile(t, dir, "compose.yaml", `services:
  app:
    build: .
    ports:
      - "8080:4000"
`)
	d := Detect(dir)
	if d.AppPort != "4000" {
		t.Fatalf("AppPort = %q, want 4000 (container side)", d.AppPort)
	}
}

func TestInjectLWA(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", `FROM golang:1.26 AS build
RUN go build -o app .
FROM gcr.io/distroless/base
COPY --from=build /app /app
EXPOSE 8080
CMD ["/app"]
`)
	changed, err := InjectLWA(dir)
	if err != nil || !changed {
		t.Fatalf("inject: changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	s := string(b)
	if !strings.Contains(s, "aws-lambda-adapter") {
		t.Fatal("LWA line not injected")
	}
	// 最後の FROM の直後に入っている(build ステージではなく最終ステージ)
	if strings.Index(s, "aws-lambda-adapter") < strings.Index(s, "distroless") {
		t.Fatal("LWA injected into the wrong stage")
	}
	// 冪等
	changed2, err := InjectLWA(dir)
	if err != nil || changed2 {
		t.Fatalf("second inject should be no-op: changed=%v err=%v", changed2, err)
	}
	// docker 的に壊れていない(FROM の数は不変)
	if strings.Count(s, "FROM ") != 2 {
		t.Fatalf("FROM count changed: %s", s)
	}
}

func TestInjectLWANoFrom(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Dockerfile", "# empty\n")
	if _, err := InjectLWA(dir); err == nil {
		t.Fatal("no FROM should be an error")
	}
}

func TestTemplateUsesDetectedPort(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "x", Region: "r", Port: "8080", HasDockerfile: true}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "template.yaml"))
	s := string(b)
	if !strings.Contains(s, `AWS_LWA_PORT: "8080"`) {
		t.Fatalf("detected port not used: %s", s)
	}
	if strings.Contains(s, "TODO: match your app's listen port") || strings.Contains(s, "TODO: run your app") {
		t.Fatal("TODO should disappear when port/Dockerfile are known")
	}
}
