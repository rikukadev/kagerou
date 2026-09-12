package stack

import (
	"strings"
	"testing"
)

func TestNormalizeOwner(t *testing.T) {
	cases := map[string]string{
		// assumed-role はセッション名を落としてロール ARN に(CI の毎回違う session 対策)
		"arn:aws:sts::123456789012:assumed-role/deploy/gha-run-1": "arn:aws:iam::123456789012:role/deploy",
		"arn:aws:sts::123456789012:assumed-role/deploy/gha-run-2": "arn:aws:iam::123456789012:role/deploy",
		// IAM ユーザ / root はそのまま
		"arn:aws:iam::123456789012:user/taishi": "arn:aws:iam::123456789012:user/taishi",
		"arn:aws:iam::123456789012:root":        "arn:aws:iam::123456789012:root",
	}
	for in, want := range cases {
		if got := normalizeOwner(in); got != want {
			t.Errorf("normalizeOwner(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckOwner(t *testing.T) {
	me := "arn:aws:iam::123456789012:user/me"
	other := "arn:aws:iam::123456789012:user/other"

	// 自分の環境 → 上書き OK
	if err := checkOwner(map[string]string{TagName: "pr-42", TagOwner: me}, me); err != nil {
		t.Errorf("same owner should be allowed: %v", err)
	}
	// owner タグの無い旧環境 → 互換のため OK(次の update で刻印される)
	if err := checkOwner(map[string]string{TagName: "pr-42"}, me); err != nil {
		t.Errorf("legacy env without owner should be allowed: %v", err)
	}
	// 他人の環境 → 名前衝突エラー(誰のものかと逃げ道を言う)
	err := checkOwner(map[string]string{TagName: "pr-42", TagOwner: other}, me)
	if err == nil {
		t.Fatal("foreign env must be rejected")
	}
	for _, want := range []string{"pr-42", other, me, "kagerou down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}
