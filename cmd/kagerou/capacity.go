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
	Unknown  bool            `json:"unknown"`
	Note     string          `json:"note,omitempty"`
}

type capacityLimit struct {
	Name      string `json:"name"`
	Max       int    `json:"max"`
	Used      *int   `json:"used,omitempty"`
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

	rep := quota.Lookup(ctx, reg, arn)
	if *asJSON {
		return writeCapacityJSON(out, rep, arn)
	}
	return writeCapacityText(out, rep, arn)
}

func writeCapacityJSON(out io.Writer, rep quota.Report, arn string) error {
	d := capacityJSON{Region: rep.Region, Listener: arn, Unknown: rep.Unknown, Note: rep.Note}
	for _, l := range rep.Limits {
		cl := capacityLimit{Name: l.Name, Max: l.Max}
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
	if n, by, ok := rep.Headroom(); ok {
		say("\nroom for %d more environments on this ALB (bound by %s)\n", n, by)
		if n == 0 {
			// ここに来た人が次に取る手は 1 つしかない
			say("the next `up` will fail with TooManyRules / TooManyTargetGroups.\n")
			say("deploy a second alb-base for another project, or request a quota increase.\n")
		}
		return nil
	}
	if arn == "" {
		say("\nno listener to count against — pass --listener-arn, or deploy deploy/alb-base.yaml\n")
		say("so the SSM contract (/kagerou/base/<project>/alb_listener_arn) has one.\n")
	}
	return nil
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
