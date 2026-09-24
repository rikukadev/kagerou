package main

// kagerou init のウィザード TUI。質問ごとに選択肢を ↑↓ で選んで enter で進む
// (sam init スタイル)。検出結果が既定の選択肢になる。最後にサマリを確認して
// enter で生成し、AWS セットアップは選択に応じてその場で適用 / スクリプト化 /
// 手動用のコマンド列挙になる。非 TTY では呼ばれない。

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rikukadev/kagerou/internal/preflight"
	"github.com/rikukadev/kagerou/internal/recommend"
	"github.com/rikukadev/kagerou/internal/scaffold"
)

var (
	tuiTitle  = lipgloss.NewStyle().Bold(true)
	tuiHint   = lipgloss.NewStyle().Faint(true)
	tuiFaint  = lipgloss.NewStyle().Faint(true)
	tuiCursor = lipgloss.NewStyle().Bold(true)
	tuiDetail = lipgloss.NewStyle().Faint(true).PaddingLeft(6)
	tuiAnswer = lipgloss.NewStyle().Bold(true)
	tuiErr    = lipgloss.NewStyle().Bold(true)
)

type option struct {
	label  string
	detail string
}

type question struct {
	key      string // "db" / "template" / "workflows" / "setup"
	title    string
	options  []option
	selected int
}

const (
	phaseAsk = iota
	phaseSummary
	// phaseConfirmAWS はファイル生成の**後**、AWS に触る直前に挟む関門。
	// ファイルは消せばよいが、AWS のリソースは課金されるし手で消すしかない。
	// 同じ enter で通してしまうと「気づいたら作られていた」になる。
	phaseConfirmAWS
	phaseApplying
	phaseResult
	phaseCanceled
)

const (
	setupRun = iota
	setupScript
	setupSkip
)

type setupDoneMsg struct {
	output string
	err    error
}

type initModel struct {
	phase int
	dir   string
	force bool

	det    scaffold.Detection
	params scaffold.Params

	questions []question
	qi        int

	result      scaffold.Result
	runErr      error
	plan        scaffold.AWSPlan
	perm        preflight.Report
	permDone    bool
	setupMode   scaffold.SetupMode
	setupOutput string
	setupErr    error
	steps       []scaffold.Step
}

func newInitModel(dir string, p scaffold.Params, det scaffold.Detection, force bool) initModel {
	var qs []question

	dbDefault := 1
	if p.Sashiki || det.SuggestSashiki() {
		dbDefault = 0
	}
	dbLabel := "sashiki — a DB branch per PR"
	if det.DBDriver != "" {
		dbLabel += " (detected " + det.DBDriver + ")"
	}
	qs = append(qs, question{
		key:   "db",
		title: "Which database?",
		options: []option{
			{dbLabel, "adds hooks (sashiki create/delete) and DB_USER: dev@{name}"},
			{"none — wire it later with --env", "shared RDS, bundled SQLite etc."},
		},
		selected: dbDefault,
	})

	// 環境の実行形。既定は推薦(internal/recommend)が決める。判定は Facts だけを
	// 見る純関数なので、ここは「既定に据えて理由を見せる」だけ(#107)。
	// 独自ドメインで配れるか(ゾーンがある = init が ALB 入口を既定にしうる)。
	// ALB の固定費は compute の値段と別勘定なので、推薦に渡して言い分けさせる(#172)
	rec := recommend.Entry(det.Facts, recommend.Options{
		ExistingALB:    det.HasSharedALB(),
		AllowFixedCost: det.HasSharedALB(), // 既にあるなら固定費は増えない
		CustomDomain:   len(det.Zones) > 0 || len(det.Bases) > 0,
	})
	// 推薦した入口をそのまま既定に据える。ecs は入口が 2 通りあるので別項目にする
	// (動くものは同じで、ALB の固定費を取るか 30 秒上限を取るかの違い)。
	computeDefault := 0
	switch rec.Default {
	case recommend.ALB:
		computeDefault = 1
	case recommend.APIGateway:
		computeDefault = 2
	}
	qs = append(qs, question{
		key:   "compute",
		title: "Compute?",
		options: []option{
			{"lambda", computeReason(rec, recommend.Lambda, "wrap the container with LWA; idle costs $0, TTL 72h")},
			{"ecs (Fargate + shared ALB)", computeReason(rec, recommend.ALB, "real long-running containers; billed while up, TTL 24h")},
			{"ecs (Fargate + shared VPC Link)", computeReason(rec, recommend.APIGateway, "same containers with no fixed monthly cost; requests capped at 30s")},
		},
		selected: computeDefault,
	})

	// プレビューを誰でも開けるか。ALB の authenticate-oidc は S3 を守れないので、
	// 共有ベースは CloudFront + Lambda@Edge の形(deploy/edge-base.yaml)になる。
	qs = append(qs, question{
		key:   "auth",
		title: "Who can open a preview?",
		options: []option{
			{"anyone with the URL", "no login; the URL is unguessable but public"},
			{"only signed-in users (OIDC)", "CloudFront + Lambda@Edge; you add the client secret to SSM afterwards"},
		},
		selected: 0,
	})

	if det.HasDockerfile && !det.HasLWA {
		detail := "one line: " + scaffold.LWALine[:60] + "… (no-op outside Lambda, safe to keep)"
		qs = append(qs, question{
			key:   "docker",
			title: "Your Dockerfile lacks Lambda Web Adapter. Add it?",
			options: []option{
				{"inject it (1 line, recommended)", detail},
				{"skip — I'll wire Lambda myself", "the template's Dockerfile TODO stays"},
			},
		})
	}

	if !det.HasTemplate {
		qs = append(qs, question{
			key:   "template",
			title: "SAM template?",
			options: []option{
				{"generate a starter", "LWA + Env<Key> + KagerouUrl; you write the Dockerfile"},
				{"write my own", "follow CONTRACT §4/§5"},
			},
		})
	}

	// base は per-app(URL は {name}.{project}.<zone>)。この project 用が既に
	// あれば質問ごと消える。旧アカウント単位 base も fallback で再利用する。
	if _, ok := det.Base(p.Project); !ok && len(det.Zones) >= 2 {
		var opts []option
		for i, z := range det.Zones {
			if i >= 4 { // 選択肢は 4 つまで(それ以上は --domain フラグで)
				break
			}
			opts = append(opts, option{p.Project + "." + z, "sets up this app's base once (us-east-1, ~15 min)"})
		}
		qs = append(qs, question{
			key:     "domain",
			title:   "Preview domain?",
			options: opts,
		})
	}

	qs = append(qs, question{
		key:   "workflows",
		title: "Workflows?",
		options: []option{
			{"preview + reap (recommended)", "PR-driven environments + TTL sweep every 6h"},
			{"preview only", "reap manually or add reap.yml later"},
			{"none", "drive everything from the CLI"},
		},
	})

	setupDefault := setupScript
	if det.AccountID != "" && det.Owner != "" {
		setupDefault = setupRun // aws も gh も生きているならその場適用を既定に
	}
	qs = append(qs, question{
		key:   "setup",
		title: "AWS setup (role / ECR / variables)?",
		options: []option{
			{"run it now", "uses your aws + gh credentials; the role gets AdministratorAccess (scope down later)"},
			{"save a script (" + scaffold.SetupScriptName + ")", "review it, then run it yourself"},
			{"skip", "the next steps will list the commands instead"},
		},
		selected: setupDefault,
	})

	return initModel{dir: dir, force: force, det: det, params: p, questions: qs}
}

func (m initModel) Init() tea.Cmd { return nil }

func (m initModel) answer(key string) int {
	for _, q := range m.questions {
		if q.key == key {
			return q.selected
		}
	}
	return -1 // 質問自体が無い(template 既存など)
}

func (m *initModel) buildPlan() (scaffold.Targets, scaffold.Params) {
	p := m.params
	p.Sashiki = m.answer("db") == 0
	// routing は preview base に焼き込まれる。後から変えるにはベースの
	// deploy し直しが要るので、確認画面(phaseConfirmAWS)にも出す。
	// driver を先に決める。既存 kagerou.yaml があればそれが最優先なので、
	// init を二度叩いて構成が入れ替わることはない(#81)。
	p.Driver = scaffold.DriverFor(m.det)
	p.Auth = m.answer("auth") == 1
	// 入口は独自ドメイン(共有 ALB)が既定。ドメインが取れないときだけ
	// 生の execute-api URL に落ちる(Params.ALB が Domain も見て判断する)。
	p.Entrypoint = "alb"
	switch m.answer("compute") {
	case 1:
		p.Compute = "ecs"
	case 2:
		// 同じ Fargate を、固定費のある ALB ではなく HTTP API + VPC Link で公開する
		p.Compute, p.Entrypoint = "ecs", "apigateway"
	default:
		p.Compute = "lambda"
	}
	p.Services = m.det.Facts.ServiceNames
	// サービスごとの Dockerfile(#184)。無ければ 1 イメージ複数バイナリとして扱う
	p.ServiceFacts = m.det.Facts.ServiceFacts
	// routing は preview base から配るときにだけ意味がある(#86)。
	// compute がルーティングを持つ構成で渡すと、設定と実際がずれる。
	if p.Static() {
		p.Routing = scaffold.RoutingFor(m.det.Framework)
	}
	if b, ok := m.det.Base(p.Project); ok {
		p.BaseBucket = b.Bucket
	}
	p.Port = m.det.AppPort
	p.HealthPath = m.det.HealthPath // #182: 非対話側と同じ値を使う
	p.HasDockerfile = m.det.HasDockerfile || m.answer("docker") == 0
	// 検出したファイル名/場所をそのまま生成物に流す。固定にすると LWA を
	// Dockerfile.lambda に分けている構成で、LWA 無しのイメージが載る(#160)
	p.DockerfileName, p.DockerfileDir = m.det.DockerfileName, m.det.DockerfileDir
	switch base, ok := m.det.Base(p.Project); {
	case ok: // この project(または旧アカウント単位)の base をそのまま使う
		p.Domain, p.DomainFromSSM = base.Domain, base.DomainFromSSM
	case len(m.det.Zones) == 1: // ゾーンが 1 つなら自動選択
		p.Domain, p.SetupBase = p.Project+"."+m.det.Zones[0], true
	case m.answer("domain") >= 0:
		p.Domain, p.SetupBase = p.Project+"."+m.det.Zones[m.answer("domain")], true
	} // ゾーン検出なしなら Domain 空 = 生 AWS URL 運用のまま
	sel := scaffold.Targets{KagerouYaml: true}
	sel.Template = m.answer("template") == 0 // 質問なし(-1)= 既存なので生成しない
	sel.Dockerfile = !p.HasDockerfile        // 持っていない人にだけ雛形を出す(#61。既存は上書きしない)
	switch m.answer("workflows") {
	case 0:
		sel.Preview, sel.Reap = true, true
	case 1:
		sel.Preview = true
	}
	return sel, p
}

type permMsg struct{ rep preflight.Report }

// checkPerms は「この構成で setup が通るか」を確認する tea.Cmd。
// 判定できない環境もあるので、結果は参考情報として confirm 画面に出す。
func checkPerms(region string, p preflight.Plan) tea.Cmd {
	return func() tea.Msg {
		rep, err := preflight.CheckPermissions(context.Background(), region, p)
		if err != nil {
			rep.Note = "identity unavailable: " + err.Error()
		}
		return permMsg{rep: rep}
	}
}

// applySetup はセットアップスクリプトを実行する tea.Cmd。
func applySetup(dir string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("sh", "./"+scaffold.SetupScriptName)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return setupDoneMsg{output: strings.TrimSpace(string(out)), err: err}
	}
}

func (m initModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if pm, ok := msg.(permMsg); ok {
		m.perm, m.permDone = pm.rep, true
		return m, nil
	}
	if done, ok := msg.(setupDoneMsg); ok {
		m.setupOutput, m.setupErr = done.output, done.err
		if done.err != nil {
			// 失敗してもスクリプトは残っているので、直して再実行できる
			m.setupMode = scaffold.SetupScript
		} else {
			m.setupMode = scaffold.SetupApplied
		}
		m.steps = scaffold.Steps(m.params, m.det, m.setupMode, m.result.Created)
		m.phase = phaseResult
		return m, nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if s := k.String(); s == "q" || s == "esc" || s == "ctrl+c" {
		if m.phase == phaseResult {
			return m, tea.Quit
		}
		if m.phase == phaseApplying {
			return m, nil // 適用中は完了を待つ
		}
		m.phase = phaseCanceled
		return m, tea.Quit
	}
	switch m.phase {
	case phaseAsk:
		q := &m.questions[m.qi]
		switch k.String() {
		case "up", "k":
			if q.selected > 0 {
				q.selected--
			}
		case "down", "j":
			if q.selected < len(q.options)-1 {
				q.selected++
			}
		case "left", "h":
			if m.qi > 0 {
				m.qi--
			}
		case "enter", " ":
			if m.qi < len(m.questions)-1 {
				m.qi++
			} else {
				m.phase = phaseSummary
			}
		}
	case phaseSummary:
		switch k.String() {
		case "left", "h":
			m.phase = phaseAsk
		case "enter":
			sel, p := m.buildPlan()
			m.params = p
			m.result, m.runErr = scaffold.Run(m.dir, p, sel, m.force)
			if m.runErr != nil {
				m.phase = phaseResult
				return m, tea.Quit
			}
			// LWA が要るのは Lambda 形だけ。ecs は素のコンテナをそのまま
			// 動かすので、注入しても意味のない 1 行が残るだけになる
			if m.answer("docker") == 0 && !p.ECS() && !p.Static() {
				if changed, err := scaffold.InjectLWA(filepath.Join(m.dir, m.det.DockerfileDir), m.det.DockerfileName); err != nil {
					m.runErr = err
					m.phase = phaseResult
					return m, tea.Quit
				} else if changed {
					m.det.HasLWA = true
					m.result.Created = append(m.result.Created, "Dockerfile (Lambda Web Adapter injected)")
				}
			}
			switch m.answer("setup") {
			case setupRun:
				if _, err := scaffold.WriteSetupScript(m.dir, p, m.det); err != nil {
					m.setupErr, m.setupMode = err, scaffold.SetupSkip
					m.steps = scaffold.Steps(p, m.det, m.setupMode, m.result.Created)
					m.phase = phaseResult
					return m, nil
				}
				// ファイルはここまでで出来ている。AWS に触るのはこの先なので、
				// 何が作られて幾らかかるかを見せてから y/n を取る。
				m.plan = scaffold.BuildAWSPlan(p, m.det)
				m.phase = phaseConfirmAWS
				return m, checkPerms(p.Region, preflight.Plan{
					Role: true, ECR: !p.Static(), Base: p.SetupBase, StaticSync: p.Static(),
				})
			case setupScript:
				if _, err := scaffold.WriteSetupScript(m.dir, p, m.det); err != nil {
					m.setupErr, m.setupMode = err, scaffold.SetupSkip
				} else {
					m.setupMode = scaffold.SetupScript
					m.result.Created = append(m.result.Created, scaffold.SetupScriptName)
				}
			default:
				m.setupMode = scaffold.SetupSkip
			}
			m.steps = scaffold.Steps(p, m.det, m.setupMode, m.result.Created)
			m.phase = phaseResult
		}
	case phaseConfirmAWS:
		switch k.String() {
		case "y", "Y":
			m.phase = phaseApplying
			return m, applySetup(m.dir)
		case "n", "N", "enter":
			// 断ってもスクリプトは残す。中身を読んでから自分で流せる。
			// ここで消すと「確認したせいで選択肢が減る」ことになる。
			m.setupMode = scaffold.SetupScript
			m.result.Created = append(m.result.Created, scaffold.SetupScriptName)
			m.steps = scaffold.Steps(m.params, m.det, m.setupMode, m.result.Created)
			m.phase = phaseResult
		}
	case phaseResult:
		if k.String() == "enter" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m initModel) View() string {
	var b strings.Builder
	header := tuiTitle.Render("kagerou init") + "  " + tuiHint.Render(m.detectionSummary()) + "\n\n"
	switch m.phase {
	case phaseAsk:
		b.WriteString(header)
		for i := 0; i < m.qi; i++ {
			q := m.questions[i]
			b.WriteString(tuiFaint.Render("✓ "+q.title) + " " + tuiAnswer.Render(q.options[q.selected].label) + "\n")
		}
		q := m.questions[m.qi]
		b.WriteString(tuiTitle.Render(fmt.Sprintf("[%d/%d] %s", m.qi+1, len(m.questions), q.title)) + "\n")
		for i, o := range q.options {
			cursor := "   "
			if i == q.selected {
				cursor = tuiCursor.Render(" ❯ ")
			}
			b.WriteString(cursor + o.label + "\n")
			if i == q.selected && o.detail != "" {
				b.WriteString(tuiDetail.Render(o.detail) + "\n")
			}
		}
		b.WriteString("\n" + tuiHint.Render("↑↓ select · enter next · ← back · q cancel"))
	case phaseSummary:
		b.WriteString(header)
		sel, p := m.buildPlan()
		b.WriteString(tuiTitle.Render("About to generate") + "\n")
		fmt.Fprintf(&b, "  project: %s / region: %s / ttl: 72h\n", p.Project, p.Region)
		files := []string{"kagerou.yaml"}
		if sel.Template {
			files = append(files, "template.yaml (starter)")
		}
		if sel.Preview {
			files = append(files, ".github/workflows/kagerou-preview.yml")
		}
		if sel.Reap {
			files = append(files, ".github/workflows/kagerou-reap.yml")
		}
		for _, f := range files {
			b.WriteString("  + " + f + "\n")
		}
		if p.Sashiki {
			b.WriteString("  + sashiki integration (hooks / DB env)\n")
		}
		if m.answer("docker") == 0 {
			b.WriteString("  + Dockerfile: inject Lambda Web Adapter (1 line)\n")
		}
		if p.Auth {
			// 認証ありの配信ベースは edge base。しかもセットアップは deploy しない
			// (sam と、人が置く client_secret が要る)ので、そう書く(#194)
			b.WriteString("  + edge base (auth): deploy/edge-base.yaml — deploy は手元で 1 回\n")
		} else if p.SetupBase {
			b.WriteString("  + preview base (one-time, us-east-1): https://{name}." + p.Domain + "\n")
		} else if p.Domain != "" {
			b.WriteString("  + preview base: reuse " + p.Domain + "\n")
		}
		switch m.answer("setup") {
		case setupRun:
			b.WriteString(tuiFaint.Render("  then: AWS setup — shown with costs before anything is created") + "\n")
		case setupScript:
			b.WriteString("  + AWS setup: " + scaffold.SetupScriptName + "\n")
		}
		b.WriteString("\n" + tuiHint.Render("enter generate files · ← back · q cancel"))
	case phaseConfirmAWS:
		b.WriteString(header)
		b.WriteString(tuiTitle.Render("Files are written. Next: AWS") + "\n\n")
		b.WriteString(m.plan.Render())
		b.WriteString("\n" + m.renderPermissions())
		b.WriteString("\n" + tuiTitle.Render("Create these in AWS? [y/N]") + "\n")
		b.WriteString(tuiHint.Render("y create now · n / enter save " + scaffold.SetupScriptName + " and stop · q cancel"))
	case phaseApplying:
		b.WriteString(header)
		b.WriteString("Applying AWS setup (role / ECR / variables)…\n")
		b.WriteString(tuiHint.Render("this takes a few seconds"))
	case phaseResult:
		if m.runErr != nil {
			return "kagerou init: " + m.runErr.Error() + "\n"
		}
		b.WriteString(header)
		b.WriteString(tuiTitle.Render("Generated") + "\n")
		for _, f := range m.result.Created {
			b.WriteString("  created  " + f + "\n")
		}
		for _, f := range m.result.Skipped {
			b.WriteString(tuiFaint.Render("  skipped  "+f+" (already exists)") + "\n")
		}
		if m.setupErr != nil {
			b.WriteString("\n" + tuiErr.Render("AWS setup failed:") + " " + m.setupErr.Error() + "\n")
			if m.setupOutput != "" {
				b.WriteString(tuiDetail.Render(lastLines(m.setupOutput, 5)) + "\n")
			}
			b.WriteString(tuiHint.Render("fix it, then rerun ./"+scaffold.SetupScriptName) + "\n")
		} else if m.setupMode == scaffold.SetupApplied && m.setupOutput != "" {
			b.WriteString("\n" + tuiDetail.Render(lastLines(m.setupOutput, 4)) + "\n")
		}
		b.WriteString("\n" + tuiTitle.Render("Next steps (✓ = done)") + "\n")
		for _, s := range m.steps {
			mark := "・"
			title := s.Title
			if s.Done {
				mark = "✓ "
				title = tuiFaint.Render(title)
			}
			b.WriteString("  " + mark + title + "\n")
			if !s.Done && s.Detail != "" {
				for _, line := range strings.Split(s.Detail, "\n") {
					b.WriteString(tuiDetail.Render(line) + "\n")
				}
			}
		}
		b.WriteString("\n" + tuiHint.Render("enter / q to exit"))
	case phaseCanceled:
		b.WriteString("Canceled (nothing was generated)\n")
	}
	return b.String()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (m initModel) detectionSummary() string {
	parts := []string{}
	if m.det.Owner != "" {
		parts = append(parts, m.det.Owner+"/"+m.det.Repo)
	}
	if m.det.Framework != "" {
		parts = append(parts, m.det.Framework)
	}
	if m.det.DBDriver != "" {
		parts = append(parts, "db:"+m.det.DBDriver)
	}
	if m.det.Facts.URLShape != "" {
		parts = append(parts, "url:"+m.det.Facts.URLShape)
	}
	if m.det.AccountID != "" {
		parts = append(parts, "aws:"+m.det.AccountID)
	}
	if b, ok := m.det.Base(m.params.Project); ok {
		parts = append(parts, "base:"+b.Domain)
	}
	if len(parts) == 0 {
		return "(nothing detected)"
	}
	return strings.Join(parts, " · ")
}

func runInitTUI(dir string, p scaffold.Params, det scaffold.Detection, force bool) error {
	final, err := tea.NewProgram(newInitModel(dir, p, det, force)).Run()
	if err != nil {
		return err
	}
	if m, ok := final.(initModel); ok && m.runErr != nil {
		return m.runErr
	}
	return nil
}

// renderPermissions は「誰として・どのアカウントに作るか」と、その資格情報で
// 足りない権限を出す。判定不能は失敗にしない(注記のみ)。
func (m initModel) renderPermissions() string {
	if !m.permDone {
		return tuiFaint.Render("checking identity and permissions…") + "\n"
	}
	var b strings.Builder
	if id := m.perm.Identity.String(); id != "" {
		b.WriteString("as " + tuiAnswer.Render(id) + "\n")
	}
	switch {
	case !m.perm.Simulated:
		if m.perm.Note != "" {
			b.WriteString(tuiFaint.Render(m.perm.Note) + "\n")
		}
	case len(m.perm.Denied()) == 0:
		b.WriteString(tuiFaint.Render("permissions ok for this plan") + "\n")
	default:
		b.WriteString(tuiErr.Render("missing permissions:") + "\n")
		for _, c := range m.perm.Denied() {
			b.WriteString(tuiDetail.Render(fmt.Sprintf("%-38s %s", c.Action, c.Why)) + "\n")
		}
		b.WriteString(tuiFaint.Render("n で "+scaffold.SetupScriptName+" を残して、権限のある人に渡せます") + "\n")
	}
	return b.String()
}

// computeReason は推薦の理由を選択肢の説明に載せる。該当する理由が無ければ
// 元の説明のままにする(推薦は既定を決めるだけで、選択は塞がない)。
func computeReason(rec recommend.Choice, e recommend.Entrypoint, fallback string) string {
	if r := rec.Reason(e); r != "" {
		return r
	}
	return fallback
}
