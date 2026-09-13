package iampolicy

// テンプレートの Resources[].Type を読んでポリシーを組むための走査(#71)。
// フラグ任せだと「昔の構成」を固定して黙ってズレる。テンプレートが作るものは
// テンプレートから導き、対応を知らない型には黙らず警告する。

import (
	"sort"

	"gopkg.in/yaml.v3"
)

// TemplateFacts はポリシー生成に必要なテンプレートの事実。
type TemplateFacts struct {
	Counts  map[string]int // リソース Type → 個数
	HasVPC  bool           // いずれかの Function が VpcConfig を持つ
	Unknown []string       // 対応する権限を知らない型(ソート済み)。cmd が警告する
}

// knownTypes は Build が権限を導出できる(または既存 statement でカバー済みの)型。
// ここに無い型は Unknown に入り、利用者へ「手で足して」と伝わる。
var knownTypes = map[string]bool{
	"AWS::Serverless::Function":      true,
	"AWS::Lambda::Function":          true,
	"AWS::Serverless::HttpApi":       true,
	"AWS::Serverless::Api":           true,
	"AWS::ApiGatewayV2::Api":         true,
	"AWS::ApiGateway::RestApi":       true,
	"AWS::S3::Bucket":                true,
	"AWS::DynamoDB::Table":           true,
	"AWS::SNS::Topic":                true,
	"AWS::SNS::Subscription":         true, // SNSTopicLifecycle の Subscribe/Unsubscribe でカバー
	"AWS::SQS::Queue":                true,
	"AWS::SQS::QueuePolicy":          true, // SQSQueueLifecycle の Get/SetQueueAttributes でカバー
	"AWS::Logs::LogGroup":            true, // LogGroups statement でカバー
	"AWS::IAM::Role":                 true, // ExecutionRole statement でカバー
	"AWS::Lambda::Permission":        true, // lambda:AddPermission でカバー
	"AWS::ApiGatewayV2::Stage":       true, // apigateway:* でカバー
	"AWS::ApiGatewayV2::Route":       true,
	"AWS::ApiGatewayV2::Integration": true,
}

// tmplResource は検査に必要な部分だけ読む。CFN の独自タグ(!Ref / !Sub 等)を
// 含んでいても yaml.Node なら受けられる(internal/validate と同じ手法)。
type tmplResource struct {
	Type       string    `yaml:"Type"`
	Properties yaml.Node `yaml:"Properties"`
}

// ScanTemplate はテンプレート本文から TemplateFacts を作る。
func ScanTemplate(body []byte) (TemplateFacts, error) {
	var t struct {
		Resources map[string]tmplResource `yaml:"Resources"`
	}
	if err := yaml.Unmarshal(body, &t); err != nil {
		return TemplateFacts{}, err
	}
	f := TemplateFacts{Counts: map[string]int{}}
	unknown := map[string]bool{}
	for _, r := range t.Resources {
		if r.Type == "" {
			continue
		}
		f.Counts[r.Type]++
		if !knownTypes[r.Type] {
			unknown[r.Type] = true
		}
		if r.Type == "AWS::Serverless::Function" || r.Type == "AWS::Lambda::Function" {
			if hasKey(&r.Properties, "VpcConfig") {
				f.HasVPC = true
			}
		}
	}
	for k := range unknown {
		f.Unknown = append(f.Unknown, k)
	}
	sort.Strings(f.Unknown)
	return f, nil
}

// has は Counts の型のうち 1 つでも存在するかを返す。
func (f TemplateFacts) has(types ...string) bool {
	for _, t := range types {
		if f.Counts[t] > 0 {
			return true
		}
	}
	return false
}

// hasKey は mapping ノードの直下にキーがあるかを見る。
func hasKey(n *yaml.Node, key string) bool {
	if n == nil || n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return true
		}
	}
	return false
}
