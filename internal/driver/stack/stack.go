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

// Up は冪等に環境を作成/更新する。進行中のスタックには完了を待ってから重ねる。
func (d *Driver) Up(ctx context.Context, in UpInput) (*Info, error) {
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
			return nil, fmt.Errorf("前回失敗したスタックの削除: %w", err)
		}
		status = ""
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
	s := out.Stacks[0]
	info := &Info{
		StackName:    stackName,
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
	return info, nil
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
		return "", errors.New("env キーが空")
	}
	var b strings.Builder
	b.WriteString("Env")
	for _, part := range strings.Split(key, "_") {
		if part == "" {
			continue
		}
		for _, r := range part {
			if !isAlnum(r) {
				return "", fmt.Errorf("env キー %q に使えない文字 %q(英数字と _ のみ)", key, r)
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
				missing = append(missing, fmt.Sprintf("%s(パラメータ %s)", k, pname))
				continue
			}
			merged[pname] = v
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("テンプレートに受け取り口が宣言されていない env: %s — template の Parameters に Env<Key> を追加する(docs/CONTRACT.md §4)", strings.Join(missing, ", "))
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
			return "", fmt.Errorf("stack %s が %s のまま安定しない", stackName, status)
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
