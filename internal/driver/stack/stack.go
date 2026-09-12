// Package stack は CloudFormation スタック 1 個 = 環境 1 個として管理する
// driver(DESIGN.md §2)。テンプレートは packaged 済み(sam build/package は
// CI の仕事)を前提に、Resources の中身は解釈しない。
//
// env の届け方(docs/CONTRACT.md §4): テンプレートが `Env<Key>` パラメータを
// 宣言していればそこへ流す。宣言が受け取り口。宣言の無い env キーはエラー。
package stack

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// タグスキーマは docs/CONTRACT.md §1 で凍結。list / reap(#5, #6)も走査に使う。
const (
	TagManaged   = "kagerou:managed"
	TagName      = "kagerou:name"
	TagProject   = "kagerou:project"
	TagDriver    = "kagerou:driver"
	TagExpiresAt = "kagerou:expires-at" // RFC3339 UTC または "none"
	TagSource    = "kagerou:source"
	TagVersion   = "kagerou:version"
)

const DriverName = "stack"

// TTLNoneTagValue は kagerou:expires-at の「明示的な無期限」。
const TTLNoneTagValue = "none"

// URL を返す CFN Output のキー(優先順)。docs/CONTRACT.md §5。
var urlOutputKeys = []string{"KagerouUrl", "PreviewUrl"}

const waitTimeout = 30 * time.Minute

// MaxLifetime は touch(up ごとの TTL 延長)の上限。初回作成からこれを超えて
// 延ばせない(無限延長の防止。DESIGN §8)。--ttl none の明示無期限には適用しない。
const MaxLifetime = 30 * 24 * time.Hour

type Driver struct {
	cfn *cloudformation.Client
}

func New(ctx context.Context, region string) (*Driver, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &Driver{cfn: cloudformation.NewFromConfig(cfg)}, nil
}

type UpInput struct {
	StackName    string
	Name         string
	Project      string // 空なら kagerou:project タグを付けない
	TemplateBody string
	Params       map[string]string
	Env          map[string]string // Env<Key> パラメータへ流す(CONTRACT §4)
	ExpiresAt    *time.Time        // nil = TTL なし(タグは "none")
	Source       string            // opaque JSON。空なら省略
	Version      string            // kagerou 自身のバージョン
	Tags         map[string]string // kagerou.yaml の追加タグ
}

// Info は環境の観測結果。Environment JSON(CONTRACT §3)の材料になる。
type Info struct {
	StackName    string
	Status       string
	CreationTime time.Time
	Outputs      map[string]string
	Tags         map[string]string
}

// tagValueRe は CFN タグ値に使える文字(moto は検証しないが実 AWS は拒否する)。
var tagValueRe = regexp.MustCompile(`^[a-zA-Z0-9 +\-=._:/@]*$`)

// ValidateSource は kagerou:source の形式を検証する(CONTRACT §1)。
// CFN に渡してから 400 で死ぬより先に、分かるエラーで止める。
func ValidateSource(s string) error {
	if len(s) > 256 {
		return fmt.Errorf("--source too long (tag values max 256 chars): %d chars", len(s))
	}
	if !tagValueRe.MatchString(s) {
		return fmt.Errorf("--source %q contains characters not allowed in CFN tag values (alphanumerics and ' +-=._:/@' only; JSON is not allowed, URI style recommended: github_pr://owner/repo/42)", s)
	}
	return nil
}

// Up は冪等に環境を作成/更新する。進行中のスタックには完了を待ってから重ねる。
func (d *Driver) Up(ctx context.Context, in UpInput) (*Info, error) {
	if err := ValidateSource(in.Source); err != nil {
		return nil, err
	}
	params, err := d.buildAllParams(ctx, in)
	if err != nil {
		return nil, err
	}

	status, err := d.waitUntilStable(ctx, in.StackName)
	if err != nil {
		return nil, err
	}

	// 初回 create の失敗残骸は update できないため、削除してから作り直す
	if status == string(cfntypes.StackStatusRollbackComplete) || status == string(cfntypes.StackStatusRollbackFailed) {
		if err := d.Down(ctx, in.StackName); err != nil {
			return nil, fmt.Errorf("deleting previously failed stack: %w", err)
		}
		status = ""
	}

	// touch の上限: 初回作成(存在しなければ今)から MaxLifetime を超えない
	if in.ExpiresAt != nil {
		base := time.Now()
		if status != "" {
			if info, err := d.Info(ctx, in.StackName); err == nil {
				base = info.CreationTime
			}
		}
		if limit := base.Add(MaxLifetime); in.ExpiresAt.After(limit) {
			in.ExpiresAt = &limit
		}
	}

	tags := buildTags(in)
	caps := []cfntypes.Capability{
		cfntypes.CapabilityCapabilityIam,
		cfntypes.CapabilityCapabilityNamedIam,
		cfntypes.CapabilityCapabilityAutoExpand,
	}

	if status == "" { // 存在しない → create
		_, err = d.cfn.CreateStack(ctx, &cloudformation.CreateStackInput{
			StackName:    &in.StackName,
			TemplateBody: &in.TemplateBody,
			Parameters:   params,
			Tags:         tags,
			Capabilities: caps,
		})
		if err != nil {
			return nil, fmt.Errorf("create stack: %w", err)
		}
		w := cloudformation.NewStackCreateCompleteWaiter(d.cfn)
		if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: &in.StackName}, waitTimeout); err != nil {
			return nil, fmt.Errorf("create stack %s: %w", in.StackName, err)
		}
	} else { // 存在する → update(差分なしは成功扱い)
		_, err = d.cfn.UpdateStack(ctx, &cloudformation.UpdateStackInput{
			StackName:    &in.StackName,
			TemplateBody: &in.TemplateBody,
			Parameters:   params,
			Tags:         tags,
			Capabilities: caps,
		})
		if err != nil {
			if isNoUpdateErr(err) {
				return d.Info(ctx, in.StackName)
			}
			return nil, fmt.Errorf("update stack: %w", err)
		}
		w := cloudformation.NewStackUpdateCompleteWaiter(d.cfn)
		if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: &in.StackName}, waitTimeout); err != nil {
			return nil, fmt.Errorf("update stack %s: %w", in.StackName, err)
		}
	}
	return d.Info(ctx, in.StackName)
}

// Down は冪等に環境を削除する。存在しなければ成功。
func (d *Driver) Down(ctx context.Context, stackName string) error {
	status, err := d.stackStatus(ctx, stackName)
	if err != nil {
		return err
	}
	if status == "" {
		return nil
	}
	if _, err := d.cfn.DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: &stackName}); err != nil {
		return fmt.Errorf("delete stack: %w", err)
	}
	w := cloudformation.NewStackDeleteCompleteWaiter(d.cfn)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: &stackName}, waitTimeout); err != nil {
		return fmt.Errorf("delete stack %s: %w", stackName, err)
	}
	return nil
}

// Info は環境の現在の観測結果を返す。
func (d *Driver) Info(ctx context.Context, stackName string) (*Info, error) {
	out, err := d.cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: &stackName})
	if err != nil || len(out.Stacks) == 0 {
		return nil, fmt.Errorf("describe stack %s: %w", stackName, err)
	}
	return infoFromStack(out.Stacks[0]), nil
}

// List は kagerou 管理(kagerou:managed=true)のスタックを列挙する。
// v0.1 は stack driver のみなので CFN の走査で足りる。Resource Groups
// Tagging API への切り替えは非 CFN リソースを持つ driver が入るとき(§8)。
func (d *Driver) List(ctx context.Context) ([]*Info, error) {
	var infos []*Info
	p := cloudformation.NewDescribeStacksPaginator(d.cfn, &cloudformation.DescribeStacksInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe stacks: %w", err)
		}
		for _, s := range page.Stacks {
			info := infoFromStack(s)
			if info.Tags[TagManaged] != "true" || s.StackStatus == cfntypes.StackStatusDeleteComplete {
				continue
			}
			infos = append(infos, info)
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Tags[TagName] < infos[j].Tags[TagName] })
	return infos, nil
}

// Expired は kagerou:expires-at を過ぎた環境か判定する(reap の判定部)。
// grace は削除までの猶予。タグが "none"・欠落・解釈不能なら回収しない
// (壊れたタグで環境を消すより、残して人間に見せる方が安全)。
func (i *Info) Expired(now time.Time, grace time.Duration) bool {
	v := i.Tags[TagExpiresAt]
	if v == "" || v == TTLNoneTagValue {
		return false
	}
	exp, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return false
	}
	return now.After(exp.Add(grace))
}

func infoFromStack(s cfntypes.Stack) *Info {
	info := &Info{
		StackName:    aws.ToString(s.StackName),
		Status:       string(s.StackStatus),
		CreationTime: aws.ToTime(s.CreationTime),
		Outputs:      map[string]string{},
		Tags:         map[string]string{},
	}
	for _, o := range s.Outputs {
		info.Outputs[aws.ToString(o.OutputKey)] = aws.ToString(o.OutputValue)
	}
	for _, t := range s.Tags {
		info.Tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return info
}

// State は CFN ステータスを Environment JSON の state(CONTRACT §3)に写す。
func (i *Info) State() string {
	switch {
	case strings.HasPrefix(i.Status, "CREATE_IN_PROGRESS"):
		return "creating"
	case strings.HasPrefix(i.Status, "DELETE_"):
		return "deleting"
	case strings.Contains(i.Status, "ROLLBACK") || strings.Contains(i.Status, "FAILED"):
		return "failed"
	case strings.HasSuffix(i.Status, "_IN_PROGRESS"):
		return "updating"
	default:
		return "ready"
	}
}

// URL は Outputs から環境 URL を規約キーで探す。
func URL(outputs map[string]string) (string, bool) {
	for _, k := range urlOutputKeys {
		if v, ok := outputs[k]; ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// EnvParamName は env キーを CFN パラメータ名に写す(CONTRACT §4)。
// 例: DB_HOST → EnvDbHost。CFN パラメータ名に `_` が使えないための規約。
func EnvParamName(key string) (string, error) {
	if key == "" {
		return "", errors.New("env key is empty")
	}
	var b strings.Builder
	b.WriteString("Env")
	for _, part := range strings.Split(key, "_") {
		if part == "" {
			continue
		}
		for _, r := range part {
			if !isAlnum(r) {
				return "", fmt.Errorf("env key %q contains invalid character %q (alphanumerics and _ only)", key, r)
			}
		}
		lower := strings.ToLower(part)
		b.WriteString(strings.ToUpper(lower[:1]) + lower[1:])
	}
	return b.String(), nil
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// buildAllParams は --param に加え、env を Env<Key> パラメータへ写して合流する。
// テンプレートが宣言していない env キーは黙殺せずエラー(CONTRACT §4)。
func (d *Driver) buildAllParams(ctx context.Context, in UpInput) ([]cfntypes.Parameter, error) {
	merged := map[string]string{}
	for k, v := range in.Params {
		merged[k] = v
	}
	if len(in.Env) > 0 {
		declared, err := d.templateParams(ctx, in.TemplateBody)
		if err != nil {
			return nil, err
		}
		var missing []string
		for k, v := range in.Env {
			pname, err := EnvParamName(k)
			if err != nil {
				return nil, err
			}
			if !declared[pname] {
				missing = append(missing, fmt.Sprintf("%s (parameter %s)", k, pname))
				continue
			}
			merged[pname] = v
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("template declares no receiving parameter for env: %s — add Env<Key> to the template Parameters (docs/CONTRACT.md §4)", strings.Join(missing, ", "))
		}
	}
	var out []cfntypes.Parameter
	for k, v := range merged {
		out = append(out, cfntypes.Parameter{ParameterKey: aws.String(k), ParameterValue: aws.String(v)})
	}
	return out, nil
}

func (d *Driver) templateParams(ctx context.Context, body string) (map[string]bool, error) {
	sum, err := d.cfn.GetTemplateSummary(ctx, &cloudformation.GetTemplateSummaryInput{TemplateBody: &body})
	if err != nil {
		return nil, fmt.Errorf("get template summary: %w", err)
	}
	declared := map[string]bool{}
	for _, p := range sum.Parameters {
		declared[aws.ToString(p.ParameterKey)] = true
	}
	return declared, nil
}

// waitUntilStable は *_IN_PROGRESS のスタックが落ち着くまで待つ(同名 up の
// 同時実行対策)。返り値は安定後のステータス(存在しなければ空)。
func (d *Driver) waitUntilStable(ctx context.Context, stackName string) (string, error) {
	deadline := time.Now().Add(waitTimeout)
	for {
		status, err := d.stackStatus(ctx, stackName)
		if err != nil {
			return "", err
		}
		if !strings.HasSuffix(status, "_IN_PROGRESS") {
			return status, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("stack %s stuck in %s", stackName, status)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// stackStatus は存在しなければ空文字を返す。
func (d *Driver) stackStatus(ctx context.Context, stackName string) (string, error) {
	out, err := d.cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: &stackName})
	if err != nil {
		if isNotExistErr(err) {
			return "", nil
		}
		return "", fmt.Errorf("describe stack %s: %w", stackName, err)
	}
	if len(out.Stacks) == 0 || out.Stacks[0].StackStatus == cfntypes.StackStatusDeleteComplete {
		return "", nil
	}
	return string(out.Stacks[0].StackStatus), nil
}

func buildTags(in UpInput) []cfntypes.Tag {
	expires := TTLNoneTagValue
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.UTC().Format(time.RFC3339)
	}
	kv := map[string]string{
		TagManaged:   "true",
		TagName:      in.Name,
		TagDriver:    DriverName,
		TagExpiresAt: expires,
	}
	if in.Project != "" {
		kv[TagProject] = in.Project
	}
	if in.Source != "" {
		kv[TagSource] = in.Source
	}
	if in.Version != "" {
		kv[TagVersion] = in.Version
	}
	for k, v := range in.Tags {
		kv[k] = v
	}
	tags := make([]cfntypes.Tag, 0, len(kv))
	for k, v := range kv {
		tags = append(tags, cfntypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return tags
}

func isNoUpdateErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No updates are to be performed")
}

func isNotExistErr(err error) bool {
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationError" {
		return strings.Contains(err.Error(), "does not exist")
	}
	return false
}
