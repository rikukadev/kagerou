package main

import (
	"os"
	"testing"
)

// E2E のデプロイロールに attach しているポリシーが、生成器の出す最小権限と
// 一致していることを固定する(#135)。
//
// 守りたいのは「穴を踏む → ci-policy.json に手で足す → 動く → 生成器は知らない
// まま」というループ。CI にも同じチェックを入れてあるが、ここにも置くのは
// `make test` の時点で気づけるようにするため。
//
// 4 fixture は 1 本のロールを共有しているので、和集合で突き合わせる。単独の
// 構成と比べると「他の構成にだけ要る権限」が全部 over-permission に見えてしまう。
func TestE2EPolicyMatchesGenerated(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })

	err = cmdIamPolicy([]string{
		"--config", "e2e/aws/3tier/kagerou.yaml",
		"--config", "e2e/aws/ssr/kagerou.yaml",
		"--config", "e2e/aws/multi/kagerou.yaml",
		"--config", "e2e/aws/worker/kagerou.yaml",
		"--check", "e2e/aws/ci-policy.json",
	}, out)
	if err != nil {
		b, _ := os.ReadFile(out.Name())
		t.Fatalf("attach しているポリシーと生成器がずれている: %v\n%s", err, b)
	}
}

// 和集合モードは --check 専用。生成物の出力は 1 構成ぶんしか意味を持たない。
func TestRepeatedConfigRequiresCheck(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })

	err = cmdIamPolicy([]string{"--config", "a.yaml", "--config", "b.yaml"}, out)
	if err == nil {
		t.Fatal("--check 無しの複数 --config は拒否されるべき")
	}
}
