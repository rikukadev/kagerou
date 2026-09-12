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
	"github.com/rikukadev/kagerou/internal/validate"
	"golang.org/x/term"
)

// kvFlag は --env / --param の KEY=VALUE 繰り返し指定を集める。
type kvFlag map[string]string

func (f kvFlag) String() string { return "" }

func (f kvFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("expected KEY=VALUE: %q", s)
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
	fs.StringVar(&f.name, "name", "", "environment name (required)")
	fs.StringVar(&f.cfgPath, "config", config.DefaultFile, "config file")
	fs.StringVar(&f.template, "template", "", "template file (overrides kagerou.yaml)")
	fs.StringVar(&f.ttl, "ttl", "", "lifetime (e.g. 72h / none; overrides kagerou.yaml)")
	fs.StringVar(&f.output, "output", "text", "text | json")
	fs.StringVar(&f.source, "source", "", "where the environment came from (opaque string set by the adapter)")
	fs.Var(f.env, "env", "env var delivered to the app, KEY=VALUE (repeatable)")
	fs.Var(f.params, "param", "template parameter KEY=VALUE (repeatable)")
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
		return fmt.Errorf("template: %w", err)
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
		return fmt.Errorf("stack has no URL output (KagerouUrl / PreviewUrl)")
	}
	_, err = fmt.Fprintln(out, u)
	return err
}

func cmdInit(args []string, out *os.File) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	project := fs.String("project", "", "project name (default: current directory name)")
	region := fs.String("region", "ap-northeast-1", "AWS region")
	sashiki := fs.Bool("sashiki", false, "include sashiki hooks / DB env")
	force := fs.Bool("force", false, "overwrite kagerou.yaml and workflows (template.yaml is never overwritten)")
	dir := fs.String("dir", ".", "output directory")
	plain := fs.Bool("plain", false, "print plain text instead of the interactive wizard")
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
		return fmt.Errorf("project name does not fit the naming rule (set --project): %w", err)
	}

	det := scaffold.Detect(*dir)
	if *region == "ap-northeast-1" && det.Region != "" { // フラグ未指定なら検出値を使う
		*region = det.Region
	}
	p := scaffold.Params{Project: *project, Region: *region, Sashiki: *sashiki}

	// TTY なら「検出結果でプリチェックされた選択 TUI → 生成 → チェックリスト」
	if !*plain && term.IsTerminal(int(out.Fd())) {
		return runInitTUI(*dir, p, det, *force)
	}

	// 非対話: 全部入りで生成してテキストのチェックリスト
	if det.SuggestSashiki() && !p.Sashiki {
		fmt.Fprintf(os.Stderr, "kagerou: hint: detected %s — add --sashiki to include DB branch integration\n", det.DBDriver)
	}
	res, err := scaffold.Run(*dir, p, scaffold.AllTargets(), *force)
	if err != nil {
		return err
	}
	for _, f := range res.Created {
		if _, err := fmt.Fprintf(out, "created\t%s\n", f); err != nil {
			return err
		}
	}
	for _, f := range res.Skipped {
		if _, err := fmt.Fprintf(out, "skipped\t%s\t(already exists; template.yaml is never overwritten)\n", f); err != nil {
			return err
		}
	}
	_, err = fmt.Fprint(out, scaffold.PlainSteps(p, det))
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
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	dryRun := fs.Bool("dry-run", false, "show what would be reaped without deleting")
	grace := fs.Duration("grace", 0, "grace period after expiry (e.g. 1h)")
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

func cmdValidate(args []string, out *os.File) error {
	f, err := parseUpFlags("validate", args)
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(f.cfgPath) // driver / ttl / 未知キーはここで落ちる
	if err != nil {
		return err
	}
	if f.template != "" {
		cfg.Template = f.template
	}
	// name があれば {name} 展開後の env で検査(dev@{name} 等を実値に)
	name := f.name
	if name != "" {
		cfg = cfg.ExpandName(name)
	}
	for k, v := range f.env {
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		cfg.Env[k] = v
	}

	// packaged.yaml(ビルド後の成果物)が無ければ、書いている素の template.yaml を見る
	tpl := cfg.Template
	if _, err := os.Stat(tpl); err != nil {
		if _, err2 := os.Stat("template.yaml"); err2 == nil {
			fmt.Fprintf(os.Stderr, "kagerou: note: %s not found, validating template.yaml instead\n", tpl)
			tpl = "template.yaml"
		}
	}

	findings, err := validate.Run(cfg, tpl, name)
	if err != nil {
		return err
	}
	for _, fd := range findings {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", fd.Level, fd.Msg); err != nil {
			return err
		}
	}
	if validate.HasErrors(findings) {
		errs := 0
		for _, fd := range findings {
			if fd.Level == validate.Error {
				errs++
			}
		}
		return fmt.Errorf("validation failed: %d error(s)", errs)
	}
	if len(findings) == 0 {
		if _, err := fmt.Fprintln(out, "ok\ttemplate satisfies the contract"); err != nil {
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
