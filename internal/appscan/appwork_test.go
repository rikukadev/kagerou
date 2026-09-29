package appscan

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func kinds(ws []AppWork) map[AppWorkKind]int {
	m := map[AppWorkKind]int{}
	for _, w := range ws {
		m[w.Kind]++
	}
	return m
}

func TestScanAppWorkFindsRealProblems(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"main.go": `package main

import "os"

func save() {
	os.WriteFile("/var/lib/app/state.db", nil, 0o644)
}
`,
		"boot.go": `package main

import "context"

func init() {
	_, _ = client.GetSecretValue(context.Background(), nil)
}
`,
		"auth.js": `const opts = { redirect_uri: "https://app.example.com/callback" };
document.cookie = "sid=1; domain=app.example.com";
`,
		"infra.go": `package main

const role = "arn:aws:iam::123456789012:role/app-runtime"
`,
	})

	got := kinds(ScanAppWork(dir))
	for _, k := range []AppWorkKind{WorkWritableFS, WorkSecretsAtInit, WorkFixedHost, WorkHardcodedResource} {
		if got[k] == 0 {
			t.Errorf("%s を検出していない: %v", k, got)
		}
	}
}

// 実アプリで実際に出た偽陽性。**偽陽性を出すくらいなら黙る**方針なので、
// ここが再発すると指摘全体が読まれなくなる。
func TestScanAppWorkStaysQuietOnFalsePositives(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		// 1) 関数リテラルの中の GetParameter は起動時に走らない
		//    (kagerou 自身の basedomain.go / capacity.go で誤検出した)
		"resolver.go": `package main

var getParameter = func(region, key string) (string, error) {
	return client.GetParameter(region, key)
}
`,
		// 2) 設定フィールドの既定値は cookie ドメインではない
		//    (sashiki の Domain: "sashiki.internal" で誤検出した)
		"config.go": `package main

func defaults() Config {
	return Config{Domain: "sashiki.internal"}
}
`,
		// 3) 連結の途中はバケット名ではない
		//    (kagerou の "aws s3 rm s3://kagerou-base-"+project で誤検出した)
		"teardown.go": `package main

func cmd(project, account string) string {
	return "aws s3 rm s3://kagerou-base-" + project + "-" + account
}
`,
		// 4) 相対パスへの書き込みは判断できない(実行場所次第)ので黙る
		"write.go": `package main

import "os"

func save() { os.WriteFile("out/report.json", nil, 0o644) }
`,
		// 5) /tmp 配下は正常
		"tmp.go": `package main

import "os"

func save() { os.WriteFile("/tmp/cache.bin", nil, 0o644) }
`,
		// 6) コメント中の例示
		"doc.go": `package main

// arn:aws:iam::123456789012:role/example のような値を渡す
const x = 1
`,
	})

	if got := ScanAppWork(dir); len(got) != 0 {
		t.Errorf("偽陽性が出ている: %+v", got)
	}
}

// テスト・ベンダ・生成物は見ない(指摘が雑音になる)。
func TestScanAppWorkSkipsNoise(t *testing.T) {
	body := `package main

import "os"

func save() { os.WriteFile("/etc/app.conf", nil, 0o644) }
`
	dir := writeFiles(t, map[string]string{
		"main_test.go":            body,
		"vendor/dep/dep.go":       body,
		"node_modules/x/index.js": `fs.writeFileSync("/etc/app.conf", "")`,
		"testdata/sample.go":      body,
	})
	if got := ScanAppWork(dir); len(got) != 0 {
		t.Errorf("見ないはずの場所を指摘している: %+v", got)
	}
}

// 対応していない言語では黙る(当てずっぽうを出さない)。
func TestScanAppWorkSilentOnUnsupportedLanguages(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"app.php": `<?php file_put_contents("/var/www/cache/x", $v); ?>`,
		"app.rb":  `File.write("/var/lib/app/state", v)`,
	})
	if got := ScanAppWork(dir); len(got) != 0 {
		t.Errorf("対応していない言語で指摘している: %+v", got)
	}
}
