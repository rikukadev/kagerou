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
	region string // 空なら呼び出し側の region
}

// sources は解決順。preview base のキーは us-east-1 に置く契約で、
// _shared-alb だけは ALB と同じリージョンにある(§9)。
func sources(project string) []source {
	return []source{
		{key: "/kagerou/base/" + project + "/domain", region: "us-east-1"},
		{key: "/kagerou/base/_shared/domain", region: "us-east-1"},
		{key: "/kagerou/base/_shared-alb/domain"},
	}
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

// Resolve はドメインと、それが見つかったキーを返す。
// どの段でも見つからなければ、探した順にキーを並べたエラーを返す。
func Resolve(ctx context.Context, project, region string) (domain, key string, err error) {
	var tried []string
	for _, s := range sources(project) {
		r := s.region
		if r == "" {
			r = region
		}
		tried = append(tried, fmt.Sprintf("%s (%s)", s.key, r))
		v, err := getParameter(ctx, r, s.key)
		if err != nil {
			continue // 権限不足も未作成もここでは同じ「無い」。次の段へ
		}
		// 空文字を採ると https://pr-42.. のような URL を作って緑にしてしまう
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		return v, s.key, nil
	}
	return "", "", fmt.Errorf("%s: no base domain found; looked for %s "+
		"(create a preview base, or replace the placeholder with a literal domain)",
		Placeholder, strings.Join(tried, ", "))
}
