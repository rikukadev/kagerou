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
func Steps(p Params, d Detection) []Step {
	acct := or(d.AccountID, "<account>")
	repo := or(d.Repo, "<repo>")
	region := or(p.Region, or(d.Region, "<region>"))
	roleArn := "arn:aws:iam::" + acct + ":role/" + repo + "-github-actions"
	ecrURI := acct + ".dkr.ecr." + region + ".amazonaws.com/" + repo

	varsDone := d.VarsSet["AWS_ROLE_ARN"] && d.VarsSet["AWS_REGION"] && d.VarsSet["ECR_REPOSITORY"]

	oidcDetail := "新しめの org は sub クレームが repo:org@ID/repo@ID:* 形式な点に注意"
	if d.Owner != "" && d.Repo != "" {
		oidcDetail = "信頼ポリシーの sub: repo:" + d.Owner + "/" + d.Repo + ":*(または ID 入り形式)\n" + oidcDetail
	}

	steps := []Step{
		{
			Title:  "Dockerfile を用意し、template.yaml の TODO を埋める",
			Detail: "アプリを HTTP サーバーとして起動し、Lambda Web Adapter で包む",
			Done:   d.HasDockerfile && d.HasTemplate, // どちらも元からあるなら経験者
		},
		{
			Title:  "GitHub OIDC ロールを作る(このリポジトリ限定)",
			Detail: oidcDetail,
			Done:   d.VarsSet["AWS_ROLE_ARN"],
		},
		{
			Title:  "ECR リポジトリを作る(sam package の押し先)",
			Detail: "aws ecr create-repository --repository-name " + repo,
			Done:   d.VarsSet["ECR_REPOSITORY"],
		},
		{
			Title: "GitHub Variables を 3 つ設定する",
			Detail: `gh variable set AWS_ROLE_ARN   --body "` + roleArn + `"
gh variable set AWS_REGION     --body "` + region + `"
gh variable set ECR_REPOSITORY --body "` + ecrURI + `"`,
			Done: varsDone,
		},
	}
	if p.Sashiki {
		steps = append(steps,
			Step{
				Title:  "kagerou.yaml の DB_HOST / DB_PASSWORD / DB_NAME の TODO を埋める",
				Detail: "sashiki プロキシのホストと baseline の認証情報に合わせる",
			},
			Step{
				Title:  "runner から sashikid に届くことを確認する",
				Detail: "hooks の sashiki create が動く場所(self-hosted / VPC 内)で workflow を実行する",
			},
		)
	}
	steps = append(steps, Step{
		Title:  "PR を開いてプレビュー環境が生えるのを見届ける",
		Detail: "閉じるか TTL(72h)で環境は消える",
	})
	return steps
}

// PlainSteps は非 TTY(CI 等)向けのプレーンテキスト版チェックリスト。
func PlainSteps(p Params, d Detection) string {
	var b strings.Builder
	b.WriteString("\n次にやること(リポジトリごとに 1 回):\n\n")
	for i, s := range Steps(p, d) {
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
