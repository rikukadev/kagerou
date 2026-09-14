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
	changed, err := InjectLWA(dir, "")
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
	changed2, err := InjectLWA(dir, "")
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
	if _, err := InjectLWA(dir, ""); err == nil {
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
			// per-app 形式(todo)と旧アカウント単位形式の混在。todo の domain は
			// SSM と食い違わせて「SSM が勝つ」ことを確かめる
			return []byte(`[` +
				`["kagerou-preview-base:todo:domain","stale.rikuka.dev"],` +
				`["kagerou-preview-base:todo:bucket","kagerou-base-todo-123"],` +
				`["kagerou-preview-base:domain","preview.rikuka.dev"],` +
				`["kagerou-preview-base:bucket","kagerou-preview-base-123"]]`), nil
		case strings.Contains(joined, "get-parameters-by-path"):
			// SSM データ契約(CONTRACT §9)。tf プロジェクトは SSM にしか無い
			// (= Terraform 製ベース)。契約外のパスは無視される
			return []byte(`[` +
				`["/kagerou/base/todo/domain","todo.rikuka.dev"],` +
				`["/kagerou/base/tf/domain","tf.rikuka.dev"],` +
				`["/kagerou/base/tf/bucket","kagerou-base-tf-123"],` +
				`["/kagerou/other/junk","x"]]`), nil
		}
		return nil, errNoCmd
	}
	d := Detect(t.TempDir())
	if len(d.Zones) != 2 || d.Zones[0] != "rikuka.dev" {
		t.Fatalf("zones = %v", d.Zones)
	}
	// per-app: SSM が真実の源(Exports の stale 値に勝つ)。bucket は Exports から補完
	if b, ok := d.Base("todo"); !ok || b.Domain != "todo.rikuka.dev" || b.Bucket != "kagerou-base-todo-123" {
		t.Fatalf("per-app base detection broken: %+v", d.Bases)
	}
	// SSM にしか無いベース(Terraform 製)も検出できる
	if b, ok := d.Base("tf"); !ok || b.Domain != "tf.rikuka.dev" || b.Bucket != "kagerou-base-tf-123" {
		t.Fatalf("SSM-only base detection broken: %+v", d.Bases)
	}
	// 別 project は旧アカウント単位 base に fallback
	if b, ok := d.Base("shop"); !ok || b.Domain != "preview.rikuka.dev" {
		t.Fatalf("legacy fallback broken: %+v", d.Bases)
	}
	// #139: ドメインの出所を覚えておく。SSM 由来のときだけ kagerou.yaml に
	// {base_domain} を書ける(Exports 由来には SSM キーが無く、解決できない)
	if b, _ := d.Base("todo"); !b.DomainFromSSM {
		t.Error("SSM が勝ったのに DomainFromSSM が false")
	}
	if b, _ := d.Base("tf"); !b.DomainFromSSM {
		t.Error("SSM のみのベースが DomainFromSSM=false")
	}
	if b, _ := d.Base("shop"); b.DomainFromSSM {
		t.Error("Exports 由来を SSM 由来と誤認している")
	}
}

var errNoCmd = os.ErrNotExist

func TestPreviewBaseTemplateAndScript(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "x", Region: "ap-northeast-1", Domain: "x.rikuka.dev", SetupBase: true}
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
		"kagerou-preview-base:${Project}:domain",    // per-app Exports(旧 fallback)
		"/kagerou/base/${Project}/domain",           // SSM データ契約(CONTRACT §9)
		"kagerou-base-${Project}-${AWS::AccountId}", // per-app バケット
		"OriginAccessControl",
		"*.${DomainName}",
		"4135ea2d-6df8-44a3-9df3-4b5a84be39ad", // CachingDisabled = invalidation 不要
		"Z2FDTNDATAQYW2",                       // CloudFront alias の固定ゾーン
	} {
		if !strings.Contains(s, want) {
			t.Errorf("preview-base.yaml missing %q", want)
		}
	}
	// kagerou.yaml に url_template({name}.{project}.<zone>)
	ky, _ := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if !strings.Contains(string(ky), `url_template: "https://{name}.x.rikuka.dev"`) {
		t.Fatalf("url_template missing: %s", ky)
	}
	// setup script に per-app スタック名 + Project パラメータで base デプロイ(us-east-1)
	if _, err := WriteSetupScript(dir, p, Detection{Owner: "o", Repo: "r"}); err != nil {
		t.Fatal(err)
	}
	sc, _ := os.ReadFile(filepath.Join(dir, SetupScriptName))
	for _, want := range []string{
		`PROJECT="x"`,
		`--stack-name "kagerou-preview-base-${PROJECT}"`,
		`Project="$PROJECT"`,
		"--region us-east-1", "list-hosted-zones-by-name",
	} {
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

func TestBaseForResolutionOrder(t *testing.T) {
	own := BaseInfo{Domain: "todo.example.com", Bucket: "b-todo"}
	shared := BaseInfo{Domain: "example.com", Bucket: "b-shared"}
	legacy := BaseInfo{Domain: "old.example.com", Bucket: "b-legacy"}

	// project 専用が最優先
	d := Detection{Bases: map[string]BaseInfo{"todo": own, SharedBaseKey: shared, "": legacy}}
	if b, isShared, ok := d.BaseFor("todo"); !ok || isShared || b.Bucket != "b-todo" {
		t.Fatalf("own base should win: %+v shared=%v ok=%v", b, isShared, ok)
	}
	// 専用が無ければ共有
	d2 := Detection{Bases: map[string]BaseInfo{SharedBaseKey: shared, "": legacy}}
	if b, isShared, ok := d2.BaseFor("todo"); !ok || !isShared || b.Bucket != "b-shared" {
		t.Fatalf("shared base should be used: %+v shared=%v ok=%v", b, isShared, ok)
	}
	// どちらも無ければ旧アカウント単位
	d3 := Detection{Bases: map[string]BaseInfo{"": legacy}}
	if b, isShared, ok := d3.BaseFor("todo"); !ok || isShared || b.Bucket != "b-legacy" {
		t.Fatalf("legacy fallback broken: %+v shared=%v ok=%v", b, isShared, ok)
	}
	// 何も無ければ見つからない
	if _, _, ok := (Detection{Bases: map[string]BaseInfo{}}).BaseFor("todo"); ok {
		t.Fatal("no base should not resolve")
	}
}

// ALB ベースはアプリのリージョンに SSM を書く(CloudFront ベースだけ us-east-1)。
// us-east-1 しか走査しないと、入口が alb のアプリのベースを検出できない。
func TestDetectBaseInAppRegion(t *testing.T) {
	// execCommand はモックしているが、region の解決は env も見る。
	// AWS_REGION がシェルに設定されている環境だと、そちらが勝って
	// 走査リージョンが変わり落ちる(外部から報告のあった失敗)
	t.Setenv("AWS_REGION", "")
	orig := execCommand
	defer func() { execCommand = orig }()
	var scanned []string
	execCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "configure get region"):
			return []byte("ap-northeast-1\n"), nil
		case strings.Contains(joined, "get-parameters-by-path"):
			for i, a := range args {
				if a == "--region" && i+1 < len(args) {
					scanned = append(scanned, args[i+1])
					if args[i+1] == "ap-northeast-1" {
						return []byte(`[["/kagerou/base/todo/domain","todo.example.com"]]`), nil
					}
				}
			}
			return []byte(`[]`), nil
		}
		return nil, errNoCmd
	}
	d := Detect(t.TempDir())
	if len(scanned) != 2 || scanned[0] != "us-east-1" || scanned[1] != "ap-northeast-1" {
		t.Fatalf("走査したリージョン = %v (us-east-1 → アプリのリージョンの順のはず)", scanned)
	}
	b, ok := d.Base("todo")
	if !ok || b.Domain != "todo.example.com" {
		t.Fatalf("ALB ベースを検出できていない: %+v", d.Bases)
	}
}

func TestSsmScanRegions(t *testing.T) {
	// 同じリージョンなら 2 回引かない
	if got := ssmScanRegions("us-east-1"); len(got) != 1 {
		t.Errorf("us-east-1 は 1 回のはず: %v", got)
	}
	if got := ssmScanRegions(""); len(got) != 1 {
		t.Errorf("region 不明なら us-east-1 だけ: %v", got)
	}
}
