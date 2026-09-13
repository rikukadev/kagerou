// Package static は「compute が要らない」環境(SSG / CSR の SPA)向けの
// driver(DESIGN §10.4)。環境 = preview base バケットの {name}/ プレフィックス。
//
// 不変条件「環境 = CFN スタック 1 個」は static でも維持する: タグの担い手として
// 実リソースを持たない極小スタック(WaitConditionHandle。無料・即時)を作るので、
// list / reap / Environment JSON / iam-policy が全 driver 共通のまま動く。
package static

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rikukadev/kagerou/internal/driver/stack"
)

const DriverName = "static"

// metaTemplate は環境を表す極小スタック。実リソースは作らず、タグと
// Outputs(URL)だけを持つ。WaitConditionHandle は課金もプロビジョニングも無い。
const metaTemplate = `AWSTemplateFormatVersion: "2010-09-09"
Description: kagerou static environment (metadata only; content lives in the preview base bucket)
Parameters:
  EnvKagerouEnv:
    Type: String
  EnvKagerouUrl:
    Type: String
    Default: ""
Resources:
  Marker:
    Type: AWS::CloudFormation::WaitConditionHandle
Outputs:
  KagerouUrl:
    Value: !Ref EnvKagerouUrl
  KagerouStaticPrefix:
    Value: !Ref EnvKagerouEnv
`

type Driver struct {
	stack    *stack.Driver
	s3       *s3.Client // 既定 region のクライアント(バケット region の解決に使う)
	cfg      aws.Config
	byRegion map[string]*s3.Client
}

func New(ctx context.Context, region string) (*Driver, error) {
	sd, err := stack.New(ctx, region)
	if err != nil {
		return nil, err
	}
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &Driver{
		stack: sd, s3: s3.NewFromConfig(cfg), cfg: cfg,
		byRegion: map[string]*s3.Client{},
	}, nil
}

// clientFor はバケットの region に合ったクライアントを返す。preview base の
// バケットは us-east-1 固定(CloudFront 証明書の制約)なので、アプリの region と
// 食い違うのが普通。region が違うと PutObject が 301 PermanentRedirect になる。
func (d *Driver) clientFor(ctx context.Context, bucket string) (*s3.Client, error) {
	loc, err := d.s3.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: &bucket})
	if err != nil {
		return nil, fmt.Errorf("get bucket location %s: %w", bucket, err)
	}
	region := string(loc.LocationConstraint)
	if region == "" {
		region = "us-east-1" // 空は us-east-1(API の歴史的仕様)
	}
	if c, ok := d.byRegion[region]; ok {
		return c, nil
	}
	cfg := d.cfg.Copy()
	cfg.Region = region
	c := s3.NewFromConfig(cfg)
	d.byRegion[region] = c
	return c, nil
}

type UpInput struct {
	stack.UpInput        // StackName / Name / Project / ExpiresAt / Source / Version / Tags
	Bucket        string // preview base のバケット
	Prefix        string // バケット内の置き場所(共有 base では <project>/<name>)
	Dist          string // 配置するローカルディレクトリ
}

// UpMeta はメタスタックだけを作る(タグと URL の担い手)。内容の同期は Sync で、
// 間に post_up を挟めるように分けてある(static は「同期 = 公開」なので、
// 環境固有のファイル生成は同期より前でなければ反映されない)。
func (d *Driver) UpMeta(ctx context.Context, in UpInput) (*stack.Info, error) {
	if in.Bucket == "" {
		return nil, errors.New("static driver needs a bucket (set static.bucket, or deploy the preview base)")
	}
	if in.Dist == "" {
		return nil, errors.New("static driver needs a dist directory (set static.dist)")
	}

	si := in.UpInput
	si.TemplateBody = metaTemplate
	si.Env = map[string]string{"KAGEROU_ENV": in.Name}
	if si.URL != "" {
		si.Env["KAGEROU_URL"] = si.URL
	}
	return d.stack.Up(ctx, si)
}

// Up は UpMeta + Sync(フックを挟まない呼び出し用)。
func (d *Driver) Up(ctx context.Context, in UpInput) (*stack.Info, error) {
	info, err := d.UpMeta(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := d.Sync(ctx, in.Bucket, in.prefix(), in.Dist); err != nil {
		return nil, err
	}
	return info, nil
}

// prefix は置き場所(未指定なら環境名)。
func (in UpInput) prefix() string {
	if in.Prefix != "" {
		return in.Prefix
	}
	return in.Name
}

// Down はメタスタックとプレフィックス配下のオブジェクトを消す。どちらも冪等。
// prefix は Up と同じもの(共有 base では <project>/<name>)を渡すこと。
func (d *Driver) Down(ctx context.Context, stackName, bucket, prefix string) error {
	if err := d.stack.Down(ctx, stackName); err != nil {
		return err
	}
	if bucket == "" {
		return nil
	}
	return d.deletePrefix(ctx, bucket, prefix)
}

// Sync は dist を s3://bucket/name/ に同期する(ローカルに無いものは消す)。
func (d *Driver) Sync(ctx context.Context, bucket, prefix, dist string) error {
	if strings.Trim(prefix, "/") == "" {
		return errors.New("static sync needs a prefix (environment name, or <project>/<name> on a shared base)")
	}
	if fi, err := os.Stat(dist); err != nil || !fi.IsDir() {
		return fmt.Errorf("static.dist %q is not a directory (build first)", dist)
	}
	cli, err := d.clientFor(ctx, bucket)
	if err != nil {
		return err
	}
	local := map[string]string{} // key -> path
	err = filepath.WalkDir(dist, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dist, path)
		if err != nil {
			return err
		}
		local[prefixKey(prefix, filepath.ToSlash(rel))] = path
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", dist, err)
	}
	if len(local) == 0 {
		return fmt.Errorf("static.dist %q has no files (build first)", dist)
	}

	for key, path := range local {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = cli.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      &bucket,
			Key:         &key,
			Body:        f,
			ContentType: contentType(path),
		})
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
	}

	// ローカルに無い残骸を消す(aws s3 sync --delete 相当)
	remote, err := d.listPrefixWith(ctx, cli, bucket, prefix)
	if err != nil {
		return err
	}
	var stale []s3types.ObjectIdentifier
	for _, key := range remote {
		if _, ok := local[key]; !ok {
			k := key
			stale = append(stale, s3types.ObjectIdentifier{Key: &k})
		}
	}
	return d.deleteObjectsWith(ctx, cli, bucket, stale)
}

func (d *Driver) listPrefix(ctx context.Context, bucket, prefix string) ([]string, error) {
	cli, err := d.clientFor(ctx, bucket)
	if err != nil {
		return nil, err
	}
	return d.listPrefixWith(ctx, cli, bucket, prefix)
}

func (d *Driver) listPrefixWith(ctx context.Context, cli *s3.Client, bucket, prefix string) ([]string, error) {
	prefix = strings.TrimSuffix(prefix, "/") + "/"
	var keys []string
	p := s3.NewListObjectsV2Paginator(cli, &s3.ListObjectsV2Input{Bucket: &bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list s3://%s/%s: %w", bucket, prefix, err)
		}
		for _, o := range page.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
	}
	return keys, nil
}

func (d *Driver) deletePrefix(ctx context.Context, bucket, prefix string) error {
	keys, err := d.listPrefix(ctx, bucket, prefix)
	if err != nil {
		return err
	}
	objs := make([]s3types.ObjectIdentifier, 0, len(keys))
	for _, k := range keys {
		key := k
		objs = append(objs, s3types.ObjectIdentifier{Key: &key})
	}
	cli, err := d.clientFor(ctx, bucket)
	if err != nil {
		return err
	}
	return d.deleteObjectsWith(ctx, cli, bucket, objs)
}

func (d *Driver) deleteObjectsWith(ctx context.Context, cli *s3.Client, bucket string, objs []s3types.ObjectIdentifier) error {
	for len(objs) > 0 {
		n := min(len(objs), 1000) // DeleteObjects の上限
		_, err := cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &bucket,
			Delete: &s3types.Delete{Objects: objs[:n]},
		})
		if err != nil {
			return fmt.Errorf("delete objects in %s: %w", bucket, err)
		}
		objs = objs[n:]
	}
	return nil
}

func prefixKey(prefix, rel string) string { return strings.TrimSuffix(prefix, "/") + "/" + rel }

// contentType は拡張子から推定する。S3 は既定で binary/octet-stream になり、
// CloudFront 経由でも直らない(ブラウザが HTML を表示できない)ため必須。
func contentType(path string) *string {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if t == "" {
		t = "application/octet-stream"
	}
	return &t
}
