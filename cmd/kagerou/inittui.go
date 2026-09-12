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
	dbDetail := "kagerou.yaml に hooks(sashiki create/delete)と DB_USER: dev@{name} が入る"
	if p.Sashiki || det.SuggestSashiki() {
		dbDefault = 0
	}
	dbLabel := "sashiki を使う(DB ブランチを PR ごとに生やす)"
	if det.DBDriver != "" {
		dbLabel += " — " + det.DBDriver + " を検出"
	}
	qs = append(qs, question{
		key:   "db",
		title: "DB はどうしますか?",
		options: []option{
			{dbLabel, dbDetail},
			{"使わない / あとで --env で繋ぐ", "共有 RDS・SQLite 同梱などは env 注入だけで済む"},
		},
		selected: dbDefault,
	})

	if !det.HasTemplate {
		qs = append(qs, question{
			key:   "template",
			title: "SAM テンプレートはどうしますか?",
			options: []option{
				{"雛形を生成する(LWA + Env<Key> + KagerouUrl)", "Dockerfile は自分で用意する(あとの案内参照)"},
				{"自前で書く", "CONTRACT §4/§5(Env<Key> パラメータと KagerouUrl Output)に合わせる"},
			},
		})
	}

	qs = append(qs, question{
		key:   "workflows",
		title: "GitHub Actions workflow はどれを入れますか?",
		options: []option{
			{"preview + reap(推奨)", "PR 連動の環境と、6 時間ごとの TTL 回収の網"},
			{"preview のみ", "回収は手動 kagerou reap か、あとで reap.yml を足す"},
			{"入れない", "kagerou.yaml だけ作って CLI から使う"},
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
		b.WriteString("\n" + tuiHint.Render("↑↓ 選ぶ・enter 次へ・← 戻る・q 中止"))
	case phaseSummary:
		b.WriteString(header)
		sel, p := m.buildPlan()
		b.WriteString(tuiTitle.Render("この内容で生成します") + "\n")
		fmt.Fprintf(&b, "  project: %s / region: %s / ttl: 72h\n", p.Project, p.Region)
		files := []string{"kagerou.yaml"}
		if sel.Template {
			files = append(files, "template.yaml(雛形)")
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
			b.WriteString("  + sashiki 連携(hooks / DB env)\n")
		}
		b.WriteString("\n" + tuiHint.Render("enter 生成・← 戻る・q 中止"))
	case phaseResult:
		if m.runErr != nil {
			return "kagerou init: " + m.runErr.Error() + "\n"
		}
		b.WriteString(header)
		b.WriteString(tuiTitle.Render("生成しました") + "\n")
		for _, f := range m.result.Created {
			b.WriteString("  created  " + f + "\n")
		}
		for _, f := range m.result.Skipped {
			b.WriteString(tuiFaint.Render("  skipped  "+f+"(既存)") + "\n")
		}
		b.WriteString("\n" + tuiTitle.Render("次にやること(✓ は検出済み)") + "\n")
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
		b.WriteString("\n" + tuiHint.Render("enter / q で終了"))
	case phaseCanceled:
		b.WriteString("中止しました(何も生成していません)\n")
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
		return "(検出なし)"
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
