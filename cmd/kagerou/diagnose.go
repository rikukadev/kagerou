package main

// kagerou diagnose — 任意のリポジトリで「kagerou ならどう構成するか」だけを出す。
// **書き込みも AWS アクセスもしない**ので、他人のリポジトリでもそのまま実行できる
// (走査は internal/appscan = ファイルのみ、判定は internal/recommend = 純関数)。

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rikukadev/kagerou/internal/appscan"
	"github.com/rikukadev/kagerou/internal/recommend"
)

// diagnosis は --json の出力形。フィールドの追加のみ互換。
type diagnosis struct {
	Dir       string            `json:"dir"`
	Repo      string            `json:"repo,omitempty"`
	Facts     diagnosisFacts    `json:"facts"`
	Assumed   diagnosisAssumed  `json:"assumed"`
	Recommend string            `json:"recommend"`
	Reasons   []diagnosisReason `json:"reasons"`
}

type diagnosisFacts struct {
	Framework  string   `json:"framework,omitempty"`
	Services   int      `json:"services"`
	DBDriver   string   `json:"db_driver,omitempty"`
	AppPort    string   `json:"app_port,omitempty"`
	Dockerfile bool     `json:"dockerfile"`
	LWA        bool     `json:"lambda_web_adapter"`
	Realtime   bool     `json:"realtime"`
	Wants      []string `json:"wants,omitempty"`
}

type diagnosisAssumed struct {
	Auth           bool `json:"auth"`
	AllowFixedCost bool `json:"allow_fixed_cost"`
	ExistingALB    bool `json:"existing_alb"`
}

type diagnosisReason struct {
	Entrypoint string `json:"entrypoint"`
	Usable     bool   `json:"usable"`
	Reason     string `json:"reason"`
}

func cmdDiagnose(args []string, out *os.File) error {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	dir := fs.String("dir", ".", "repository to inspect")
	auth := fs.Bool("auth", false, "assume previews must be behind login (Google OIDC etc.)")
	fixed := fs.Bool("allow-fixed-cost", false, "allow entrypoints with a fixed monthly cost (ALB)")
	existingALB := fs.Bool("existing-alb", false, "assume a shared ALB base already exists")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	facts := appscan.Scan(abs)
	opts := recommend.Options{Auth: *auth, AllowFixedCost: *fixed, ExistingALB: *existingALB}
	choice := recommend.Entry(facts, opts)

	d := diagnosis{
		Dir:       abs,
		Recommend: string(choice.Default),
		Assumed:   diagnosisAssumed{Auth: opts.Auth, AllowFixedCost: opts.AllowFixedCost, ExistingALB: opts.ExistingALB},
		Facts: diagnosisFacts{
			Framework: facts.Framework, Services: facts.Services, DBDriver: facts.DBDriver,
			AppPort: facts.AppPort, Dockerfile: facts.HasDockerfile, LWA: facts.HasLWA,
			Realtime: facts.Realtime, Wants: wantsList(facts.Wants),
		},
	}
	if facts.Owner != "" {
		d.Repo = facts.Owner + "/" + facts.Repo
	}
	for _, c := range choice.Candidates {
		d.Reasons = append(d.Reasons, diagnosisReason{
			Entrypoint: string(c.Entrypoint), Usable: c.Usable, Reason: c.Reason,
		})
	}

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	}
	return writeDiagnosis(out, d)
}

func writeDiagnosis(out *os.File, d diagnosis) error {
	var b strings.Builder
	head := d.Repo
	if head == "" {
		head = filepath.Base(d.Dir)
	}
	b.WriteString("kagerou diagnose  " + head + "\n\n")

	b.WriteString("detected\n")
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "  %-12s %s\n", k, v)
		}
	}
	line("framework", d.Facts.Framework)
	line("services", fmt.Sprintf("%d", d.Facts.Services))
	line("port", d.Facts.AppPort)
	line("database", d.Facts.DBDriver)
	if d.Facts.Dockerfile {
		v := "yes"
		if d.Facts.LWA {
			v += " (Lambda Web Adapter present)"
		}
		line("dockerfile", v)
	}
	if d.Facts.Realtime {
		line("realtime", "WebSocket / SSE")
	}
	if len(d.Facts.Wants) > 0 {
		line("uses", strings.Join(d.Facts.Wants, ", "))
	}

	assumed := []string{}
	if d.Assumed.Auth {
		assumed = append(assumed, "login required")
	}
	if d.Assumed.AllowFixedCost {
		assumed = append(assumed, "fixed cost allowed")
	}
	if d.Assumed.ExistingALB {
		assumed = append(assumed, "shared ALB exists")
	}
	if len(assumed) > 0 {
		b.WriteString("\nassumed: " + strings.Join(assumed, ", ") + "\n")
	}

	b.WriteString("\nrecommended\n")
	for _, r := range d.Reasons {
		mark := "  "
		if r.Entrypoint == d.Recommend {
			mark = "❯ "
		}
		fmt.Fprintf(&b, "%s%-11s %s\n", mark, r.Entrypoint, r.Reason)
	}
	b.WriteString("\nno files were written and no AWS calls were made.\n")
	b.WriteString("run `kagerou init` in this repository to generate the setup.\n")

	_, err := out.WriteString(b.String())
	return err
}

func wantsList(w appscan.Wants) []string {
	var out []string
	for _, x := range []struct {
		on   bool
		name string
	}{
		{w.DynamoDB, "dynamodb"}, {w.SNS, "sns"}, {w.SQS, "sqs"},
		{w.S3, "s3"}, {w.Redis, "redis"}, {w.OpenSearch, "opensearch"},
	} {
		if x.on {
			out = append(out, x.name)
		}
	}
	return out
}
