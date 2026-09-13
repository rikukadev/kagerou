package appscan

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanNodeApp(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{
	  "dependencies": {
	    "react-router": "7", "mysql2": "3",
	    "@aws-sdk/client-sqs": "3", "@aws-sdk/client-dynamodb": "3",
	    "ioredis": "5"
	  }
	}`)
	f := Scan(dir)
	if f.Framework != "react-router" || f.DBDriver != "mysql2" {
		t.Fatalf("framework/db: %+v", f)
	}
	w := f.Wants
	if !w.SQS || !w.DynamoDB || !w.Redis || w.S3 || w.OpenSearch {
		t.Fatalf("wants: %+v", w)
	}
	if !w.Any() {
		t.Fatal("Any() should be true")
	}
}

func TestScanGoApp(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", `module example.com/app

require (
	github.com/aws/aws-sdk-go-v2/service/s3 v1.0.0
	github.com/opensearch-project/opensearch-go/v4 v4.0.0
	github.com/go-sql-driver/mysql v1.8.0
)
`)
	f := Scan(dir)
	if f.Framework != "go" || f.DBDriver != "go-sql-driver/mysql" {
		t.Fatalf("framework/db: %+v", f)
	}
	if !f.Wants.S3 || !f.Wants.OpenSearch || f.Wants.Redis || f.Wants.SQS {
		t.Fatalf("wants: %+v", f.Wants)
	}
}

func TestScanComposeEvidence(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", `services:
  app:
    build: .
    ports:
      - "8080:3000"
  cache:
    image: redis:7
  db:
    image: postgres:16
`)
	f := Scan(dir)
	if !f.Wants.Redis {
		t.Error("compose の redis サービスを拾うはず")
	}
	if f.DBDriver != "postgres (compose)" {
		t.Errorf("db = %q", f.DBDriver)
	}
	if f.AppPort != "3000" {
		t.Errorf("port = %q (container side)", f.AppPort)
	}
}

func TestScanEmptyDir(t *testing.T) {
	f := Scan(t.TempDir())
	if f.Framework != "" || f.Wants.Any() || f.HasDockerfile || f.HasTemplate {
		t.Fatalf("empty dir should yield zero facts: %+v", f)
	}
}

func TestScanDockerfileLWAAndPort(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Dockerfile", `FROM node:22 AS build
EXPOSE 9999
FROM node:22-slim
COPY --from=public.ecr.aws/awsguru/aws-lambda-adapter:0.9.1 /lambda-adapter /opt/extensions/lambda-adapter
EXPOSE 3000
`)
	f := Scan(dir)
	if !f.HasDockerfile || !f.HasLWA || f.AppPort != "3000" {
		t.Fatalf("dockerfile facts: %+v", f)
	}
}

func TestScanMonorepo(t *testing.T) {
	// 3tier 型: ルートは空、api/ に Go+mysql、web/ に素の React。
	dir := t.TempDir()
	for _, d := range []string{"api", "web"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, "api/go.mod", "module m\nrequire github.com/go-sql-driver/mysql v1.10.0\nrequire github.com/aws/aws-sdk-go-v2/service/sqs v1.0.0\n")
	write(t, dir, "api/Dockerfile", "FROM golang:1\nEXPOSE 8080\n")
	write(t, dir, "web/package.json", `{"dependencies":{"react":"19"}}`)

	f := Scan(dir)
	if f.Framework != "go" { // 具体度: go > 素の node(コンテナ化対象が勝つ)
		t.Errorf("framework = %q, want go", f.Framework)
	}
	if f.DBDriver != "go-sql-driver/mysql" {
		t.Errorf("db = %q", f.DBDriver)
	}
	if !f.Wants.SQS {
		t.Error("サブディレクトリの Wants を拾うはず")
	}
	if !f.HasDockerfile || f.DockerfileDir != "api" || f.AppPort != "8080" {
		t.Errorf("dockerfile facts: dir=%q port=%q has=%v", f.DockerfileDir, f.AppPort, f.HasDockerfile)
	}
}

func TestScanFrameworkRankPrefersSSR(t *testing.T) {
	// SSR フレームワーク(next)は go より勝つ。
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "package.json", `{"dependencies":{"next":"15"}}`)
	write(t, dir, "tool/go.mod", "module tool\n")
	if f := Scan(dir); f.Framework != "next" {
		t.Errorf("framework = %q, want next", f.Framework)
	}
}

func TestScanSkipsNoise(t *testing.T) {
	// node_modules / 隠しディレクトリの中身は事実に数えない。
	dir := t.TempDir()
	for _, d := range []string{"node_modules/ioredis", ".cache"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, "node_modules/ioredis/package.json", `{"dependencies":{"ioredis":"5"}}`)
	write(t, dir, ".cache/go.mod", "module junk\nrequire github.com/aws/aws-sdk-go-v2/service/s3 v1.0.0\n")
	f := Scan(dir)
	if f.Wants.Any() || f.Framework != "" {
		t.Fatalf("noise leaked into facts: %+v", f)
	}
}
