// Package config は kagerou.yaml の読み込みと {name} 展開を提供する。
// 仕様は docs/DESIGN.md §5.5。優先順位は「フラグ > kagerou.yaml > デフォルト」で、
// フラグによる上書きは各コマンド側の責務(この package は下 2 層を担う)。
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultFile は探索するデフォルトの設定ファイル名。
const DefaultFile = "kagerou.yaml"

// TTLNone は「寿命なし」を表す ttl の値。明示したときだけ無期限になる。
const TTLNone = "none"

type Config struct {
	Project    string `yaml:"project"` // kagerou:project タグ(Backstage 連携の紐付けキー)
	Driver     string `yaml:"driver"`
	Template   string `yaml:"template"`
	Region     string `yaml:"region"`
	NamePrefix string `yaml:"name_prefix"`
	// URLTemplate は環境 URL を作成前に確定させる(例 "https://{name}.preview.example.com")。
	// CORS 許可元やコールバック URL の循環参照を避ける(CONTRACT §5)。
	URLTemplate string `yaml:"url_template"`
	// ReadinessPath を設定すると、up はこのパスに 200 が返るまで ready にしない(#26)。
	// 未設定なら従来どおりスタック完成 = ready。
	ReadinessPath    string `yaml:"readiness_path"`
	ReadinessTimeout string `yaml:"readiness_timeout"` // 例 90s。未設定は 90s
	TTL              string `yaml:"ttl"`
	// MaxLifetime は touch(up ごとの TTL 延長)の上限。空なら既定 30 日。
	// CI 用に数時間、sandbox 用に長め、と使い分ける(#51)。
	MaxLifetime string            `yaml:"max_lifetime"`
	Tags        map[string]string `yaml:"tags"`
	Env         map[string]string `yaml:"env"`
	Static      Static            `yaml:"static"`
	Hooks       Hooks             `yaml:"hooks"`
	// Peer は相手アプリ(別 kagerou プロジェクト)との連動(#99/#109)。
	// up 時に相手の同名 env を探し、居なければ fallback の env に繋ぐ。
	// 解決結果はテンプレートの EnvPeerEnv / EnvPeerUrl に宣言時のみ届く。
	Peer Peer `yaml:"peer"`
	// Auth はプレビューにログインを必須にする宣言(#112)。
	Auth Auth `yaml:"auth"`
}

// Auth は「このプレビューは誰が開けるか」の宣言。
//
// **client_secret はここに書けない。** 書けてしまうと必ずリポジトリに入るので、
// 置き場は Secrets Manager 固定にしてある(SecretArn で指す)。validate が
// それらしいキーを弾く。
type Auth struct {
	// Provider は OIDC プロバイダ。今は "google-oidc" のみ。
	// 値を取るのは、Issuer/Endpoint を毎回書かせないため。
	Provider string `yaml:"provider"`
	// Domain は通す組織のドメイン(例 example.com)。空なら IdP で
	// 認証できる全員が入れる。
	//
	// ALB には `hd` を認可リクエストの追加パラメータとして渡すが、**hd は
	// ヒントであって強制ではない**。最終的な関門はアプリ側で
	// x-amzn-oidc-data の hd / email クレームを検証すること(生成される
	// テンプレートにその手順を書く)。
	Domain string `yaml:"domain"`
	// SecretArn は client_id / client_secret を収めた Secrets Manager の
	// シークレット。JSON で {"client_id": …, "client_secret": …} を入れる。
	//
	// ssm-secure ではなく Secrets Manager なのは、ssm-secure の動的参照が
	// 11 のリソース型にしか対応しておらず ELBv2 が含まれないため。
	// secretsmanager は「すべてのリソースプロパティで使える」と明記がある。
	SecretArn string `yaml:"secret_arn"`
}

// Enabled は認証が宣言されているか。
func (a Auth) Enabled() bool { return a.Provider != "" }

// Static は driver: static の設定。bucket は preview base スタックの
// Output(kagerou-preview-base:…:bucket)を写す。
type Static struct {
	Dist   string `yaml:"dist"`
	Bucket string `yaml:"bucket"`
	// Prefix はバケット内の置き場所。空なら "{name}"(per-app base)。
	// 共有 base では "{project}/{name}" にして project 境界を作る。
	Prefix string `yaml:"prefix"`
	// Routing は拡張子の無いパスの解決方法。preview base の CloudFront Function
	// に渡る値で、**ベースを作るときに決まる**(環境ごとには変えられない)。
	//   directory(既定) /about → …/about/index.html  静的サイト生成器向け
	//   spa              /about → …/index.html       クライアントルーター向け
	// SPA で directory のままだと、実体が無いので 403 になる(#86)。
	Routing string `yaml:"routing"`
}

// RoutingDirectory / RoutingSPA は Static.Routing の値。
const (
	RoutingDirectory = "directory"
	RoutingSPA       = "spa"
)

// StaticRouting は既定を埋めた routing を返す。
func (c Config) StaticRouting() string {
	if c.Static.Routing == "" {
		return RoutingDirectory
	}
	return c.Static.Routing
}

// StaticPrefix は静的成果物の置き場所(末尾スラッシュなし)。
func (c Config) StaticPrefix(name string) string {
	p := c.Static.Prefix
	if p == "" {
		p = "{name}"
	}
	p = strings.ReplaceAll(p, "{name}", name)
	p = strings.ReplaceAll(p, "{project}", c.Project)
	return strings.Trim(p, "/")
}

// Peer は環境名で連動する相手(#99)。project は相手の kagerou:project。
type Peer struct {
	Project  string `yaml:"project"`
	Fallback string `yaml:"fallback"` // 同名 env が居ないとき繋ぐ env 名。空なら "main"
}

// FallbackName は fallback の実効値。
func (p Peer) FallbackName() string {
	if p.Fallback == "" {
		return "main"
	}
	return p.Fallback
}

type Hooks struct {
	PreUp string `yaml:"pre_up"`
	// PostUp は環境作成後の仕上げ。Outputs が KAGEROU_* で届く(CONTRACT §7)
	PostUp string `yaml:"post_up"`
	// PreDown は環境を消す**前**の後始末。Outputs がまだ引けるのでここで使える
	// (中身の入った S3 バケットは DeleteStack が消せない、といった前処理用)。
	// post_down では遅い — そのときスタックはもう無く、Outputs も引けない。
	PreDown  string `yaml:"pre_down"`
	PostDown string `yaml:"post_down"`
}

// Default は kagerou.yaml が無い/項目が省略されたときの既定値。
func Default() Config {
	return Config{
		Driver:   "stack",
		Template: "template.yaml",
		TTL:      "72h",
	}
}

// Load は path の kagerou.yaml を読み、デフォルトに重ねて返す。
// 未知のキーはエラーにする(タイポの黙殺は事故のもと)。
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	// KnownFields でも弾けるが、出るのは「未知のフィールド」という一般論。
	// 秘密の直書きは理由まで伝えないと、別のキー名で書き直されて終わる
	if err := RejectInlineSecrets(b); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// LoadOrDefault は path が存在しなければデフォルト設定を返す。
// kagerou.yaml は必須にしない(フラグだけでも動けるように)。
func LoadOrDefault(path string) (Config, error) {
	cfg, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	return cfg, err
}

func (c Config) validate() error {
	switch c.Driver {
	case "stack":
	case "static":
		// static は preview base のバケットへ配置するだけなので、URL は
		// 作成前に確定していなければならない(Output を持つ compute が無い)
		if c.URLTemplate == "" {
			return errors.New(`driver "static" requires url_template (there is no compute to emit a URL output)`)
		}
		if c.Static.Dist == "" {
			return errors.New(`driver "static" requires static.dist (the built output directory)`)
		}
		if c.Static.Bucket == "" {
			return errors.New(`driver "static" requires static.bucket (the preview base bucket output)`)
		}
	default:
		return fmt.Errorf("unknown driver %q (\"stack\" or \"static\")", c.Driver)
	}
	// routing は driver: static 専用ではない。stack 構成でも SPA を preview base
	// から配るので(3 層デモがそれ)、driver の外で検証する。
	switch c.Static.Routing {
	case "", RoutingDirectory, RoutingSPA:
	default:
		return fmt.Errorf("unknown static.routing %q (%q or %q)", c.Static.Routing, RoutingDirectory, RoutingSPA)
	}
	if _, _, err := ParseTTL(c.TTL); err != nil {
		return err
	}
	if c.ReadinessTimeout != "" {
		if d, err := time.ParseDuration(c.ReadinessTimeout); err != nil || d <= 0 {
			return fmt.Errorf("readiness_timeout %q: must be a positive duration", c.ReadinessTimeout)
		}
	}
	if c.MaxLifetime != "" {
		if d, err := time.ParseDuration(c.MaxLifetime); err != nil || d <= 0 {
			return fmt.Errorf("max_lifetime %q: must be a positive duration", c.MaxLifetime)
		}
	}
	if c.Peer.Project != "" {
		if err := ValidateName(c.Peer.Project); err != nil {
			return fmt.Errorf("peer.project: %w", err)
		}
		if err := ValidateName(c.Peer.FallbackName()); err != nil {
			return fmt.Errorf("peer.fallback: %w", err)
		}
	} else if c.Peer.Fallback != "" {
		return fmt.Errorf("peer.fallback is set but peer.project is empty")
	}
	if err := c.Auth.validate(); err != nil {
		return err
	}
	return nil
}

// authProviders は対応する OIDC プロバイダ。値で取るのは、issuer や各
// エンドポイントを毎回書かせないため(増やすときはテンプレート側も足す)。
var authProviders = map[string]bool{"google-oidc": true}

// secretLikeKeys は kagerou.yaml に **絶対に書かせない** キー。
// ここを緩めると、client_secret がリポジトリに入る事故が必ず起きる。
var secretLikeKeys = []string{"client_secret", "clientsecret", "secret", "password"}

func (a Auth) validate() error {
	if !a.Enabled() {
		// provider が空なら他も空であるべき。書いたのに効かない状態を残さない
		if a.Domain != "" || a.SecretArn != "" {
			return fmt.Errorf("auth.domain / auth.secret_arn is set but auth.provider is empty")
		}
		return nil
	}
	if !authProviders[a.Provider] {
		return fmt.Errorf("unknown auth.provider %q (only \"google-oidc\" for now)", a.Provider)
	}
	if a.SecretArn == "" {
		return fmt.Errorf("auth.provider is set but auth.secret_arn is empty — " +
			"put client_id / client_secret in Secrets Manager and point at it " +
			"(the secret itself must never live in kagerou.yaml)")
	}
	if !strings.HasPrefix(a.SecretArn, "arn:") {
		return fmt.Errorf("auth.secret_arn %q: want a Secrets Manager ARN", a.SecretArn)
	}
	return nil
}

// RejectInlineSecrets は kagerou.yaml に秘密が直接書かれていないかを見る。
// Auth の構造体に入らないキー(タイポや独自拡張)も拾いたいので、
// 生の YAML に対して当てる。
func RejectInlineSecrets(raw []byte) error {
	var probe struct {
		Auth map[string]any `yaml:"auth"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return nil // 本体のパースで別途エラーになる
	}
	for k, v := range probe.Auth {
		lk := strings.ToLower(strings.ReplaceAll(k, "_", ""))
		for _, bad := range secretLikeKeys {
			if lk != strings.ReplaceAll(bad, "_", "") {
				continue
			}
			if s, ok := v.(string); ok && s == "" {
				continue
			}
			return fmt.Errorf("auth.%s must not be written in kagerou.yaml — "+
				"this file is committed. Put it in Secrets Manager and set auth.secret_arn", k)
		}
	}
	return nil
}

// ParseTTL は "72h" 等を Duration に解釈する。TTLNone("none")のときだけ
// hasTTL=false を返す。ゼロ・負値は事故防止のため拒否する。
func ParseTTL(s string) (d time.Duration, hasTTL bool, err error) {
	if s == TTLNone {
		return 0, false, nil
	}
	d, err = time.ParseDuration(s)
	if err != nil {
		return 0, false, fmt.Errorf("ttl %q: %w", s, err)
	}
	if d <= 0 {
		return 0, false, fmt.Errorf("ttl %q: must be positive or %q", s, TTLNone)
	}
	return d, true, nil
}

var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

// ValidateName は環境名の形式を検証する(docs/CONTRACT.md §2)。
// Route53 サブドメインラベル(63 文字)と S3 プレフィックスに後からそのまま
// 使えるよう、最初から狭くしておく(広げるのは安全、狭めるのは既存名を壊す)。
func ValidateName(name string) error {
	if name == "" {
		return errors.New("environment name is empty")
	}
	if len(name) > 63 {
		return fmt.Errorf("environment name %q is too long (max 63 chars)", name)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid environment name %q (must start with a lowercase letter, contain only lowercase letters, digits and hyphens, and not end with a hyphen)", name)
	}
	return nil
}

// NormalizeName は環境名の制約(CONTRACT §2)を満たす候補に変換する。
// 例: "feature/Foo_Bar" → "feature-foo-bar"。何も残らなければ空を返す。
func NormalizeName(raw string) string {
	var b []rune
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b = append(b, r)
		default:
			if len(b) > 0 && b[len(b)-1] != '-' {
				b = append(b, '-')
			}
		}
	}
	// 先頭は小文字英字: それ以外を削る
	for len(b) > 0 && (b[0] < 'a' || b[0] > 'z') {
		b = b[1:]
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	if len(b) > 63 {
		b = b[:63]
		for len(b) > 0 && b[len(b)-1] == '-' {
			b = b[:len(b)-1]
		}
	}
	return string(b)
}

// BaseDomainPlaceholder は preview base のドメイン(CONTRACT §9)に解決される
// プレースホルダ。ドメインをここに書き写さず SSM の契約を真実の源にするため(#136)。
const BaseDomainPlaceholder = "{base_domain}"

// expandable は {name} 等の展開対象になる値をまとめて走査/差し替えする。
// 対象を値側(url_template / env の値 / hooks 本文)に限るのは、キーまで
// 動的になると追えなくなるため。
func (c Config) expandable(fn func(string) string) Config {
	out := c
	if c.Env != nil {
		out.Env = make(map[string]string, len(c.Env))
		for k, v := range c.Env {
			out.Env[k] = fn(v)
		}
	}
	out.URLTemplate = fn(c.URLTemplate)
	out.Hooks.PreUp = fn(c.Hooks.PreUp)
	out.Hooks.PostUp = fn(c.Hooks.PostUp)
	out.Hooks.PreDown = fn(c.Hooks.PreDown)
	out.Hooks.PostDown = fn(c.Hooks.PostDown)
	return out
}

// UsesBaseDomain は {base_domain} がどこかで使われているかを返す。
// 使っていなければ SSM を一切読まない(既存の挙動と必要権限のまま)。
func (c Config) UsesBaseDomain() bool {
	used := false
	c.expandable(func(s string) string {
		if strings.Contains(s, BaseDomainPlaceholder) {
			used = true
		}
		return s
	})
	return used
}

// ExpandBaseDomain は {base_domain} を解決済みのドメインに置き換えた複製を返す。
func (c Config) ExpandBaseDomain(domain string) Config {
	return c.expandable(func(s string) string {
		return strings.ReplaceAll(s, BaseDomainPlaceholder, domain)
	})
}

// ExpandName は env 値と hooks 内の {name} を展開した複製を返す。
// 展開対象を値側に限るのは、キーまで動的になると追えなくなるため。
func (c Config) ExpandName(name string) Config {
	// {project} は共有 base(1 ドメインを複数アプリで使う)の URL 規約
	// <project>--<name>.<domain> を書けるようにするため
	return c.expandable(func(s string) string {
		s = strings.ReplaceAll(s, "{name}", name)
		return strings.ReplaceAll(s, "{project}", c.Project)
	})
}

// StackName は driver が作るスタック名(name_prefix + 環境名)。
func (c Config) StackName(name string) string {
	return c.NamePrefix + name
}
