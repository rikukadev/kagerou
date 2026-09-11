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
	Driver     string            `yaml:"driver"`
	Template   string            `yaml:"template"`
	Region     string            `yaml:"region"`
	NamePrefix string            `yaml:"name_prefix"`
	TTL        string            `yaml:"ttl"`
	Tags       map[string]string `yaml:"tags"`
	Env        map[string]string `yaml:"env"`
	Hooks      Hooks             `yaml:"hooks"`
}

type Hooks struct {
	PreUp    string `yaml:"pre_up"`
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
		return fmt.Errorf("unknown driver %q (v0.1 で使えるのは stack のみ)", c.Driver)
	}
	if _, _, err := ParseTTL(c.TTL); err != nil {
		return err
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
		return 0, false, fmt.Errorf("ttl %q: 正の値か %q を指定する", s, TTLNone)
	}
	return d, true, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]*$`)

// ValidateName は環境名の形式を検証する。CFN スタック名の制約
// (英字始まり・英数字とハイフン)に合わせる。長さは prefix と合わせて
// スタック名上限 128 に収まるよう控えめに 64 までとする。
func ValidateName(name string) error {
	if name == "" {
		return errors.New("環境名が空")
	}
	if len(name) > 64 {
		return fmt.Errorf("環境名 %q が長すぎる(64 文字まで)", name)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("環境名 %q が不正(英字始まり・英数字とハイフンのみ)", name)
	}
	return nil
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
	out.Hooks.PreUp = strings.ReplaceAll(c.Hooks.PreUp, "{name}", name)
	out.Hooks.PostDown = strings.ReplaceAll(c.Hooks.PostDown, "{name}", name)
	return out
}

// StackName は driver が作るスタック名(name_prefix + 環境名)。
func (c Config) StackName(name string) string {
	return c.NamePrefix + name
}
