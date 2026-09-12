package main

// init のプリフライト: 検出と setup は aws / gh の資格情報に依存するので、
// 走り出す前に確認し、足りなければその場でログインに誘導する。
// gh auth login / aws sso login は自身が対話コマンドなので、
// bubbletea(ウィザード)が立ち上がる前の素の stdio で実行する。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type authStatus struct {
	AWS bool
	GH  bool
}

// preflightCheck はテストで差し替えるフック(exit code だけ見る)。
var preflightCheck = func(name string, args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// runInteractive はログインコマンドを端末に繋いだまま実行する。
var runInteractive = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func checkAuth() authStatus {
	return authStatus{
		AWS: preflightCheck("aws", "sts", "get-caller-identity"),
		GH:  preflightCheck("gh", "auth", "status"),
	}
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// ensureAuth は不足資格情報を案内する。interactive ならその場でログインを促す。
// 最終的に足りなくても止めない(検出と setup が縮退するだけ)が、状態は明示する。
func ensureAuth(interactive bool, in io.Reader, out io.Writer) authStatus {
	st := checkAuth()
	if st.AWS && st.GH {
		return st
	}

	fmt.Fprintln(out, "kagerou init uses your AWS and GitHub credentials for detection and setup:")
	fmt.Fprintf(out, "  aws  %s  (account detection, preview base, \"run it now\" setup)\n", mark(st.AWS))
	fmt.Fprintf(out, "  gh   %s  (repo detection, variables, OIDC role IDs)\n", mark(st.GH))

	if interactive {
		r := bufio.NewReader(in)
		if !st.GH {
			fmt.Fprint(out, "Run `gh auth login` now? [Y/n] ")
			if ans, _ := r.ReadString('\n'); strings.TrimSpace(strings.ToLower(ans)) != "n" {
				_ = runInteractive("gh", "auth", "login")
				st.GH = checkAuth().GH
			}
		}
		if !st.AWS {
			fmt.Fprint(out, "AWS login: [1] aws configure (access keys)  [2] aws sso login  [enter] continue without AWS: ")
			switch ans, _ := r.ReadString('\n'); strings.TrimSpace(ans) {
			case "1":
				_ = runInteractive("aws", "configure")
				st.AWS = checkAuth().AWS
			case "2":
				_ = runInteractive("aws", "sso", "login")
				st.AWS = checkAuth().AWS
			}
		}
	}

	if !st.AWS {
		fmt.Fprintln(out, "continuing without AWS — detection is limited and setup falls back to a script")
	}
	if !st.GH {
		fmt.Fprintln(out, "continuing without gh — repo detection and variable checks are skipped")
	}
	return st
}
