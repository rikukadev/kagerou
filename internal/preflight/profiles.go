package preflight

// AWS プロファイルの列挙。複数アカウントを持っている人は「どのアカウントに
// 作るか」を最初に選べないと、気づいたときには別アカウントにロールができている。

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile は ~/.aws/config の 1 プロファイル。
type Profile struct {
	Name   string
	Region string
}

var profileHeaderRe = regexp.MustCompile(`^\[(?:profile\s+)?([^\]]+)\]$`)

// Profiles は ~/.aws/config(と credentials)からプロファイル名を集める。
// AWS_CONFIG_FILE / AWS_SHARED_CREDENTIALS_FILE を尊重する。
func Profiles() []Profile {
	seen := map[string]*Profile{}
	var order []string

	add := func(name, region string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if p, ok := seen[name]; ok {
			if p.Region == "" {
				p.Region = region
			}
			return
		}
		seen[name] = &Profile{Name: name, Region: region}
		order = append(order, name)
	}

	for _, path := range []string{configPath(), credentialsPath()} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var current string
		var region string
		flush := func() {
			if current != "" {
				add(current, region)
			}
			current, region = "", ""
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if m := profileHeaderRe.FindStringSubmatch(line); m != nil {
				flush()
				current = m[1]
				continue
			}
			if current != "" && strings.HasPrefix(line, "region") {
				if _, v, ok := strings.Cut(line, "="); ok {
					region = strings.TrimSpace(v)
				}
			}
		}
		flush()
		_ = f.Close()
	}

	out := make([]Profile, 0, len(order))
	for _, n := range order {
		out = append(out, *seen[n])
	}
	return out
}

// Active は現在有効なプロファイル名(未設定なら default)。
func Active() string {
	for _, k := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "default"
}

func configPath() string {
	if p := os.Getenv("AWS_CONFIG_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws", "config")
}

func credentialsPath() string {
	if p := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".aws", "credentials")
}
