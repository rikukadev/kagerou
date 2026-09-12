// kagerou — ephemeral environments on AWS.
// 設計は docs/DESIGN.md を参照。サブコマンドの実体は今後の issue で埋める。
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "kagerou:", err)
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("subcommand required")
	}
	switch args[0] {
	case "version", "--version":
		_, err := fmt.Fprintln(out, "kagerou", version)
		return err
	case "up":
		return cmdUp(args[1:], out)
	case "down":
		return cmdDown(args[1:], out)
	case "url":
		return cmdURL(args[1:], out)
	case "init":
		return cmdInit(args[1:], out)
	case "list":
		return cmdList(args[1:], out)
	case "reap":
		return cmdReap(args[1:], out)
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `kagerou — ephemeral environments on AWS

Usage:
  kagerou init [--sashiki]           導入ファイル一式を生成する
  kagerou up --name <name> [flags]   環境を作成/更新する(冪等)
  kagerou down --name <name>         環境を削除する(冪等)
  kagerou list                       環境の一覧
  kagerou url --name <name>          環境の URL を表示
  kagerou reap [--dry-run]           TTL 切れ環境の回収
  kagerou version                    バージョン表示
`)
}
