package main

import (
	"os"
	"strings"
	"testing"
)

// E2E のデプロイロールに attach しているポリシーが、生成器の出す最小権限と
// 一致していることを固定する(#135)。
//
// 守りたいのは「穴を踏む → ci-policy.json に手で足す → 動く → 生成器は知らない
// まま」というループ。CI にも同じチェックを入れてあるが、ここにも置くのは
// `make test` の時点で気づけるようにするため。
//
// fixture は 1 本のロールを共有しているので、和集合で突き合わせる。単独の
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
		"--config", "e2e/aws/apigw/kagerou.yaml",
		"--config", "e2e/aws/alb/kagerou.yaml",
		// ベースは kagerou.yaml から指されていないが CI がデプロイする。
		// 含めないと、ベースが作る SSM / ALB の権限が生成側から抜ける
		"--template", "e2e/aws/alb/base.yaml",
		"--template", "e2e/aws/apigw/base.yaml",
		"--check", "e2e/aws/ci-policy.json",
	}, out)
	if err != nil {
		b, _ := os.ReadFile(out.Name())
		t.Fatalf("attach しているポリシーと生成器がずれている: %v\n%s", err, b)
	}
}

// 和集合は出力にも効く。1 ロールを複数構成が共有するとき、アタッチする
// ポリシーは和集合そのものなので、生成できないと結局手で書くことになる
// (#135 が止めたかったループに戻る)。
func TestRepeatedConfigUnionsForOutput(t *testing.T) {
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

	// worker(SQS)と apigw(ECS)は要る権限が重ならない。和集合なら両方出る
	if err := cmdIamPolicy([]string{
		"--config", "e2e/aws/worker/kagerou.yaml",
		"--config", "e2e/aws/apigw/kagerou.yaml",
		"--config", "e2e/aws/alb/kagerou.yaml",
	}, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sqs:CreateQueue", "ecs:CreateService", "servicediscovery:GetOperation"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("和集合に %q が無い", want)
		}
	}
}

// --doc policy 以外では和集合に意味が無い(Action 集合の形が違う)。
func TestRepeatedConfigRejectedForOtherDocs(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })

	err = cmdIamPolicy([]string{
		"--doc", "boundary", "--config", "a.yaml", "--config", "b.yaml",
	}, out)
	if err == nil {
		t.Fatal("--doc policy 以外の複数 --config は拒否されるべき")
	}
}
