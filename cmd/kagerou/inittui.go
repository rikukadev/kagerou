package main

// kagerou init のウィザード TUI。質問ごとに選択肢を ↑↓ で選んで enter で進む
// (sam init スタイル)。検出結果が既定の選択肢になる。最後にサマリを確認して
// enter で生成、続けて実値入りの「次にやること」を表示する。
// 非 TTY では呼ばれない(cmdInit がテキストにフォールバックする)。

import (
	"fmt"
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
)

type option struct {
	label  string
	detail string
}

type question struct {
	key      string // "db" / "template" / "workflows"
	title    string
	options  []option
	selected int
}

const (
	phaseAsk = iota
	phaseSummary
	phaseResult
	phaseCanceled
)

type initModel struct {
	phase int
	dir   string
	force bool

	det    scaffold.Detection
	params scaffold.Params

	questions []question
	qi        int // いま答えている質問

	result scaffold.Result
	runErr error
	steps  []scaffold.Step
}

func newInitModel(dir string, p scaffold.Params, det scaffold.Detection, force bool) initModel {
	var qs []question

	dbDefault := 1
	dbDetail := "adds hooks (sashiki create/delete) and DB_USER: dev@{name} to kagerou.yaml"
	if p.Sashiki || det.SuggestSashiki() {
		dbDefault = 0
	}
	dbLabel := "use sashiki (grow a DB branch per PR)"
	if det.DBDriver != "" {
		dbLabel += " — detected " + det.DBDriver
	}
	qs = append(qs, question{
		key:   "db",
		title: "How should the database be handled?",
		options: []option{
			{dbLabel, dbDetail},
			{"none / wire it later via --env", "shared RDS, bundled SQLite etc. only need env injection"},
		},
		selected: dbDefault,
	})

	if !det.HasTemplate {
		qs = append(qs, question{
			key:   "template",
			title: "What about the SAM template?",
			options: []option{
				{"generate a starter (LWA + Env<Key> + KagerouUrl)", "you still write the Dockerfile yourself (see next steps)"},
				{"write my own", "follow CONTRACT §4/§5 (Env<Key> parameters and the KagerouUrl output)"},
			},
		})
	}

	qs = append(qs, question{
		key:   "workflows",
		title: "Which GitHub Actions workflows do you want?",
		options: []option{
			{"preview + reap (recommended)", "PR-driven environments plus a TTL sweep every 6 hours"},
			{"preview only", "reap manually with `kagerou reap`, or add reap.yml later"},
			{"none", "generate kagerou.yaml only and drive everything from the CLI"},
		},
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

func (m initModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if s := k.String(); s == "q" || s == "esc" || s == "ctrl+c" {
		if m.phase == phaseResult { // 生成後の q は普通の終了
			return m, tea.Quit
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
			m.phase = phaseAsk // 戻って選び直す
		case "enter":
			sel, p := m.buildPlan()
			m.params = p
			m.result, m.runErr = scaffold.Run(m.dir, p, sel, m.force)
			m.steps = scaffold.Steps(p, m.det)
			m.phase = phaseResult
			if m.runErr != nil {
				return m, tea.Quit
			}
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
		// 回答済みの質問は 1 行サマリで残す(選んできた道が見える)
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
		b.WriteString("\n" + tuiHint.Render("enter generate · ← back · q cancel"))
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
		b.WriteString("\n" + tuiTitle.Render("Next steps (✓ = already detected)") + "\n")
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
