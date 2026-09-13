package main

import (
	"os"
	"strings"
	"testing"
)

func stubChecks(t *testing.T, aws, gh bool) {
	t.Helper()
	orig := preflightCheck
	t.Cleanup(func() { preflightCheck = orig })
	preflightCheck = func(name string, _ ...string) bool {
		if name == "aws" {
			return aws
		}
		return gh
	}
}

func TestEnsureAuthAllGood(t *testing.T) {
	stubChecks(t, true, true)
	var out strings.Builder
	st := ensureAuth(true, strings.NewReader(""), &out)
	if !st.AWS || !st.GH {
		t.Fatalf("status = %+v", st)
	}
	if out.Len() != 0 {
		t.Fatalf("both ok なら何も表示しないはず: %s", out.String())
	}
}

func TestEnsureAuthOffersGhLogin(t *testing.T) {
	stubChecks(t, true, false)
	ran := []string{}
	orig := runInteractive
	t.Cleanup(func() { runInteractive = orig })
	runInteractive = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		preflightCheck = func(string, ...string) bool { return true } // ログイン成功を模擬
		return nil
	}
	var out strings.Builder
	st := ensureAuth(true, strings.NewReader("\n"), &out) // Enter = Yes
	if len(ran) != 1 || ran[0] != "gh auth login" {
		t.Fatalf("ran = %v", ran)
	}
	if !st.GH {
		t.Fatal("ログイン後は GH ok になるはず")
	}
}

func TestEnsureAuthAwsChoiceAndDecline(t *testing.T) {
	stubChecks(t, false, true)
	ran := []string{}
	orig := runInteractive
	t.Cleanup(func() { runInteractive = orig })
	runInteractive = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	var out strings.Builder
	st := ensureAuth(true, strings.NewReader("2\n"), &out)
	if len(ran) != 1 || ran[0] != "aws sso login" {
		t.Fatalf("ran = %v", ran)
	}
	_ = st

	// enter だけなら実行せず縮退の注意が出る
	ran = nil
	var out2 strings.Builder
	st2 := ensureAuth(true, strings.NewReader("\n"), &out2)
	if len(ran) != 0 {
		t.Fatalf("should not run anything: %v", ran)
	}
	if st2.AWS || !strings.Contains(out2.String(), "continuing without AWS") {
		t.Fatalf("degraded notice missing: %s", out2.String())
	}
}

func TestEnsureAuthNonInteractive(t *testing.T) {
	stubChecks(t, false, false)
	orig := runInteractive
	t.Cleanup(func() { runInteractive = orig })
	runInteractive = func(name string, _ ...string) error {
		t.Fatalf("non-interactive では実行しない: %s", name)
		return nil
	}
	var out strings.Builder
	ensureAuth(false, strings.NewReader(""), &out)
	if !strings.Contains(out.String(), "aws  ✗") || !strings.Contains(out.String(), "gh   ✗") {
		t.Fatalf("status display missing: %s", out.String())
	}
}

func TestChooseProfileSelects(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config"
	if err := os.WriteFile(cfg, []byte("[default]\nregion = ap-northeast-1\n\n[profile coco]\nregion = us-east-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", dir+"/none")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")

	var out strings.Builder
	chooseProfile(true, strings.NewReader("2\n"), &out)
	if os.Getenv("AWS_PROFILE") != "coco" {
		t.Fatalf("AWS_PROFILE = %q, want coco", os.Getenv("AWS_PROFILE"))
	}
	if !strings.Contains(out.String(), "Multiple AWS profiles") {
		t.Fatalf("prompt missing: %s", out.String())
	}
}

func TestChooseProfileRespectsExplicitAndSingle(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config"
	_ = os.WriteFile(cfg, []byte("[default]\nregion = ap-northeast-1\n\n[profile coco]\n"), 0o644)
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", dir+"/none")

	// 明示済みなら聞かない
	t.Setenv("AWS_PROFILE", "coco")
	var out strings.Builder
	chooseProfile(true, strings.NewReader("1\n"), &out)
	if out.Len() != 0 || os.Getenv("AWS_PROFILE") != "coco" {
		t.Fatalf("explicit profile should be respected: %q %s", os.Getenv("AWS_PROFILE"), out.String())
	}

	// 非対話では聞かない
	t.Setenv("AWS_PROFILE", "")
	var out2 strings.Builder
	chooseProfile(false, strings.NewReader("1\n"), &out2)
	if out2.Len() != 0 || os.Getenv("AWS_PROFILE") != "" {
		t.Fatalf("non-interactive should not prompt: %q %s", os.Getenv("AWS_PROFILE"), out2.String())
	}
}
