package static

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Orphan はメタスタックの無いプレフィックス(#253)。
//
// static の環境は「メタ CFN スタック」と「S3 のプレフィックス」の 2 つで
// できている。list / reap が環境を見つける台帳はスタックなので、スタックだけ
// 消えると **公開されているのに誰からも見つけられない** 状態になる。
type Orphan struct {
	// Name はプレフィックスから復元した環境名。
	Name string
	// Prefix はバケット内の位置。
	Prefix string
	// Newest は配下でいちばん新しいオブジェクトの時刻。grace 判定に使う。
	Newest time.Time
	// Objects は配下のオブジェクト数。
	Objects int
}

// PrefixLayout は static.prefix テンプレートを「親 + 環境名」に分解した結果。
//
// **消してよいかはここで決まる。** 親が空(バケット直下)だと、同じバケットを
// 他の project が共有していても区別できないので消せない。
type PrefixLayout struct {
	// Parent は {name} の手前。末尾に / を含む。バケット直下なら空。
	Parent string
	// Deletable は「親の下のプレフィックスは全部この project のもの」と言えるか。
	Deletable bool
	// Reason は Deletable が false の理由(利用者に見せる)。
	Reason string
}

// ParsePrefixLayout は展開済みのテンプレートから layout を求める。
// template は {project} を展開し {name} は残したもの(例 "spa-demo/{name}")。
func ParsePrefixLayout(template string) PrefixLayout {
	t := strings.Trim(template, "/")
	i := strings.Index(t, "{name}")
	if i < 0 {
		return PrefixLayout{Reason: "static.prefix に {name} が無いため、プレフィックスから環境名を復元できません"}
	}
	parent := t[:i]
	rest := t[i+len("{name}"):]
	// {name} が 1 セグメントを丸ごと占めていないと、CommonPrefixes と環境名が
	// 1 対 1 にならない(site-{name} だと "site-pr-1/" から "pr-1" を復元する
	// ことになり、別の命名と混ざったときに取り違える)
	if parent != "" && !strings.HasSuffix(parent, "/") {
		return PrefixLayout{Parent: "", Reason: fmt.Sprintf(
			"static.prefix の {name} が 1 セグメントを占めていません(%q)。環境名を復元できません", template)}
	}
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return PrefixLayout{Parent: parent, Reason: fmt.Sprintf(
			"static.prefix の {name} が 1 セグメントを占めていません(%q)。環境名を復元できません", template)}
	}
	if parent == "" {
		return PrefixLayout{Parent: "", Reason: "static.prefix に project の名前空間が無いため" +
			"(既定は {name})、同じバケットを共有する他 project と区別できません。" +
			"消すには static.prefix を {project}/{name} のようにしてください"}
	}
	return PrefixLayout{Parent: parent, Deletable: true}
}

// FindOrphans は親の下のプレフィックスのうち、known に無いものを返す(#253)。
//
// known は生きている環境名の集合。newerThan より新しいオブジェクトを含む
// プレフィックスは**孤児として返さない** — 作成直後の環境を「台帳にまだ無い」と
// 誤判定して消さないため(タグ検索は結果整合で、up 直後は list に出ないことがある)。
func (d *Driver) FindOrphans(ctx context.Context, bucket, parent string, known map[string]bool, newerThan time.Time) ([]Orphan, error) {
	cli, err := d.clientFor(ctx, bucket)
	if err != nil {
		return nil, err
	}
	var out []Orphan
	p := s3.NewListObjectsV2Paginator(cli, &s3.ListObjectsV2Input{
		Bucket:    &bucket,
		Prefix:    &parent,
		Delimiter: aws.String("/"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list prefixes under s3://%s/%s: %w", bucket, parent, err)
		}
		for _, cp := range page.CommonPrefixes {
			prefix := strings.TrimSuffix(aws.ToString(cp.Prefix), "/")
			name := strings.TrimPrefix(prefix, parent)
			if name == "" || strings.Contains(name, "/") || known[name] {
				continue
			}
			o, err := d.describePrefix(ctx, cli, bucket, prefix)
			if err != nil {
				return nil, err
			}
			if o.Objects == 0 {
				continue
			}
			if o.Newest.After(newerThan) {
				// grace の中。作られたばかりの環境かもしれない
				continue
			}
			o.Name = name
			out = append(out, o)
		}
	}
	return out, nil
}

// describePrefix はプレフィックス配下の数と最新時刻を返す。
func (d *Driver) describePrefix(ctx context.Context, cli *s3.Client, bucket, prefix string) (Orphan, error) {
	o := Orphan{Prefix: prefix}
	withSlash := prefix + "/"
	p := s3.NewListObjectsV2Paginator(cli, &s3.ListObjectsV2Input{Bucket: &bucket, Prefix: &withSlash})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return o, fmt.Errorf("list s3://%s/%s: %w", bucket, withSlash, err)
		}
		for _, obj := range page.Contents {
			o.Objects++
			if t := aws.ToTime(obj.LastModified); t.After(o.Newest) {
				o.Newest = t
			}
		}
	}
	return o, nil
}

// DeleteOrphan は孤児プレフィックス配下を消す。
func (d *Driver) DeleteOrphan(ctx context.Context, bucket string, o Orphan) error {
	return d.deletePrefix(ctx, bucket, o.Prefix)
}
