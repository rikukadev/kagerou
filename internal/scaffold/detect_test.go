package scaffold

import (
	"context"
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

func TestDetectZonesAndBase(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "list-hosted-zones"):
			return []byte(`["rikuka.dev.", "example.org."]`), nil
		case strings.Contains(joined, "list-exports"):
			return []byte(`[["kagerou-preview-base:domain","preview.rikuka.dev"],["kagerou-preview-base:bucket","kagerou-preview-base-123"]]`), nil
		}
		return nil, errNoCmd
	}
	d := Detect(t.TempDir())
	if len(d.Zones) != 2 || d.Zones[0] != "rikuka.dev" {
		t.Fatalf("zones = %v", d.Zones)
	}
	if d.BaseDomain != "preview.rikuka.dev" || d.BaseBucket != "kagerou-preview-base-123" {
		t.Fatalf("base detection broken: %+v", d)
	}
}

var errNoCmd = os.ErrNotExist

func TestPreviewBaseTemplateAndScript(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "x", Region: "ap-northeast-1", Domain: "preview.rikuka.dev", SetupBase: true}
	if _, err := Run(dir, p, Targets{KagerouYaml: true}, false); err != nil {
		t.Fatal(err)
	}
	// base テンプレートが書き出され、要点が入っている
	b, err := os.ReadFile(filepath.Join(dir, "deploy", "preview-base.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"kagerou-preview-base:domain", // Exports(2 回目以降の init が検出する)
		"OriginAccessControl",
		"*.${DomainName}",
		"4135ea2d-6df8-44a3-9df3-4b5a84be39ad", // CachingDisabled = invalidation 不要
		"Z2FDTNDATAQYW2",                       // CloudFront alias の固定ゾーン
	} {
		if !strings.Contains(s, want) {
			t.Errorf("preview-base.yaml missing %q", want)
		}
	}
	// kagerou.yaml に url_template
	ky, _ := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if !strings.Contains(string(ky), `url_template: "https://{name}.preview.rikuka.dev"`) {
		t.Fatalf("url_template missing: %s", ky)
	}
	// setup script に base デプロイ(us-east-1)
	if _, err := WriteSetupScript(dir, p, Detection{Owner: "o", Repo: "r"}); err != nil {
		t.Fatal(err)
	}
	sc, _ := os.ReadFile(filepath.Join(dir, SetupScriptName))
	for _, want := range []string{"kagerou-preview-base", "--region us-east-1", "list-hosted-zones-by-name"} {
		if !strings.Contains(string(sc), want) {
			t.Errorf("setup script missing %q", want)
		}
	}
}

func TestDetectRegionFromAwsConfig(t *testing.T) {
	t.Setenv("AWS_REGION", "") // env 経路を無効化して config フォールバックを検証
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "aws" && len(args) >= 3 && args[0] == "configure" && args[1] == "get" && args[2] == "region" {
			return []byte("us-west-2\n"), nil
		}
		return nil, errNoCmd
	}
	d := Detect(t.TempDir())
	if d.Region != "us-west-2" {
		t.Fatalf("Region = %q, want us-west-2 (from ~/.aws/config)", d.Region)
	}

	// samconfig.toml があればそちらが勝つ(プロジェクト固有 > グローバル)
	dir := t.TempDir()
	writeFile(t, dir, "samconfig.toml", "[default.deploy.parameters]\nregion = \"eu-west-1\"\n")
	if d2 := Detect(dir); d2.Region != "eu-west-1" {
		t.Fatalf("Region = %q, want eu-west-1 (samconfig wins)", d2.Region)
	}
}
