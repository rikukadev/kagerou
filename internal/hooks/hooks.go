// Package hooks はライフサイクルフック(kagerou.yaml の hooks)を実行する。
// 失敗時の扱いは呼び出し側の責務(DESIGN §8: pre_up 失敗は up を止める、
// post_down 失敗は記録して続行、reap も post_down を呼ぶ)。
package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Timeout は 1 フックの実行上限。ハングした hook が up/reap を道連れにしない。
const Timeout = 10 * time.Minute

// Run は hook を sh -c で実行する。command が空なら何もしない。
// extraEnv は親プロセスの環境に重ねて渡す(KAGEROU_* の受け渡し。CONTRACT §6)。
// hook の出力は診断情報なので stderr に流す(stdout は kagerou の結果用)。
func Run(ctx context.Context, name, command string, extraEnv map[string]string) error {
	if command == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = os.Environ()
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook %s (%q): %w", name, command, err)
	}
	return nil
}
