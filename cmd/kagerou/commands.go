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
	"github.com/rikukadev/kagerou/internal/iampolicy"
	"github.com/rikukadev/kagerou/internal/readiness"
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
	if err := hooks.Run(ctx, "pre_up", cfg.Hooks.PreUp, map[string]string{"KAGEROU_NAME": f.name}); err != nil {
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
		URL:          cfg.URLTemplate, // ExpandName 済み。空なら Output に任せる
		Source:       f.source,
		Version:      version,
		Tags:         cfg.Tags,
	})
	if err != nil {
		return err
	}
	// post_up: 環境作成後の仕上げ(config.json 生成・静的成果物の配置など)。
	// 失敗は up の失敗にする — 環境はあるが仕上がっていない状態を green にしない
	if err := hooks.Run(ctx, "post_up", cfg.Hooks.PostUp, hookEnvForUp(f.name, info)); err != nil {
		return err
	}
	// readiness: アプリが応答するまで ready にしない(#26)。post_up の後に見るのは
	// SPA の配置などが済んでから初めて 200 が返る構成があるため
	if cfg.ReadinessPath != "" {
		u, ok := info.EnvironmentURL()
		if !ok {
			return fmt.Errorf("readiness_path is set but the environment has no URL (set url_template or a KagerouUrl output)")
		}
		timeout := readiness.DefaultTimeout
		if cfg.ReadinessTimeout != "" {
			timeout, _ = time.ParseDuration(cfg.ReadinessTimeout) // 妥当性は config.Load 済み
		}
		if err := readiness.Wait(ctx, u, cfg.ReadinessPath, timeout); err != nil {
			// スタックは出来ている(down の対象ではある)ので failed にはせず、
			// ready でもない "starting" として情報を返してから失敗させる
			_ = printEnvironmentState(out, f.output, f.name, info, "starting")
			return fmt.Errorf("environment is up but %v", err)
		}
	}
	return printEnvironment(out, f.output, f.name, info)
}

// hookEnvForUp は post_up に渡す KAGEROU_* 環境変数(CONTRACT §7)。
func hookEnvForUp(name string, info *stack.Info) map[string]string {
	env := map[string]string{"KAGEROU_NAME": name}
	if u, ok := info.EnvironmentURL(); ok {
		env["KAGEROU_URL"] = u
	}
	for k, v := range info.Outputs {
		env["KAGEROU_OUTPUT_"+strings.ToUpper(k)] = v
	}
	if b, err := json.Marshal(environmentJSON(name, info)); err == nil {
		env["KAGEROU_ENVIRONMENT_JSON"] = string(b)
	}
	return env
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
	if err := hooks.Run(ctx, "post_down", cfg.Hooks.PostDown, map[string]string{"KAGEROU_NAME": f.name}); err != nil {
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
	u, ok := info.EnvironmentURL()
	if !ok {
		return fmt.Errorf("no environment URL (set url_template, or declare a KagerouUrl / PreviewUrl output)")
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
	p := scaffold.Params{Project: *project, Region: *region, Sashiki: *sashiki,
		Port: det.AppPort, HasDockerfile: det.HasDockerfile, Framework: det.Framework}

	// TTY なら「検出結果でプリチェックされた選択 TUI → 生成 → チェックリスト」
	if !*plain && term.IsTerminal(int(out.Fd())) {
		return runInitTUI(*dir, p, det, *force)
	}

	// 非対話: 全部入りで生成してテキストのチェックリスト
	if det.HasDockerfile && !det.HasLWA {
		fmt.Fprintln(os.Stderr, "kagerou: hint: your Dockerfile lacks Lambda Web Adapter — add this line to the final stage:\n  "+scaffold.LWALine)
	}
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

// filterByProject は kagerou:project タグで環境を絞る(#48: reap / list の分離境界)。
// allProjects なら素通し。そうでなければ project タグが cfg.Project に一致するものだけ。
// 別プロジェクト(別リポジトリ)の環境を list / reap が巻き込まないようにする。
func filterByProject(infos []*stack.Info, project string, allProjects bool) []*stack.Info {
	if allProjects {
		return infos
	}
	out := infos[:0:0]
	for _, info := range infos {
		if info.Tags[stack.TagProject] == project {
			out = append(out, info)
		}
	}
	return out
}

func cmdList(args []string, out *os.File) error {
	// list は名前/テンプレ等を取らないので専用の FlagSet(config/output/all-projects のみ)。
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	output := fs.String("output", "text", "text | json")
	allProjects := fs.Bool("all-projects", false, "list environments of all projects (default: this project only)")
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
	infos = filterByProject(infos, cfg.Project, *allProjects)
	f := upFlags{output: *output} // 以降の分岐が f.output を見るため
	if f.output == "json" {
		envs := make([]map[string]any, 0, len(infos))
		for _, info := range infos {
			envs = append(envs, environmentJSON(info.Tags[stack.TagName], info))
		}
		return json.NewEncoder(out).Encode(envs)
	}
	for _, info := range infos {
		u, _ := info.EnvironmentURL()
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
	allProjects := fs.Bool("all-projects", false, "reap across all projects (does NOT run post_down hooks)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}
	// project スコープの安全確認(#48): kagerou:project が分離境界。project を絞れない
	// まま全件 reap すると、同じアカウントの別リポジトリの環境まで消してしまう。
	if !*allProjects && cfg.Project == "" {
		return fmt.Errorf("reap: kagerou.yaml に project が無いため対象を絞れません。project を設定するか、明示的に --all-projects を付けてください(--all-projects は post_down を実行しません)")
	}
	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	infos, err := drv.List(ctx)
	if err != nil {
		return err
	}
	infos = filterByProject(infos, cfg.Project, *allProjects)
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
		// reap でも post_down を呼ぶ(呼ばないと sashiki 側に孤児が残る経路になる)。
		// ただし --all-projects のときは、対象がどのリポジトリの環境か決められず
		// この kagerou.yaml の post_down を他プロジェクトの環境名で実行してしまうため
		// 呼ばない(#48)。その分の孤児は各プロジェクトの reap が回収する。
		if hook := cfg.ExpandName(name).Hooks.PostDown; hook != "" && !*allProjects {
			if err := hooks.Run(ctx, "post_down", hook, map[string]string{"KAGEROU_NAME": name}); err != nil {
				fmt.Fprintf(os.Stderr, "kagerou: warning: %v\n", err)
			}
		}
		if _, err := fmt.Fprintf(out, "reaped\t%s\t(expired %s)\n", name, info.Tags[stack.TagExpiresAt]); err != nil {
			return err
		}
	}
	return nil
}

func cmdIamPolicy(args []string, out *os.File) error {
	fs := flag.NewFlagSet("iam-policy", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	doc := fs.String("doc", "policy", "which document to emit: policy | trust | boundary")
	prefix := fs.String("prefix", "", "ARN scope prefix (default: name_prefix in kagerou.yaml)")
	ecr := fs.Bool("with-ecr", false, "Lambda container image (SSR etc.): ECR auth + push")
	ecrRepo := fs.String("ecr-repo", "", "ECR repository name (default: project in kagerou.yaml)")
	s3 := fs.Bool("with-s3", false, "static website bucket (3-tier etc.)")
	vpc := fs.Bool("with-vpc", false, "Lambda inside a VPC (ENI management)")
	ssm := fs.Bool("with-sashiki-ssm", false, "sashiki action transport=ssm")
	instance := fs.String("instance-id", "", "target instance for --with-sashiki-ssm")
	cf := fs.Bool("with-cloudfront", false, "CloudFront cache invalidation")
	r53 := fs.Bool("with-route53", false, "Route53 record changes")
	zone := fs.String("hosted-zone-id", "", "hosted zone for --with-route53")
	// --doc trust / boundary 用
	repo := fs.String("repo", "", "owner/name for the OIDC trust policy (--doc trust)")
	account := fs.String("account", "", "AWS account ID for ARNs in --doc trust / boundary (default: placeholder)")
	branch := fs.String("branch", "", "default branch allowed to assume (--doc trust, default main)")
	regions := fs.String("regions", "", "comma-separated regions for --doc boundary (default: region in kagerou.yaml)")
	boundaryArn := fs.String("boundary-arn", "", "ARN of this boundary policy (--doc boundary, default derived from --account)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}

	var (
		b    []byte
		note string
	)
	switch *doc {
	case "policy":
		if *prefix == "" {
			*prefix = cfg.NamePrefix
		}
		if *ecrRepo == "" {
			*ecrRepo = cfg.Project
		}
		pol, err := iampolicy.Build(iampolicy.Options{
			Prefix: *prefix,
			ECR:    *ecr, EcrRepo: *ecrRepo,
			S3: *s3, VPC: *vpc,
			SashikiSSM: *ssm, InstanceID: *instance,
			CloudFront: *cf, Route53: *r53, HostedZoneID: *zone,
		})
		if err != nil {
			return err
		}
		if b, err = pol.JSON(); err != nil {
			return err
		}
		note = "apigateway:* is a documented compromise (cannot be scoped per stack); review before attaching"
	case "trust":
		tp, err := iampolicy.BuildTrust(iampolicy.TrustOptions{Repo: *repo, Account: *account, Branch: *branch})
		if err != nil {
			return err
		}
		if b, err = tp.JSON(); err != nil {
			return err
		}
		note = "attach as the deploy role's trust policy; the GitHub OIDC provider must already exist in the account"
		if *account == "" {
			note += "; replace " + iampolicy.AccountPlaceholder + " with your account ID"
		}
	case "boundary":
		if *prefix == "" {
			*prefix = cfg.NamePrefix
		}
		var regs []string
		src := *regions
		if src == "" {
			src = cfg.Region
		}
		for _, r := range strings.Split(src, ",") {
			if r = strings.TrimSpace(r); r != "" {
				regs = append(regs, r)
			}
		}
		bp, err := iampolicy.BuildBoundary(iampolicy.BoundaryOptions{
			Prefix: *prefix, Regions: regs, BoundaryArn: *boundaryArn, Account: *account,
		})
		if err != nil {
			return err
		}
		if b, err = bp.JSON(); err != nil {
			return err
		}
		note = "attach as a permissions boundary to BOTH the deploy role and the roles it creates; review before use"
		if *boundaryArn == "" && *account == "" {
			note += "; replace " + iampolicy.AccountPlaceholder + " in the boundary ARN"
		}
	default:
		return fmt.Errorf("unknown --doc %q (want policy | trust | boundary)", *doc)
	}

	fmt.Fprintln(os.Stderr, "kagerou: note: "+note)
	if _, err := out.Write(append(b, '\n')); err != nil {
		return err
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
	if u, ok := info.EnvironmentURL(); ok {
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
	return printEnvironmentState(out, format, name, info, "")
}

func printEnvironmentState(out *os.File, format, name string, info *stack.Info, stateOverride string) error {
	env := environmentJSON(name, info)
	if stateOverride != "" {
		env["state"] = stateOverride
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(env)
	}
	u, _ := info.EnvironmentURL()
	state, _ := env["state"].(string)
	_, err := fmt.Fprintf(out, "%s\t%s\t%s\n", name, state, u)
	return err
}
