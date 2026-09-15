package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rikukadev/kagerou/internal/appscan"
	"github.com/rikukadev/kagerou/internal/iampolicy"
	"github.com/rikukadev/kagerou/internal/scaffold"
)

// kagerou init が生成しうる構成すべてについて、iam-policy が「知らない型」を
// 出さないことを固定する(#175)。
//
// #135 の drift 番人は **attach しているロール** と突き合わせるので、CI が
// 実際にデプロイする構成しか見張れない。ロールに要らない権限を足すわけには
// いかないから、E2E に無い入口は構造的にカバーできない。実際 #170 —
// 既定の ALB 入口で ELBv2 が Unknown のまま、{{resolve:ssm}} に至っては警告すら
// 出ない — はそこをすり抜けた。
//
// ここで見るのは AWS ではなく **生成器どうしの整合**。scaffold が書く型を
// iampolicy が知らない、という食い違いだけを、実 AWS もロール変更も無しに止める。
func TestGeneratedTemplatesHavePolicyCoverage(t *testing.T) {
	cases := []struct {
		name string
		p    scaffold.Params
	}{
		{
			// #131 以降の既定。独自ドメインがあれば lambda も共有 ALB に載る
			name: "lambda × alb(既定)",
			p:    scaffold.Params{Compute: "lambda", Entrypoint: "alb", Domain: "relay.example.com"},
		},
		{
			name: "lambda × apigateway(ドメイン無しの逃げ道)",
			p:    scaffold.Params{Compute: "lambda", Entrypoint: "apigateway"},
		},
		{
			name: "ecs × alb",
			p:    scaffold.Params{Compute: "ecs", Entrypoint: "alb", Domain: "relay.example.com"},
		},
		{
			name: "ecs × apigateway(VPC Link + Cloud Map)",
			p:    scaffold.Params{Compute: "ecs", Entrypoint: "apigateway", Domain: "relay.example.com"},
		},
		{
			// 1 環境に複数サービス。ALB のホストで分ける
			name: "multi service",
			p: scaffold.Params{Compute: "lambda", Entrypoint: "alb", Domain: "relay.example.com",
				Services: []string{"gateway", "api"}},
		},
		{
			// 周辺リソースを同梱する形。型が増えるので別枠で見る
			name: "wants 同梱",
			p: scaffold.Params{Compute: "lambda", Entrypoint: "alb", Domain: "relay.example.com",
				Wants: appscan.Wants{DynamoDB: true, SNS: true, SQS: true, S3: true, Redis: true, OpenSearch: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			p.Project, p.Region = "relay", "ap-northeast-1"
			dir := t.TempDir()
			if _, err := scaffold.Run(dir, p, scaffold.AllTargets(), false); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dir, "template.yaml"))
			if err != nil {
				t.Fatalf("この構成は template.yaml を生成するはず: %v", err)
			}
			facts, err := iampolicy.ScanTemplate(body)
			if err != nil {
				t.Fatal(err)
			}
			if len(facts.Unknown) > 0 {
				t.Errorf("kagerou が生成した型を iam-policy が知らない: %v\n"+
					"→ internal/iampolicy の knownTypes と Build に足す", facts.Unknown)
			}

			// 動的参照はリソース型に現れないので、Unknown では捕まらない。
			// 落としても警告が出ないぶん、ここで別に固定する
			pol, err := iampolicy.Build(iampolicy.Options{
				Prefix: p.Project + "-", Template: &facts, HostedZoneID: "Z123456789",
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "{{resolve:ssm") && !hasActionPrefix(pol, "ssm:") {
				t.Error("テンプレートが {{resolve:ssm}} を使うのに ssm の権限が出ていない " +
					"(CFN は動的参照をデプロイロールの資格情報で解決する)")
			}
		})
	}
}

func hasActionPrefix(p iampolicy.Policy, prefix string) bool {
	for _, s := range p.Statement {
		for _, a := range s.Action {
			if strings.HasPrefix(a, prefix) {
				return true
			}
		}
	}
	return false
}
