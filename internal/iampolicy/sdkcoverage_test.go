package iampolicy_test

// コードが呼ぶ AWS API を、生成器が覆っているかを機械的に確かめる。
//
// **なぜ要るか。** 生成器は手で維持したリストで、足りない権限は
// 「絞ったロールで 403 を踏む → 足す」の繰り返しで見つけてきた。踏む場所は
// たいてい schedule の reap(人が見ていない時間)で、admin が付いたロールでは
// 一生出ない。実際に 1 日で 3 回足した。
//
// このテストは **新しい SDK 呼び出しを書いた時点で落ちる**。403 を踏むより前に、
// 「権限を足すか、足さない理由を書くか」の判断を強制する。
//
// **何を捕まえ、何を捕まえないか。** 同じ日に踏んだ 3 件で言うとこうなる。
//
//	tag:GetResources       どの statement にも無い          → **捕まえる**
//	s3:GetBucketLocation   別リソース(SAM バケット)には有る → 捕まえない
//	peer の DescribeStacks  action は有り Resource が狭い     → 捕まえない
//
// アクション名の集合しか見ないので、**別のリソースに対して同じアクションが
// 許されていると覆い隠される**。1 つ目だけが取れる範囲で、そこは正直に諦める。
// リソースまで見るには呼び出しごとに対象 ARN を静的に決める必要があり、
// 現実的でない。
//
// 残る 2 つは **絞ったロールで実際に回す**しかない。iam-policy-live が実ロールと
// 生成器を突き合わせ、e2e-aws が最小権限で本物の AWS を触る。この 3 段で見る。
import (
	"fmt"
	"go/ast"
	"go/types"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/rikukadev/kagerou/internal/iampolicy"
)

// sdkPath は aws-sdk-go-v2 のサービスパッケージを見分ける。
var sdkPath = regexp.MustCompile(`aws-sdk-go-v2/service/([a-z0-9]+)$`)

// paginatorName は NewXxxPaginator から操作名を取る。
var paginatorName = regexp.MustCompile(`^New(.+)Paginator$`)

// iamPrefix は SDK のパッケージ名を IAM の service prefix に写す。
// 大半は同じだが、違うものがある(ここを間違えると照合が静かに空振りする)。
var iamPrefix = map[string]string{
	"resourcegroupstaggingapi": "tag",
	"costexplorer":             "ce",
	"elasticloadbalancingv2":   "elasticloadbalancing",
}

// apiToAction は **API 名と IAM アクション名が違う** ものを写す。
// S3 に多い。ここを書かないと「呼んでいるのに権限が無い」と誤報する。
//
//	ListObjectsV2 / ListObjects  バケットの一覧は s3:ListBucket
//	HeadBucket                   同じく s3:ListBucket
//	DeleteObjects(複数)         権限は単数の s3:DeleteObject
//	HeadObject                   s3:GetObject
var apiToAction = map[string]string{
	"s3:ListObjectsV2": "s3:ListBucket",
	"s3:ListObjects":   "s3:ListBucket",
	"s3:HeadBucket":    "s3:ListBucket",
	"s3:DeleteObjects": "s3:DeleteObject",
	"s3:HeadObject":    "s3:GetObject",
}

// allowed は「呼ぶが CI ロールのポリシーには出さない」もの。**理由を必ず書く。**
// 空の許可リストにすると、足すべきものを黙って通す道になる。
var allowed = map[string]string{
	"sts:GetCallerIdentity": "IAM の権限を必要としない(誰でも呼べる)。owner タグの解決に使う",

	// kagerou cost は利用者が手元で回す横断ビューで、CI ロールには出さない。
	// 出すと「プレビュー環境を作る権限」に課金データの読み取りが混ざる。
	"ce:GetCostAndUsage": "kagerou cost 専用。CI ロールには出さない(課金データは別の権限境界)",

	// --check-role が実ロールを読むための権限。これは点検する側の権限で、
	// デプロイには要らない。要る環境では別の inline policy で与える
	// (e2e/aws/selfcheck-policy.json)。
	"iam:ListRolePolicies":         "--check-role が実ロールを読むための権限。デプロイには要らない",
	"iam:ListAttachedRolePolicies": "--check-role が実ロールを読むための権限。デプロイには要らない",
	"iam:GetPolicy":                "--check-role が attached policy を読むための権限。デプロイには要らない",
	"iam:GetPolicyVersion":         "--check-role が attached policy を読むための権限。デプロイには要らない",

	// preflight は「権限が足りているか」を事前に見る道具で、この API 自体が
	// 無い環境が普通にある(コード側もそう書いて失敗を握っている)。
	// CI ロールに足すと、権限を調べるための権限を配ることになる。
	"iam:SimulatePrincipalPolicy": "preflight の事前確認。無い環境が普通にあり、コード側も失敗を握っている",

	// kagerou capacity が共有リスナーの上限を見るのに使う。利用者が手元で
	// 回す確認コマンドで、プレビューの作成経路には入らない。
	"elasticloadbalancing:DescribeAccountLimits": "kagerou capacity 専用。プレビューの作成経路では呼ばない",
}

func TestGeneratedPolicyCoversSDKCalls(t *testing.T) {
	calls, err := sdkCallsInRepo()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) < 10 {
		// 解析が壊れて 0 件になっても緑になってしまうので、下限を置く
		t.Fatalf("SDK 呼び出しの検出が少なすぎる(%d 件)。解析が壊れている", len(calls))
	}

	granted := unionOfGeneratedActions(t)

	var missing []string
	for raw, where := range calls {
		action := raw
		if mapped, ok := apiToAction[action]; ok {
			action = mapped
		}
		if reason, ok := allowed[action]; ok {
			if reason == "" {
				t.Errorf("%s: allowed に入っているが理由が空", action)
			}
			continue
		}
		if !coveredBy(action, granted) {
			missing = append(missing, fmt.Sprintf("%s\t(%s)", action, where))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf(`コードが呼ぶのに生成ポリシーが出さない API があります:

%s

どちらかを選んでください:
  - internal/iampolicy に statement を足す(絞ったロールで 403 になる側)
  - このテストの allowed に **理由つきで** 足す(CI ロールには出さないと決める側)`,
			strings.Join(missing, "\n"))
	}
}

// unionOfGeneratedActions は生成器が出しうるアクションを全部集める。
// 構成ごとに出る statement が違うので、フラグを全部立てた和集合で見る。
func unionOfGeneratedActions(t *testing.T) map[string]bool {
	t.Helper()
	// **テンプレート由来の statement も入れる。** リソース型から導く分
	// (ALB / DynamoDB / SNS / SQS / ECS ...)を入れないと、union が本当の
	// 上限にならず「覆っているのに不足」と誤報する。実際に ALB で誤報した。
	facts := &iampolicy.TemplateFacts{
		Counts:                map[string]int{},
		HasVPC:                true,
		HasImage:              true,
		HasEventSourceMapping: true,
		HasRoute53:            true,
		HasCustomDomain:       true,
		SSMParams:             []string{"/kagerou/base/x/alb_listener_arn"},
		HasSecureSSM:          true,
		Secrets:               []string{"x/secret"},
	}
	for _, typ := range iampolicy.KnownResourceTypes() {
		facts.Counts[typ] = 1
	}
	opts := iampolicy.Options{
		Prefix:       "x-",
		Template:     facts,
		ECR:          true,
		EcrRepo:      "repo",
		SashikiSSM:   true,
		InstanceTag:  "Name=db",
		CloudFront:   true,
		Route53:      true,
		HostedZoneID: "Z123",
		BaseBucket:   "bucket",
		BaseDomain:   true,
		S3:           true,
		VPC:          true,
		PeerPrefix:   "peer-",
		AllProjects:  true,
	}
	pol, err := iampolicy.Build(opts)
	if err != nil {
		t.Fatalf("生成できない: %v", err)
	}
	return iampolicy.PolicyAllowActions(pol)
}

// coveredBy は granted のどれか(ワイルドカード込み)が action を覆うかを返す。
func coveredBy(action string, granted map[string]bool) bool {
	if granted[action] {
		return true
	}
	lower := strings.ToLower(action)
	for pat := range granted {
		if !strings.Contains(pat, "*") {
			continue
		}
		if globMatch(strings.ToLower(pat), lower) {
			return true
		}
	}
	return false
}

func globMatch(pat, s string) bool {
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(s) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == s[si]):
			pi, si = pi+1, si+1
		case pi < len(pat) && pat[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi, mark = star+1, mark+1
			si = mark
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}

// sdkCallsInRepo は "service:Operation" -> どこで呼んでいるか を返す。
//
// 3 通りの書き方を拾う:
//
//  1. *service.Client のメソッド呼び出し(d.cfn.DescribeStacks など)
//  2. service.NewXxxPaginator(ページネータ。中の操作は名前から分かる)
//  3. 手書きインターフェースのメソッド宣言(引数か戻り値が SDK の型)。
//     テストで差し替えるために切ったインターフェースは 1 で拾えないため。
func sdkCallsInRepo() (map[string]string, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Dir:   "../..",
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	record := func(svc, op, where string) {
		if svc == "" || op == "" {
			return
		}
		prefix := svc
		if p, ok := iamPrefix[svc]; ok {
			prefix = p
		}
		key := prefix + ":" + op
		if _, seen := out[key]; !seen {
			out[key] = where
		}
	}

	for _, p := range pkgs {
		if p.TypesInfo == nil {
			continue
		}
		for _, f := range p.Syntax {
			pos := func(n ast.Node) string {
				return fmt.Sprintf("%s:%d", p.Name, p.Fset.Position(n.Pos()).Line)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					sel, ok := node.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					// (2) パッケージ名 + NewXxxPaginator
					if id, ok := sel.X.(*ast.Ident); ok {
						if pkgName, ok := p.TypesInfo.Uses[id].(*types.PkgName); ok {
							if m := sdkPath.FindStringSubmatch(pkgName.Imported().Path()); m != nil {
								if pm := paginatorName.FindStringSubmatch(sel.Sel.Name); pm != nil {
									record(m[1], pm[1], pos(node))
								}
							}
							return true
						}
					}
					// (1) *service.Client のメソッド
					if svc := sdkClientService(p.TypesInfo.TypeOf(sel.X)); svc != "" {
						record(svc, sel.Sel.Name, pos(node))
					}
				case *ast.InterfaceType:
					// (3) 手書きインターフェース
					for _, m := range node.Methods.List {
						ft, ok := m.Type.(*ast.FuncType)
						if !ok || len(m.Names) == 0 {
							continue
						}
						if svc := sdkInSignature(p.TypesInfo, ft); svc != "" {
							record(svc, m.Names[0].Name, pos(m))
						}
					}
				}
				return true
			})
		}
	}
	return out, nil
}

// sdkClientService は型が *service.Client なら service 名を返す。
func sdkClientService(t types.Type) string {
	if t == nil {
		return ""
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Name() != "Client" || named.Obj().Pkg() == nil {
		return ""
	}
	if m := sdkPath.FindStringSubmatch(named.Obj().Pkg().Path()); m != nil {
		return m[1]
	}
	return ""
}

// sdkInSignature はシグネチャに現れる SDK パッケージ名を返す。
// 手書きインターフェースの識別に使う(引数が service.XxxInput なら、その service)。
func sdkInSignature(info *types.Info, ft *ast.FuncType) string {
	var found string
	visit := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			ast.Inspect(f.Type, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if pkgName, ok := info.Uses[id].(*types.PkgName); ok {
					if m := sdkPath.FindStringSubmatch(pkgName.Imported().Path()); m != nil {
						found = m[1]
					}
				}
				return true
			})
		}
	}
	visit(ft.Params)
	visit(ft.Results)
	return found
}
