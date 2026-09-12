package main

// kagerou init の「次にやること」チェックリスト TUI。
// チェック状態は表示上のもの(永続化しない)。非 TTY では呼ばれない。

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rikukadev/kagerou/internal/scaffold"
)

var (
	tuiTitle  = lipgloss.NewStyle().Bold(true)
	tuiHint   = lipgloss.NewStyle().Faint(true)
	tuiDone   = lipgloss.NewStyle().Faint(true).Strikethrough(true)
	tuiCursor = lipgloss.NewStyle().Bold(true)
	tuiDetail = lipgloss.NewStyle().Faint(true).PaddingLeft(6)
)

type checklistModel struct {
	steps  []scaffold.Step
	done   []bool
	cursor int
}

func (m checklistModel) Init() tea.Cmd { return nil }

func (m checklistModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.steps)-1 {
				m.cursor++
			}
		case " ", "enter", "x":
			m.done[m.cursor] = !m.done[m.cursor]
		}
	}
	return m, nil
}

func (m checklistModel) View() string {
	var b strings.Builder
	b.WriteString(tuiTitle.Render("次にやること(リポジトリごとに 1 回)") + "\n")
	b.WriteString(tuiHint.Render("↑↓/jk 移動・space チェック・q 終了") + "\n\n")
	allDone := true
	for i, s := range m.steps {
		mark := "[ ]"
		title := s.Title
		if m.done[i] {
			mark = "[x]"
			title = tuiDone.Render(title)
		} else {
			allDone = false
		}
		cursor := "  "
		if i == m.cursor {
			cursor = tuiCursor.Render("> ")
		}
		b.WriteString(cursor + mark + " " + title + "\n")
		if i == m.cursor && s.Detail != "" {
			for _, line := range strings.Split(s.Detail, "\n") {
				b.WriteString(tuiDetail.Render(line) + "\n")
			}
		}
	}
	b.WriteString("\n")
	if allDone {
		b.WriteString("すべて完了。PR を開けば陽炎が立ちます 🌫\n")
	}
	return b.String()
}

func runChecklistTUI(p scaffold.Params) error {
	steps := scaffold.Steps(p)
	m := checklistModel{steps: steps, done: make([]bool, len(steps))}
	_, err := tea.NewProgram(m).Run()
	return err
}
