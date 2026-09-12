package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rikukadev/kagerou/internal/config"
	"github.com/rikukadev/kagerou/internal/driver/stack"
	"github.com/rikukadev/kagerou/internal/hooks"
	"github.com/rikukadev/kagerou/internal/scaffold"
	"golang.org/x/term"
)

// kvFlag は --env / --param の KEY=VALUE 繰り返し指定を集める。
type kvFlag map[string]string

func (f kvFlag) String() string { return "" }

func (f kvFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("KEY=VALUE 形式で指定する: %q", s)
	}
	f[k] = v
	return nil
}

// mergeKV は base(kagerou.yaml 由来)に over(フラグ由来)を重ねる。
// 優先順位「フラグ > kagerou.yaml」(DESIGN.md §5.5)。
func mergeKV(base, over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

type upFlags struct {
	name, cfgPath, template, ttl, output, source string
	env, params                                  kvFlag
}

func parseUpFlags(cmd string, args []string) (upFlags, error) {
	f := upFlags{env: kvFlag{}, params: kvFlag{}}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.StringVar(&f.name, "name", "", "環境名(必須)")
	fs.StringVar(&f.cfgPath, "config", config.DefaultFile, "設定ファイル")
	fs.StringVar(&f.template, "template", "", "テンプレート(kagerou.yaml を上書き)")
	fs.StringVar(&f.ttl, "ttl", "", "寿命(例 72h / none。kagerou.yaml を上書き)")
	fs.StringVar(&f.output, "output", "text", "text | json")
	fs.StringVar(&f.source, "source", "", "環境の出自(opaque JSON。adapter が渡す)")
	fs.Var(f.env, "env", "アプリに届ける環境変数 KEY=VALUE(繰り返し可)")
	fs.Var(f.params, "param", "テンプレートパラメータ KEY=VALUE(繰り返し可)")
	err := fs.Parse(args)
	return f, err
}

// loadConfigFor は設定を読み、フラグを重ね、{name} 展開まで済ませて返す。
func loadConfigFor(f upFlags) (config.Config, error) {
	cfg, err := config.LoadOrDefault(f.cfgPath)
	if err != nil {
		return config.Config{}, err
	}
	if f.template != "" {
		cfg.Template = f.template
	}
	if f.ttl != "" {
		cfg.TTL = f.ttl
	}
	if err := config.ValidateName(f.name); err != nil {
		return config.Config{}, err
	}
	return cfg.ExpandName(f.name), nil
}

func newDriver(cfg config.Config) (*stack.Driver, context.Context, error) {
	ctx := context.Background()
	drv, err := stack.New(ctx, cfg.Region)
	return drv, ctx, err
}

func cmdUp(args []string, out *os.File) error {
	f, err := parseUpFlags("up", args)
	if err != nil {
		return err
	}
	cfg, err := loadConfigFor(f)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(cfg.Template)
	if err != nil {
		return fmt.Errorf("テンプレート: %w", err)
	}

	var expiresAt *time.Time
	d, hasTTL, err := config.ParseTTL(cfg.TTL)
	if err != nil {
		return err
	}
	if hasTTL {
		t := time.Now().Add(d)
		expiresAt = &t
	}

	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	// pre_up 失敗は up を止める(DESIGN §8)
	if err := hooks.Run(ctx, "pre_up", cfg.Hooks.PreUp); err != nil {
		return err
	}
	info, err := drv.Up(ctx, stack.UpInput{
		StackName:    cfg.StackName(f.name),
		Name:         f.name,
		Project:      cfg.Project,
		TemplateBody: string(body),
		Params:       f.params,
		Env:          mergeKV(cfg.Env, f.env),
		ExpiresAt:    expiresAt,
		Source:       f.source,
		Version:      version,
		Tags:         cfg.Tags,
	})
	if err != nil {
		return err
	}
	return printEnvironment(out, f.output, f.name, info)
}

func cmdDown(args []string, _ *os.File) error {
	f, err := parseUpFlags("down", args)
	if err != nil {
		return err
	}
	cfg, err := loadConfigFor(f)
	if err != nil {
		return err
	}
	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	if err := drv.Down(ctx, cfg.StackName(f.name)); err != nil {
		return err
	}
	// post_down 失敗は記録して続行(環境自体は消えている)
	if err := hooks.Run(ctx, "post_down", cfg.Hooks.PostDown); err != nil {
		fmt.Fprintf(os.Stderr, "kagerou: warning: %v\n", err)
	}
	return nil
}

func cmdURL(args []string, out *os.File) error {
	f, err := parseUpFlags("url", args)
	if err != nil {
		return err
	}
	cfg, err := loadConfigFor(f)
	if err != nil {
		return err
	}
	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	info, err := drv.Info(ctx, cfg.StackName(f.name))
	if err != nil {
		return err
	}
	if f.output == "json" {
		return printEnvironment(out, "json", f.name, info)
	}
	u, ok := stack.URL(info.Outputs)
	if !ok {
		return fmt.Errorf("URL の Output(KagerouUrl / PreviewUrl)がスタックに無い")
	}
	_, err = fmt.Fprintln(out, u)
	return err
}

func cmdInit(args []string, out *os.File) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	project := fs.String("project", "", "プロジェクト名(既定: カレントディレクトリ名)")
	region := fs.String("region", "ap-northeast-1", "AWS リージョン")
	sashiki := fs.Bool("sashiki", false, "sashiki 併用の hooks / DB env を含める")
	force := fs.Bool("force", false, "kagerou.yaml と workflows を上書きする(template.yaml は対象外)")
	dir := fs.String("dir", ".", "生成先ディレクトリ")
	plain := fs.Bool("plain", false, "チェックリストを TUI でなくテキストで出す")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		*project = strings.ToLower(filepath.Base(abs))
	}
	if err := config.ValidateName(*project); err != nil {
		return fmt.Errorf("プロジェクト名が命名規約に合わない(--project で指定する): %w", err)
	}

	p := scaffold.Params{Project: *project, Region: *region, Sashiki: *sashiki}
	res, err := scaffold.Run(*dir, p, *force)
	if err != nil {
		return err
	}
	for _, f := range res.Created {
		if _, err := fmt.Fprintf(out, "created\t%s\n", f); err != nil {
			return err
		}
	}
	for _, f := range res.Skipped {
		if _, err := fmt.Fprintf(out, "skipped\t%s\t(既存。--force でも template.yaml は上書きしない)\n", f); err != nil {
			return err
		}
	}
	// 残りの手作業チェックリスト: TTY なら TUI、そうでなければテキスト
	if !*plain && term.IsTerminal(int(out.Fd())) {
		return runChecklistTUI(p)
	}
	_, err = fmt.Fprint(out, scaffold.PlainSteps(p))
	return err
}

func cmdList(args []string, out *os.File) error {
	f, err := parseUpFlags("list", args)
	if err != nil {
		return err
	}
	// list は名前を取らないので loadConfigFor(名前検証つき)は通さない
	cfg, err := config.LoadOrDefault(f.cfgPath)
	if err != nil {
		return err
	}
	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	infos, err := drv.List(ctx)
	if err != nil {
		return err
	}
	if f.output == "json" {
		envs := make([]map[string]any, 0, len(infos))
		for _, info := range infos {
			envs = append(envs, environmentJSON(info.Tags[stack.TagName], info))
		}
		return json.NewEncoder(out).Encode(envs)
	}
	for _, info := range infos {
		u, _ := stack.URL(info.Outputs)
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n",
			info.Tags[stack.TagName], info.State(), info.Tags[stack.TagExpiresAt], u); err != nil {
			return err
		}
	}
	return nil
}

func cmdReap(args []string, out *os.File) error {
	fs := flag.NewFlagSet("reap", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "設定ファイル")
	dryRun := fs.Bool("dry-run", false, "削除せず対象を表示するだけ")
	grace := fs.Duration("grace", 0, "期限切れから削除までの猶予(例 1h)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}
	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	infos, err := drv.List(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, info := range infos {
		if !info.Expired(now, *grace) {
			continue
		}
		name := info.Tags[stack.TagName]
		if *dryRun {
			if _, err := fmt.Fprintf(out, "would reap\t%s\t(expired %s)\n", name, info.Tags[stack.TagExpiresAt]); err != nil {
				return err
			}
			continue
		}
		if err := drv.Down(ctx, info.StackName); err != nil {
			// 1 件の失敗で全体を止めない(残りは回収する)
			fmt.Fprintf(os.Stderr, "kagerou: reap %s: %v\n", name, err)
			continue
		}
		// reap でも post_down を呼ぶ(呼ばないと sashiki 側に孤児が残る経路になる)
		if hook := cfg.ExpandName(name).Hooks.PostDown; hook != "" {
			if err := hooks.Run(ctx, "post_down", hook); err != nil {
				fmt.Fprintf(os.Stderr, "kagerou: warning: %v\n", err)
			}
		}
		if _, err := fmt.Fprintf(out, "reaped\t%s\t(expired %s)\n", name, info.Tags[stack.TagExpiresAt]); err != nil {
			return err
		}
	}
	return nil
}

// environmentJSON は docs/CONTRACT.md §3 の Environment JSON を組む。
// フィールドは追加のみ可(削除・改名は互換性破壊)。
func environmentJSON(name string, info *stack.Info) map[string]any {
	env := map[string]any{
		"name":       name,
		"project":    info.Tags[stack.TagProject],
		"driver":     stack.DriverName,
		"state":      info.State(),
		"url":        nil,
		"created_at": info.CreationTime.UTC().Format(time.RFC3339),
		"expires_at": nil,
		"source":     nil,
		"stack": map[string]any{
			"name":   info.StackName,
			"status": info.Status,
		},
	}
	if u, ok := stack.URL(info.Outputs); ok {
		env["url"] = u
	}
	if v := info.Tags[stack.TagExpiresAt]; v != "" && v != stack.TTLNoneTagValue {
		env["expires_at"] = v
	}
	if v := info.Tags[stack.TagSource]; v != "" {
		env["source"] = v
	}
	return env
}

func printEnvironment(out *os.File, format, name string, info *stack.Info) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(environmentJSON(name, info))
	}
	u, _ := stack.URL(info.Outputs)
	_, err := fmt.Fprintf(out, "%s\t%s\t%s\n", name, info.State(), u)
	return err
}
