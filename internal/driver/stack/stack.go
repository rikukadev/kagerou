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
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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
	TagURL       = "kagerou:url"   // url_template で作成前に確定した URL(CONTRACT §5)
	TagOwner     = "kagerou:owner" // 作成者の IAM プリンシパル。up の上書きは同一 owner のみ
)

const DriverName = "stack"

// TTLNoneTagValue は kagerou:expires-at の「明示的な無期限」。
const TTLNoneTagValue = "none"

// URL を返す CFN Output のキー(優先順)。docs/CONTRACT.md §5。
var urlOutputKeys = []string{"KagerouUrl", "PreviewUrl"}

const waitTimeout = 30 * time.Minute

// progressInterval は待機中に進捗を stderr へ出す間隔。SDK の waiter は無言で
// ブロックするため、VPC Lambda の ENI 削除待ち(数分〜十数分)がハングと区別
// できない(#47)。この間隔ごとに「今どのリソースで待っているか + 経過」を出す。
const progressInterval = 30 * time.Second

// MaxLifetime は touch(up ごとの TTL 延長)の上限。初回作成からこれを超えて
// 延ばせない(無限延長の防止。DESIGN §8)。--ttl none の明示無期限には適用しない。
const MaxLifetime = 30 * 24 * time.Hour

type Driver struct {
	cfn *cloudformation.Client
	sts *sts.Client
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
	return &Driver{cfn: cloudformation.NewFromConfig(cfg), sts: sts.NewFromConfig(cfg)}, nil
}

// callerOwner は呼び出し元の IAM プリンシパルを kagerou:owner 用に返す。
// assumed-role はセッション名が呼び出しごとに変わるため、ロール ARN に正規化する
// (CI は全員同じデプロイロール = 同一 owner になり、PR への push で更新が続く)。
func (d *Driver) callerOwner(ctx context.Context) (string, error) {
	out, err := d.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("resolving caller identity for the owner check: %w", err)
	}
	return normalizeOwner(aws.ToString(out.Arn)), nil
}

// normalizeOwner は arn:aws:sts::123:assumed-role/Role/session を
// arn:aws:iam::123:role/Role に畳む。その他の ARN はそのまま。
func normalizeOwner(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[2] != "sts" || !strings.HasPrefix(parts[5], "assumed-role/") {
		return arn
	}
	seg := strings.Split(parts[5], "/") // assumed-role/Role/session
	if len(seg) < 2 {
		return arn
	}
	return fmt.Sprintf("arn:%s:iam::%s:role/%s", parts[1], parts[4], seg[1])
}

// checkOwner は既存環境を上書きしてよいか判定する。owner タグが無い(旧環境)か
// 呼び出し元と同じなら OK。他人の環境なら名前衝突としてエラーにする(#77 議論)。
func checkOwner(existingTags map[string]string, caller string) error {
	owner := existingTags[TagOwner]
	if owner == "" || owner == caller {
		return nil
	}
	return fmt.Errorf("environment %q already exists and is owned by %s (you are %s): pick another --name, or ask the owner to run `kagerou down` (TTL + reap will also collect it eventually)",
		existingTags[TagName], owner, caller)
}

type UpInput struct {
	StackName    string
	Name         string
	Project      string // 空なら kagerou:project タグを付けない
	TemplateBody string
	Params       map[string]string
	Env          map[string]string // Env<Key> パラメータへ流す(CONTRACT §4)
	ExpiresAt    *time.Time        // nil = TTL なし(タグは "none")
	URL          string            // url_template で確定した URL。空なら Output に任せる
	Source       string            // opaque JSON。空なら省略
	Version      string            // kagerou 自身のバージョン
	Tags         map[string]string // kagerou.yaml の追加タグ
	MaxLifetime  time.Duration     // touch の上限。0 なら既定の MaxLifetime 定数(#51)
	PeerEnv      string            // peer 連動の解決結果(#99)。EnvPeerEnv に宣言時のみ配送
	PeerURL      string            // 同・相手 env の URL。EnvPeerUrl に宣言時のみ配送

	owner string // Up が STS から解決して埋める(kagerou:owner タグ)
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

	owner, err := d.callerOwner(ctx)
	if err != nil {
		return nil, err
	}
	in.owner = owner

	// 既存環境がある場合は所有者チェック: 他人の環境なら名前衝突としてエラー、
	// 自分の(または owner タグの無い旧)環境なら従来どおり冪等に上書きする。
	// rollback 残骸の削除より前に見る — 他人の失敗スタックを消さないため。
	var existing *Info
	if status != "" {
		if info, err := d.Info(ctx, in.StackName); err == nil {
			existing = info
			if err := checkOwner(info.Tags, owner); err != nil {
				return nil, err
			}
		}
	}

	// 初回 create の失敗残骸は update できないため、削除してから作り直す
	if status == string(cfntypes.StackStatusRollbackComplete) || status == string(cfntypes.StackStatusRollbackFailed) {
		if err := d.Down(ctx, in.StackName); err != nil {
			return nil, fmt.Errorf("deleting previously failed stack: %w", err)
		}
		status = ""
	}

	// touch の上限: 初回作成(存在しなければ今)から maxLife を超えない。
	// maxLife は kagerou.yaml の max_lifetime(未設定なら既定定数)(#51)。
	maxLife := MaxLifetime
	if in.MaxLifetime > 0 {
		maxLife = in.MaxLifetime
	}
	if in.ExpiresAt != nil {
		base := time.Now()
		if status != "" && existing != nil {
			base = existing.CreationTime
		}
		if limit := base.Add(maxLife); in.ExpiresAt.After(limit) {
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
		stop := d.startProgress(ctx, "creating", in.StackName)
		err = w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: &in.StackName}, waitTimeout)
		stop()
		if err != nil {
			return nil, fmt.Errorf("create stack %s: %w", in.StackName, err)
		}
	} else { // 存在する → update(差分なしは成功扱い)
		updateInput := &cloudformation.UpdateStackInput{
			StackName:    &in.StackName,
			TemplateBody: &in.TemplateBody,
			Parameters:   params,
			Tags:         tags,
			Capabilities: caps,
		}
		_, err = d.cfn.UpdateStack(ctx, updateInput)
		// 冒頭の waitUntilStable と UpdateStack の間に別プロセスが更新を始めると
		// 「*_IN_PROGRESS で更新できない」で弾かれる。安定を待って 1 回だけ再試行する(#51)。
		if err != nil && !isNoUpdateErr(err) && isInProgressErr(err) {
			if _, werr := d.waitUntilStable(ctx, in.StackName); werr == nil {
				_, err = d.cfn.UpdateStack(ctx, updateInput)
			}
		}
		if err != nil {
			if isNoUpdateErr(err) {
				return d.Info(ctx, in.StackName)
			}
			return nil, fmt.Errorf("update stack: %w", err)
		}
		w := cloudformation.NewStackUpdateCompleteWaiter(d.cfn)
		stop := d.startProgress(ctx, "updating", in.StackName)
		err = w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: &in.StackName}, waitTimeout)
		stop()
		if err != nil {
			return nil, d.explainWaitFailure(ctx, in.StackName, "update", err)
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
	stop := d.startProgress(ctx, "deleting", stackName)
	err = w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: &stackName}, waitTimeout)
	stop()
	if err != nil {
		return fmt.Errorf("delete stack %s: %w", stackName, err)
	}
	return nil
}

// startProgress は待機中、progressInterval ごとに「今どのリソースで待っているか +
// 経過時間」を stderr に出す goroutine を起動し、停止用の関数を返す(#47)。
// 停止時、待機が 1 間隔を超えていたら完了行も出す(それ以下なら黙る)。
func (d *Driver) startProgress(ctx context.Context, op, stackName string) func() {
	start := time.Now()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(progressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				elapsed := time.Since(start).Round(time.Second)
				if res := d.inProgressResource(ctx, stackName); res != "" {
					fmt.Fprintf(os.Stderr, "kagerou: %s %s — waiting on %s (%s)\n", op, stackName, res, elapsed)
				} else {
					fmt.Fprintf(os.Stderr, "kagerou: %s %s (%s)\n", op, stackName, elapsed)
				}
			}
		}
	}()
	return func() {
		close(done)
		if el := time.Since(start); el >= progressInterval {
			fmt.Fprintf(os.Stderr, "kagerou: %s %s done (%s)\n", op, stackName, el.Round(time.Second))
		}
	}
}

// inProgressResource は今まさに *_IN_PROGRESS のリソースを 1 つ返す(新しい順で最初)。
// 取れなければ空文字。VPC Lambda の削除では Function/ENI が DELETE_IN_PROGRESS で出るので、
// 「ENI 待ち」だと利用者が気づける。スタック自身のイベントは除く。
func (d *Driver) inProgressResource(ctx context.Context, stackName string) string {
	out, err := d.cfn.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{StackName: &stackName})
	if err != nil || len(out.StackEvents) == 0 {
		return ""
	}
	return firstInProgress(out.StackEvents)
}

// firstInProgress は新しい順のイベント列から、最新イベントが *_IN_PROGRESS の
// 非スタックリソースを 1 つ返す。同一リソースは最新イベントだけで判定する。
func firstInProgress(events []cfntypes.StackEvent) string {
	seen := map[string]bool{}
	for _, e := range events {
		id := aws.ToString(e.LogicalResourceId)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if aws.ToString(e.ResourceType) == "AWS::CloudFormation::Stack" {
			continue
		}
		if strings.HasSuffix(string(e.ResourceStatus), "_IN_PROGRESS") {
			return fmt.Sprintf("%s %s (%s)", aws.ToString(e.ResourceType), id, e.ResourceStatus)
		}
	}
	return ""
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

// EnvironmentURL は環境 URL を解決する。優先順: kagerou:url タグ
// (url_template で作成前に確定)> KagerouUrl > PreviewUrl(CONTRACT §5)。
func (i *Info) EnvironmentURL() (string, bool) {
	if u := i.Tags[TagURL]; u != "" {
		return u, true
	}
	return URL(i.Outputs)
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
	if len(in.Env) > 0 || in.URL != "" || in.PeerEnv != "" {
		declared, err := d.templateParams(ctx, in.TemplateBody)
		if err != nil {
			return nil, err
		}
		// URL は「宣言していれば受け取れる」任意の口(CORS 等で使う。CONTRACT §5)。
		// env と違い、宣言が無くてもエラーにしない(タグと表示には常に使われる)
		if in.URL != "" && declared["EnvKagerouUrl"] {
			merged["EnvKagerouUrl"] = in.URL
		}
		// peer 連動の解決結果も同じ流儀: 宣言していれば受け取れる任意の口(#99)
		if in.PeerEnv != "" && declared["EnvPeerEnv"] {
			merged["EnvPeerEnv"] = in.PeerEnv
		}
		if in.PeerURL != "" && declared["EnvPeerUrl"] {
			merged["EnvPeerUrl"] = in.PeerURL
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
	if in.URL != "" {
		kv[TagURL] = in.URL
	}
	if in.Source != "" {
		kv[TagSource] = in.Source
	}
	if in.Version != "" {
		kv[TagVersion] = in.Version
	}
	if in.owner != "" {
		kv[TagOwner] = in.owner
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

// isInProgressErr は「スタックが *_IN_PROGRESS で今は更新できない」系のエラーか。
func isInProgressErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "_IN_PROGRESS")
}

// explainWaitFailure は待機失敗のエラーに状態別の対処案内を足す。特に
// UPDATE_ROLLBACK_FAILED は continue-update-rollback が要る手詰まり状態(#51)。
func (d *Driver) explainWaitFailure(ctx context.Context, stackName, op string, cause error) error {
	if status, _ := d.stackStatus(ctx, stackName); status == string(cfntypes.StackStatusUpdateRollbackFailed) {
		return fmt.Errorf("%s stack %s: %w\n"+
			"  UPDATE_ROLLBACK_FAILED です。次のいずれかで復旧してください:\n"+
			"    aws cloudformation continue-update-rollback --stack-name %s\n"+
			"    kagerou down --name <name>   (削除して作り直す)",
			op, stackName, cause, stackName)
	}
	return fmt.Errorf("%s stack %s: %w", op, stackName, cause)
}

func isNotExistErr(err error) bool {
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationError" {
		return strings.Contains(err.Error(), "does not exist")
	}
	return false
}
