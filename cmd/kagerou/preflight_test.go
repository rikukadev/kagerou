package main

import (
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
