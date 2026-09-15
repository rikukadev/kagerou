package main

// 検出した事実が **生成物に出ているか** を見るテスト(#182)。
//
// appscan が拾い、Params にフィールドがあり、雛形に分岐もあるのに、cmd の
// 配線だけが抜けている — という壊れ方を 2 回している(#160 の Dockerfile、
// #182 の health path)。どの層も単体では正しいので、層ごとのテストでは
// 捕まらない。**リポジトリを置いて init を回し、出てきたファイルを読む**。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/scaffold"
)

// initInto は dir にリポジトリを作って、非対話 init と同じ経路で生成する。
func initInto(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if d := filepath.Dir(path); d != dir {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	det := scaffold.Detect(dir)
	// cmd が Params に詰める値。ここが commands.go と食い違うと意味が無いので、
	// 増やすときは両方を直すこと
	p := scaffold.Params{
		Project: "web", Region: "ap-northeast-1",
		Port: det.AppPort, HasDockerfile: det.HasDockerfile, Framework: det.Framework,
		DockerfileName: det.DockerfileName, DockerfileDir: det.DockerfileDir,
		HealthPath: det.HealthPath,
		Domain:     "web.example.com", Entrypoint: "alb",
	}
	if _, err := scaffold.Run(dir, p, scaffold.AllTargets(), false); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// #182: 検出した health path が kagerou.yaml と LWA の両方に出る。
//
// 出ないと LWA は /healthz を叩き続ける。そのパスが無くても LWA は 5xx 未満を
// healthy 扱いにするので**起動は止まらず、検査として黙って無意味になる**。
func TestDetectedHealthPathReachesGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	initInto(t, dir, map[string]string{
		"Dockerfile":   "FROM node:22\nEXPOSE 3000\nHEALTHCHECK CMD curl -f http://localhost:3000/health\n",
		"package.json": `{"dependencies":{"next":"15"}}`,
	})

	if ky := read(t, dir, "kagerou.yaml"); !strings.Contains(ky, "readiness_path: /health\n") {
		t.Errorf("kagerou.yaml に検出値が出ていない:\n%s", ky)
	}
	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "AWS_LWA_READINESS_CHECK_PATH: /health\n") {
		t.Errorf("LWA の readiness が検出値になっていない")
	}
	// 2 つが食い違うと、kagerou は ready と言い LWA は別のパスを見ることになる
	if strings.Contains(tp, "AWS_LWA_READINESS_CHECK_PATH: /healthz") {
		t.Error("固定値が残っている")
	}
}

// 検出できないときは従来どおり。当てずっぽうのパスを書くと up が毎回落ちる。
func TestNoHealthPathKeepsTheDefault(t *testing.T) {
	dir := t.TempDir()
	initInto(t, dir, map[string]string{
		"Dockerfile":   "FROM node:22\nEXPOSE 3000\n",
		"package.json": `{"dependencies":{"next":"15"}}`,
	})

	if ky := read(t, dir, "kagerou.yaml"); strings.Contains(ky, "readiness_path") {
		t.Errorf("根拠が無いのに readiness_path を書いている:\n%s", ky)
	}
	if tp := read(t, dir, "template.yaml"); !strings.Contains(tp, "AWS_LWA_READINESS_CHECK_PATH: /healthz") {
		t.Error("未検出のときは /healthz のままのはず")
	}
}
