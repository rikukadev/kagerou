package main

// kagerou init のウィザード TUI。質問ごとに選択肢を ↑↓ で選んで enter で進む
// (sam init スタイル)。検出結果が既定の選択肢になる。最後にサマリを確認して
// enter で生成し、AWS セットアップは選択に応じてその場で適用 / スクリプト化 /
// 手動用のコマンド列挙になる。非 TTY では呼ばれない。

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	p.Port = m.det.AppPort
	p.HasDockerfile = m.det.HasDockerfile || m.answer("docker") == 0
	sel := scaffold.Targets{KagerouYaml: true}
	sel.Template = m.answer("template") == 0 // 質問なし(-1)= 既存なので生成しない
	switch m.answer("workflows") {
	case 0:
		sel.Preview, sel.Reap = true, true
	case 1:
		sel.Preview = true
	}
	return sel, p
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
	if done, ok := msg.(setupDoneMsg); ok {
		m.setupOutput, m.setupErr = done.output, done.err
		if done.err != nil {
			// 失敗してもスクリプトは残っているので、直して再実行できる
			m.setupMode = scaffold.SetupScript
		} else {
			m.setupMode = scaffold.SetupApplied
		}
		m.steps = scaffold.Steps(m.params, m.det, m.setupMode)
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
			if m.answer("docker") == 0 {
				if changed, err := scaffold.InjectLWA(m.dir); err != nil {
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
					m.steps = scaffold.Steps(p, m.det, m.setupMode)
					m.phase = phaseResult
					return m, nil
				}
				m.phase = phaseApplying
				return m, applySetup(m.dir)
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
			m.steps = scaffold.Steps(p, m.det, m.setupMode)
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
		switch m.answer("setup") {
		case setupRun:
			b.WriteString("  + AWS setup: run now (role / ECR / variables)\n")
		case setupScript:
			b.WriteString("  + AWS setup: " + scaffold.SetupScriptName + "\n")
		}
		b.WriteString("\n" + tuiHint.Render("enter generate · ← back · q cancel"))
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
	if m.det.AccountID != "" {
		parts = append(parts, "aws:"+m.det.AccountID)
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
