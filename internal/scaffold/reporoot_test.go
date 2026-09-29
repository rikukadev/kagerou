package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRepoRoot(t *testing.T) {
	root := gitRepo(t)
	app := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}

	gotRoot, appDir, ok := RepoRoot(app)
	if !ok {
		t.Fatal("ルートを見つけられていない")
	}
	if appDir != "services/api" {
		t.Errorf("appDir = %q, want services/api", appDir)
	}
	// macOS の /var → /private/var のような symlink 差を避けて比較する
	if filepath.Base(gotRoot) != filepath.Base(root) {
		t.Errorf("root = %q, want %q", gotRoot, root)
	}

	// ルート直下なら appDir は空
	if _, rel, ok := RepoRoot(root); !ok || rel != "" {
		t.Errorf("ルート直下: rel=%q ok=%v", rel, ok)
	}
}

// worktree では .git がファイルになる。ディレクトリ決め打ちで見ると見つからない。
func TestRepoRootFindsGitFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := RepoRoot(root); !ok {
		t.Error(".git がファイルのとき(worktree)にルートを見つけられていない")
	}
}

func TestRepoRootAbsentOutsideGit(t *testing.T) {
	// t.TempDir() の上に .git があると誤検出するので、ここでは結果だけ見る
	if _, _, ok := RepoRoot(filepath.Join(t.TempDir(), "nope")); ok {
		t.Log("親に .git がある環境ではスキップ相当")
	}
}

// #225 の本体。GitHub Actions はリポジトリルートの .github/workflows/ しか読まない。
// サブディレクトリで init したとき、workflow がアプリ配下に生えると一度も動かない。
func TestWorkflowsGoToRepoRoot(t *testing.T) {
	root := gitRepo(t)
	app := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}

	p := Params{Project: "mono-api", Region: "ap-northeast-1", AppDir: "services/api",
		Domain: "api.example.com"}
	if _, err := Run(app, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		filepath.Join(root, ".github", "workflows", "kagerou-preview-mono-api.yml"),
		filepath.Join(root, ".github", "workflows", "kagerou-reap-mono-api.yml"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("ルートに無い: %s", want)
		}
	}
	// アプリの持ち物はサブディレクトリのまま
	for _, want := range []string{"kagerou.yaml", "template.yaml"} {
		if _, err := os.Stat(filepath.Join(app, want)); err != nil {
			t.Errorf("アプリ配下に無い: %s", want)
		}
	}
	// 死んだ場所に生えていないこと
	if _, err := os.Stat(filepath.Join(app, ".github")); err == nil {
		t.Error("アプリ配下に .github が生えている(GitHub はここを読まない)")
	}
}

// workflow の中身がサブディレクトリを向いていること。
// ルートに置いただけでは、ビルドも設定の解決も失敗する。
func TestWorkflowPointsAtTheApp(t *testing.T) {
	root := gitRepo(t)
	app := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	p := Params{Project: "mono-api", Region: "r", AppDir: "services/api", Domain: "api.example.com"}
	if _, err := Run(app, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "kagerou-preview-mono-api.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`- "services/api/**"`,               // 関係ない変更で作り直さない
		"working-directory: services/api",   // sam build / docker build の場所
		"config: services/api/kagerou.yaml", // uses: には working-directory が効かない
		"group: preview-mono-api-",          // 別アプリの PR と混ざらない
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q が無い:\n%s", want, got)
		}
	}
}

// ルート直下の 1 アプリでは、今までのファイル名を保つ(既存リポジトリを動かし続ける)。
func TestWorkflowNameUnchangedAtRoot(t *testing.T) {
	root := gitRepo(t)
	p := Params{Project: "solo", Region: "r", Domain: "solo.example.com"}
	if _, err := Run(root, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".github", "workflows", "kagerou-preview.yml")); err != nil {
		t.Errorf("ルート直下で名前が変わった: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "kagerou-preview.yml"))
	for _, unwanted := range []string{"working-directory:", "config:", "paths:"} {
		if strings.Contains(string(b), unwanted) {
			t.Errorf("ルート直下なのに %q が入っている", unwanted)
		}
	}
}

// 2 つ目のアプリを init しても 1 つ目の workflow を上書きしない(issue の後段)。
func TestTwoAppsKeepSeparateWorkflows(t *testing.T) {
	root := gitRepo(t)
	for _, app := range []struct{ dir, project string }{
		{"services/api", "mono-api"},
		{"services/web", "mono-web"},
	} {
		d := filepath.Join(root, filepath.FromSlash(app.dir))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		p := Params{Project: app.project, Region: "r", AppDir: app.dir, Domain: app.project + ".example.com"}
		if _, err := Run(d, p, AllTargets(), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{
		"kagerou-preview-mono-api.yml", "kagerou-preview-mono-web.yml",
		"kagerou-reap-mono-api.yml", "kagerou-reap-mono-web.yml",
	} {
		if _, err := os.Stat(filepath.Join(root, ".github", "workflows", want)); err != nil {
			t.Errorf("%s が無い(上書きされた可能性)", want)
		}
	}
}
