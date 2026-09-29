// Package basedomain は url_template 等の {base_domain} を、preview base の
// SSM キー契約(CONTRACT §9)から解決する(#136)。
//
// ドメインをベースと kagerou.yaml の両方に置くと、ベース側を変えたときに
// kagerou.yaml が黙って古くなる。url_template の値はそのまま kagerou:url タグに
// なり Output と突き合わされないので、**どこにも到達しない URL を指したまま
// 緑になる**。真実の源を SSM 側 1 つに寄せるのがこのパッケージの目的。
package basedomain

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/rikukadev/kagerou/internal/config"
)

// Placeholder は kagerou.yaml に書くプレースホルダ。定義は設定スキーマ側。
const Placeholder = config.BaseDomainPlaceholder

// source は探索する 1 段。CONTRACT §9 の解決順(project 専用 → _shared →
// _shared-alb)をそのまま並べる。
type source struct {
	key    string
	region string
}

// sources は解決順。**同じキーが 2 つのリージョンにありうる**のが要点で、
// ベースの種類で置き場所が変わる(§9):
//
//	CloudFront ベース … us-east-1(CloudFront の証明書がそこにしか置けないため)
//	ALB ベース        … アプリのリージョン(ALB と同居)
//
// `domain` は入口によらず「このアプリのプレビュードメイン」なのでキー名は同じ。
// 入口が alb かどうかを解決側は知らないので、両方を順に見るしかない。
// ALB が既定の入口(#131)なのでアプリのリージョンを先に見る。
func sources(namespace, region string, explicit bool) []source {
	var out []source
	add := func(key string) {
		if region != "" && region != "us-east-1" {
			out = append(out, source{key: key, region: region})
		}
		out = append(out, source{key: key, region: "us-east-1"})
	}
	add("/kagerou/base/" + namespace + "/domain")
	// 設定で名前空間を明示したなら、そこだけを見る。空振りして _shared に
	// 落ちると**別製品の土台に黙って繋がる**(#196)
	if explicit {
		return out
	}
	add("/kagerou/base/_shared/domain")
	// ALB を全アプリで 1 本に共有する運用(alb-base の Project に _shared-alb を
	// 渡すオプトイン)。ALB ベースなのでアプリのリージョンにしか無い
	if region != "" {
		out = append(out, source{key: "/kagerou/base/_shared-alb/domain", region: region})
	}
	return out
}

// getParameter は SSM から 1 つ引く。テストで差し替える。
var getParameter = func(ctx context.Context, region, key string) (string, error) {
	c, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
	if err != nil {
		return "", err
	}
	out, err := ssm.NewFromConfig(c).GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(key),
	})
	if err != nil {
		return "", err
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return "", errors.New("empty parameter")
	}
	return *out.Parameter.Value, nil
}

// Resolve はドメインと、それが見つかったキーを返す。namespace はベースの
// SSM 名前空間(既定は project)。explicit なら他の名前空間へ落ちない。
func Resolve(ctx context.Context, namespace, region string, explicit bool) (domain, key string, err error) {
	var tried []string
	for _, s := range sources(namespace, region, explicit) {
		tried = append(tried, fmt.Sprintf("%s (%s)", s.key, s.region))
		v, err := getParameter(ctx, s.region, s.key)
		if err != nil {
			continue // 権限不足も未作成もここでは同じ「無い」。次の段へ
		}
		// 空文字を採ると https://pr-42.. のような URL を作って緑にしてしまう
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		return v, s.key, nil
	}
	if explicit {
		return "", "", fmt.Errorf("%s: base %q not found; looked for %s "+
			"(it is set explicitly in kagerou.yaml, so kagerou does not fall back to another base)",
			Placeholder, namespace, strings.Join(tried, ", "))
	}
	return "", "", fmt.Errorf("%s: no base domain found; looked for %s "+
		"(create a preview base, or replace the placeholder with a literal domain)",
		Placeholder, strings.Join(tried, ", "))
}
