package stack

import "testing"

func peerInfo(project, name, status, url string) *Info {
	tags := map[string]string{TagProject: project, TagName: name}
	if url != "" {
		tags[TagURL] = url
	}
	return &Info{StackName: project + "-" + name, Status: status, Tags: tags}
}

func TestResolvePeer(t *testing.T) {
	infos := []*Info{
		peerInfo("pub-demo", "pr-42", "CREATE_COMPLETE", "https://pr-42.pub-demo.example.com"),
		peerInfo("pub-demo", "main", "CREATE_COMPLETE", "https://main.pub-demo.example.com"),
		peerInfo("other", "pr-42", "CREATE_COMPLETE", "https://x"), // 別 project は無関係
	}

	// 同名が居れば同名(URL 付き)
	env, url, ok := ResolvePeer(infos, "pub-demo", "pr-42", "main")
	if !ok || env != "pr-42" || url != "https://pr-42.pub-demo.example.com" {
		t.Fatalf("same-name: %q %q %v", env, url, ok)
	}
	// 同名不在 → fallback
	env, url, ok = ResolvePeer(infos, "pub-demo", "pr-99", "main")
	if !ok || env != "main" || url != "https://main.pub-demo.example.com" {
		t.Fatalf("fallback: %q %q %v", env, url, ok)
	}
	// どちらも不在 → fallback 名のまま found=false(呼び出し側が警告)
	env, url, ok = ResolvePeer(nil, "pub-demo", "pr-1", "main")
	if ok || env != "main" || url != "" {
		t.Fatalf("absent: %q %q %v", env, url, ok)
	}
	// ready でない同名は相手にしない(creating の相手に繋がない)→ fallback
	creating := []*Info{
		peerInfo("pub-demo", "pr-42", "CREATE_IN_PROGRESS", ""),
		peerInfo("pub-demo", "main", "CREATE_COMPLETE", "https://main.pub-demo.example.com"),
	}
	if env, _, _ = ResolvePeer(creating, "pub-demo", "pr-42", "main"); env != "main" {
		t.Fatalf("creating peer should be skipped: %q", env)
	}
}
