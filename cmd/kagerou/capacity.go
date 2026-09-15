package main

// kagerou capacity — 共有 ALB にあと何面置けるか(#162)。
//
// 上限に当たるのは作ろうとした瞬間で、そのとき出るのは CFN の TooManyRules /
// TooManyTargetGroups だけ。事前に見えれば「ALB を分ける」判断ができる。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/rikukadev/kagerou/internal/config"
	"github.com/rikukadev/kagerou/internal/quota"
)

// capacityJSON は --json の出力形。フィールドの追加のみ互換。
type capacityJSON struct {
	Region   string          `json:"region"`
	Listener string          `json:"listener_arn,omitempty"`
	Limits   []capacityLimit `json:"limits"`
	Headroom *int            `json:"headroom,omitempty"`
	BoundBy  string          `json:"bound_by,omitempty"`
	// PerEnv は headroom の分母。これを出さないと、複数サービス構成で
	// 数字だけ読んだ側が N 倍に取り違える(#188)。
	PerEnv  capacityPerEnv `json:"per_environment"`
	Unknown bool           `json:"unknown"`
	Note    string         `json:"note,omitempty"`
}

type capacityPerEnv struct {
	Rules        int    `json:"rules"`
	TargetGroups int    `json:"target_groups"`
	Source       string `json:"source"` // template | assumed
}

type capacityLimit struct {
	Name      string `json:"name"`
	Max       int    `json:"max"`
	Used      *int   `json:"used,omitempty"`
	PerEnv    int    `json:"per_environment"`
	Remaining *int   `json:"remaining_environments,omitempty"`
}

func cmdCapacity(args []string, out *os.File) error {
	fs := flag.NewFlagSet("capacity", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	region := fs.String("region", "", "region to query (default: region in kagerou.yaml)")
	listener := fs.String("listener-arn", "", "shared ALB listener to count rules on (default: read from the SSM base contract)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}
	reg := *region
	if reg == "" {
		reg = cfg.Region
	}
	ctx := context.Background()

	arn := *listener
	if arn == "" && cfg.Project != "" {
		// ベースが書いた SSM(CONTRACT §9)。取れなくても上限だけは出せる
		arn = lookupListenerARN(ctx, reg, cfg.Project)
	}

	rep := quota.Lookup(ctx, reg, arn, perEnvFromTemplate(cfg.Template, *cfgPath))
	if *asJSON {
		return writeCapacityJSON(out, rep, arn)
	}
	return writeCapacityText(out, rep, arn)
}

// perEnvFromTemplate は「環境 1 つが ALB から取る枠」をテンプレートから数える。
//
// 環境の実体は template.yaml なので、**そこに書いてある個数がそのまま答え**になる。
// サービス数を別に推定するより確かで、複数サービス構成(#137)でも合う。
// 読めなければ単一サービスと仮定し、仮定であることを Source に残す(#188)。
func perEnvFromTemplate(templatePath, cfgPath string) quota.PerEnv {
	facts := loadTemplateFacts(templatePath, cfgPath)
	if facts == nil {
		return quota.Assumed()
	}
	p := quota.PerEnv{
		Rules:        facts.Counts["AWS::ElasticLoadBalancingV2::ListenerRule"],
		TargetGroups: facts.Counts["AWS::ElasticLoadBalancingV2::TargetGroup"],
		Source:       "template",
	}
	if !p.UsesALB() {
		// static や HTTP API 入口。共有 ALB の枠は取らない
		return p
	}
	// 片方だけ書いてある形は想定していないが、0 で割らない
	if p.Rules == 0 {
		p.Rules = p.TargetGroups
	}
	if p.TargetGroups == 0 {
		p.TargetGroups = p.Rules
	}
	return p
}

func writeCapacityJSON(out io.Writer, rep quota.Report, arn string) error {
	d := capacityJSON{Region: rep.Region, Listener: arn, Unknown: rep.Unknown, Note: rep.Note,
		PerEnv: capacityPerEnv{rep.PerEnv.Rules, rep.PerEnv.TargetGroups, rep.PerEnv.Source}}
	for _, l := range rep.Limits {
		cl := capacityLimit{Name: l.Name, Max: l.Max, PerEnv: l.PerEnv}
		if l.HasUsed {
			used := l.Used
			cl.Used = &used
		}
		if n, ok := l.Remaining(); ok {
			r := n
			cl.Remaining = &r
		}
		d.Limits = append(d.Limits, cl)
	}
	if n, by, ok := rep.Headroom(); ok {
		d.Headroom, d.BoundBy = &n, by
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

func writeCapacityText(out io.Writer, rep quota.Report, arn string) error {
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	if rep.Unknown {
		// 届かなかっただけで、上限が無いわけではない。失敗にはしない
		say("could not read the ALB limits (%s)\n", rep.Note)
		say("this is not a failure: kagerou never blocks on it. re-run with credentials for %s to see the numbers.\n", rep.Region)
		return nil
	}
	say("alb limits  %s\n", rep.Region)
	for _, l := range rep.Limits {
		if l.HasUsed {
			say("  %-46s %d / %d\n", l.Name, l.Used, l.Max)
			continue
		}
		say("  %-46s %d\n", l.Name, l.Max)
	}
	// 割り算の分子だけ出して分母を隠すと、複数サービス構成で数字を N 倍に
	// 読み違える(#188)。仮定で埋めた場合はそれも書く。
	if !rep.PerEnv.UsesALB() {
		say("\nthis configuration does not use the shared ALB — environments add no rules or target groups.\n")
		return nil
	}
	say("\n1 environment = %s (%s)\n", perEnvPhrase(rep.PerEnv), perEnvSourceNote(rep.PerEnv))
	if n, by, ok := rep.Headroom(); ok {
		say("room for %d more environments on this ALB (bound by %s)\n", n, by)
		if n == 0 {
			// ここに来た人が次に取る手は 1 つしかない
			say("the next `up` will fail with TooManyRules / TooManyTargetGroups.\n")
			say("deploy a second alb-base for another project, or request a quota increase.\n")
		}
		return nil
	}
	if arn == "" {
		say("no listener to count against — pass --listener-arn, or deploy deploy/alb-base.yaml\n")
		say("so the SSM contract (/kagerou/base/<project>/alb_listener_arn) has one.\n")
	}
	return nil
}

// perEnvPhrase は 1 環境あたりの消費量を人が読む形にする。
func perEnvPhrase(p quota.PerEnv) string {
	return fmt.Sprintf("%s + %s",
		plural(p.Rules, "rule"), plural(p.TargetGroups, "target group"))
}

// perEnvSourceNote は数の出どころ。仮定なら、直し方まで書く。
func perEnvSourceNote(p quota.PerEnv) string {
	if p.Source == "template" {
		return "counted in template.yaml"
	}
	return "assumed — no template found; run this where template.yaml is, or the number below is N times too high"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// lookupListenerARN はベースが書いた listener ARN を SSM から引く(CONTRACT §9)。
// 取れなければ空を返す — 上限だけでも出す価値があるので失敗にしない。
var lookupListenerARN = func(ctx context.Context, region, project string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return ""
	}
	out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String("/kagerou/base/" + project + "/alb_listener_arn"),
	})
	if err != nil || out.Parameter == nil || out.Parameter.Value == nil {
		return ""
	}
	return *out.Parameter.Value
}
