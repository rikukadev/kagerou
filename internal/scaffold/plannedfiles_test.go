package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlannedFilesMatchesRun は PlannedFiles の予告と Run の生成物が一致することを固定する。
//
// diagnose は「init で何が生えるか」をこの一覧で見せる。ここがずれると、
// 書き込みも AWS アクセスもしない診断が平然と嘘をつくことになる。
func TestPlannedFilesMatchesRun(t *testing.T) {
	cases := []struct {
		name string
		p    Params
	}{
		{"lambda(Dockerfile 無し)", Params{Project: "app", Framework: "go", Compute: "lambda"}},
		{"lambda(既存 Dockerfile)", Params{Project: "app", Framework: "node", Compute: "lambda", HasDockerfile: true}},
		{"ecs", Params{Project: "app", Framework: "go", Compute: "ecs"}},
		{"apigateway", Params{Project: "app", Framework: "go", Compute: "apigateway"}},
		{"認証あり(静的)", Params{Project: "app", Framework: "astro", Driver: "static", Auth: true}},
		{"認証あり(lambda)", Params{Project: "app", Framework: "go", Compute: "lambda", Auth: true}},
		{"static", Params{Project: "app", Framework: "astro", Driver: "static"}},
		{"preview base も作る", Params{Project: "app", Framework: "go", Compute: "lambda", SetupBase: true, Domain: "app.example.com"}},
		{"雛形の無いフレームワーク", Params{Project: "app", Framework: "rust", Compute: "lambda"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// 既存 Dockerfile のケースは実物を置く(Run はディスクを見て判断する)
			if tc.p.HasDockerfile {
				if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res, err := Run(dir, tc.p, AllTargets(), false)
			if err != nil {
				t.Fatal(err)
			}
			created := append(append([]string{}, res.Created...), res.Skipped...)
			for i, p := range created {
				created[i] = filepath.ToSlash(p)
			}

			var planned []string
			for _, f := range PlannedFiles(tc.p, AllTargets()) {
				planned = append(planned, f.Path)
				if f.Note == "" {
					t.Errorf("%s に説明が無い", f.Path)
				}
			}
			if strings.Join(sorted(planned), "\n") != strings.Join(sorted(created), "\n") {
				t.Fatalf("予告と生成物がずれている\n  planned: %v\n  run:     %v", sorted(planned), sorted(created))
			}
		})
	}
}

func TestPlannedFilesWritesNothing(t *testing.T) {
	dir := t.TempDir()
	PlannedFiles(Params{Project: "app", Framework: "go", Compute: "lambda", SetupBase: true}, AllTargets())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("PlannedFiles がファイルを作った: %v", entries)
	}
}

func TestPlannedFilesDockerfileNote(t *testing.T) {
	// 既存 Dockerfile は上書きされない。予告の文言もそれを言う
	p := Params{Project: "app", Framework: "go", Compute: "lambda", HasDockerfile: true}
	var note string
	for _, f := range PlannedFiles(p, AllTargets()) {
		if f.Path == "Dockerfile" {
			note = f.Note
		}
	}
	if !strings.Contains(note, "上書きしない") {
		t.Fatalf("既存 Dockerfile の扱いが伝わらない: %q", note)
	}

	// static には Dockerfile も template.yaml も出ない(compute が無い)
	for _, f := range PlannedFiles(Params{Project: "app", Framework: "astro", Driver: "static"}, AllTargets()) {
		if f.Path == "Dockerfile" || f.Path == "template.yaml" {
			t.Errorf("static で %s を予告している", f.Path)
		}
	}
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
