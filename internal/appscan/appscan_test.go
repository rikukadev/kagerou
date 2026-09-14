package appscan

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if d := filepath.Dir(path); d != dir {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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

func TestScanWantsSNS(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"dependencies":{"@aws-sdk/client-sns":"3"}}`)
	if f := Scan(dir); !f.Wants.SNS {
		t.Error("node の SNS クライアントを拾うはず")
	}
	dir2 := t.TempDir()
	write(t, dir2, "go.mod", "module m\nrequire github.com/aws/aws-sdk-go-v2/service/sns v1.0.0\n")
	if f := Scan(dir2); !f.Wants.SNS {
		t.Error("go の SNS クライアントを拾うはず")
	}
}

func TestScanServicesAndRealtime(t *testing.T) {
	dir := t.TempDir()
	// compose に 3 サービス
	write(t, dir, "compose.yaml", `services:
  gateway:
    build: .
    ports:
      - "8080:8080"
  api:
    build: .
  worker:
    build: .
volumes:
  shared:
`)
	if got := Scan(dir).Services; got != 3 {
		t.Fatalf("Services = %d, want 3 (compose services)", got)
	}

	// Go の cmd/*/main.go も数える
	dir2 := t.TempDir()
	for _, name := range []string{"gateway", "api"} {
		if err := os.MkdirAll(filepath.Join(dir2, "cmd", name), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, dir2, filepath.Join("cmd", name, "main.go"), "package main\nfunc main() {}\n")
	}
	if got := Scan(dir2).Services; got != 2 {
		t.Fatalf("Services = %d, want 2 (cmd/*/main.go)", got)
	}

	// Dockerfile だけなら 1
	dir3 := t.TempDir()
	write(t, dir3, "Dockerfile", "FROM alpine\nEXPOSE 8080\n")
	if got := Scan(dir3).Services; got != 1 {
		t.Fatalf("Services = %d, want 1", got)
	}

	// WebSocket 依存の検出(上限のある入口を避ける根拠)
	dir4 := t.TempDir()
	write(t, dir4, "go.mod", "module x\n\nrequire github.com/gorilla/websocket v1.5.0\n")
	if !Scan(dir4).Realtime {
		t.Fatal("gorilla/websocket を realtime として検出できていない")
	}
	dir5 := t.TempDir()
	write(t, dir5, "package.json", `{"dependencies":{"socket.io":"^4"}}`)
	if !Scan(dir5).Realtime {
		t.Fatal("socket.io を realtime として検出できていない")
	}
	// 無関係な依存で誤検知しない
	dir6 := t.TempDir()
	write(t, dir6, "package.json", `{"dependencies":{"react":"^19"}}`)
	if Scan(dir6).Realtime {
		t.Fatal("realtime を誤検知している")
	}
}
func TestScanServicesGoModuleMain(t *testing.T) {
	// 3tier 型のうち、Dockerfile も compose も cmd/ も無い構成(zip Lambda)。
	// go.mod の直下に main.go を置く Go サーバをサービスに数えないと、
	// recommend が compute 無しと見て「サーバが見つからない」と誤判定する。
	dir := t.TempDir()
	for _, d := range []string{"api", "web"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, "api/go.mod", "module m\nrequire github.com/go-sql-driver/mysql v1.10.0\n")
	write(t, dir, "api/main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "web/package.json", `{"dependencies":{"react":"19"}}`)

	f := Scan(dir)
	if f.Services != 1 {
		t.Fatalf("Services = %d, want 1 (go.mod 直下の main)", f.Services)
	}
	if f.HasDockerfile {
		t.Error("Dockerfile は無い")
	}

	// go.mod の無い main.go(生成スクリプト等)はサービスに数えない
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir2, "gen/main.go", "package main\n\nfunc main() {}\n")
	if got := Scan(dir2).Services; got != 0 {
		t.Fatalf("Services = %d, want 0 (go.mod が無い main.go)", got)
	}

	// package main でなければ数えない
	dir3 := t.TempDir()
	write(t, dir3, "go.mod", "module m\n")
	write(t, dir3, "main.go", "package app\n")
	if got := Scan(dir3).Services; got != 0 {
		t.Fatalf("Services = %d, want 0 (package main ではない)", got)
	}
}

func TestURLShapeCrossFromTraefik(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", "services:\n  web:\n    labels:\n      - traefik.http.routers.web.rule=Host(`app.example.com`)\n  api:\n    labels:\n      - traefik.http.routers.api.rule=Host(`api.example.com`)\n")
	f := Scan(dir)
	if f.URLShape != "cross" {
		t.Fatalf("shape = %q, want cross", f.URLShape)
	}
	if len(f.Hosts) != 2 || f.Hosts[0] != "app.example.com" {
		t.Fatalf("hosts = %v", f.Hosts)
	}
}

func TestURLShapeCrossFromNginxAndCors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "nginx.conf", "server {\n  server_name app.example.com api.example.com;\n}\n")
	if f := Scan(dir); f.URLShape != "cross" || len(f.Hosts) != 2 {
		t.Fatalf("nginx: %+v", f)
	}
	dir2 := t.TempDir()
	write(t, dir2, "package.json", `{"dependencies":{"express":"4","cors":"2"}}`)
	if f := Scan(dir2); f.URLShape != "cross" {
		t.Fatalf("cors dep should hint cross: %q", f.URLShape)
	}
	// go 側の CORS ミドルウェア
	dir3 := t.TempDir()
	write(t, dir3, "go.mod", "module m\nrequire github.com/rs/cors v1.11.0\n")
	if f := Scan(dir3); f.URLShape != "cross" {
		t.Fatalf("rs/cors should hint cross: %q", f.URLShape)
	}
}

func TestURLShapeCrossFromHandwrittenCORS(t *testing.T) {
	// 3tier-demo 型: CORS ミドルウェアの依存を持たず、api/cors.go に手で書く。
	// 依存だけ見ていると別オリジン構成を素通りする
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "api/go.mod", "module m\n")
	write(t, dir, "api/cors.go", `package main

func withCORS(next http.Handler) http.Handler {
	w.Header().Set("Access-Control-Allow-Origin", origin)
}
`)
	if f := Scan(dir); f.URLShape != "cross" {
		t.Fatalf("手書き CORS を cross として拾えていない: %q", f.URLShape)
	}

	// vite の dev proxy が同居していても cross のまま。あれは localhost の
	// 話でしかなく、本番のヘッダを上書きする証拠にはならない(3tier-demo の形)
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "web/package.json", `{"dependencies":{"react":"19"}}`)
	write(t, dir, "web/vite.config.ts", `export default { server: { proxy: { "/api": "http://127.0.0.1:8080" } } }`)
	if f := Scan(dir); f.URLShape != "cross" {
		t.Fatalf("dev proxy が本番の CORS を上書きしている: %q", f.URLShape)
	}

	// 名前だけでは決めない(CORS ヘッダを書いていなければ信号にしない)
	dir2 := t.TempDir()
	write(t, dir2, "go.mod", "module m\n")
	write(t, dir2, "cors.go", "package main\n\n// CORS は使わない\n")
	if f := Scan(dir2); f.URLShape != "" {
		t.Fatalf("ファイル名だけで cross にしている: %q", f.URLShape)
	}
}

func TestURLShapePathFromViteProxy(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"dependencies":{"react":"19"}}`)
	write(t, dir, "vite.config.ts", "export default { server: { proxy: { \"/api\": \"http://localhost:8081\" } } }\n")
	if f := Scan(dir); f.URLShape != "path" {
		t.Fatalf("vite proxy should be path: %q", f.URLShape)
	}
	// 明示ホスト(cross の確定信号)があれば path より勝つ
	write(t, dir, "compose.yaml", "services:\n  web:\n    labels:\n      - traefik.http.routers.w.rule=Host(`app.example.com`)\n")
	if f := Scan(dir); f.URLShape != "cross" {
		t.Fatalf("explicit hosts should win: %q", f.URLShape)
	}
}

func TestURLShapeUnknownStaysEmpty(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"dependencies":{"react":"19"}}`)
	if f := Scan(dir); f.URLShape != "" {
		t.Fatalf("no signals should stay empty: %q", f.URLShape)
	}
}

func TestURLShapeAPIEnvName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "compose.yaml", "services:\n  web:\n    environment:\n      VITE_API_BASE_URL: https://api.example.com\n")
	if f := Scan(dir); f.URLShape != "cross" {
		t.Fatalf("*_API_URL env should hint cross: %q", f.URLShape)
	}
}

func TestScanPHPComposer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "composer.json", `{"require":{"php":"~8.3.0","yiisoft/yii":"^1.1","koriym/dii":"~0.5.0"}}`)
	if f := Scan(dir); f.Framework != "yii" {
		t.Fatalf("framework = %q, want yii", f.Framework)
	}

	// 既知のフレームワークが無い composer.json は素の php。
	// 素の node と同じ優先度なので、ルート優先で先に読んだ方が残る。
	dir2 := t.TempDir()
	write(t, dir2, "composer.json", `{"require":{"monolog/monolog":"^3"}}`)
	if f := Scan(dir2); f.Framework != "php" {
		t.Fatalf("framework = %q, want php", f.Framework)
	}
}

func TestScanPHPComposerInSubdir(t *testing.T) {
	// PHP アプリは composer.json がルートに無いことがある(authense は service/protected/)。
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "service", "protected"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, filepath.Join("service", "protected", "composer.json"), `{"require":{"yiisoft/yii":"^1.1"}}`)
	if f := Scan(dir); f.Framework != "yii" {
		t.Fatalf("framework = %q, want yii", f.Framework)
	}
}

func TestScanComposePortPrefersAppService(t *testing.T) {
	// アプリは expose だけ、DB の ports は変数展開、検索エンジンは素の expose。
	// ports: の外まで走査すると、読めない DB の行を飛び越えて 9200 を拾ってしまう。
	dir := t.TempDir()
	write(t, dir, "compose.yaml", `services:
  app:
    image: ghcr.io/example/app:latest
    expose:
      - "80"
  database:
    image: ghcr.io/example/images/mysql:8
    ports:
      - "${DATABASE_PORT:-11001}:3306"
  opensearch:
    image: opensearchproject/opensearch:2
    expose:
      - "9200"
volumes:
  data:
`)
	if got := Scan(dir).AppPort; got != "80" {
		t.Fatalf("AppPort = %q, want 80 (アプリのサービスの expose)", got)
	}
}

func TestScanComposePortPrefersBuildService(t *testing.T) {
	// build: があるサービスが最優先(既製 image の判定より強い)。
	dir := t.TempDir()
	write(t, dir, "compose.yaml", `services:
  cache:
    image: redis:7
    ports:
      - "6379:6379"
  web:
    build: .
    ports:
      - "8080:3000"
`)
	if got := Scan(dir).AppPort; got != "3000" {
		t.Fatalf("AppPort = %q, want 3000", got)
	}
}

func TestScanPHPAppWithJSAssets(t *testing.T) {
	// authense 型: PHP アプリのリポジトリに、画面用の JS バンドルが同居している。
	// 走査順では assets/ が先に見つかるが、デプロイされるのは PHP のアプリ。
	dir := t.TempDir()
	for _, d := range []string{"assets", filepath.Join("service", "protected")} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, filepath.Join("assets", "package.json"), `{"dependencies":{"react-router":"6","react":"18"}}`)
	write(t, dir, filepath.Join("service", "protected", "composer.json"), `{"require":{"yiisoft/yii":"^1.1"}}`)
	if f := Scan(dir); f.Framework != "yii" {
		t.Fatalf("framework = %q, want yii", f.Framework)
	}
}

// #151: Dockerfile(ECS / 本番)と Dockerfile.lambda を分けている構成。
// 素の Dockerfile しか見ないと「LWA 未導入」と誤判定し、init が
// **用途の違うイメージ定義のほうに**注入してしまう。
func TestScanFindsLWAInDockerfileVariant(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Dockerfile", "FROM node:22-alpine\nEXPOSE 3000\n")
	write(t, dir, "Dockerfile.lambda",
		"FROM node:22-alpine\n"+
			"COPY --from=public.ecr.aws/awsguru/aws-lambda-adapter:0.8.4 /lambda-adapter /opt/extensions/lambda-adapter\n"+
			"EXPOSE 3000\n")
	f := Scan(dir)
	if !f.HasLWA {
		t.Error("Dockerfile.lambda の LWA を見落としている")
	}
	// 注入先は「既に入っているほう」。素の Dockerfile を書き換えさせない
	if f.DockerfileName != "Dockerfile.lambda" {
		t.Errorf("DockerfileName = %q (want Dockerfile.lambda)", f.DockerfileName)
	}
	want := []string{"Dockerfile", "Dockerfile.lambda"}
	if len(f.Dockerfiles) != 2 || f.Dockerfiles[0] != want[0] || f.Dockerfiles[1] != want[1] {
		t.Errorf("Dockerfiles = %v (want %v)", f.Dockerfiles, want)
	}
}

func TestScanPlainDockerfileUnchanged(t *testing.T) {
	// 既存の「ルート直下 Dockerfile だけ」の構成は挙動を変えない
	dir := t.TempDir()
	write(t, dir, "Dockerfile", "FROM node:22\nEXPOSE 8080\n")
	f := Scan(dir)
	if !f.HasDockerfile || f.HasLWA || f.DockerfileName != "Dockerfile" || f.AppPort != "8080" {
		t.Errorf("%+v", f)
	}
}

func TestScanLambdaOnlyDockerfile(t *testing.T) {
	// 素の Dockerfile が無く、Dockerfile.lambda だけの構成でも検出する
	dir := t.TempDir()
	write(t, dir, "Dockerfile.lambda", "FROM node:22\nEXPOSE 3000\n")
	f := Scan(dir)
	if !f.HasDockerfile || f.DockerfileName != "Dockerfile.lambda" {
		t.Errorf("%+v", f)
	}
}

// #165: readiness の既定 "/" はルートが重い SSR で無駄に遅く、リダイレクトする
// アプリでは誤判定する。アプリが専用のパスを持つ事実はファイルに書かれている。
func TestScanHealthPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"LWA の readiness 設定", map[string]string{
			"Dockerfile": "FROM node:22\nENV AWS_LWA_READINESS_CHECK_PATH=/healthz\nEXPOSE 3000\n",
		}, "/healthz"},
		{"Dockerfile の HEALTHCHECK", map[string]string{
			"Dockerfile": "FROM node:22\nEXPOSE 3000\nHEALTHCHECK CMD curl -f http://localhost:3000/api/health || exit 1\n",
		}, "/api/health"},
		{"compose の healthcheck", map[string]string{
			"Dockerfile":   "FROM node:22\nEXPOSE 3000\n",
			"compose.yaml": "services:\n  web:\n    build: .\n    healthcheck:\n      test: [\"CMD\", \"curl\", \"-f\", \"http://localhost:3000/-/ready\"]\n",
		}, "/-/ready"},
		{"クエリは落とす", map[string]string{
			"Dockerfile": "FROM node:22\nHEALTHCHECK CMD curl http://localhost/health?deep=1\n",
		}, "/health"},
		{"ルートだけなら言わない", map[string]string{
			"Dockerfile": "FROM node:22\nHEALTHCHECK CMD curl -f http://localhost:3000/\n",
		}, ""},
		{"何も無ければ空", map[string]string{
			"Dockerfile": "FROM node:22\nEXPOSE 3000\n",
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for n, c := range tc.files {
				write(t, dir, n, c)
			}
			if got := Scan(dir).HealthPath; got != tc.want {
				t.Errorf("HealthPath = %q (want %q)", got, tc.want)
			}
		})
	}
}

// 既にイメージを作って公開しているリポジトリに sam build の雛形を出すのは
// 二度手間かもしれない。事実として出すために検出する。
func TestScanPublishesImage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".github/workflows/ci.yml",
		"jobs:\n  build:\n    steps:\n      - uses: docker/build-push-action@v6\n        with:\n          tags: ghcr.io/acme/app:latest\n")
	f := Scan(dir)
	if !f.PublishesImage || f.ImageRegistry != "ghcr.io" {
		t.Errorf("PublishesImage=%v registry=%q", f.PublishesImage, f.ImageRegistry)
	}
}

func TestScanIgnoresKagerouOwnWorkflows(t *testing.T) {
	// kagerou 自身が出した workflow は「既存の CI」ではない。
	// これを数えると、2 回目の init 以降ずっと「公開している」と言い続ける
	dir := t.TempDir()
	write(t, dir, ".github/workflows/kagerou-preview.yml",
		"jobs:\n  up:\n    steps:\n      - run: docker push ghcr.io/acme/app\n")
	if f := Scan(dir); f.PublishesImage {
		t.Error("kagerou 自身の workflow を既存 CI と数えている")
	}
}

func TestScanNoWorkflowsIsQuiet(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Dockerfile", "FROM node:22\n")
	if f := Scan(dir); f.PublishesImage || f.ImageRegistry != "" {
		t.Errorf("%+v", f)
	}
}
