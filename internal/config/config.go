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
	Project    string            `yaml:"project"` // kagerou:project タグ(Backstage 連携の紐付けキー)
	Driver     string            `yaml:"driver"`
	Template   string            `yaml:"template"`
	Region     string            `yaml:"region"`
	NamePrefix string            `yaml:"name_prefix"`
	// URLTemplate は環境 URL を作成前に確定させる(例 "https://{name}.preview.example.com")。
	// CORS 許可元やコールバック URL の循環参照を避ける(CONTRACT §5)。
	URLTemplate string           `yaml:"url_template"`
	// ReadinessPath を設定すると、up はこのパスに 200 が返るまで ready にしない(#26)。
	// 未設定なら従来どおりスタック完成 = ready。
	ReadinessPath    string `yaml:"readiness_path"`
	ReadinessTimeout string `yaml:"readiness_timeout"` // 例 90s。未設定は 90s
	TTL        string            `yaml:"ttl"`
	Tags       map[string]string `yaml:"tags"`
	Env        map[string]string `yaml:"env"`
	Hooks      Hooks             `yaml:"hooks"`
}

type Hooks struct {
	PreUp    string `yaml:"pre_up"`
	PostUp   string `yaml:"post_up"` // 環境作成後の仕上げ。Outputs が KAGEROU_* で届く(CONTRACT §7)
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
	default:
		return fmt.Errorf("unknown driver %q (only \"stack\" is available in v0.1)", c.Driver)
	}
	if _, _, err := ParseTTL(c.TTL); err != nil {
		return err
	}
	if c.ReadinessTimeout != "" {
		if d, err := time.ParseDuration(c.ReadinessTimeout); err != nil || d <= 0 {
			return fmt.Errorf("readiness_timeout %q: must be a positive duration", c.ReadinessTimeout)
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

// ExpandName は env 値と hooks 内の {name} を展開した複製を返す。
// 展開対象を値側に限るのは、キーまで動的になると追えなくなるため。
func (c Config) ExpandName(name string) Config {
	out := c
	if c.Env != nil {
		out.Env = make(map[string]string, len(c.Env))
		for k, v := range c.Env {
			out.Env[k] = strings.ReplaceAll(v, "{name}", name)
		}
	}
	out.URLTemplate = strings.ReplaceAll(c.URLTemplate, "{name}", name)
	out.Hooks.PreUp = strings.ReplaceAll(c.Hooks.PreUp, "{name}", name)
	out.Hooks.PostUp = strings.ReplaceAll(c.Hooks.PostUp, "{name}", name)
	out.Hooks.PostDown = strings.ReplaceAll(c.Hooks.PostDown, "{name}", name)
	return out
}

// StackName は driver が作るスタック名(name_prefix + 環境名)。
func (c Config) StackName(name string) string {
	return c.NamePrefix + name
}
