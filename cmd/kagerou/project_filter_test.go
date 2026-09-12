package main

import (
	"testing"

	"github.com/rikukadev/kagerou/internal/driver/stack"
)

func infoWithProject(name, project string) *stack.Info {
	return &stack.Info{Tags: map[string]string{stack.TagName: name, stack.TagProject: project}}
}

// #48: filterByProject は kagerou:project で reap / list を分離する。
func TestFilterByProject(t *testing.T) {
	infos := []*stack.Info{
		infoWithProject("todo-pr-1", "todo"),
		infoWithProject("shop-pr-9", "shop"),
		infoWithProject("todo-pr-2", "todo"),
		infoWithProject("legacy", ""), // project タグ無し
	}

	// 既定: 自プロジェクトだけ(別リポジトリの環境を巻き込まない)
	got := filterByProject(infos, "todo", false)
	if len(got) != 2 || got[0].Tags[stack.TagProject] != "todo" || got[1].Tags[stack.TagProject] != "todo" {
		t.Fatalf("project=todo filter = %d 件, want 2 (todo のみ)", len(got))
	}

	// --all-projects: 全件素通し
	if all := filterByProject(infos, "todo", true); len(all) != 4 {
		t.Fatalf("all-projects = %d 件, want 4", len(all))
	}

	// project タグ無しの環境は、他プロジェクトの絞り込みには出てこない
	for _, i := range filterByProject(infos, "shop", false) {
		if i.Tags[stack.TagProject] != "shop" {
			t.Fatalf("shop filter に %q(project=%q)が混入", i.Tags[stack.TagName], i.Tags[stack.TagProject])
		}
	}
}
