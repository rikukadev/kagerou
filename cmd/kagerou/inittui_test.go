package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rikukadev/kagerou/internal/scaffold"
)

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

var enter = tea.KeyMsg{Type: tea.KeyEnter}

func step(t *testing.T, m initModel, msg tea.Msg) initModel {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(initModel)
}

func TestWizardDetectionSetsDefaults(t *testing.T) {
	det := scaffold.Detection{DBDriver: "mysql2", VarsSet: map[string]bool{}}
	m := newInitModel(t.TempDir(), scaffold.Params{Project: "myapp", Region: "r"}, det, false)
	if m.questions[0].key != "db" || m.questions[0].selected != 0 {
		t.Fatalf("mysql2 検出時は sashiki が既定になるはず: %+v", m.questions[0])
	}
	if !strings.Contains(m.View(), "mysql2 を検出") {
		t.Fatal("検出内容が選択肢に出ていない")
	}
	// 検出なしなら「使わない」が既定
	m2 := newInitModel(t.TempDir(), scaffold.Params{Project: "myapp", Region: "r"}, scaffold.Detection{VarsSet: map[string]bool{}}, false)
	if m2.questions[0].selected != 1 {
		t.Fatal("DB 検出なしでは sashiki は既定にならないはず")
	}
}

func TestWizardChoicesDriveGeneration(t *testing.T) {
	dir := t.TempDir()
	det := scaffold.Detection{DBDriver: "mysql2", VarsSet: map[string]bool{}}
	m := newInitModel(dir, scaffold.Params{Project: "myapp", Region: "r"}, det, false)

	m = step(t, m, enter)     // Q1 DB: 既定(sashiki)で確定
	m = step(t, m, enter)     // Q2 template: 既定(雛形生成)
	m = step(t, m, key('j'))  // Q3 workflows: preview のみへ
	m = step(t, m, enter)     // 確定 → サマリ
	if m.phase != phaseSummary {
		t.Fatalf("phase = %d, want summary", m.phase)
	}
	v := m.View()
	if !strings.Contains(v, "kagerou-preview.yml") || strings.Contains(v, "kagerou-reap.yml") {
		t.Fatalf("summary に選択が反映されていない: %s", v)
	}
	m = step(t, m, enter) // 生成
	if m.runErr != nil {
		t.Fatal(m.runErr)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github/workflows/kagerou-preview.yml")); err != nil {
		t.Fatal("preview workflow が生成されていない")
	}
	if _, err := os.Stat(filepath.Join(dir, ".github/workflows/kagerou-reap.yml")); err == nil {
		t.Fatal("選ばなかった reap workflow が生成されている")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "kagerou.yaml"))
	if !strings.Contains(string(b), "sashiki create {name}") {
		t.Fatal("sashiki 選択が kagerou.yaml に反映されていない")
	}
}

func TestWizardSkipsTemplateQuestionWhenExists(t *testing.T) {
	det := scaffold.Detection{HasTemplate: true, VarsSet: map[string]bool{}}
	m := newInitModel(t.TempDir(), scaffold.Params{Project: "x", Region: "r"}, det, false)
	for _, q := range m.questions {
		if q.key == "template" {
			t.Fatal("template 既存なら質問自体が出ないはず")
		}
	}
}

func TestWizardQuitGeneratesNothing(t *testing.T) {
	dir := t.TempDir()
	m := newInitModel(dir, scaffold.Params{Project: "x", Region: "r"}, scaffold.Detection{VarsSet: map[string]bool{}}, false)
	m = step(t, m, key('q'))
	if m.phase != phaseCanceled {
		t.Fatalf("phase = %d, want canceled", m.phase)
	}
	if _, err := os.Stat(filepath.Join(dir, "kagerou.yaml")); err == nil {
		t.Fatal("中止したのにファイルが生成されている")
	}
}

func TestWizardResultShowsPrefilledSteps(t *testing.T) {
	det := scaffold.Detection{
		AccountID: "199410355960",
		Owner:     "rikukadev", Repo: "myapp",
		VarsSet: map[string]bool{"AWS_ROLE_ARN": true, "AWS_REGION": true, "ECR_REPOSITORY": true},
	}
	m := newInitModel(t.TempDir(), scaffold.Params{Project: "myapp", Region: "r"}, det, false)
	for m.phase == phaseAsk {
		m = step(t, m, enter)
	}
	m = step(t, m, enter) // サマリ → 生成
	if m.runErr != nil {
		t.Fatal(m.runErr)
	}
	v := m.View()
	if !strings.Contains(v, "✓") {
		t.Fatalf("検出済み項目に ✓ が付いていない: %s", v)
	}
	// Variables 設定済みなので、その 3 手順は Done になっている
	doneCount := 0
	for _, s := range m.steps {
		if s.Done {
			doneCount++
		}
	}
	if doneCount < 3 {
		t.Fatalf("prechecked steps = %d, want >= 3", doneCount)
	}
}
