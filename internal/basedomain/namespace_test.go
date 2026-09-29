package basedomain

import (
	"errors"
	"context"
	"strings"
	"testing"
)

// #196: 名前空間を明示したら、空振りしても他の土台へ落ちない。
// 落ちると**別製品の土台に黙って繋がる**ので、失敗させるほうが安全。
func TestExplicitNamespaceDoesNotFallBack(t *testing.T) {
	var asked []string
	orig := getParameter
	getParameter = func(_ context.Context, region, key string) (string, error) {
		asked = append(asked, key)
		if strings.Contains(key, "_shared") {
			return "shared.example.com", nil // 落ちたら拾えてしまう値を置く
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { getParameter = orig })

	_, _, err := Resolve(context.Background(), "bengo4-legal", "ap-northeast-1", true)
	if err == nil {
		t.Fatal("明示指定が空振りしたのに成功している(別の土台に落ちた)")
	}
	for _, k := range asked {
		if strings.Contains(k, "_shared") {
			t.Errorf("_shared を見に行っている: %s", k)
		}
	}
	// 何を探したかと、落とさない理由がエラーに出ること
	for _, want := range []string{"bengo4-legal", "does not fall back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラーに %q が無い: %v", want, err)
		}
	}
}

// 明示していなければ従来どおり _shared へ落ちる(既存リポジトリを壊さない)。
func TestImplicitStillFallsBack(t *testing.T) {
	orig := getParameter
	getParameter = func(_ context.Context, region, key string) (string, error) {
		if strings.Contains(key, "_shared/") {
			return "shared.example.com", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { getParameter = orig })

	d, k, err := Resolve(context.Background(), "todo", "ap-northeast-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if d != "shared.example.com" || !strings.Contains(k, "_shared") {
		t.Errorf("domain=%q key=%q", d, k)
	}
}

// 明示した名前空間が実際に引かれること(project ではなく base を見る)。
func TestExplicitNamespaceIsUsed(t *testing.T) {
	orig := getParameter
	getParameter = func(_ context.Context, region, key string) (string, error) {
		if key == "/kagerou/base/bengo4-legal/domain" {
			return "legal.example.com", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { getParameter = orig })

	d, _, err := Resolve(context.Background(), "bengo4-legal", "ap-northeast-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if d != "legal.example.com" {
		t.Errorf("domain = %q", d)
	}
}
