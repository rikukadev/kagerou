package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rikukadev/kagerou/internal/basedomain"
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
	cfg = cfg.ExpandName(f.name)
	// {base_domain} は SSM の preview base 契約から引く(#136)。使っていなければ
	// 読みに行かない — 既存の構成に SSM の権限を要求しないため
	if cfg.UsesBaseDomain() {
		domain, key, err := basedomain.Resolve(context.Background(), cfg.Project, cfg.Region)
		if err != nil {
			return config.Config{}, err
		}
		fmt.Fprintf(os.Stderr, "kagerou: base domain %s (from %s)\n", domain, key)
		cfg = cfg.ExpandBaseDomain(domain)
	}
	return cfg, nil
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

// explicitFlags は **実際に指定された**フラグ名の集合。
//
// 既定値との一致で「未指定」を判定してはいけない。既定値と同じ値を明示した
// ときに検出値へ負ける(`-region ap-northeast-1` が効かなかった #161)。
// flag.FlagSet.Visit は指定されたものだけを回すので、この違いを拾える。
func explicitFlags(fs *flag.FlagSet) map[string]bool {
	out := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { out[f.Name] = true })
	return out
}

// resolveRegion は init のリージョンを決める。明示指定が最優先で、
// 無いときだけ検出値(samconfig.toml / AWS_REGION / ~/.aws/config)を使う。
func resolveRegion(explicit bool, flagValue, detected string) string {
	if !explicit && detected != "" {
		return detected
	}
	return flagValue
}

func cmdInit(args []string, out *os.File) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	project := fs.String("project", "", "project name (default: current directory name)")
	region := fs.String("region", "ap-northeast-1", "AWS region")
	sashiki := fs.Bool("sashiki", false, "include sashiki hooks / DB env")
	compute := fs.String("compute", "lambda", "how environments run: lambda (LWA, idle $0) | ecs (Fargate + shared ALB)")
	entrypoint := fs.String("entrypoint", "alb", "how environments are exposed: alb (shared ALB, custom domain) | apigateway (lambda: raw execute-api URL; ecs: HTTP API + VPC Link, no fixed cost)")
	auth := fs.Bool("auth", false, "put previews behind an OIDC login "+
		"(ALB: authenticate-oidc / static: CloudFront + Lambda@Edge)")
	authDomain := fs.String("auth-domain", "", "organisation domain allowed to sign in (e.g. example.com)")
	memory := fs.Int("memory", 0, "Lambda MemorySize in MB (default 512)")
	timeout := fs.Int("timeout", 0, "Lambda timeout in seconds (default: 60 on the shared ALB, 30 behind HTTP API)")
	authSecret := fs.String("auth-secret-arn", "", "Secrets Manager ARN holding client_id / client_secret")
	domain := fs.String("domain", "", "preview domain (e.g. myapp.example.com). Default: detected from your Route53 zone")
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

	explicit := explicitFlags(fs)

	det := scaffold.Detect(*dir)
	*region = resolveRegion(explicit["region"], *region, det.Region)
	if *compute != "lambda" && *compute != "ecs" {
		return fmt.Errorf("--compute %q: want lambda or ecs", *compute)
	}
	if *entrypoint != "alb" && *entrypoint != "apigateway" {
		return fmt.Errorf("--entrypoint %q: want alb or apigateway", *entrypoint)
	}
	p := scaffold.Params{Project: *project, Region: *region, Sashiki: *sashiki,
		Port: det.AppPort, HasDockerfile: det.HasDockerfile, Framework: det.Framework,
		DockerfileName: det.DockerfileName, DockerfileDir: det.DockerfileDir,
		// 検出したヘルスチェックのパス(#182)。渡さないと readiness_path が出ず、
		// LWA の readiness も /healthz 固定になる — アプリが別のパスを使っていると
		// **存在しないパスを叩き続ける**が、LWA は 5xx 未満を healthy 扱いにするので
		// 起動は止まらない。検査として黙って無意味になる
		HealthPath: det.HealthPath,
		Wants:      det.Wants, Driver: scaffold.DriverFor(det), Compute: *compute, Entrypoint: *entrypoint,
		URLShape: det.Facts.URLShape, Services: det.Facts.ServiceNames, Auth: *auth,
		AuthDomain: *authDomain, AuthSecretArn: *authSecret,
		Memory: *memory, Timeout: *timeout}
	// routing は preview base から配るときにだけ意味がある。compute が
	// ルーティングを持つ構成で渡すと、設定と実際がずれる。
	if p.Static() {
		p.Routing = scaffold.RoutingFor(det.Framework)
	}
	if b, ok := det.Base(p.Project); ok {
		p.BaseBucket = b.Bucket
	}
	// 独自ドメインが既定(DESIGN §13)。TTY では質問で決まるが、非対話でも
	// 決められるところまでは決める: --domain > 既存ベース > ゾーンが 1 つなら自動。
	// ゾーンが複数あるときだけ利用者に選んでもらう(生 URL にフォールバック)。
	if *plain || !term.IsTerminal(int(out.Fd())) {
		switch base, ok := det.Base(p.Project); {
		case *domain != "":
			p.Domain = *domain
		case ok:
			p.Domain, p.DomainFromSSM = base.Domain, base.DomainFromSSM
		case len(det.Zones) == 1:
			p.Domain = p.Project + "." + det.Zones[0]
		case len(det.Zones) > 1:
			fmt.Fprintf(os.Stderr, "kagerou: hint: %d hosted zones found — pass --domain <host> to serve previews on a custom domain (falling back to the raw AWS URL)\n", len(det.Zones))
		}
		// preview base(CloudFront)が要るのは static だけ。ALB 構成は
		// deploy/alb-base.yaml が入口になる(両方は出さない)
		p.SetupBase = p.Static() && p.Domain != ""
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
	// LWA が要るのは Lambda 形だけ。ecs は素のコンテナをそのまま動かすので、
	// ここで勧めると「要らないものを足せ」と言うことになる
	if det.HasDockerfile && !det.HasLWA && !p.ECS() && !p.Static() {
		fmt.Fprintln(os.Stderr, "kagerou: hint: your Dockerfile lacks Lambda Web Adapter — add this line to the final stage:\n  "+scaffold.LWALine)
	}
	// 入口の上限を超えた timeout は「効かない設定」になる。Lambda は動き続けるが
	// 応答は入口で切れるので、黙って書き出すと原因の分からない 504 になる
	if p.TimeoutExceedsEntrypoint() {
		fmt.Fprintf(os.Stderr, "kagerou: warning: --timeout %d exceeds the 30s response limit of the HTTP API entrypoint; "+
			"the function keeps running but the caller gets a 504. Use --entrypoint alb (custom domain) for longer responses\n", p.Timeout)
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
//
// 落ち先は **設定ファイルの隣**を先に見る(#144)。カレント直下だけを見ていると、
// 設定を別ディレクトリに置いた構成で素のテンプレートに辿り着けず、テンプレート
// 由来の導出がまるごと効かない = 権限の足りないポリシーを黙って出してしまう。
func loadTemplateFacts(templatePath, cfgPath string) *iampolicy.TemplateFacts {
	for _, p := range templateCandidates(templatePath, cfgPath) {
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

// templateCandidates は導出に使うテンプレートの探索順。
func templateCandidates(templatePath, cfgPath string) []string {
	var out []string
	if templatePath != "" {
		out = append(out, templatePath)
	}
	if d := filepath.Dir(cfgPath); d != "" && d != "." {
		out = append(out, filepath.Join(d, "template.yaml"))
	}
	return append(out, "template.yaml")
}

// allowFlag は繰り返し可能な --allow 'actions=resources' を集める(--doc execution)。
// stringsFlag は繰り返し指定できる文字列フラグ。
type stringsFlag []string

func (f *stringsFlag) String() string     { return strings.Join(*f, ",") }
func (f *stringsFlag) Set(v string) error { *f = append(*f, v); return nil }

// first は最初の値(未指定なら既定のファイル名)を返す。
func (f stringsFlag) first() string {
	if len(f) == 0 {
		return config.DefaultFile
	}
	return f[0]
}

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
	var extraTemplates stringsFlag
	var cfgPaths stringsFlag
	fs.Var(&cfgPaths, "config", "config file (repeatable; with --check the generated policies are unioned)")
	// ベースのテンプレート(deploy/alb-base.yaml 等)は kagerou.yaml から
	// 指されていないのに **CI がデプロイする**。config 経由では見えないので、
	// 直接渡せるようにする。渡さないとベースが作る SSM / ALB の権限が出ない
	fs.Var(&extraTemplates, "template", "extra CloudFormation template to cover, e.g. a base deployed by CI (repeatable)")
	doc := fs.String("doc", "policy", "which document to emit: policy | trust | boundary | execution")
	prefix := fs.String("prefix", "", "ARN scope prefix (default: name_prefix in kagerou.yaml)")
	ecr := fs.Bool("with-ecr", false, "Lambda container image (SSR etc.): ECR auth + push")
	ecrRepo := fs.String("ecr-repo", "", "ECR repository name (default: project in kagerou.yaml)")
	s3 := fs.Bool("with-s3", false, "static website bucket (only without a readable template; the template is the source of truth)")
	vpc := fs.Bool("with-vpc", false, "Lambda inside a VPC (only without a readable template; also for --doc execution)")
	baseBucket := fs.String("base-bucket", "", "shared preview base bucket to sync artifacts into (post_up aws s3 sync)")
	ssm := fs.Bool("with-sashiki-ssm", false, "sashiki action transport=ssm")
	instance := fs.String("instance-id", "", "target instance for --with-sashiki-ssm")
	instanceTag := fs.String("instance-tag", "", "scope --with-sashiki-ssm by instance tag 'Key=Value' instead of a fixed id")
	cf := fs.Bool("with-cloudfront", false, "CloudFront cache invalidation")
	r53 := fs.Bool("with-route53", false, "Route53 record changes")
	zone := fs.String("hosted-zone-id", "", "hosted zone for --with-route53")
	// --doc trust / boundary 用
	repo := fs.String("repo", "", "owner/name for the OIDC trust policy (--doc trust)")
	account := fs.String("account", "", "AWS account ID for ARNs in --doc trust / boundary (default: placeholder)")
	branch := fs.String("branch", "", "default branch allowed to assume (--doc trust, default main)")
	ownerID := fs.String("owner-id", "", "numeric owner id for the immutable-subject sub form (--doc trust; looked up with gh when omitted)")
	repoID := fs.String("repo-id", "", "numeric repo id for the immutable-subject sub form (--doc trust; looked up with gh when omitted)")
	regions := fs.String("regions", "", "comma-separated regions for --doc boundary (default: region in kagerou.yaml)")
	boundaryArn := fs.String("boundary-arn", "", "ARN of this boundary policy (--doc boundary, default derived from --account)")
	// --doc execution 用 / drift 検出
	var allow allowFlag
	fs.Var(&allow, "allow", "declared access for --doc execution: 'action1,action2=arn1,arn2' (repeatable)")
	check := fs.String("check", "", "compare an attached policy JSON file against the generated one and report drift (exit non-zero on over-permission)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(cfgPaths.first())
	if err != nil {
		return err
	}
	// 複数指定は「複数構成ぶんの最小ポリシーを足し合わせたものが、実際に attach
	// されているポリシーと一致するか」を見るための機能(#135)。
	//
	// 出力にも効かせる: **1 つのロールを複数構成が共有する**とき、アタッチする
	// ポリシーはその和集合そのもの。生成できないと結局手で書くことになり、
	// #135 が止めたかった「手で足して生成器が知らない」に戻る。
	if len(cfgPaths) > 1 && *doc != "policy" {
		return fmt.Errorf("--config can only be repeated with --doc policy")
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

	// buildPolicy は 1 つの config から最小ポリシーを作る。--check の和集合モードで
	// config ごとに呼ぶので、switch の外に出してある(#135)。
	buildPolicy := func(cfg config.Config, cfgPath string) (iampolicy.Policy, error) {
		pfx := *prefix
		if pfx == "" {
			pfx = cfg.NamePrefix
		}
		ecrRepoName := *ecrRepo
		if ecrRepoName == "" {
			ecrRepoName = cfg.Project
		}
		// テンプレートが読めれば「テンプレートが作るもの」はそこから導出する(#71)。
		// packaged.yaml(CI 生成物)が無ければ素の template.yaml に落ちる。
		facts := loadTemplateFacts(cfg.Template, cfgPath)
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
			// 「フラグに落ちた」ではなく「**このポリシーでは足りない**」と言う。
			// テンプレート由来の導出(S3 / DynamoDB / SQS / ECR / …)がまるごと
			// 効いていないので、アタッチしても 403 になるだけ(#144)
			tried := strings.Join(templateCandidates(cfg.Template, cfgPath), ", ")
			msg := fmt.Sprintf("no template found (looked for %s) — "+
				"the generated policy is derived from --with-* flags ONLY and is almost certainly incomplete; "+
				"point `template` in %s at the CloudFormation template", tried, cfgPath)
			if *check != "" {
				// drift 検出は「生成した最小集合」を基準に判定する。その基準が
				// 壊れていたら結果は無意味なので、報告ではなく失敗にする
				return iampolicy.Policy{}, fmt.Errorf("%s (refusing to --check against it)", msg)
			}
			fmt.Fprintf(os.Stderr, "kagerou iam-policy: warning: %s\n", msg)
		}
		return iampolicy.Build(iampolicy.Options{
			Prefix:   pfx,
			Template: facts,
			// テンプレートが Image なら ECR は要る。--with-ecr を付け忘れても
			// 足りないポリシーを出さない(テンプレートが真実の源、#71/#135)
			ECR: *ecr || (facts != nil && facts.HasImage), EcrRepo: ecrRepoName,
			S3: *s3, VPC: *vpc,
			SashikiSSM: *ssm, InstanceID: *instance, InstanceTag: *instanceTag,
			// {base_domain} を使う設定なら SSM の読み取り権限も要る。フラグにすると
			// 付け忘れて 403 になるので、設定から導く
			BaseDomain: cfg.UsesBaseDomain(),
			CloudFront: *cf, Route53: *r53, HostedZoneID: *zone,
			BaseBucket: *baseBucket,
		})
	}

	// 生成する Policy(policy / boundary / execution)。trust は別扱いで先に返す。
	var pol iampolicy.Policy
	var note string
	switch *doc {
	case "policy":
		pol, err = buildPolicy(cfg, cfgPaths.first())
		for _, tp := range extraTemplates {
			if err != nil {
				break
			}
			// テンプレートだけで prefix は同じ。config 側の設定は引き継ぐ
			bc := cfg
			bc.Template = tp
			var bp iampolicy.Policy
			bp, err = buildPolicy(bc, cfgPaths.first())
			if err == nil {
				pol = iampolicy.Union(pol, bp)
			}
		}
		if err == nil && len(cfgPaths) > 1 {
			// 1 ロールを複数構成が共有する場合、出力も判定も和集合で見る
			pols := []iampolicy.Policy{pol}
			for _, path := range cfgPaths[1:] {
				other, oerr := config.LoadOrDefault(path)
				if oerr != nil {
					return oerr
				}
				op, oerr := buildPolicy(other, path)
				if oerr != nil {
					return oerr
				}
				pols = append(pols, op)
			}
			pol = iampolicy.Union(pols...)
		}
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
		// immutable subject(新しい org の既定)ではトークンの sub に数値 id が入る。
		// 明示されなければ gh で引く — 手で調べさせると、忘れたときの症状が
		// 「Not authorized」だけで原因に辿り着けない(#118)
		oid, rid := *ownerID, *repoID
		if (oid == "" || rid == "") && *repo != "" {
			if o, r, ok := lookupRepoIDs(*repo); ok {
				if oid == "" {
					oid = o
				}
				if rid == "" {
					rid = r
				}
			} else {
				fmt.Fprintf(os.Stderr, "kagerou iam-policy: could not look up the numeric ids for %s with gh — "+
					"emitting the classic sub form only. If the repository has immutable subjects enabled "+
					"(the default for newer orgs), AssumeRoleWithWebIdentity will be denied; "+
					"pass --owner-id and --repo-id (gh api repos/%s --jq '.owner.id, .id')\n", *repo, *repo)
			}
		}
		tp, terr := iampolicy.BuildTrust(iampolicy.TrustOptions{
			Repo: *repo, Account: *account, Branch: *branch, OwnerID: oid, RepoID: rid,
		})
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
		// pol は和集合済み(複数 config のとき)
		gen := iampolicy.PolicyAllowActions(pol)
		extra, missing, err := iampolicy.CheckDriftActions(gen, data)
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
		if len(missing) > 0 {
			// 生成器が要求するのに attach に無い = デプロイが 403 で落ちる側。
			// over-permission より重いので、こちらも必ず失敗させる。
			return fmt.Errorf("drift: %d action(s) missing from the attached policy", len(missing))
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
	// 生成物が古くないか(#207)。契約検査の入口がここなので同じ場所で言う。
	findings = append(findings, validate.GeneratedStamps(validate.GeneratedPathsFor(tpl), version)...)
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

// lookupRepoIDs は gh で owner / repo の数値 id を引く。gh が無い・未ログイン・
// リポジトリが見えない、のいずれでも ok=false を返す(呼び出し側が警告する)。
var lookupRepoIDs = func(repo string) (ownerID, repoID string, ok bool) {
	out, err := exec.Command("gh", "api", "repos/"+repo, "--jq", "[.owner.id, .id] | @tsv").Output()
	if err != nil {
		return "", "", false
	}
	o, r, found := strings.Cut(strings.TrimSpace(string(out)), "\t")
	if !found || o == "" || r == "" {
		return "", "", false
	}
	return o, r, true
}
