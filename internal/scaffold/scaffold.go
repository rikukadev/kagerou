// Package scaffold は kagerou init の生成物(kagerou.yaml / workflows /
// テンプレート雛形)を書き出す。既存ファイルは既定で触らない。
package scaffold

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var tmplFS embed.FS

type Params struct {
	Project string
	Region  string
	Sashiki bool // sashiki 併用の hooks / DB env を含める
}

type Result struct {
	Created []string
	Skipped []string // 既存のため触らなかったもの
}

// Targets は何を生成するか(TUI のチェックがここに落ちる)。
type Targets struct {
	KagerouYaml bool
	Preview     bool
	Reap        bool
	Template    bool
}

// AllTargets は全部入り(非対話モードの既定)。
func AllTargets() Targets { return Targets{KagerouYaml: true, Preview: true, Reap: true, Template: true} }

// Run は dir に選択された生成物を書き出す。force は kagerou.yaml と workflows
// のみ上書きを許す。template.yaml はアプリの実体なので force でも上書きしない。
func Run(dir string, p Params, sel Targets, force bool) (Result, error) {
	var res Result
	files := []struct {
		enabled   bool
		path      string
		tmpl      string
		overwrite bool // force 時に上書きしてよいか
	}{
		{sel.KagerouYaml, "kagerou.yaml", "kagerou.yaml.tmpl", true},
		{sel.Preview, filepath.Join(".github", "workflows", "kagerou-preview.yml"), "preview.yml.tmpl", true},
		{sel.Reap, filepath.Join(".github", "workflows", "kagerou-reap.yml"), "reap.yml.tmpl", true},
		{sel.Template, "template.yaml", "template.yaml.tmpl", false},
	}
	for _, f := range files {
		if !f.enabled {
			continue
		}
		dst := filepath.Join(dir, f.path)
		if _, err := os.Stat(dst); err == nil {
			if !force || !f.overwrite {
				res.Skipped = append(res.Skipped, f.path)
				continue
			}
		}
		if err := renderTo(dst, f.tmpl, p); err != nil {
			return res, err
		}
		res.Created = append(res.Created, f.path)
	}
	return res, nil
}

func renderTo(dst, name string, p Params) error {
	t, err := template.ParseFS(tmplFS, "templates/"+name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := t.Execute(f, p); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", name, err)
	}
	return f.Close()
}

// SetupScriptName は AWS セットアップスクリプトの生成先。
const SetupScriptName = "kagerou-setup.sh"

// WriteSetupScript は AWS セットアップ(OIDC ロール / ECR / Variables)を
// 行うスクリプトを dir に書き出す。実行するかは呼び出し側の選択。
func WriteSetupScript(dir string, p Params, d Detection) (string, error) {
	data := struct {
		Owner, Repo, Region string
	}{d.Owner, d.Repo, or(p.Region, d.Region)}
	t, err := template.ParseFS(tmplFS, "templates/setup.sh.tmpl")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, SetupScriptName)
	if err := os.WriteFile(dst, []byte(b.String()), 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

// SetupMode は AWS セットアップ(role / ECR / variables)をどう扱ったか。
type SetupMode int

const (
	SetupSkip    SetupMode = iota // 手動(コマンドを列挙する)
	SetupScript                   // kagerou-setup.sh を書き出した
	SetupApplied                  // その場で適用済み
)

// Step は生成後に残る手作業の 1 項目(TUI チェックリストの行になる)。
type Step struct {
	Title  string
	Detail string // 補足(コマンド例など)。改行可
	Done   bool   // 検出により最初からチェック済み
}

// or は v が空のとき placeholder を返す(検出できた値を優先して埋める)。
func or(v, placeholder string) string {
	if v != "" {
		return v
	}
	return placeholder
}

// Steps は生成後に残る手作業のチェックリスト。検出できた値(アカウント ID・
// リポジトリ名・設定済み Variables)は実値で埋め、済みの項目は Done にする。
// mode により AWS セットアップ 3 手順は 1 行(スクリプト実行)や済みに畳まれる。
func Steps(p Params, d Detection, mode SetupMode) []Step {
	acct := or(d.AccountID, "<account>")
	repo := or(d.Repo, "<repo>")
	region := or(p.Region, or(d.Region, "<region>"))
	roleArn := "arn:aws:iam::" + acct + ":role/" + repo + "-github-actions"
	ecrURI := acct + ".dkr.ecr." + region + ".amazonaws.com/" + repo

	varsDone := d.VarsSet["AWS_ROLE_ARN"] && d.VarsSet["AWS_REGION"] && d.VarsSet["ECR_REPOSITORY"]

	oidcDetail := "note: newer orgs use ID-style sub claims (repo:org@ID/repo@ID:*)"
	if d.Owner != "" && d.Repo != "" {
		oidcDetail = "trust policy sub: repo:" + d.Owner + "/" + d.Repo + ":* (or the ID-style form)\n" + oidcDetail
	}

	steps := []Step{
		{
			Title:  "Write a Dockerfile and fill the TODOs in template.yaml",
			Detail: "run your app as an HTTP server and wrap it with Lambda Web Adapter",
			Done:   d.HasDockerfile && d.HasTemplate, // どちらも元からあるなら経験者
		},
	}
	switch mode {
	case SetupApplied:
		steps = append(steps, Step{
			Title: "AWS setup (role / ECR / variables)",
			Done:  true,
		})
	case SetupScript:
		steps = append(steps, Step{
			Title:  "Review and run ./" + SetupScriptName,
			Detail: "creates the OIDC role and ECR repository, and sets the 3 GitHub variables",
		})
	default: // SetupSkip: 手動でやる人向けにコマンドを列挙する
		steps = append(steps,
			Step{
				Title:  "Create a GitHub OIDC role (scoped to this repository)",
				Detail: oidcDetail,
				Done:   d.VarsSet["AWS_ROLE_ARN"],
			},
			Step{
				Title:  "Create an ECR repository (push target for sam package)",
				Detail: "aws ecr create-repository --repository-name " + repo,
				Done:   d.VarsSet["ECR_REPOSITORY"],
			},
			Step{
				Title: "Set the three GitHub Variables",
				Detail: `gh variable set AWS_ROLE_ARN   --body "` + roleArn + `"
gh variable set AWS_REGION     --body "` + region + `"
gh variable set ECR_REPOSITORY --body "` + ecrURI + `"`,
				Done: varsDone,
			},
		)
	}
	if p.Sashiki {
		steps = append(steps,
			Step{
				Title:  "Fill the DB_HOST / DB_PASSWORD / DB_NAME TODOs in kagerou.yaml",
				Detail: "match your sashiki proxy host and baseline credentials",
			},
			Step{
				Title:  "Make sure the runner can reach sashikid",
				Detail: "run the workflow where `sashiki create` works (self-hosted / inside the VPC)",
			},
		)
	}
	steps = append(steps, Step{
		Title:  "Open a PR and watch the environment appear",
		Detail: "it vanishes on close or when the TTL (72h) expires",
	})
	return steps
}

// PlainSteps は非 TTY(CI 等)向けのプレーンテキスト版チェックリスト。
func PlainSteps(p Params, d Detection) string {
	var b strings.Builder
	b.WriteString("\nNext steps (once per repository):\n\n")
	for i, s := range Steps(p, d, SetupSkip) {
		mark := " "
		if s.Done {
			mark = "x"
		}
		fmt.Fprintf(&b, "  [%s] %d. %s\n", mark, i+1, s.Title)
		for _, line := range strings.Split(s.Detail, "\n") {
			fmt.Fprintf(&b, "        %s\n", line)
		}
	}
	return b.String()
}
