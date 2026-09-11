package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rikukadev/kagerou/internal/config"
	"github.com/rikukadev/kagerou/internal/driver/stack"
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
	if cfg.Hooks.PreUp != "" || cfg.Hooks.PostDown != "" {
		fmt.Fprintln(os.Stderr, "kagerou: warning: hooks は未実装(#7)。今は無視される")
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
	return drv.Down(ctx, cfg.StackName(f.name))
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
	if v := info.Tags[stack.TagSource]; v != "" && json.Valid([]byte(v)) {
		env["source"] = json.RawMessage(v)
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
