package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rikukadev/kagerou/internal/scaffold"
)

func TestChecklistModel(t *testing.T) {
	steps := scaffold.Steps(scaffold.Params{Region: "r"})
	m := checklistModel{steps: steps, done: make([]bool, len(steps))}

	v := m.View()
	if !strings.Contains(v, "[ ]") || strings.Contains(v, "すべて完了") {
		t.Fatalf("initial view: %s", v)
	}

	// space でチェック、j で移動
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = next.(checklistModel)
	if !m.done[0] {
		t.Fatal("space should toggle current item")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(checklistModel)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	// 全部チェックすると完了メッセージ
	for i := range m.done {
		m.done[i] = true
	}
	if !strings.Contains(m.View(), "すべて完了") {
		t.Fatal("all-done message missing")
	}

	// q で終了コマンド
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q should quit")
	}
}
