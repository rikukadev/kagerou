package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rikukadev/kagerou/internal/config"
)

// expiryFor は ttl(相対)と --expires-at(絶対)の両方から期限を出す(#280)。
func TestExpiryFor(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	// 空の TTL は既存どおりエラー(config.ParseTTL の仕様。"none" を明示するか
	// 値を入れるかの二択で、黙って無期限にはしない)。--expires-at の追加で
	// ここが変わっていないことを確かめる。
	t.Run("ttl 空はエラーのまま", func(t *testing.T) {
		if _, err := expiryFor(config.Config{}, ""); err == nil {
			t.Error("error を期待したが nil")
		}
	})

	t.Run("ttl none は nil(期限なし)", func(t *testing.T) {
		got, err := expiryFor(config.Config{TTL: "none"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("ttl は now からの加算", func(t *testing.T) {
		before := time.Now()
		got, err := expiryFor(config.Config{TTL: "72h"}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("got nil")
		}
		lo, hi := before.Add(72*time.Hour), time.Now().Add(72*time.Hour)
		if got.Before(lo) || got.After(hi) {
			t.Errorf("got %v, want between %v and %v", got, lo, hi)
		}
	})

	t.Run("expires-at はそのままの瞬間になる", func(t *testing.T) {
		got, err := expiryFor(config.Config{TTL: config.TTLNone}, future)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := time.Parse(time.RFC3339, future)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("expires-at は config の ttl より優先する", func(t *testing.T) {
		// フラグ同士の排他は parseUpFlags が見る。ここに来るのは
		// 「kagerou.yaml の ttl + フラグの --expires-at」の組み合わせ。
		got, err := expiryFor(config.Config{TTL: "72h"}, future)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, _ := time.Parse(time.RFC3339, future)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v (--expires-at が勝つこと)", got, want)
		}
	})

	t.Run("RFC3339 でなければエラー", func(t *testing.T) {
		for _, bad := range []string{"2026-10-01", "tomorrow", "72h", "1759312800"} {
			if _, err := expiryFor(config.Config{TTL: config.TTLNone}, bad); err == nil {
				t.Errorf("%q は拒否されるべき", bad)
			}
		}
	})

	t.Run("過去の時刻は拒否", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		_, err := expiryFor(config.Config{TTL: config.TTLNone}, past)
		if err == nil {
			t.Fatal("error を期待したが nil")
		}
		if !strings.Contains(err.Error(), "future") {
			t.Errorf("未来であることを要求するエラー: %v", err)
		}
	})

	t.Run("タイムゾーンが違っても同じ瞬間なら同値", func(t *testing.T) {
		// 2 年後の同じ瞬間を 2 通りの表記で
		utc, err1 := expiryFor(config.Config{TTL: config.TTLNone}, "2028-10-01T10:00:00Z")
		jst, err2 := expiryFor(config.Config{TTL: config.TTLNone}, "2028-10-01T19:00:00+09:00")
		if err1 != nil || err2 != nil {
			t.Fatalf("unexpected error: %v / %v", err1, err2)
		}
		if !utc.Equal(*jst) {
			t.Errorf("同じ瞬間を指すはず: %v != %v", utc, jst)
		}
	})
}

// --ttl と --expires-at を両方明示したらエラー(#280)。片方を暗黙に優先すると
// 渡し間違いが黙って通って期限がずれる。
func TestParseUpFlagsTTLExpiresAtExclusive(t *testing.T) {
	_, err := parseUpFlags("up", []string{
		"--name", "pr-1", "--ttl", "72h", "--expires-at", "2028-10-01T10:00:00Z",
	})
	if err == nil {
		t.Fatal("error を期待したが nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("排他のエラーであること: %v", err)
	}
}

// 片方だけなら通る。
func TestParseUpFlagsExpiryEitherOK(t *testing.T) {
	for _, args := range [][]string{
		{"--name", "pr-1", "--ttl", "72h"},
		{"--name", "pr-1", "--expires-at", "2028-10-01T10:00:00Z"},
		{"--name", "pr-1"},
	} {
		if _, err := parseUpFlags("up", args); err != nil {
			t.Errorf("%v: unexpected error: %v", args, err)
		}
	}
}
