package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/config"
)

func TestDriverFor(t *testing.T) {
	// 既存の設定が最優先。init を二度叩いて構成が入れ替わったら事故(#81)。
	t.Run("既存 kagerou.yaml の driver が勝つ", func(t *testing.T) {
		d := Detection{Driver: "static", HasDockerfile: true, Framework: "next"}
		if got := DriverFor(d); got != "static" {
			t.Errorf("DriverFor = %q, want static(既存設定を上書きしてはいけない)", got)
		}
	})

	t.Run("compute があれば stack", func(t *testing.T) {
		for _, d := range []Detection{
			{HasDockerfile: true, Framework: "react-router"},
			{HasTemplate: true, Framework: "vite"},
		} {
			if got := DriverFor(d); got != "stack" {
				t.Errorf("DriverFor(%+v) = %q, want stack", d, got)
			}
		}
	})

	t.Run("compute が無いフロントエンドは static", func(t *testing.T) {
		for _, f := range []string{"react-router", "vite", "astro"} {
			if got := DriverFor(Detection{Framework: f}); got != "static" {
				t.Errorf("DriverFor(%q) = %q, want static", f, got)
			}
		}
	})

	// 迷ったら stack。static で作って足りないより、余分な雛形を消すほうが
	// 復帰しやすい(template.yaml は消せるが、無い状態からは書けない)。
	t.Run("分からないものは stack に倒す", func(t *testing.T) {
		for _, f := range []string{"go", "next", ""} {
			if got := DriverFor(Detection{Framework: f}); got != "stack" {
				t.Errorf("DriverFor(%q) = %q, want stack", f, got)
			}
		}
	})
}

// static で init したら compute 用の成果物が出ないこと。
// 出ると「消してから手で workflow を書く」に戻る(#81 の現状の回避策)。
func TestStaticInitOmitsComputeArtifacts(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "spa", Region: "ap-northeast-1", Driver: "static",
		Framework: "react-router", Domain: "spa.example.com", BaseBucket: "kagerou-base-spa-123"}
	if _, err := Run(dir, p, AllTargets(), true); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"template.yaml", "Dockerfile"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("static なのに %s が生成された", f)
		}
	}
	for _, f := range []string{"kagerou.yaml", filepath.Join(".github", "workflows", "kagerou-preview.yml")} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s が無い: %v", f, err)
		}
	}
}

// 生成した kagerou.yaml が **そのまま読める** こと。driver: static は
// url_template / static.dist / static.bucket が揃っていないと Load が弾く。
// ここが通らないと、利用者は init 直後に設定エラーに当たる。
func TestStaticInitProducesLoadableConfig(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "spa", Region: "ap-northeast-1", Driver: "static",
		Framework: "react-router", Domain: "spa.example.com", BaseBucket: "kagerou-base-spa-123"}
	if _, err := Run(dir, p, AllTargets(), true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "kagerou.yaml"))
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
		t.Fatalf("生成した設定が読めない: %v\n%s", err, b)
	}
	if cfg.Driver != "static" {
		t.Errorf("driver = %q", cfg.Driver)
	}
	if cfg.Static.Dist != "dist" {
		t.Errorf("static.dist = %q, want dist", cfg.Static.Dist)
	}
	if cfg.Static.Bucket != "kagerou-base-spa-123" {
		t.Errorf("static.bucket = %q(検出済みベースを埋めるべき)", cfg.Static.Bucket)
	}
	// react-router はクライアントルーターなので spa になる(#86)
	if cfg.StaticRouting() != config.RoutingSPA {
		t.Errorf("routing = %q, want spa", cfg.StaticRouting())
	}
}

// ベース未作成のときは bucket を空にして TODO を残す。ここを推測で
// 埋めると、存在しないバケットへ同期して原因の遠い失敗になる。
func TestStaticInitLeavesBucketTodo(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "spa", Region: "ap-northeast-1", Driver: "static",
		Framework: "vite", Domain: "spa.example.com"}
	if _, err := Run(dir, p, Targets{KagerouYaml: true}, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if !strings.Contains(string(b), "TODO") {
		t.Errorf("bucket 未検出なら TODO を残すべき:\n%s", b)
	}
}

// static の workflow に SAM / ECR が混ざらないこと。
func TestStaticWorkflowHasNoSAM(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "spa", Region: "ap-northeast-1", Driver: "static", Framework: "vite"}
	if _, err := Run(dir, p, Targets{Preview: true}, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".github", "workflows", "kagerou-preview.yml"))
	wf := string(b)
	for _, bad := range []string{"sam build", "sam package", "ECR_REPOSITORY", "setup-sam"} {
		if strings.Contains(wf, bad) {
			t.Errorf("static の workflow に %q が入っている:\n%s", bad, wf)
		}
	}
	if !strings.Contains(wf, "npm run build") {
		t.Errorf("ビルド手順が無い:\n%s", wf)
	}
}

// static のプランに compute のものを出さない。作られないものを見せると、
// 費用も取り壊し手順も嘘になる。
func TestStaticPlanHasNoCompute(t *testing.T) {
	p := BuildAWSPlan(
		Params{Project: "spa", Region: "ap-northeast-1", Driver: "static"},
		Detection{AccountID: "123456789012", Repo: "spa"})
	out := p.Render()
	for _, bad := range []string{"ECR", "Lambda", "HTTP API"} {
		if strings.Contains(out, bad) {
			t.Errorf("static のプランに %q が出ている:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "IAM role") {
		t.Error("ロールは static でも要る")
	}
}

// stack 構成では従来どおり compute を出す(上の変更で消えていないこと)。
func TestStackPlanKeepsCompute(t *testing.T) {
	p := BuildAWSPlan(
		Params{Project: "app", Region: "ap-northeast-1"},
		Detection{AccountID: "123456789012", Repo: "app"})
	out := p.Render()
	for _, want := range []string{"ECR repository", "Lambda"} {
		if !strings.Contains(out, want) {
			t.Errorf("stack のプランに %q が無い:\n%s", want, out)
		}
	}
}
