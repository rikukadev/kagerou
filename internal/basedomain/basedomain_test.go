package basedomain

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stub は「このキーだけが存在する」SSM を作る。キーは region 付きで持つ —
// 契約上 preview base は us-east-1、_shared-alb は ALB のリージョンなので、
// リージョンを間違えると引けないことまで再現する。
func stub(t *testing.T, have map[string]string) {
	t.Helper()
	orig := getParameter
	t.Cleanup(func() { getParameter = orig })
	getParameter = func(_ context.Context, region, key string) (string, error) {
		if v, ok := have[region+" "+key]; ok {
			return v, nil
		}
		return "", errors.New("ParameterNotFound")
	}
}

func TestResolveOrder(t *testing.T) {
	// 3 段すべてが在るときは project 専用が勝つ
	stub(t, map[string]string{
		"us-east-1 /kagerou/base/todo/domain":             "todo.example.com",
		"us-east-1 /kagerou/base/_shared/domain":          "shared.example.com",
		"ap-northeast-1 /kagerou/base/_shared-alb/domain": "alb.example.com",
	})
	d, k, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err != nil {
		t.Fatal(err)
	}
	if d != "todo.example.com" || k != "/kagerou/base/todo/domain" {
		t.Errorf("project 専用が勝つはず: %q %q", d, k)
	}
}

func TestResolveFallsBackToShared(t *testing.T) {
	stub(t, map[string]string{
		"us-east-1 /kagerou/base/_shared/domain":          "shared.example.com",
		"ap-northeast-1 /kagerou/base/_shared-alb/domain": "alb.example.com",
	})
	d, k, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err != nil {
		t.Fatal(err)
	}
	if d != "shared.example.com" || k != "/kagerou/base/_shared/domain" {
		t.Errorf("_shared に落ちるはず: %q %q", d, k)
	}
}

func TestResolveSharedAlbUsesCallerRegion(t *testing.T) {
	// _shared-alb は us-east-1 ではなく ALB と同じリージョンに在る(§9)。
	// ここを us-east-1 で引きに行くと永久に見つからない
	stub(t, map[string]string{
		"ap-northeast-1 /kagerou/base/_shared-alb/domain": "alb.example.com",
	})
	d, _, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err != nil {
		t.Fatal(err)
	}
	if d != "alb.example.com" {
		t.Errorf("_shared-alb を呼び出し側の region で引くはず: %q", d)
	}
	if _, _, err := Resolve(context.Background(), "todo", "us-west-2"); err == nil {
		t.Error("別 region では引けないはず")
	}
}

func TestResolveSkipsEmpty(t *testing.T) {
	// 空文字を採ると https://pr-42.. のような URL を作って緑にしてしまう
	stub(t, map[string]string{
		"us-east-1 /kagerou/base/todo/domain":    "   ",
		"us-east-1 /kagerou/base/_shared/domain": "shared.example.com",
	})
	d, _, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err != nil {
		t.Fatal(err)
	}
	if d != "shared.example.com" {
		t.Errorf("空のパラメータは飛ばすはず: %q", d)
	}
}

func TestResolveNotFoundListsKeys(t *testing.T) {
	stub(t, nil)
	_, _, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err == nil {
		t.Fatal("見つからなければエラーのはず(空文字で続行しない)")
	}
	// 「どこを探したか」が出ないと、利用者はベース未作成か権限不足かを切り分けられない
	for _, want := range []string{
		"/kagerou/base/todo/domain", "us-east-1",
		"/kagerou/base/_shared/domain",
		"/kagerou/base/_shared-alb/domain", "ap-northeast-1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラーに %q が無い: %v", want, err)
		}
	}
}

// ALB ベースは **アプリのリージョン** に /kagerou/base/<project>/domain を書く
// (CloudFront ベースだけが us-east-1)。#131 以降 ALB が既定の入口なので、
// us-east-1 しか見ないと既定構成の {base_domain} が必ず失敗する。
func TestResolveProjectKeyInAppRegion(t *testing.T) {
	stub(t, map[string]string{
		"ap-northeast-1 /kagerou/base/todo/domain": "todo.example.com",
	})
	d, k, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err != nil {
		t.Fatalf("ALB ベースのドメインを解決できていない: %v", err)
	}
	if d != "todo.example.com" || k != "/kagerou/base/todo/domain" {
		t.Errorf("got %q %q", d, k)
	}
}

// 両方のベースがある(static は CloudFront、compute は ALB)構成。
// domain は入口によらず同じ値なので、どちらを採っても結果は変わらない。
func TestResolveWorksWithBothBases(t *testing.T) {
	stub(t, map[string]string{
		"ap-northeast-1 /kagerou/base/todo/domain": "todo.example.com",
		"us-east-1 /kagerou/base/todo/domain":      "todo.example.com",
	})
	d, _, err := Resolve(context.Background(), "todo", "ap-northeast-1")
	if err != nil || d != "todo.example.com" {
		t.Errorf("got %q %v", d, err)
	}
}

// region が us-east-1 のときに同じキーを 2 回引きに行かない。
func TestResolveNoDuplicateLookupInUsEast1(t *testing.T) {
	var calls int
	orig := getParameter
	t.Cleanup(func() { getParameter = orig })
	getParameter = func(_ context.Context, region, key string) (string, error) {
		calls++
		return "", errors.New("ParameterNotFound")
	}
	_, _, _ = Resolve(context.Background(), "todo", "us-east-1")
	// project / _shared / _shared-alb の 3 本だけ
	if calls != 3 {
		t.Errorf("引いた回数 = %d (want 3)", calls)
	}
}
