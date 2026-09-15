package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupScriptOnlyDeploysGeneratedTemplates は、セットアップスクリプトが
// **生成されていないテンプレート**を指さないことを固定する。
//
// #194 がこれ: TUI はゾーンが見つかると SetupBase=true にするが、その判断は
// -auth と無関係だった。認証ありのとき書き出されるのは deploy/edge-base.yaml
// なのに、スクリプトは deploy/preview-base.yaml を deploy しようとしていた。
// 生成物とスクリプトは別々の条件で分岐するので、人が気をつけるだけでは
// また外れる。
func TestSetupScriptOnlyDeploysGeneratedTemplates(t *testing.T) {
	det := Detection{Owner: "acme", Repo: "demo", AccountID: "123456789012"}
	cases := []struct {
		name string
		p    Params
	}{
		{"配信ベースを作る", Params{Project: "app", Region: "ap-northeast-1", Framework: "astro",
			Driver: "static", Domain: "app.example.com", SetupBase: true}},
		{"認証あり(ゾーン検出で SetupBase も立つ)", Params{Project: "app", Region: "ap-northeast-1",
			Framework: "astro", Driver: "static", Domain: "app.example.com",
			SetupBase: true, Auth: true, AuthDomain: "example.com"}},
		{"認証あり(lambda)", Params{Project: "app", Region: "ap-northeast-1", Framework: "go",
			Compute: "lambda", Entrypoint: "alb", Domain: "app.example.com",
			SetupBase: true, Auth: true}},
		{"ベースは既存のものを使う", Params{Project: "app", Region: "ap-northeast-1", Framework: "go",
			Compute: "lambda", Entrypoint: "alb", Domain: "app.example.com"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Run(dir, tc.p, AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			path, err := WriteSetupScript(dir, tc.p, det)
			if err != nil {
				t.Fatal(err)
			}
			script := readFile(t, path)

			// --template-file で指す先は、必ず生成物の中にあること。
			// 実行される deploy も、手順として印字するだけの行も同じ扱いにする
			// (存在しないファイルを案内するのも同じ間違い)。
			for _, ref := range templateRefs(script) {
				if _, err := os.Stat(filepath.Join(dir, ref)); err != nil {
					t.Errorf("生成されていない %s を指している:\n%s", ref, script)
				}
			}

			// 生成した *-base.yaml は、deploy するにせよ手順を出すにせよ
			// スクリプトのどこかに出ていること(黙って落とさない)。
			for _, f := range PlannedFiles(tc.p, AllTargets()) {
				if strings.HasSuffix(f.Path, "-base.yaml") && !strings.Contains(script, f.Path) {
					t.Errorf("%s を書き出すのに、セットアップが一言も触れていない:\n%s", f.Path, script)
				}
			}

			// 生成した shell が壊れていないこと(heredoc やクォートの崩れを拾う)
			if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
				t.Errorf("生成したスクリプトが shell として壊れている: %v\n%s", err, out)
			}
		})
	}
}

// TestSetupScriptAuthDoesNotDeploy は、認証ベースだけはセットアップが
// **deploy しない** ことを固定する。
//
// Lambda@Edge のコードを同梱するのでパッケージ(sam)が要り、client_secret は
// 人が SecureString として置くもの。先に stack だけ作ると、ログインの通らない
// 入口が出来上がる。
func TestSetupScriptAuthDoesNotDeploy(t *testing.T) {
	dir := t.TempDir()
	p := Params{Project: "app", Region: "ap-northeast-1", Framework: "astro", Driver: "static",
		Domain: "app.example.com", SetupBase: true, Auth: true, AuthDomain: "example.com"}
	if _, err := Run(dir, p, AllTargets(), false); err != nil {
		t.Fatal(err)
	}
	path, err := WriteSetupScript(dir, p, det())
	if err != nil {
		t.Fatal(err)
	}
	script := readFile(t, path)

	if strings.Contains(script, "preview-base.yaml") {
		t.Errorf("認証ありのとき生成されない preview-base.yaml を指している:\n%s", script)
	}
	for _, want := range []string{
		"deploy/edge-base.yaml",              // 何を deploy するか
		"client_secret",                      // 人が置くもの
		"allowed_domain --value example.com", // 通す組織(指定があるときだけ)
		"/_kagerou/auth/callback",            // IdP に登録する 1 本
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%q が手順に出ていない:\n%s", want, script)
		}
	}
	// 認証ありで「ベースが出来た」と言わない(まだ無い)
	if strings.Contains(script, `echo "  base  https://`) {
		t.Errorf("deploy していないベースを出来たものとして出している:\n%s", script)
	}

	// Next steps にも残作業として出ること
	var titles []string
	for _, s := range Steps(p, det(), SetupScript) {
		titles = append(titles, s.Title)
	}
	if !strings.Contains(strings.Join(titles, "\n"), "edge-base.yaml") {
		t.Errorf("認証ベースの deploy が Next steps に出ていない: %v", titles)
	}
}

func det() Detection { return Detection{Owner: "acme", Repo: "demo", AccountID: "123456789012"} }

// templateRefs は `--template-file <path>` の <path> を全部拾う。
func templateRefs(script string) []string {
	var out []string
	fields := strings.Fields(script)
	for i, f := range fields {
		if f == "--template-file" && i+1 < len(fields) {
			out = append(out, fields[i+1])
		}
	}
	return out
}
