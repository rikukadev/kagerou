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

// chooseSetup は setup 質問の selected を直接固定する(テストで aws/gh を叩かないため)。
func chooseSetup(m initModel, v int) initModel {
	for i := range m.questions {
		if m.questions[i].key == "setup" {
			m.questions[i].selected = v
		}
	}
	return m
}

// answerAll は phaseAsk を既定選択のまま enter で進めて summary まで運ぶ。
func answerAll(t *testing.T, m initModel) initModel {
	t.Helper()
	for m.phase == phaseAsk {
		m = step(t, m, enter)
	}
	return m
}

func TestWizardDetectionSetsDefaults(t *testing.T) {
	det := scaffold.Detection{DBDriver: "mysql2", VarsSet: map[string]bool{}}
	m := newInitModel(t.TempDir(), scaffold.Params{Project: "myapp", Region: "r"}, det, false)
	if m.questions[0].key != "db" || m.questions[0].selected != 0 {
		t.Fatalf("mysql2 検出時は sashiki が既定になるはず: %+v", m.questions[0])
	}
	if !strings.Contains(m.View(), "detected mysql2") {
		t.Fatal("detection not shown in option")
	}
	// 検出なしなら「使わない」+ setup はスクリプトが既定
	m2 := newInitModel(t.TempDir(), scaffold.Params{Project: "myapp", Region: "r"}, scaffold.Detection{VarsSet: map[string]bool{}}, false)
	if m2.questions[0].selected != 1 {
		t.Fatal("DB 検出なしでは sashiki は既定にならないはず")
	}
	if m2.answer("setup") != setupScript {
		t.Fatal("creds 検出なしでは setup の既定は script のはず")
	}
	// creds + repo が見えていれば run now が既定
	m3 := newInitModel(t.TempDir(), scaffold.Params{Project: "myapp", Region: "r"},
		scaffold.Detection{AccountID: "1", Owner: "o", Repo: "r", VarsSet: map[string]bool{}}, false)
	if m3.answer("setup") != setupRun {
		t.Fatal("creds 検出ありでは setup の既定は run now のはず")
	}
}

func TestWizardChoicesDriveGeneration(t *testing.T) {
	dir := t.TempDir()
	det := scaffold.Detection{DBDriver: "mysql2", VarsSet: map[string]bool{}}
	m := newInitModel(dir, scaffold.Params{Project: "myapp", Region: "r"}, det, false)
	m = chooseSetup(m, setupSkip)

	m = step(t, m, enter)    // Q1 DB: 既定(sashiki)
	m = step(t, m, enter)    // Q2 template: 既定(雛形生成)
	m = step(t, m, key('j')) // Q3 workflows: preview のみへ
	m = step(t, m, enter)
	m = step(t, m, enter) // Q4 setup(skip 固定)→ summary
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

func TestWizardSetupScriptChoice(t *testing.T) {
	dir := t.TempDir()
	m := newInitModel(dir, scaffold.Params{Project: "myapp", Region: "r"},
		scaffold.Detection{Owner: "o", Repo: "myapp", VarsSet: map[string]bool{}}, false)
	m = chooseSetup(m, setupScript)
	m = answerAll(t, m)
	m = step(t, m, enter) // summary → 生成
	if m.runErr != nil {
		t.Fatal(m.runErr)
	}
	if _, err := os.Stat(filepath.Join(dir, scaffold.SetupScriptName)); err != nil {
		t.Fatal("setup script が書き出されていない")
	}
	// 手順は 1 行(スクリプト実行)に畳まれている
	found := false
	for _, s := range m.steps {
		if strings.Contains(s.Title, scaffold.SetupScriptName) {
			found = true
		}
		if strings.Contains(s.Title, "GitHub Variables") {
			t.Fatal("script モードでは 3 手順は列挙されないはず")
		}
	}
	if !found {
		t.Fatal("script 実行の手順が出ていない")
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
	m = chooseSetup(m, setupSkip) // aws/gh を実行しない
	m = answerAll(t, m)
	m = step(t, m, enter)
	if m.runErr != nil {
		t.Fatal(m.runErr)
	}
	v := m.View()
	if !strings.Contains(v, "✓") {
		t.Fatalf("検出済み項目に ✓ が付いていない: %s", v)
	}
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

func TestWizardDomainQuestion(t *testing.T) {
	// ゾーン 2 つ & base なし → 質問が出る(raw URL の選択肢は無い)
	det := scaffold.Detection{Zones: []string{"rikuka.dev", "example.org"}, VarsSet: map[string]bool{}}
	m := newInitModel(t.TempDir(), scaffold.Params{Project: "x", Region: "r"}, det, false)
	found := false
	for _, q := range m.questions {
		if q.key == "domain" {
			found = true
			if len(q.options) != 2 {
				t.Fatalf("options = %d, want 2 (zones only, no raw URL option)", len(q.options))
			}
		}
	}
	if !found {
		t.Fatal("domain question should appear with 2 zones")
	}
	// 2 番目のゾーンを選ぶと Domain / SetupBase に反映される
	m = chooseSetup(m, setupSkip)
	for i := range m.questions {
		if m.questions[i].key == "domain" {
			m.questions[i].selected = 1
		}
	}
	_, p := m.buildPlan()
	if p.Domain != "x.example.org" || !p.SetupBase { // per-app: {project}.<zone>
		t.Fatalf("plan = %+v", p)
	}

	// ゾーン 1 つ → 質問は出ず自動選択
	det1 := scaffold.Detection{Zones: []string{"rikuka.dev"}, VarsSet: map[string]bool{}}
	m1 := newInitModel(t.TempDir(), scaffold.Params{Project: "x", Region: "r"}, det1, false)
	for _, q := range m1.questions {
		if q.key == "domain" {
			t.Fatal("single zone should not ask")
		}
	}
	_, p1 := m1.buildPlan()
	if p1.Domain != "x.rikuka.dev" || !p1.SetupBase {
		t.Fatalf("auto plan = %+v", p1)
	}

	// この project の base 既存 → 質問なし・再利用(SetupBase=false)
	detB := scaffold.Detection{Zones: []string{"rikuka.dev"},
		Bases:   map[string]scaffold.BaseInfo{"x": {Domain: "x.rikuka.dev"}},
		VarsSet: map[string]bool{}}
	mB := newInitModel(t.TempDir(), scaffold.Params{Project: "x", Region: "r"}, detB, false)
	for _, q := range mB.questions {
		if q.key == "domain" {
			t.Fatal("existing base should not ask")
		}
	}
	_, pB := mB.buildPlan()
	if pB.Domain != "x.rikuka.dev" || pB.SetupBase {
		t.Fatalf("reuse plan = %+v", pB)
	}
	if !strings.Contains(mB.View(), "base:x.rikuka.dev") {
		t.Fatal("header should show existing base")
	}

	// 旧アカウント単位 base(key "")にも fallback して再利用する
	detL := scaffold.Detection{Zones: []string{"rikuka.dev"},
		Bases:   map[string]scaffold.BaseInfo{"": {Domain: "preview.rikuka.dev"}},
		VarsSet: map[string]bool{}}
	mL := newInitModel(t.TempDir(), scaffold.Params{Project: "x", Region: "r"}, detL, false)
	_, pL := mL.buildPlan()
	if pL.Domain != "preview.rikuka.dev" || pL.SetupBase {
		t.Fatalf("legacy reuse plan = %+v", pL)
	}

	// ゾーンなし → 質問なし・Domain 空(生 URL 運用)
	m0 := newInitModel(t.TempDir(), scaffold.Params{Project: "x", Region: "r"}, scaffold.Detection{VarsSet: map[string]bool{}}, false)
	_, p0 := m0.buildPlan()
	if p0.Domain != "" || p0.SetupBase {
		t.Fatalf("no-zone plan = %+v", p0)
	}
}

// AWS に触る前に必ず関門を通ること。ファイル生成と同じ enter で
// 通り抜けられると「気づいたら課金されるものが作られていた」になる。
func TestConfirmAWSGate(t *testing.T) {
	dir := t.TempDir()
	newModel := func() initModel {
		p := scaffold.Params{Project: "demo", Region: "ap-northeast-1"}
		det := scaffold.Detection{AccountID: "123456789012", Owner: "acme", Repo: "demo"}
		m := newInitModel(dir, p, det, true)
		return chooseSetup(m, setupRun)
	}

	t.Run("生成の enter では AWS に進まない", func(t *testing.T) {
		m := answerAll(t, newModel())
		if m.phase != phaseSummary {
			t.Fatalf("phase = %d, want summary", m.phase)
		}
		m = step(t, m, enter) // ここでファイルが書かれる
		if m.phase != phaseConfirmAWS {
			t.Fatalf("phase = %d, want confirmAWS(生成の enter で適用まで走ってはいけない)", m.phase)
		}
		if _, err := os.Stat(filepath.Join(dir, "kagerou.yaml")); err != nil {
			t.Errorf("ファイルは生成されているべき: %v", err)
		}
	})

	t.Run("断るとスクリプトが残り、AWS には触らない", func(t *testing.T) {
		m := step(t, answerAll(t, newModel()), enter)
		m = step(t, m, key('n'))
		if m.phase != phaseResult {
			t.Fatalf("phase = %d, want result", m.phase)
		}
		if m.setupMode != scaffold.SetupScript {
			t.Errorf("setupMode = %v, 断ったらスクリプトを残すべき(選択肢を減らさない)", m.setupMode)
		}
		if _, err := os.Stat(filepath.Join(dir, scaffold.SetupScriptName)); err != nil {
			t.Errorf("%s が無い: %v", scaffold.SetupScriptName, err)
		}
	})

	// enter は「読み飛ばして進む」キーとして使ってきたので、ここでも
	// 肯定に倒すと事故る。y を押したときだけ進む。
	t.Run("enter は肯定ではない", func(t *testing.T) {
		m := step(t, answerAll(t, newModel()), enter)
		m = step(t, m, enter)
		if m.phase == phaseApplying {
			t.Fatal("enter で AWS に進んでしまった。肯定は y のみであるべき")
		}
	})

	t.Run("見せる内容が実際に作るものと揃っている", func(t *testing.T) {
		m := step(t, answerAll(t, newModel()), enter)
		view := m.View()
		for _, want := range []string{"demo-github-actions", "ECR repository", "費用の目安", "取り壊すとき", "[y/N]"} {
			if !strings.Contains(view, want) {
				t.Errorf("確認画面に %q が無い", want)
			}
		}
	})
}
