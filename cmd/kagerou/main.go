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
	case "iam-policy":
		return cmdIamPolicy(args[1:], out)
	case "validate":
		return cmdValidate(args[1:], out)
	case "list":
		return cmdList(args[1:], out)
	case "serve":
		return cmdServe(args[1:], out)
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
  kagerou init [--sashiki]           generate the onboarding files (interactive)
  kagerou validate [--name <name>]   check the template against the contract
  kagerou iam-policy [--with-*]      generate a least-privilege CI role policy
  kagerou iam-policy --doc trust     generate the deploy role's OIDC trust policy
  kagerou iam-policy --doc boundary  generate the self-serve permissions boundary
  kagerou up --name <name> [flags]   create/update an environment (idempotent)
  kagerou down --name <name>         delete an environment (idempotent)
  kagerou list                       list environments
  kagerou serve [--addr :8080]       read-only HTTP for environments (Backstage)
  kagerou url --name <name>          print the environment URL
  kagerou reap [--dry-run]           collect environments past their TTL
  kagerou version                    print version
`)
}
