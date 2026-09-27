package appscan

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJava(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const springPom = `<project><modelVersion>4.0.0</modelVersion>
  <parent><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-parent</artifactId><version>3.4.0</version></parent>
  <groupId>com.example</groupId><artifactId>api</artifactId><version>1.0</version>
  <dependencies>
    <dependency><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-web</artifactId></dependency>
    <dependency><groupId>org.postgresql</groupId><artifactId>postgresql</artifactId></dependency>
    <dependency><groupId>org.springframework.boot</groupId><artifactId>spring-boot-starter-data-redis</artifactId></dependency>
    <dependency><groupId>software.amazon.awssdk</groupId><artifactId>sqs</artifactId></dependency>
  </dependencies></project>`

// Dockerfile の無い Spring Boot が「サーバ無し」に見えていた(#228)。
// recommend はここの Services を見て static かどうかを決めるので、
// 0 のままだと Web API が静的サイトとして推薦される。
func TestJavaSpringBootWithoutDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, dir, "pom.xml", springPom)

	f := Scan(dir)
	if f.Framework != "spring-boot" {
		t.Errorf("framework = %q (want spring-boot)", f.Framework)
	}
	if f.DBDriver != "postgresql" {
		t.Errorf("db = %q (want postgresql)", f.DBDriver)
	}
	if !f.Wants.Redis || !f.Wants.SQS {
		t.Errorf("wants = %+v (want Redis+SQS)", f.Wants)
	}
	if f.Services != 1 {
		t.Errorf("services = %d — 0 だと static と誤診される", f.Services)
	}
}

// 古典的な Tomcat WAR(pom + tomcat ベースの Dockerfile)。framework が空欄だと
// 「何のアプリか」がどこにも出ず、mysql の検出も沈黙していた。
func TestJavaTomcatWar(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, dir, "pom.xml", `<project><modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId><artifactId>legacy</artifactId><version>1.0</version>
  <packaging>war</packaging>
  <dependencies>
    <dependency><groupId>jakarta.servlet</groupId><artifactId>jakarta.servlet-api</artifactId></dependency>
    <dependency><groupId>com.mysql</groupId><artifactId>mysql-connector-j</artifactId></dependency>
  </dependencies></project>`)
	writeJava(t, dir, "Dockerfile", "FROM tomcat:10.1-jdk17\nEXPOSE 8080\n")

	f := Scan(dir)
	if f.Framework != "java" {
		t.Errorf("framework = %q (want java)", f.Framework)
	}
	if f.DBDriver != "mysql-connector-j" {
		t.Errorf("db = %q (want mysql-connector-j)", f.DBDriver)
	}
	if f.AppPort != "8080" {
		t.Errorf("port = %q", f.AppPort)
	}
}

func TestJavaGradle(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, dir, "build.gradle", `plugins { id "org.springframework.boot" version "3.4.0" }
dependencies {
  implementation "org.springframework.boot:spring-boot-starter-web"
  runtimeOnly "com.mysql:mysql-connector-j"
  implementation "io.lettuce:lettuce-core"
}`)
	f := Scan(dir)
	if f.Framework != "spring-boot" {
		t.Errorf("framework = %q (want spring-boot)", f.Framework)
	}
	if f.DBDriver != "mysql-connector-j" {
		t.Errorf("db = %q", f.DBDriver)
	}
	if !f.Wants.Redis {
		t.Errorf("lettuce を Redis と読めていない: %+v", f.Wants)
	}
	if f.Services != 1 {
		t.Errorf("services = %d", f.Services)
	}
}

// pom は同梱 JS assets より強い(composer と同じ理屈。#154)。
// Spring アプリの src/main/resources 隣に package.json が居ても、デプロイ対象は Java。
func TestJavaBeatsColocatedAssets(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, dir, "pom.xml", springPom)
	writeJava(t, filepath.Join(dir, "assets"), "package.json",
		`{"dependencies": {"react-router": "7"}}`)

	f := Scan(dir)
	if f.Framework != "spring-boot" {
		t.Errorf("framework = %q — 同梱 assets の JS が勝っている", f.Framework)
	}
}

// モノレポの集約 pom(packaging=pom)はアプリではないので、サービスに数えない。
func TestJavaAggregatorPomIsNotAService(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, dir, "pom.xml", `<project><modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId><artifactId>parent</artifactId><version>1.0</version>
  <packaging>pom</packaging>
  <modules><module>api</module></modules></project>`)
	writeJava(t, filepath.Join(dir, "api"), "pom.xml", springPom)

	f := Scan(dir)
	if f.Services != 1 {
		t.Errorf("services = %d — 集約 pom を数えている(want 1: api だけ)", f.Services)
	}
}

// 既存の検出への非影響: go / node のリポジトリで Java の走査が何も足さない。
func TestJavaScanDoesNotTouchOtherStacks(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, dir, "go.mod", "module x\nrequire github.com/go-sql-driver/mysql v1.8.0\n")
	f := Scan(dir)
	if f.Framework != "go" || f.DBDriver != "go-sql-driver/mysql" {
		t.Errorf("go の検出が変わった: %q / %q", f.Framework, f.DBDriver)
	}
}

// framework は検出できたのに Dockerfile が無いディレクトリは「環境に載らない」。
// 黙って落とすと framework 表示と生成物が食い違うので、事実として持つ(#227)。
func TestWithoutImageRecordsDroppedDirs(t *testing.T) {
	dir := t.TempDir()
	writeJava(t, filepath.Join(dir, "services", "api"), "Dockerfile", "FROM golang:1.25\nEXPOSE 8080\n")
	writeJava(t, filepath.Join(dir, "services", "api"), "go.mod", "module api\n")
	writeJava(t, filepath.Join(dir, "web"), "package.json", `{"dependencies":{"next":"15"}}`)
	// ルートのマーカーは「リポジトリ全体」の話なのでサービス扱いしない
	writeJava(t, dir, "package.json", `{"dependencies":{"typescript":"5"}}`)

	f := Scan(dir)
	if len(f.WithoutImage) != 1 {
		t.Fatalf("WithoutImage = %+v (want web だけ)", f.WithoutImage)
	}
	w := f.WithoutImage[0]
	if w.Name != "web" || w.Framework != "next" || w.Dir != "web" {
		t.Errorf("%+v", w)
	}
	// Dockerfile を持つ側は ServiceFacts に居て、WithoutImage には居ない
	for _, s := range f.WithoutImage {
		if s.Name == "api" {
			t.Error("Dockerfile 持ちが WithoutImage に入っている")
		}
	}
}
