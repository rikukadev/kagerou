// Package validate は「テンプレートが契約(docs/CONTRACT.md)を満たすか」を
// デプロイなしで検査する(#24)。宣言忘れは沈黙する設計なので、ここで音を出す。
package validate

import (
	"fmt"
	"os"
	"sort"

	"github.com/rikukadev/kagerou/internal/config"
	"github.com/rikukadev/kagerou/internal/driver/stack"
	"gopkg.in/yaml.v3"
)

type Level string

const (
	Error Level = "error"
	Warn  Level = "warn"
)

type Finding struct {
	Level Level
	Msg   string
}

// cfnTemplate は検査に必要な部分だけを読む。CFN の独自タグ(!Ref / !Sub 等)を
// 含んでいても yaml.Node なら受けられる。
type cfnTemplate struct {
	Parameters map[string]yaml.Node `yaml:"Parameters"`
	Outputs    map[string]yaml.Node `yaml:"Outputs"`
}

// Run は kagerou.yaml(cfg)とテンプレートを突き合わせる。
// name は空なら検査しない。
func Run(cfg config.Config, templatePath, name string) ([]Finding, error) {
	var fs []Finding

	body, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", templatePath, err)
	}
	var t cfnTemplate
	if err := yaml.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("template %s: %w", templatePath, err)
	}

	// §4: env キーすべてに対応する Env<Key> パラメータが宣言されているか
	envKeys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	matched := map[string]bool{}
	for _, k := range envKeys {
		pname, err := stack.EnvParamName(k)
		if err != nil {
			fs = append(fs, Finding{Error, fmt.Sprintf("env %s: %v", k, err)})
			continue
		}
		if _, ok := t.Parameters[pname]; !ok {
			fs = append(fs, Finding{Error, fmt.Sprintf("env %s has no receiving parameter: declare %s in the template Parameters (CONTRACT §4)", k, pname)})
			continue
		}
		matched[pname] = true
	}

	// 逆向き: Env* パラメータが宣言されているのに env から渡されない。
	// Default があれば正常。無ければ警告(--env で実行時に渡す運用もあるため)
	pnames := make([]string, 0, len(t.Parameters))
	for p := range t.Parameters {
		pnames = append(pnames, p)
	}
	sort.Strings(pnames)
	for _, p := range pnames {
		if len(p) <= 3 || p[:3] != "Env" || matched[p] {
			continue
		}
		if !hasDefault(t.Parameters[p]) {
			fs = append(fs, Finding{Warn, fmt.Sprintf("parameter %s is declared but not fed by kagerou.yaml env and has no Default — pass it via --env or add a Default", p)})
		}
	}

	// url_template を使うなら、受け取り口 EnvKagerouUrl があると CORS 等に使える
	if cfg.URLTemplate != "" {
		if _, ok := t.Parameters["EnvKagerouUrl"]; !ok {
			fs = append(fs, Finding{Warn, "url_template is set but the template does not declare EnvKagerouUrl — the app cannot receive the URL (CORS / callback values)"})
		}
	}

	// §5: URL Output(url_template があれば Output は無くてもよい)
	if _, ok := t.Outputs["KagerouUrl"]; !ok {
		if _, ok := t.Outputs["PreviewUrl"]; !ok && cfg.URLTemplate == "" {
			fs = append(fs, Finding{Error, "no environment URL: set url_template in kagerou.yaml, or declare KagerouUrl (or PreviewUrl) in Outputs (CONTRACT §5)"})
		}
	}

	// §2: 環境名
	if name != "" {
		if err := config.ValidateName(name); err != nil {
			msg := fmt.Sprintf("%v", err)
			if s := config.NormalizeName(name); s != "" {
				msg += fmt.Sprintf(" — suggestion: %q", s)
			}
			fs = append(fs, Finding{Error, msg})
		}
	}

	return fs, nil
}

// hasDefault は Parameters の 1 エントリに Default キーがあるか見る。
func hasDefault(n yaml.Node) bool {
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "Default" {
			return true
		}
	}
	return false
}

// HasErrors は error レベルの指摘が含まれるか。
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == Error {
			return true
		}
	}
	return false
}
