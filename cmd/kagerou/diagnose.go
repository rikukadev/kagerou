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
	"github.com/rikukadev/kagerou/internal/scaffold"
)

// diagnosis は --json の出力形。フィールドの追加のみ互換。
type diagnosis struct {
	Dir       string           `json:"dir"`
	Repo      string           `json:"repo,omitempty"`
	Facts     diagnosisFacts   `json:"facts"`
	Assumed   diagnosisAssumed `json:"assumed"`
	Recommend string           `json:"recommend"`
	// EntryNote は compute とは別に決まる入口の注記(#152)。
	EntryNote string            `json:"entrypoint_note,omitempty"`
	Reasons   []diagnosisReason `json:"reasons"`
	// Diagram は推薦した入口の構成図(1 要素 1 行)。JSON でも行の配列で返す。
	// 呼び出し側が自前で組み直さずに、そのまま貼れる形にしておく。
	Diagram []string `json:"diagram"`
	// Files は kagerou init が生成するファイル。scaffold が実際に使う表から
	// 引いているので、ここに出た名前がそのまま生成物になる。
	Files []scaffold.PlannedFile `json:"files"`
}

type diagnosisFacts struct {
	Framework  string `json:"framework,omitempty"`
	Services   int    `json:"services"`
	DBDriver   string `json:"db_driver,omitempty"`
	AppPort    string `json:"app_port,omitempty"`
	Dockerfile bool   `json:"dockerfile"`
	// Dockerfiles は見つかった Dockerfile 全部。どれを見て LWA を判定したかが
	// 分からないと、検出漏れなのか本当に無いのかを読者が切り分けられない(#151)
	Dockerfiles []string `json:"dockerfiles,omitempty"`
	LWA         bool     `json:"lambda_web_adapter"`
	LWAFile     string   `json:"lambda_web_adapter_file,omitempty"`
	Realtime    bool     `json:"realtime"`
	Wants       []string `json:"wants,omitempty"`
	// HealthPath / PublishesImage は導入の判断が変わる事実(#165)。
	HealthPath     string `json:"health_path,omitempty"`
	PublishesImage bool   `json:"ci_publishes_image"`
	ImageRegistry  string `json:"image_registry,omitempty"`
	// ServiceFacts はサービスごとの事実(#184)。どのサービスがどの Dockerfile から
	// 来るかが見えないと、複数サービス構成で何が生成されるか読めない。
	ServiceFacts []diagnosisService `json:"service_facts,omitempty"`
	// URLShape は web と api を別オリジンに割るかどうかを決めている値。
	// 図の形が変わる根拠なので、機械可読側にも出す("" | "path" | "cross")。
	URLShape string `json:"url_shape,omitempty"`
}

type diagnosisService struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	Dockerfile string `json:"dockerfile,omitempty"`
	LWA        bool   `json:"lambda_web_adapter"`
	Framework  string `json:"framework,omitempty"`
	Port       string `json:"port,omitempty"`
	HealthPath string `json:"health_path,omitempty"`
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
	customDomain := fs.Bool("custom-domain", false, "assume previews are served on a custom domain (the entrypoint becomes the shared ALB)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	facts := appscan.Scan(abs)
	opts := recommend.Options{
		Auth: *auth, AllowFixedCost: *fixed, ExistingALB: *existingALB,
		CustomDomain: *customDomain,
	}
	choice := recommend.Entry(facts, opts)

	d := diagnosis{
		Dir:       abs,
		Recommend: string(choice.Default),
		EntryNote: choice.EntryNote,
		Assumed:   diagnosisAssumed{Auth: opts.Auth, AllowFixedCost: opts.AllowFixedCost, ExistingALB: opts.ExistingALB},
		Facts: diagnosisFacts{
			Framework: facts.Framework, Services: facts.Services, DBDriver: facts.DBDriver,
			AppPort: facts.AppPort, Dockerfile: facts.HasDockerfile,
			Dockerfiles: facts.Dockerfiles, LWA: facts.HasLWA, LWAFile: lwaFile(facts),
			Realtime: facts.Realtime, Wants: wantsList(facts.Wants),
			HealthPath: facts.HealthPath, PublishesImage: facts.PublishesImage,
			ImageRegistry: facts.ImageRegistry,
			URLShape:      facts.URLShape,
			ServiceFacts:  serviceFacts(facts),
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
	d.Diagram = recommend.Diagram(facts, choice.Default).Render()
	d.Files = scaffold.PlannedFiles(scaffoldFor(facts, choice.Default, filepath.Base(abs)), scaffold.AllTargets())

	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	}
	return writeDiagnosis(out, d)
}

// serviceFacts は appscan のサービスごとの事実を出力形に写す。
func serviceFacts(f appscan.Facts) []diagnosisService {
	out := make([]diagnosisService, 0, len(f.ServiceFacts))
	for _, s := range f.ServiceFacts {
		out = append(out, diagnosisService{
			Name: s.Name, Dir: s.Dir, Dockerfile: s.Dockerfile, LWA: s.HasLWA,
			Framework: s.Framework, Port: s.Port, HealthPath: s.HealthPath,
		})
	}
	return out
}

// scaffoldFor は入口を init の生成パラメータに写す。
//
// recommend の 5 つの入口と scaffold の生成物はここで 1 対 1 に対応する。
// 対応が抜けると「diagnose が薦めたのに init から出てこない」になるので、
// 入口を増やすときは必ずこの switch も埋めること。
func scaffoldFor(f appscan.Facts, e recommend.Entrypoint, dirName string) scaffold.Params {
	project := dirName
	if f.Repo != "" {
		project = f.Repo
	}
	// 入口は 2 軸: 何が動くか(Compute)と、どう公開するか(Entrypoint)。
	// recommend の 5 つはこの組み合わせに落ちる。
	//
	// Domain は診断では分からない(AWS を見ないので既存ベースを引けない)。
	// ALB 入口は独自ドメインが前提なので、プレースホルダを置いて
	// 「ドメインを決めれば ALB になる」側の生成物を並べる。
	p := scaffold.Params{
		Project:       project,
		Framework:     f.Framework,
		Port:          f.AppPort,
		HasDockerfile: f.HasDockerfile,
		// 予告するファイル名は検出値。ここを落とすと、Dockerfile.lambda の
		// 構成で「Dockerfile を触る」と予告して別名を触ることになる(#192)
		DockerfileName: f.DockerfileName,
		DockerfileDir:  f.DockerfileDir,
		Wants:          f.Wants,
		URLShape:       f.URLShape,
		Driver:         "stack",
		Compute:        "lambda",
		Entrypoint:     "alb",
		Domain:         "<your preview domain>",
	}
	// 共有ベース(CloudFront + S3)が要るのは静的成果物を配るときだけ。
	// 構成図に web の経路が出るときと同じ条件にしてある(図と一覧を食い違わせない)。
	p.SetupBase = e == recommend.Static || e == recommend.EdgeAuth || f.URLShape == "cross"

	switch e {
	case recommend.Static:
		p.Driver, p.Compute, p.Entrypoint, p.Domain = "static", "", "", ""
	case recommend.Lambda:
		// compute: lambda + 共有 ALB(HEAD の既定)
	case recommend.ALB:
		p.Compute = "ecs"
	case recommend.APIGateway:
		// 同じ Fargate を、固定費のある ALB ではなく HTTP API + VPC Link で公開する
		p.Compute, p.Entrypoint = "ecs", "apigateway"
	case recommend.EdgeAuth:
		// 認証は共有ベース側の話。compute の有無はそれとは独立に決まる
		p.Auth = true
		if !recommend.HasCompute(f) {
			p.Driver, p.Compute, p.Entrypoint, p.Domain = "static", "", "", ""
		}
	}
	return p
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
		v := strings.Join(d.Facts.Dockerfiles, ", ")
		if v == "" {
			v = "yes"
		}
		if d.Facts.LWA {
			v += "  (Lambda Web Adapter in " + d.Facts.LWAFile + ")"
		}
		line("dockerfile", v)
	}
	if d.Facts.Realtime {
		line("realtime", "WebSocket / SSE")
	}
	if len(d.Facts.ServiceFacts) > 0 {
		// 「複数サービス」と言うだけでは、何がどのイメージから来るか読めない。
		// 生成物がサービスごとに分かれる根拠なので並べる(#184)
		b.WriteString("\nservices\n")
		w := 0
		for _, s := range d.Facts.ServiceFacts {
			if n := len(s.Name); n > w {
				w = n
			}
		}
		for _, s := range d.Facts.ServiceFacts {
			fmt.Fprintf(&b, "  %-*s %s", w, s.Name, filepath.Join(s.Dir, s.Dockerfile))
			if s.LWA {
				b.WriteString("  (LWA)")
			}
			var extra []string
			if s.Framework != "" {
				extra = append(extra, s.Framework)
			}
			if s.Port != "" {
				extra = append(extra, "port "+s.Port)
			}
			if s.HealthPath != "" {
				extra = append(extra, s.HealthPath)
			}
			if len(extra) > 0 {
				b.WriteString("  " + strings.Join(extra, " / "))
			}
			b.WriteString("\n")
		}
	}
	if d.Facts.HealthPath != "" {
		// readiness の既定 "/" を上書きする根拠。重い SSR やリダイレクトを避ける
		line("health path", d.Facts.HealthPath)
	}
	if d.Facts.PublishesImage {
		v := "yes"
		if d.Facts.ImageRegistry != "" {
			v += " (" + d.Facts.ImageRegistry + ")"
		}
		// 既にイメージがあるなら sam build で作り直すのは二度手間かもしれない
		line("ci image", v)
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
	if d.EntryNote != "" {
		// compute(上の ❯)とは別の軸。同じ列に並べると選択肢に見えるので字下げを変える
		fmt.Fprintf(&b, "\n  entrypoint  %s\n", d.EntryNote)
	}

	if len(d.Diagram) > 0 {
		b.WriteString("\narchitecture\n")
		for _, l := range d.Diagram {
			b.WriteString("  " + l + "\n")
		}
	}

	if len(d.Files) > 0 {
		b.WriteString("\ngenerated by `kagerou init`\n")
		w := 0
		for _, f := range d.Files {
			if n := len(f.Path); n > w { // パスは ASCII なのでバイト幅で足りる
				w = n
			}
		}
		for _, f := range d.Files {
			fmt.Fprintf(&b, "  %-*s  %s\n", w, f.Path, f.Note)
		}
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

// lwaFile は LWA が入っていたファイル名。入っていなければ空。
func lwaFile(f appscan.Facts) string {
	if !f.HasLWA {
		return ""
	}
	return f.DockerfileName
}
