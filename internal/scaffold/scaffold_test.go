package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, dir, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunCreatesAll(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, Params{Project: "myapp", Region: "ap-northeast-1"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 4 || len(res.Skipped) != 0 {
		t.Fatalf("created=%v skipped=%v", res.Created, res.Skipped)
	}

	ky := read(t, dir, "kagerou.yaml")
	if !strings.Contains(ky, "project: myapp") || !strings.Contains(ky, "name_prefix: myapp-") {
		t.Fatalf("kagerou.yaml: %s", ky)
	}
	if strings.Contains(ky, "sashiki") {
		t.Fatal("sashiki block should be absent without --sashiki")
	}

	pv := read(t, dir, ".github/workflows/kagerou-preview.yml")
	if !strings.Contains(pv, "${{ vars.AWS_ROLE_ARN }}") || !strings.Contains(pv, "rikukadev/kagerou/action@v0") {
		t.Fatalf("preview.yml placeholders broken: %s", pv[:200])
	}

	tp := read(t, dir, "template.yaml")
	if !strings.Contains(tp, "EnvKagerouEnv") || !strings.Contains(tp, "KagerouUrl") {
		t.Fatalf("template.yaml: missing contract pieces")
	}
}

func TestRunSashikiBlock(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, Params{Project: "myapp", Region: "ap-northeast-1", Sashiki: true}, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	ky := read(t, dir, "kagerou.yaml")
	for _, want := range []string{"pre_up: sashiki create {name}", "post_down: sashiki delete {name}", "DB_USER: dev@{name}"} {
		if !strings.Contains(ky, want) {
			t.Errorf("kagerou.yaml missing %q", want)
		}
	}
}

func TestRunSkipsExistingAndForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kagerou.yaml"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template.yaml"), []byte("my template"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(dir, Params{Project: "x", Region: "r"}, AllTargets(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped=%v", res.Skipped)
	}
	if read(t, dir, "kagerou.yaml") != "mine" {
		t.Fatal("existing kagerou.yaml was overwritten without force")
	}

	// force: kagerou.yaml は上書き、template.yaml は force でも守る
	if _, err := Run(dir, Params{Project: "x", Region: "r"}, AllTargets(), true); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "kagerou.yaml") == "mine" {
		t.Fatal("force should overwrite kagerou.yaml")
	}
	if read(t, dir, "template.yaml") != "my template" {
		t.Fatal("template.yaml must never be overwritten")
	}
}

func TestStepsAndPlain(t *testing.T) {
	det := Detection{VarsSet: map[string]bool{}}
	base := Steps(Params{Region: "r"}, det, SetupSkip)
	withSashiki := Steps(Params{Region: "r", Sashiki: true}, det, SetupSkip)
	if len(withSashiki) != len(base)+2 {
		t.Fatalf("sashiki steps not added: %d vs %d", len(withSashiki), len(base))
	}
	plain := PlainSteps(Params{Region: "ap-northeast-1"}, det)
	if !strings.Contains(plain, "1. ") || !strings.Contains(plain, "ECR") {
		t.Fatalf("plain steps: %s", plain)
	}
}

func TestWriteSetupScript(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteSetupScript(dir, Params{Region: "ap-northeast-1"}, Detection{Owner: "rikukadev", Repo: "myapp"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`OWNER="rikukadev"`, `REPO="myapp"`, `REGION="ap-northeast-1"`,
		"create-open-id-connect-provider", // provider が無ければ作る
		"repo:${OWNER}/${REPO}:*",         // 旧形式 sub
		"repo:${OWNER}@${OWNER_ID}/${REPO}@${REPO_ID}:*", // ID 形式 sub(新 org)
		"create-repository",
		"gh variable set AWS_ROLE_ARN",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("setup script missing %q", want)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode()&0o100 == 0 {
		t.Error("setup script should be executable")
	}
}

func TestStepsSetupModes(t *testing.T) {
	det := Detection{VarsSet: map[string]bool{}}
	p := Params{Region: "r"}
	skip := Steps(p, det, SetupSkip)
	script := Steps(p, det, SetupScript)
	applied := Steps(p, det, SetupApplied)
	if len(script) != len(skip)-2 || len(applied) != len(skip)-2 {
		t.Fatalf("script/applied should fold 3 steps into 1: skip=%d script=%d applied=%d", len(skip), len(script), len(applied))
	}
	foundDone := false
	for _, s := range applied {
		if s.Done && strings.Contains(s.Title, "AWS setup") {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatal("applied mode should mark AWS setup as done")
	}
}
