package main

// kagerou cost — プレビュー基盤に月いくらかかっているかを Cost Explorer から出す(#208)。
//
// 既定は「基盤でいくら / 何環境を支えているか」。--by name で環境ごとの内訳。
// **環境ごとの直接費と基盤の固定費は分けて出す**(基盤は按分しない)。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"

	"github.com/rikukadev/kagerou/internal/config"
	"github.com/rikukadev/kagerou/internal/cost"
)

func cmdCost(args []string, out *os.File) error {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	by := fs.String("by", "", "breakdown: 'name' for per-environment lines (default: totals only)")
	period := fs.String("period", "7d", "period to sum, e.g. 7d / 30d")
	allProjects := fs.Bool("all-projects", false, "do not filter by kagerou:project")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *by != "" && *by != "name" {
		return fmt.Errorf("--by %q: only 'name' is supported", *by)
	}
	days, err := parseDays(*period)
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}
	project := cfg.Project
	if *allProjects {
		project = ""
	}

	ctx := context.Background()
	// Cost Explorer はグローバル。エンドポイントは us-east-1 に固定する。
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		return err
	}
	start, end := cost.Window(time.Now(), days)
	rep, err := cost.Fetch(ctx, costexplorer.NewFromConfig(awsCfg), project, start, end)
	if err != nil {
		// デプロイロールは boundary で ce:* を Deny している(CONTRACT §8)。
		// これは意図した設計なので、権限を足せではなく「別の資格情報で」と言う。
		if strings.Contains(err.Error(), "AccessDenied") {
			return fmt.Errorf("cost explorer: access denied — kagerou's deploy role denies ce:* on purpose "+
				"(the permissions boundary blocks billing APIs). run this with your own credentials, "+
				"not the CI role: %w", err)
		}
		return fmt.Errorf("cost explorer: %w", err)
	}

	if *asJSON {
		return writeCostJSON(out, rep, *by == "name")
	}
	return writeCostText(out, rep, *by == "name")
}

// parseDays は "7d" / "30" を日数にする。
func parseDays(s string) (int, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "d")
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("--period %q: want a number of days, e.g. 7d", s)
	}
	if n > 365 {
		return 0, fmt.Errorf("--period: at most 365d")
	}
	return n, nil
}

func writeCostText(out *os.File, r cost.Report, byName bool) error {
	var b strings.Builder

	head := "kagerou cost"
	if r.Project != "" {
		head += "  " + r.Project
	}
	fmt.Fprintf(&b, "%s\n\n", head)
	fmt.Fprintf(&b, "period    %s .. %s (%d days, UTC)\n",
		r.Start.Format("2006-01-02"), r.End.Format("2006-01-02"),
		int(r.End.Sub(r.Start).Hours()/24))

	if r.Empty() {
		b.WriteString("\n" + cost.ActivateHint() + "\n")
		_, err := out.WriteString(b.String())
		return err
	}

	fmt.Fprintf(&b, "\nenvironments   %s   (%d environments, %s each on average)\n",
		cost.FormatUSD(r.PerEnvTotal()), len(r.PerEnv), cost.FormatUSD(r.PerEnvAverage()))
	fmt.Fprintf(&b, "base           %s   (shared ALB / CloudFront / NAT — NOT split across environments)\n",
		cost.FormatUSD(r.Base))
	fmt.Fprintf(&b, "total          %s\n", cost.FormatUSD(r.Total()))
	fmt.Fprintf(&b, "\nat this rate   %s / month (base %s)\n",
		cost.FormatUSD(cost.Monthly(r.Total(), r.Start, r.End)),
		cost.FormatUSD(cost.Monthly(r.Base, r.Start, r.End)))

	if byName && len(r.PerEnv) > 0 {
		b.WriteString("\nby environment\n")
		for _, l := range r.PerEnv {
			fmt.Fprintf(&b, "  %-24s %s\n", l.Name, cost.FormatUSD(l.Amount))
		}
	}

	// 数字の意味を取り違えないための但し書き。省くと「今いくら」と読まれる。
	b.WriteString("\nCost Explorer lags about a day, so the period ends in the past.\n")
	fmt.Fprintf(&b, "this run made %d Cost Explorer request(s) ($0.01 each).\n", r.Calls)
	if r.Base == 0 {
		b.WriteString("base is $0.00 — the base stack may predate kagerou tagging it (kagerou validate reports this).\n")
	}
	_, err := out.WriteString(b.String())
	return err
}

func writeCostJSON(out *os.File, r cost.Report, byName bool) error {
	type line struct {
		Name   string  `json:"name"`
		Amount float64 `json:"amount"`
	}
	doc := map[string]any{
		"project":  r.Project,
		"start":    r.Start.Format("2006-01-02"),
		"end":      r.End.Format("2006-01-02"),
		"currency": r.Currency,
		"environments": map[string]any{
			"count":   len(r.PerEnv),
			"total":   r.PerEnvTotal(),
			"average": r.PerEnvAverage(),
		},
		"base":            r.Base,
		"total":           r.Total(),
		"monthly_at_rate": cost.Monthly(r.Total(), r.Start, r.End),
		"calls":           r.Calls,
	}
	if byName {
		lines := make([]line, 0, len(r.PerEnv))
		for _, l := range r.PerEnv {
			lines = append(lines, line{Name: l.Name, Amount: l.Amount})
		}
		doc["by_name"] = lines
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
