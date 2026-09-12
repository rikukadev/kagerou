package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	ctx := context.Background()

	if err := Run(ctx, "pre_up", "", nil); err != nil {
		t.Fatalf("empty hook should be no-op: %v", err)
	}
	if err := Run(ctx, "pre_up", "true", nil); err != nil {
		t.Fatalf("successful hook: %v", err)
	}

	err := Run(ctx, "pre_up", "exit 3", nil)
	if err == nil || !strings.Contains(err.Error(), "pre_up") {
		t.Fatalf("failing hook should error with hook name: %v", err)
	}

	// 実際にコマンドが動いている(sh -c 経由)ことをファイルで確認
	marker := filepath.Join(t.TempDir(), "ran")
	if err := Run(ctx, "post_down", "touch "+marker, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
}

func TestRunPassesExtraEnv(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env-out")
	err := Run(context.Background(), "post_up",
		`printf '%s|%s' "$KAGEROU_NAME" "$KAGEROU_OUTPUT_APIURL" > `+out,
		map[string]string{"KAGEROU_NAME": "pr-42", "KAGEROU_OUTPUT_APIURL": "https://api"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "pr-42|https://api" {
		t.Fatalf("env not passed: %q", b)
	}
}
