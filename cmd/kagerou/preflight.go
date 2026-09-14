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
	"strconv"
	"strings"
	"time"

	"github.com/rikukadev/kagerou/internal/preflight"
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
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

	say("kagerou init uses your AWS and GitHub credentials for detection and setup:\n")
	say("  aws  %s  (account detection, preview base, \"run it now\" setup)\n", mark(st.AWS))
	say("  gh   %s  (repo detection, variables, OIDC role IDs)\n", mark(st.GH))

	if interactive {
		r := bufio.NewReader(in)
		if !st.GH {
			say("Run `gh auth login` now? [Y/n] ")
			if ans, _ := r.ReadString('\n'); strings.TrimSpace(strings.ToLower(ans)) != "n" {
				_ = runInteractive("gh", "auth", "login")
				st.GH = checkAuth().GH
			}
		}
		if !st.AWS {
			say("AWS login: [1] aws configure (access keys)  [2] aws sso login  [enter] continue without AWS: ")
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
		say("continuing without AWS — detection is limited and setup falls back to a script\n")
	}
	if !st.GH {
		say("continuing without gh — repo detection and variable checks are skipped\n")
	}
	return st
}

// chooseProfile は複数プロファイルがあるとき、どのアカウントで作業するかを選ばせる。
// 選択は AWS_PROFILE として以後のプロセス全体(検出・setup)に効く。
func chooseProfile(interactive bool, in io.Reader, out io.Writer) {
	if os.Getenv("AWS_PROFILE") != "" || os.Getenv("AWS_DEFAULT_PROFILE") != "" {
		return // 明示済みなら尊重する
	}
	profiles := preflight.Profiles()
	if len(profiles) < 2 || !interactive {
		return
	}
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	say("Multiple AWS profiles found. Which one should kagerou use?\n")
	for i, p := range profiles {
		region := p.Region
		if region == "" {
			region = "-"
		}
		say("  [%d] %s (%s)\n", i+1, p.Name, region)
	}
	say("choose [1-%d, enter = %s]: ", len(profiles), profiles[0].Name)
	ans, _ := bufio.NewReader(in).ReadString('\n')
	idx := 0
	if n := strings.TrimSpace(ans); n != "" {
		if v, err := strconv.Atoi(n); err == nil && v >= 1 && v <= len(profiles) {
			idx = v - 1
		}
	}
	_ = os.Setenv("AWS_PROFILE", profiles[idx].Name)
	say("using profile %s\n", profiles[idx].Name)
}

// reportPermissions は「誰として・どのアカウントに作るか」と、その資格情報で
// setup が通るかを表示する。判定できない環境(SimulatePrincipalPolicy が無い、
// SSO 等)は失敗にせず注記に留める。返り値は「明確に拒否された権限があるか」。
func reportPermissions(ctx context.Context, region string, plan preflight.Plan, out io.Writer) bool {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	rep, err := preflight.CheckPermissions(ctx, region, plan)
	if err != nil {
		say("aws identity unavailable: %v\n", err)
		return false
	}
	say("aws  %s\n", rep.Identity)
	if !rep.Simulated {
		if rep.Note != "" {
			say("     %s\n", rep.Note)
		}
		return false
	}
	denied := rep.Denied()
	if len(denied) == 0 {
		say("     permissions ok for the selected setup\n")
		return false
	}
	setup, teardown := rep.DeniedSetup(), rep.DeniedTeardown()
	if len(setup) > 0 {
		say("     missing permissions for the selected setup:\n")
		for _, c := range setup {
			say("       %-38s %s\n", c.Action, c.Why)
		}
	}
	if len(teardown) > 0 {
		// 作れるのに壊せない状態は、**作った後にしか表に出ない**。
		// down / reap が落ち続け、TTL が切れても環境が残って課金される。
		// 「足りない」で一括りにせず、何が起きるかまで書く(#158)
		if len(setup) == 0 {
			say("     the setup can be created but NOT removed with these credentials:\n")
		} else {
			say("     also missing the permissions to remove it again:\n")
		}
		for _, c := range teardown {
			say("       %-38s %s\n", c.Action, c.Why)
		}
		say("     (a preview environment you cannot tear down keeps costing money)\n")
	}
	return true
}
