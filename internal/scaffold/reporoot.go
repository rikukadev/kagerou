package scaffold

import (
	"os"
	"path/filepath"
	"strings"
)

// RepoRoot は dir を含むリポジトリのルートと、ルートから見た dir の位置を返す(#225)。
//
// GitHub Actions は**リポジトリルートの .github/workflows/ しか読まない**。
// モノレポの 1 アプリだけを載せるとき、workflow をアプリのディレクトリに書くと
// 一度も動かない死んだファイルになる。生成先を分けるためにルートが要る。
//
// exec せず .git を辿るのは appscan と同じ流儀。worktree では .git がファイルに
// なるので、ディレクトリかどうかは見ない。
//
// 見つからなければ ok=false(git リポジトリでない場所で init している)。
func RepoRoot(dir string) (root, appDir string, ok bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	for cur := abs; ; {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			rel, err := filepath.Rel(cur, abs)
			if err != nil {
				return "", "", false
			}
			if rel == "." {
				rel = ""
			}
			return cur, filepath.ToSlash(rel), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", false
		}
		cur = parent
	}
}

// InSubdir はアプリがリポジトリルートではない場所にあるか。
func (p Params) InSubdir() bool { return p.AppDir != "" }

// ConfigPath はリポジトリルートから見た kagerou.yaml の位置。
// workflow はルートで動くので、action にはこの値を渡す。
func (p Params) ConfigPath() string {
	if p.AppDir == "" {
		return "kagerou.yaml"
	}
	return p.AppDir + "/kagerou.yaml"
}

// PathsFilter は workflow の paths フィルタ。サブディレクトリのアプリは、
// 自分に関係のない変更でプレビューを作り直さない。
func (p Params) PathsFilter() string {
	if p.AppDir == "" {
		return ""
	}
	return p.AppDir + "/**"
}

// WorkflowSuffix は workflow のファイル名に付ける識別子。
//
// 名前が固定だと、**2 つ目のアプリを init したとき 1 つ目の workflow を上書きする**
// (force なしなら skip されて、2 つ目の workflow が存在しないまま終わる)。
// ルート直下の 1 アプリでは今までの名前を保つ。
func (p Params) WorkflowSuffix() string {
	if p.AppDir == "" {
		return ""
	}
	return "-" + p.Project
}

// workflowPath は生成する workflow のパス(リポジトリルートから見た位置)。
func workflowPath(p Params, kind string) string {
	return strings.Join([]string{".github", "workflows", "kagerou-" + kind + p.WorkflowSuffix() + ".yml"}, "/")
}

// repoRootFrom は dir(アプリのディレクトリ)と appDir から、リポジトリルートを求める。
// appDir が空ならルート直下なので dir がそのままルート。
func repoRootFrom(dir, appDir string) string {
	if appDir == "" {
		return dir
	}
	up := make([]string, 0, strings.Count(appDir, "/")+1)
	for range strings.Split(appDir, "/") {
		up = append(up, "..")
	}
	return filepath.Join(append([]string{dir}, up...)...)
}
