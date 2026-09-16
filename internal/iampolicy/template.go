package iampolicy

// テンプレートの Resources[].Type を読んでポリシーを組むための走査(#71)。
// フラグ任せだと「昔の構成」を固定して黙ってズレる。テンプレートが作るものは
// テンプレートから導き、対応を知らない型には黙らず警告する。

import (
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// TemplateFacts はポリシー生成に必要なテンプレートの事実。
type TemplateFacts struct {
	Counts map[string]int // リソース Type → 個数
	HasVPC bool           // いずれかの Function が VpcConfig を持つ
	// HasImage はいずれかの Function がコンテナイメージで配られる(ECR が要る)。
	// --with-ecr を付け忘れても足りないポリシーを出さないため(#135)。
	HasImage bool
	Unknown  []string // 対応する権限を知らない型(ソート済み)。cmd が警告する

	// 以下は「SAM が展開して初めて現れる」リソース。型を数えるだけでは
	// 永久に見えないので、元になるプロパティから導く。
	HasEventSourceMapping bool // Events の SQS 等、または明示の EventSourceMapping
	HasRoute53            bool // 明示の RecordSet、または Api の Domain.Route53
	HasCustomDomain       bool // 明示の DomainName、または Api の Domain

	// SSMParams は {{resolve:ssm:<path>}} で読まれる SSM パラメータのパス
	// (ソート済み・重複なし)。CloudFormation は動的参照を **デプロイロールの
	// 資格情報で** 解決するので ssm:GetParameters が要る。
	//
	// これはプロパティの **値** なのでリソース型の数え上げには現れず、Unknown
	// にも入らない。つまり拾わないと「警告も出ないまま足りないポリシー」になる。
	SSMParams []string
	// HasSecureSSM は {{resolve:ssm-secure:…}} を使っているか(kms:Decrypt が要る)。
	HasSecureSSM bool
	// Secrets は {{resolve:secretsmanager:<id>…}} で読まれるシークレット
	// (ソート済み・重複なし)。SSM と同じく **デプロイロールの資格情報で**
	// 解決されるので secretsmanager:GetSecretValue が要る。
	//
	// ALB の authenticate-oidc(#112)がここを通る。ssm-secure が使えない
	// リソースで秘密を渡す唯一の道なので、これから増える。
	Secrets []string
}

// knownTypes は Build が権限を導出できる(または既存 statement でカバー済みの)型。
// ここに無い型は Unknown に入り、利用者へ「手で足して」と伝わる。
var knownTypes = map[string]bool{
	"AWS::Serverless::Function":        true,
	"AWS::Lambda::Function":            true,
	"AWS::Serverless::HttpApi":         true,
	"AWS::Serverless::Api":             true,
	"AWS::ApiGatewayV2::Api":           true,
	"AWS::ApiGateway::RestApi":         true,
	"AWS::S3::Bucket":                  true,
	"AWS::DynamoDB::Table":             true,
	"AWS::SNS::Topic":                  true,
	"AWS::SNS::Subscription":           true, // SNSTopicLifecycle の Subscribe/Unsubscribe でカバー
	"AWS::SQS::Queue":                  true,
	"AWS::SQS::QueuePolicy":            true, // SQSQueueLifecycle の Get/SetQueueAttributes でカバー
	"AWS::Logs::LogGroup":              true, // LogGroups statement でカバー
	"AWS::IAM::Role":                   true, // ExecutionRole statement でカバー
	"AWS::Lambda::Permission":          true, // lambda:AddPermission でカバー
	"AWS::ApiGatewayV2::Stage":         true, // apigateway:* でカバー
	"AWS::ApiGatewayV2::Route":         true,
	"AWS::ApiGatewayV2::Integration":   true,
	"AWS::ApiGatewayV2::DomainName":    true, // 同上(/domainnames も apigateway:*)
	"AWS::ApiGatewayV2::ApiMapping":    true,
	"AWS::S3::BucketPolicy":            true, // WebBucketLifecycle の s3:PutBucketPolicy でカバー
	"AWS::Lambda::EventSourceMapping":  true,
	"AWS::Route53::RecordSet":          true,
	"AWS::Route53::RecordSetGroup":     true,
	"AWS::ApiGateway::DomainName":      true,
	"AWS::ApiGateway::BasePathMapping": true,
	// 共有 ALB 入口(#131 以降は compute: lambda の既定でもある)と compute: ecs。
	// どちらも kagerou init が生成する一次対応の構成なので、権限を手書きさせない
	"AWS::ElasticLoadBalancingV2::TargetGroup":  true,
	"AWS::ElasticLoadBalancingV2::ListenerRule": true,
	"AWS::ECS::TaskDefinition":                  true,
	"AWS::ECS::Service":                         true,
	// Cloud Map 経由の apigw 構成(#105)。E2E fixture はクラスタも自前で作る
	"AWS::ECS::Cluster":                          true,
	"AWS::ServiceDiscovery::PrivateDnsNamespace": true,
	"AWS::ServiceDiscovery::Service":             true,
	"AWS::EC2::SecurityGroup":                    true,
	"AWS::EC2::SecurityGroupIngress":             true,
	"AWS::EC2::SecurityGroupEgress":              true,
	"AWS::ApiGatewayV2::VpcLink":                 true, // apigateway:* でカバー
	"AWS::SSM::Parameter":                        true, // BaseSsmParameters でカバー
}

// ssmRefRe は CloudFormation の動的参照 {{resolve:ssm:<path>}} / {{resolve:ssm-secure:<path>}}。
// path の後ろにバージョン指定(:1)が付くことがあるので、そこは落とす。
var ssmRefRe = regexp.MustCompile(`\{\{resolve:(ssm|ssm-secure):([^}:]+)(?::\d+)?\}\}`)

// scanDynamicRefs はテンプレート本文から SSM の動的参照を拾う。
//
// YAML を解析せず本文を直接見るのは、動的参照が**どのプロパティにも**書けるため。
// 構造をたどると拾い漏れるし、拾うべき場所を列挙し続けることになる。
func scanDynamicRefs(body []byte, f *TemplateFacts) {
	seen := map[string]bool{}
	for _, m := range ssmRefRe.FindAllStringSubmatch(string(body), -1) {
		if m[1] == "ssm-secure" {
			f.HasSecureSSM = true
		}
		if p := strings.TrimSpace(m[2]); p != "" && !seen[p] {
			seen[p] = true
			f.SSMParams = append(f.SSMParams, p)
		}
	}
	sort.Strings(f.SSMParams)

	seenSecret := map[string]bool{}
	for _, m := range secretRefRe.FindAllStringSubmatch(string(body), -1) {
		if id := secretID(m[1]); id != "" && !seenSecret[id] {
			seenSecret[id] = true
			f.Secrets = append(f.Secrets, id)
		}
	}
	sort.Strings(f.Secrets)
}

// secretRefRe は {{resolve:secretsmanager:<secret-id>[:...]}}。
// secret-id が ARN だとコロンを含むので、まるごと取ってから分解する。
var secretRefRe = regexp.MustCompile(`\{\{resolve:secretsmanager:([^}]+)\}\}`)

// secretID は動的参照の中身から **シークレットの識別子だけ** を取り出す。
//
//	arn:aws:secretsmanager:ap-northeast-1:1234:secret:foo-AbCdEf:SecretString:client_id
//	→ arn:…:secret:foo-AbCdEf
//
// ARN は 7 フィールド固定(シークレット名にコロンは使えない)なので、そこで切る。
// ARN でなければ先頭フィールドがシークレット名。
func secretID(ref string) string {
	parts := strings.Split(strings.TrimSpace(ref), ":")
	if len(parts) == 0 {
		return ""
	}
	if parts[0] == "arn" {
		if len(parts) < 7 {
			return "" // ARN として壊れている。権限を推測で広げない
		}
		return strings.Join(parts[:7], ":")
	}
	return parts[0]
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
		Resources  map[string]tmplResource  `yaml:"Resources"`
		Parameters map[string]tmplParameter `yaml:"Parameters"`
	}
	if err := yaml.Unmarshal(body, &t); err != nil {
		return TemplateFacts{}, err
	}
	f := TemplateFacts{Counts: map[string]int{}}
	unknown := map[string]bool{}
	// SSM パラメータ型(AWS::SSM::Parameter::Value<...>)の Default も、
	// デプロイ時に **このロールの資格情報で** 解決される。{{resolve:ssm:}} では
	// ないので本文の走査には引っかからないが、要る権限は同じ
	scanSSMParameterTypes(t.Parameters, &f)
	for _, r := range t.Resources {
		if r.Type == "" {
			continue
		}
		f.Counts[r.Type]++
		if !knownTypes[r.Type] {
			unknown[r.Type] = true
		}
		switch r.Type {
		case "AWS::Serverless::Function", "AWS::Lambda::Function":
			if hasKey(&r.Properties, "VpcConfig") {
				f.HasVPC = true
			}
			// sam package の前は PackageType、後は ImageUri で現れる
			if nodeValue(&r.Properties, "PackageType") == "Image" || hasKey(&r.Properties, "ImageUri") {
				f.HasImage = true
			}
			if usesEventSourceMapping(&r.Properties) {
				f.HasEventSourceMapping = true
			}
		case "AWS::Serverless::Api", "AWS::Serverless::HttpApi":
			// Domain は DomainName + ApiMapping に、Domain.Route53 は
			// さらに RecordSet に展開される
			if d := childNode(&r.Properties, "Domain"); d != nil {
				f.HasCustomDomain = true
				if hasKey(d, "Route53") {
					f.HasRoute53 = true
				}
			}
		case "AWS::Lambda::EventSourceMapping":
			f.HasEventSourceMapping = true
		case "AWS::Route53::RecordSet", "AWS::Route53::RecordSetGroup":
			f.HasRoute53 = true
		case "AWS::ApiGatewayV2::DomainName", "AWS::ApiGateway::DomainName":
			f.HasCustomDomain = true
		}
	}
	for k := range unknown {
		f.Unknown = append(f.Unknown, k)
	}
	sort.Strings(f.Unknown)
	scanDynamicRefs(body, &f)
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

// eventSourceMappingTypes は SAM の Events のうち、展開すると
// AWS::Lambda::EventSourceMapping になるもの。ここにある型を使った関数は
// テンプレート本文に EventSourceMapping と 1 文字も書かれないのに
// マッピングの権限を要求する。
var eventSourceMappingTypes = map[string]bool{
	"SQS": true, "DynamoDB": true, "Kinesis": true,
	"MSK": true, "MQ": true, "SelfManagedKafka": true, "DocumentDB": true,
}

// childNode は mapping ノードの直下の値ノードを返す。無ければ nil。
func childNode(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// usesEventSourceMapping は Properties.Events に SQS 等の
// ポーリング型イベントがあるかを見る。
func usesEventSourceMapping(props *yaml.Node) bool {
	events := childNode(props, "Events")
	if events == nil || events.Kind != yaml.MappingNode {
		return false
	}
	for i := 1; i < len(events.Content); i += 2 {
		t := childNode(events.Content[i], "Type")
		if t != nil && eventSourceMappingTypes[t.Value] {
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

// nodeValue はマッピングの key に対応するスカラ値を返す(無ければ "")。
func nodeValue(n *yaml.Node, key string) string {
	if n == nil || n.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1].Value
		}
	}
	return ""
}

// tmplParameter は Parameters の 1 つ。型と既定値だけ見る。
type tmplParameter struct {
	Type    string `yaml:"Type"`
	Default string `yaml:"Default"`
}

// ssmParamTypeRe は SSM パラメータ型。List 版・型指定版も同じ扱い。
//
//	AWS::SSM::Parameter::Value<String>
//	AWS::SSM::Parameter::Value<List<AWS::EC2::Subnet::Id>>
var ssmParamTypeRe = regexp.MustCompile(`^AWS::SSM::Parameter::Value<.+>$`)

// scanSSMParameterTypes は SSM パラメータ型の Default にあるパスを集める。
//
// 動的参照(#171)とは書き方が違うだけで、CFN がデプロイ時にデプロイロールの
// 資格情報で読む点は同じ。拾わないと ssm:GetParameters が出ず、**スタックの
// 作成そのものが 400 で落ちる**(compute: ecs の雛形がこの形)。
func scanSSMParameterTypes(params map[string]tmplParameter, f *TemplateFacts) {
	seen := map[string]bool{}
	for _, p := range f.SSMParams {
		seen[p] = true
	}
	for _, p := range params {
		if !ssmParamTypeRe.MatchString(strings.TrimSpace(p.Type)) {
			continue
		}
		path := strings.TrimSpace(p.Default)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		f.SSMParams = append(f.SSMParams, path)
	}
	sort.Strings(f.SSMParams)
}
