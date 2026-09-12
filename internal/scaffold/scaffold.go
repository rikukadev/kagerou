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

// Run は dir に生成物を書き出す。force は kagerou.yaml と workflows のみ
// 上書きを許す。template.yaml はアプリの実体なので force でも上書きしない。
func Run(dir string, p Params, force bool) (Result, error) {
	var res Result
	files := []struct {
		path      string
		tmpl      string
		overwrite bool // force 時に上書きしてよいか
	}{
		{"kagerou.yaml", "kagerou.yaml.tmpl", true},
		{filepath.Join(".github", "workflows", "kagerou-preview.yml"), "preview.yml.tmpl", true},
		{filepath.Join(".github", "workflows", "kagerou-reap.yml"), "reap.yml.tmpl", true},
		{"template.yaml", "template.yaml.tmpl", false},
	}
	for _, f := range files {
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
}

// Steps は生成後に残る手作業のチェックリスト。
func Steps(p Params) []Step {
	steps := []Step{
		{
			Title:  "Dockerfile を用意し、template.yaml の TODO を埋める",
			Detail: "アプリを HTTP サーバーとして起動し、Lambda Web Adapter で包む",
		},
		{
			Title:  "GitHub OIDC ロールを作る(このリポジトリ限定)",
			Detail: "新しめの org は sub クレームが repo:org@ID/repo@ID:* 形式な点に注意",
		},
		{
			Title:  "ECR リポジトリを作る(sam package の押し先)",
			Detail: "aws ecr create-repository --repository-name <repo>",
		},
		{
			Title: "GitHub Variables を 3 つ設定する",
			Detail: `gh variable set AWS_ROLE_ARN   --body "arn:aws:iam::<account>:role/<role>"
gh variable set AWS_REGION     --body "` + p.Region + `"
gh variable set ECR_REPOSITORY --body "<account>.dkr.ecr.` + p.Region + `.amazonaws.com/<repo>"`,
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
func PlainSteps(p Params) string {
	var b strings.Builder
	b.WriteString("\n次にやること(リポジトリごとに 1 回):\n\n")
	for i, s := range Steps(p) {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, s.Title)
		for _, line := range strings.Split(s.Detail, "\n") {
			fmt.Fprintf(&b, "       %s\n", line)
		}
	}
	return b.String()
}
