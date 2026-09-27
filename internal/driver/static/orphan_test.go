package static

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// 消してよいかは prefix テンプレートで決まる(#253)。既定の {name} は
// バケット直下に並ぶので、同じバケットを共有する他 project と区別できない。
func TestParsePrefixLayout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		template  string
		parent    string
		deletable bool
		reason    string // 部分一致
	}{
		{"project 名前空間つきは消せる", "spa-demo/{name}", "spa-demo/", true, ""},
		{"深い名前空間も消せる", "previews/spa-demo/{name}", "previews/spa-demo/", true, ""},
		{"{name} の後ろにセグメントがあってもよい", "spa-demo/{name}/site", "spa-demo/", true, ""},
		{"既定は消せない", "{name}", "", false, "project の名前空間が無い"},
		{"{name} が無ければ復元できない", "spa-demo/fixed", "", false, "{name} が無い"},
		{"セグメントを占めていないと復元できない", "spa-demo/site-{name}", "", false, "1 セグメントを占めていません"},
		{"後ろがくっついていても復元できない", "spa-demo/{name}-site", "spa-demo/", false, "1 セグメントを占めていません"},
	} {
		got := ParsePrefixLayout(tc.template)
		if got.Parent != tc.parent {
			t.Errorf("%s: parent = %q, want %q", tc.name, got.Parent, tc.parent)
		}
		if got.Deletable != tc.deletable {
			t.Errorf("%s: deletable = %v, want %v (reason=%s)", tc.name, got.Deletable, tc.deletable, got.Reason)
		}
		if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
			t.Errorf("%s: reason = %q, want to contain %q", tc.name, got.Reason, tc.reason)
		}
		// 消せないときは必ず理由を言う。黙って見逃すと公開が続く
		if !got.Deletable && got.Reason == "" {
			t.Errorf("%s: 消せないのに理由が無い", tc.name)
		}
	}
}

// 孤児だけを拾う。生きている環境と、grace の中にある新しいものは返さない。
func TestFindOrphans(t *testing.T) {
	d, ctx := testDriver(t)
	bucket := "kagerou-test-orphans"
	makeBucket(t, d, ctx, bucket)

	put := func(key string) {
		body := strings.NewReader("x")
		if _, err := d.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &bucket, Key: &key, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 生きている環境 / 孤児 / 名前空間の外
	put("spa/pr-1/index.html")
	put("spa/pr-2/index.html")
	put("spa/pr-2/app.js")
	put("other/pr-3/index.html")

	known := map[string]bool{"pr-1": true}
	// すべて「今」作ったので、grace を跨いだ時刻で見れば全部が候補になる
	orphans, err := d.FindOrphans(ctx, bucket, "spa/", known, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 {
		var got []string
		for _, o := range orphans {
			got = append(got, o.Name)
		}
		t.Fatalf("孤児は pr-2 だけのはず: %v", got)
	}
	if orphans[0].Name != "pr-2" || orphans[0].Prefix != "spa/pr-2" {
		t.Errorf("orphan = %+v", orphans[0])
	}
	if orphans[0].Objects != 2 {
		t.Errorf("objects = %d, want 2", orphans[0].Objects)
	}

	// **grace の中は返さない。** タグ検索は結果整合で、up 直後の環境は list に
	// 出ないことがある。それを孤児と読んで消すと作りたてを壊す
	fresh, err := d.FindOrphans(ctx, bucket, "spa/", known, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 {
		t.Errorf("grace の中のものを孤児として返した: %+v", fresh)
	}

	// 名前空間の外は見ない
	for _, o := range orphans {
		if strings.HasPrefix(o.Prefix, "other/") {
			t.Errorf("名前空間の外を拾った: %s", o.Prefix)
		}
	}
}

// 消したら本当に消えていること。消し残しを成功扱いしない(#238 と同じ理由)。
func TestDeleteOrphan(t *testing.T) {
	d, ctx := testDriver(t)
	bucket := "kagerou-test-orphan-delete"
	makeBucket(t, d, ctx, bucket)

	for _, key := range []string{"spa/pr-9/index.html", "spa/pr-9/assets/app.js", "spa/pr-8/index.html"} {
		k := key
		body := strings.NewReader("x")
		if _, err := d.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: &k, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	orphans, err := d.FindOrphans(ctx, bucket, "spa/", map[string]bool{"pr-8": true}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Name != "pr-9" {
		t.Fatalf("孤児の検出がおかしい: %+v", orphans)
	}
	if err := d.DeleteOrphan(ctx, bucket, orphans[0]); err != nil {
		t.Fatal(err)
	}
	left, err := d.listPrefix(ctx, bucket, "spa/pr-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("消し残し: %v", left)
	}
	// 生きている方は残る
	alive, err := d.listPrefix(ctx, bucket, "spa/pr-8")
	if err != nil {
		t.Fatal(err)
	}
	if len(alive) != 1 {
		t.Errorf("生きている環境を消した: %v", alive)
	}
}
