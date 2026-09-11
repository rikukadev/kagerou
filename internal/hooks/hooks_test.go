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

	if err := Run(ctx, "pre_up", ""); err != nil {
		t.Fatalf("empty hook should be no-op: %v", err)
	}
	if err := Run(ctx, "pre_up", "true"); err != nil {
		t.Fatalf("successful hook: %v", err)
	}

	err := Run(ctx, "pre_up", "exit 3")
	if err == nil || !strings.Contains(err.Error(), "pre_up") {
		t.Fatalf("failing hook should error with hook name: %v", err)
	}

	// 実際にコマンドが動いている(sh -c 経由)ことをファイルで確認
	marker := filepath.Join(t.TempDir(), "ran")
	if err := Run(ctx, "post_down", "touch "+marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
}
