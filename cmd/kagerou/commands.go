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
	staticdrv "github.com/rikukadev/kagerou/internal/driver/static"
	"github.com/rikukadev/kagerou/internal/hooks"
	"github.com/rikukadev/kagerou/internal/iampolicy"
	"github.com/rikukadev/kagerou/internal/preflight"
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
	if cfg.Driver == staticdrv.DriverName {
		return upStatic(out, f, cfg)
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
	var maxLife time.Duration
	if cfg.MaxLifetime != "" {
		maxLife, _ = time.ParseDuration(cfg.MaxLifetime) // 妥当性は config.Load 済み
	}

	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}
	// peer 連動(#99): 相手プロジェクトの同名 env を探し、居なければ fallback。
	// CREATE_FAILED(相手不在で 4 分半 rollback)を up 前の即決に変える。
	var peerEnv, peerURL string
	if cfg.Peer.Project != "" {
		infos, err := drv.List(ctx)
		if err != nil {
			return err
		}
		var found bool
		peerEnv, peerURL, found = stack.ResolvePeer(infos, cfg.Peer.Project, f.name, cfg.Peer.FallbackName())
		if found {
			fmt.Fprintf(os.Stderr, "kagerou: peer: %s/%s (%s)\n", cfg.Peer.Project, peerEnv, peerURL)
		} else {
			fmt.Fprintf(os.Stderr, "kagerou: warning: peer %s has neither %q nor %q ready — proceeding with %q and no URL (deploy the peer, or adjust peer.fallback)\n",
				cfg.Peer.Project, f.name, cfg.Peer.FallbackName(), peerEnv)
		}
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
		MaxLifetime:  maxLife,
		PeerEnv:      peerEnv,
		PeerURL:      peerURL,
	})
	if err != nil {
		return err
	}
	// post_up: 環境作成後の仕上げ(config.json 生成・静的成果物の配置など)。
	// 失敗は up の失敗にする — 環境はあるが仕上がっていない状態を green にしない
	if err := hooks.Run(ctx, "post_up", cfg.Hooks.PostUp, hookEnv(f.name, info)); err != nil {
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

// hookEnv は環境の情報を KAGEROU_* 環境変数にする(CONTRACT §7)。
// post_up と pre_down の両方で使う。どちらも「スタックが在る」時点なので
// Outputs を渡せる(post_down だけは渡せない — もう消えている)。
func hookEnv(name string, info *stack.Info) map[string]string {
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

// expiryFor は ttl から期限を出す(規則は cmdUp と同じ)。
func expiryFor(cfg config.Config) (*time.Time, error) {
	d, hasTTL, err := config.ParseTTL(cfg.TTL)
	if err != nil {
		return nil, err
	}
	if !hasTTL {
		return nil, nil
	}
	t := time.Now().Add(d)
	return &t, nil
}

// upStatic は driver: static の up。compute が無いので template も
// readiness も使わず、メタスタック + プレフィックス同期で環境を作る。
func upStatic(out *os.File, f upFlags, cfg config.Config) error {
	expiresAt, err := expiryFor(cfg)
	if err != nil {
		return err
	}
	ctx := context.Background()
	drv, err := staticdrv.New(ctx, cfg.Region)
	if err != nil {
		return err
	}
	if err := hooks.Run(ctx, "pre_up", cfg.Hooks.PreUp, map[string]string{"KAGEROU_NAME": f.name}); err != nil {
		return err
	}
	in := staticdrv.UpInput{
		UpInput: stack.UpInput{
			StackName: cfg.StackName(f.name),
			Name:      f.name,
			Project:   cfg.Project,
			ExpiresAt: expiresAt,
			URL:       cfg.URLTemplate,
			Source:    f.source,
			Version:   version,
			Tags:      cfg.Tags,
		},
		Bucket: cfg.Static.Bucket,
		Prefix: cfg.StaticPrefix(f.name), // 共有 base では <project>/<name>
		Dist:   cfg.Static.Dist,
	}
	// static は「同期 = 公開」なので、post_up(環境固有ファイルの生成)は
	// 同期より前に走らせないと反映されない。URL は url_template で作成前に
	// 確定しているので、フックに渡す情報は揃っている
	info, err := drv.UpMeta(ctx, in)
	if err != nil {
		return err
	}
	if err := hooks.Run(ctx, "post_up", cfg.Hooks.PostUp, hookEnv(f.name, info)); err != nil {
		return err
	}
	// prefix は UpMeta と同じもの(共有 base では <project>/<name>)
	if err := drv.Sync(ctx, in.Bucket, in.Prefix, in.Dist); err != nil {
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
	stackName := cfg.StackName(f.name)
	// pre_down は driver に依らず先に走らせる(環境がまだある状態で割り込む)
	if err := runPreDown(ctx, drv, cfg.Hooks.PreDown, f.name, stackName); err != nil {
		return err
	}
	if cfg.Driver == staticdrv.DriverName {
		sdrv, serr := staticdrv.New(ctx, cfg.Region)
		if serr != nil {
			return serr
		}
		// メタスタックとプレフィックス配下の両方を消す
		if serr := sdrv.Down(ctx, stackName, cfg.Static.Bucket, cfg.StaticPrefix(f.name)); serr != nil {
			return serr
		}
	} else if err := drv.Down(ctx, stackName); err != nil {
		return err
	}
	// post_down 失敗は記録して続行(環境自体は消えている)
	if err := hooks.Run(ctx, "post_down", cfg.Hooks.PostDown, map[string]string{"KAGEROU_NAME": f.name}); err != nil {
		fmt.Fprintf(os.Stderr, "kagerou: warning: %v\n", err)
	}
	return nil
}

// runPreDown は環境を消す前の後始末を実行する。
//
// post_down と違って **失敗したら削除に進まない**。pre_down が受け持つのは
// 「これをやらないと削除が失敗する」類の前処理(中身の入った S3 バケットを
// 空にする等)なので、失敗を無視して Down を呼んでも、より分かりにくい
// CFN のエラーになるだけになる。
//
// スタックがもう無ければ何もしない。down は冪等であることを求められており
// (close の再送や reap との競合で 2 回走る)、無い環境に対して hook を
// 走らせると「消えたはずのものを消す」処理が二重に動く。
func runPreDown(ctx context.Context, drv *stack.Driver, hook, name, stackName string) error {
	if hook == "" {
		return nil
	}
	info, err := drv.Info(ctx, stackName)
	if err != nil {
		return nil // 存在しない(= 既に消えている)。冪等なので黙って抜ける
	}
	// 作成に失敗して巻き戻ったスタックには Outputs が無く、リソースも既に
	// 消えている。pre_down は「これをやらないと削除が失敗する」前処理なので、
	// 対象が無い以上やることも無い。ここで実行すると Outputs 前提の hook が
	// 失敗し、「失敗したら down 中止」の仕様と噛み合って **kagerou からは
	// 二度と消せないスタック** になる(実 AWS E2E の CI で踏んだ)。
	if strings.HasPrefix(info.Status, "ROLLBACK_") {
		fmt.Fprintf(os.Stderr, "kagerou: %s is %s (resources already rolled back) — skipping pre_down\n", stackName, info.Status)
		return nil
	}
	return hooks.Run(ctx, "pre_down", hook, hookEnv(name, info))
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
	compute := fs.String("compute", "lambda", "how environments run: lambda (LWA, idle $0) | ecs (Fargate + shared ALB)")
	entrypoint := fs.String("entrypoint", "alb", "how environments are exposed: alb (shared ALB, custom domain) | apigateway (raw execute-api URL)")
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

	// プリフライト: 検出の前に資格情報を確認し、無ければログインに誘導
	interactive := !*plain && term.IsTerminal(int(out.Fd()))
	ensureAuth(interactive, os.Stdin, os.Stderr)
	// 複数アカウントを持っている人は、どこに作るかを先に選ぶ(以後の検出と
	// setup は選んだプロファイルで動く)
	chooseProfile(interactive, os.Stdin, os.Stderr)

	det := scaffold.Detect(*dir)
	if *region == "ap-northeast-1" && det.Region != "" { // フラグ未指定なら検出値を使う
		*region = det.Region
	}
	if *compute != "lambda" && *compute != "ecs" {
		return fmt.Errorf("--compute %q: want lambda or ecs", *compute)
	}
	if *entrypoint != "alb" && *entrypoint != "apigateway" {
		return fmt.Errorf("--entrypoint %q: want alb or apigateway", *entrypoint)
	}
	p := scaffold.Params{Project: *project, Region: *region, Sashiki: *sashiki,
		Port: det.AppPort, HasDockerfile: det.HasDockerfile, Framework: det.Framework,
		Wants: det.Wants, Driver: scaffold.DriverFor(det), Compute: *compute, Entrypoint: *entrypoint,
		URLShape: det.Facts.URLShape}
	// routing は preview base から配るときにだけ意味がある。compute が
	// ルーティングを持つ構成で渡すと、設定と実際がずれる。
	if p.Static() {
		p.Routing = scaffold.RoutingFor(det.Framework)
	}
	if b, ok := det.Base(p.Project); ok {
		p.BaseBucket = b.Bucket
	}

	// TTY なら「検出結果でプリチェックされた選択 TUI → 生成 → チェックリスト」
	if !*plain && term.IsTerminal(int(out.Fd())) {
		return runInitTUI(*dir, p, det, *force)
	}

	// 非対話でも「誰として・どのアカウントに作るか」と権限の過不足は出す
	// (TTY では AWS 確認画面に出る。ここはその代替)
	reportPermissions(context.Background(), p.Region, preflight.Plan{
		Role: true, ECR: !p.Static(), Base: p.SetupBase, StaticSync: p.Static(),
	}, os.Stderr)

	// 非対話: 全部入りで生成してテキストのチェックリスト
	if det.HasDockerfile && !det.HasLWA {
		fmt.Fprintln(os.Stderr, "kagerou: hint: your Dockerfile lacks Lambda Web Adapter — add this line to the final stage:\n  "+scaffold.LWALine)
	}
	if det.SuggestSashiki() && !p.Sashiki {
		fmt.Fprintf(os.Stderr, "kagerou: hint: detected %s — add --sashiki to include DB branch integration\n", det.DBDriver)
	}
	// URL 構成の検出(#109 v1)。生成への反映(自動配線 / パスルーティング)は
	// #99 の実機実験を見てから v2 で行う — いまは事実の提示に留める
	switch det.Facts.URLShape {
	case "cross":
		extra := ""
		if len(det.Facts.Hosts) > 0 {
			extra = " (" + strings.Join(det.Facts.Hosts, ", ") + ")"
		}
		fmt.Fprintf(os.Stderr, "kagerou: hint: cross-origin layout detected%s — declare `peer:` in kagerou.yaml to link environments by name (a commented block was generated; resolved values arrive as EnvPeerEnv / EnvPeerUrl)\n", extra)
	case "path":
		fmt.Fprintln(os.Stderr, "kagerou: hint: same-origin path routing detected (/api behind one host) — base path routing is planned in kagerou#109; until then the scaffold keeps a single origin")
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
	// 非対話では AWS に触らない(スクリプトを置くだけ)。それでも中身と費用は
	// 出す — 実行するかどうかを決めるのに必要な情報は、TUI かどうかで変わらない。
	if _, err := fmt.Fprintf(out, "\n%s\n", scaffold.BuildAWSPlan(p, det).Render()); err != nil {
		return err
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
		// reap でも pre_down を呼ぶ。呼ばないと「手で down すれば消えるのに
		// TTL 回収では消えない」環境が生まれる(中身の入ったバケット等)。
		// --all-projects で呼ばないのは post_down と同じ理由(#48)。
		if hook := cfg.ExpandName(name).Hooks.PreDown; hook != "" && !*allProjects {
			if err := hooks.Run(ctx, "pre_down", hook, hookEnv(name, info)); err != nil {
				// 前処理が失敗したものは消しにいかない。消せずに失敗するか、
				// 消せてしまって後始末だけ漏れるかのどちらかになる。
				fmt.Fprintf(os.Stderr, "kagerou: reap %s: pre_down: %v\n", name, err)
				continue
			}
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

// loadTemplateFacts はテンプレートを読んで TemplateFacts を返す(#71)。
// kagerou.yaml の template(既定 packaged.yaml は CI 生成物なので無いことがある)
// が読めなければ素の template.yaml に落ち、どちらも無ければ nil(フラグ挙動)。
func loadTemplateFacts(templatePath string) *iampolicy.TemplateFacts {
	for _, p := range []string{templatePath, "template.yaml"} {
		if p == "" {
			continue
		}
		body, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		facts, err := iampolicy.ScanTemplate(body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kagerou iam-policy: could not parse %s (%v) — falling back to flags\n", p, err)
			return nil
		}
		return &facts
	}
	return nil
}

// allowFlag は繰り返し可能な --allow 'actions=resources' を集める(--doc execution)。
type allowFlag []iampolicy.AllowRule

func (a *allowFlag) String() string { return "" }
func (a *allowFlag) Set(v string) error {
	i := strings.Index(v, "=")
	if i < 0 {
		return fmt.Errorf("--allow %q: want 'action1,action2=arn1,arn2'", v)
	}
	actions, resources := splitCSV(v[:i]), splitCSV(v[i+1:])
	if len(actions) == 0 || len(resources) == 0 {
		return fmt.Errorf("--allow %q: actions and resources are both required", v)
	}
	*a = append(*a, iampolicy.AllowRule{Actions: actions, Resources: resources})
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdIamPolicy(args []string, out *os.File) error {
	fs := flag.NewFlagSet("iam-policy", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	doc := fs.String("doc", "policy", "which document to emit: policy | trust | boundary | execution")
	prefix := fs.String("prefix", "", "ARN scope prefix (default: name_prefix in kagerou.yaml)")
	ecr := fs.Bool("with-ecr", false, "Lambda container image (SSR etc.): ECR auth + push")
	ecrRepo := fs.String("ecr-repo", "", "ECR repository name (default: project in kagerou.yaml)")
	s3 := fs.Bool("with-s3", false, "static website bucket (only without a readable template; the template is the source of truth)")
	vpc := fs.Bool("with-vpc", false, "Lambda inside a VPC (only without a readable template; also for --doc execution)")
	baseBucket := fs.String("base-bucket", "", "shared preview base bucket to sync artifacts into (post_up aws s3 sync)")
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
	// --doc execution 用 / drift 検出
	var allow allowFlag
	fs.Var(&allow, "allow", "declared access for --doc execution: 'action1,action2=arn1,arn2' (repeatable)")
	check := fs.String("check", "", "compare an attached policy JSON file against the generated one and report drift (exit non-zero on over-permission)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}
	prefixOrCfg := func() string {
		if *prefix != "" {
			return *prefix
		}
		return cfg.NamePrefix
	}

	// trust は Action 集合の形が違うので drift 検出の対象外。
	if *check != "" && *doc == "trust" {
		return fmt.Errorf("--check does not apply to --doc trust")
	}

	// 生成する Policy(policy / boundary / execution)。trust は別扱いで先に返す。
	var pol iampolicy.Policy
	var note string
	switch *doc {
	case "policy":
		ecrRepoName := *ecrRepo
		if ecrRepoName == "" {
			ecrRepoName = cfg.Project
		}
		// テンプレートが読めれば「テンプレートが作るもの」はそこから導出する(#71)。
		// packaged.yaml(CI 生成物)が無ければ素の template.yaml に落ちる。
		facts := loadTemplateFacts(cfg.Template)
		if facts != nil {
			for _, u := range facts.Unknown {
				fmt.Fprintf(os.Stderr, "kagerou iam-policy: no permission mapping for %s — the generated policy does NOT cover it; add statements by hand\n", u)
			}
			// テンプレートが真実の源: 導出と食い違うフラグは無視して、その旨を言う
			if *s3 && facts.Counts["AWS::S3::Bucket"] == 0 {
				fmt.Fprintln(os.Stderr, "kagerou iam-policy: --with-s3 ignored — the template declares no AWS::S3::Bucket (syncing to the shared preview base? use --base-bucket)")
			}
			if *vpc && !facts.HasVPC {
				fmt.Fprintln(os.Stderr, "kagerou iam-policy: --with-vpc ignored — no function in the template has VpcConfig")
			}
		} else {
			fmt.Fprintln(os.Stderr, "kagerou iam-policy: template not found — falling back to --with-* flags only (the template is normally the source of truth)")
		}
		pol, err = iampolicy.Build(iampolicy.Options{
			Prefix:   prefixOrCfg(),
			Template: facts,
			ECR:      *ecr, EcrRepo: ecrRepoName,
			S3: *s3, VPC: *vpc,
			SashikiSSM: *ssm, InstanceID: *instance,
			CloudFront: *cf, Route53: *r53, HostedZoneID: *zone,
			BaseBucket: *baseBucket,
		})
		note = "apigateway:* is a documented compromise (cannot be scoped per stack); review before attaching"
	case "boundary":
		var regs []string
		src := *regions
		if src == "" {
			src = cfg.Region
		}
		regs = splitCSV(src)
		pol, err = iampolicy.BuildBoundary(iampolicy.BoundaryOptions{
			Prefix: prefixOrCfg(), Regions: regs, BoundaryArn: *boundaryArn, Account: *account,
		})
		note = "attach as a permissions boundary to BOTH the deploy role and the roles it creates; review before use"
		if *boundaryArn == "" && *account == "" {
			note += "; replace " + iampolicy.AccountPlaceholder + " in the boundary ARN"
		}
	case "execution":
		pol, err = iampolicy.BuildExecution(iampolicy.ExecutionOptions{
			Prefix: prefixOrCfg(), VPC: *vpc, Allow: allow,
		})
		note = "attach as the Lambda execution (runtime) role; declare app ARNs with --allow"
	case "trust":
		tp, terr := iampolicy.BuildTrust(iampolicy.TrustOptions{Repo: *repo, Account: *account, Branch: *branch})
		if terr != nil {
			return terr
		}
		b, terr := tp.JSON()
		if terr != nil {
			return terr
		}
		note = "attach as the deploy role's trust policy; the GitHub OIDC provider must already exist in the account"
		if *account == "" {
			note += "; replace " + iampolicy.AccountPlaceholder + " with your account ID"
		}
		fmt.Fprintln(os.Stderr, "kagerou: note: "+note)
		_, err = out.Write(append(b, '\n'))
		return err
	default:
		return fmt.Errorf("unknown --doc %q (want policy | trust | boundary | execution)", *doc)
	}
	if err != nil {
		return err
	}

	// --check: 生成した最小ポリシーを基準に、実 attach ポリシーの差分を報告する(#53 ④)。
	if *check != "" {
		data, err := os.ReadFile(*check)
		if err != nil {
			return fmt.Errorf("--check: %w", err)
		}
		extra, missing, err := iampolicy.CheckDrift(pol, data)
		if err != nil {
			return err
		}
		if len(extra) == 0 && len(missing) == 0 {
			_, err := fmt.Fprintln(out, "no drift: attached policy matches the generated minimal actions")
			return err
		}
		for _, a := range missing {
			if _, err := fmt.Fprintf(out, "missing\t%s\t(generated policy needs it; attached may break)\n", a); err != nil {
				return err
			}
		}
		for _, a := range extra {
			if _, err := fmt.Fprintf(out, "extra\t%s\t(over-permission: not in the generated minimal set)\n", a); err != nil {
				return err
			}
		}
		if len(extra) > 0 {
			return fmt.Errorf("drift: %d action(s) beyond the generated minimal policy", len(extra))
		}
		return nil
	}

	b, err := pol.JSON()
	if err != nil {
		return err
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
