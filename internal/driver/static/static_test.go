package static

// moto に対する結合テスト(DESIGN §9)。AWS_ENDPOINT_URL 未設定なら skip。

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rikukadev/kagerou/internal/driver/stack"
)

func testDriver(t *testing.T) (*Driver, context.Context) {
	t.Helper()
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		t.Skip("AWS_ENDPOINT_URL 未設定(moto なし)のため skip — make test-aws で実行する")
	}
	ctx := context.Background()
	d, err := New(ctx, os.Getenv("AWS_REGION"))
	if err != nil {
		t.Fatal(err)
	}
	return d, ctx
}

func makeBucket(t *testing.T, d *Driver, ctx context.Context, name string) {
	t.Helper()
	if _, err := d.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &name}); err != nil {
		t.Fatal(err)
	}
}

func writeDist(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func keysUnder(t *testing.T, d *Driver, ctx context.Context, bucket, name string) []string {
	t.Helper()
	keys, err := d.listPrefix(ctx, bucket, name)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	return keys
}

func TestStaticLifecycle(t *testing.T) {
	d, ctx := testDriver(t)
	bucket := "kagerou-test-static-base"
	makeBucket(t, d, ctx, bucket)

	stackName := "kagerou-test-static-pr-1"
	t.Cleanup(func() { _ = d.Down(ctx, stackName, bucket, "pr-1") })

	dist := writeDist(t, map[string]string{
		"index.html":     "<html>hi</html>",
		"assets/app.js":  "console.log(1)",
		"assets/app.css": "body{}",
		"config.json":    `{"api":"https://x"}`,
	})
	expires := time.Now().Add(72 * time.Hour)

	info, err := d.Up(ctx, UpInput{
		UpInput: stack.UpInput{
			StackName: stackName, Name: "pr-1", Project: "spa",
			ExpiresAt: &expires, URL: "https://pr-1.spa.example.test", Version: "test",
		},
		Bucket: bucket, Dist: dist,
	})
	if err != nil {
		t.Fatal(err)
	}

	// メタスタックが「環境 = CFN スタック 1 個」を満たし、共通タグが付いている
	if info.Tags[stack.TagManaged] != "true" || info.Tags[stack.TagName] != "pr-1" || info.Tags[stack.TagProject] != "spa" {
		t.Fatalf("tags = %v", info.Tags)
	}
	// URL は url_template 由来(タグ経由)で解決できる
	if u, ok := info.EnvironmentURL(); !ok || u != "https://pr-1.spa.example.test" {
		t.Fatalf("EnvironmentURL = %q ok=%v", u, ok)
	}
	// list(stack driver 共通)から見える
	infos, err := d.stack.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range infos {
		if i.StackName == stackName {
			found = true
		}
	}
	if !found {
		t.Fatal("static environment should appear in the common list")
	}

	// 内容が {name}/ プレフィックスに載っている
	want := []string{"pr-1/assets/app.css", "pr-1/assets/app.js", "pr-1/config.json", "pr-1/index.html"}
	if got := keysUnder(t, d, ctx, bucket, "pr-1"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	// Content-Type が付いている(既定の octet-stream だとブラウザが表示できない)
	key := "pr-1/index.html"
	head, err := d.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		t.Fatal(err)
	}
	if head.ContentType == nil || !strings.HasPrefix(*head.ContentType, "text/html") {
		t.Fatalf("content-type = %v, want text/html", head.ContentType)
	}

	// 再 up で消えたファイルは S3 からも消える(--delete 相当)
	dist2 := writeDist(t, map[string]string{"index.html": "<html>v2</html>"})
	if _, err := d.Up(ctx, UpInput{
		UpInput: stack.UpInput{StackName: stackName, Name: "pr-1", Project: "spa", URL: "https://pr-1.spa.example.test"},
		Bucket:  bucket, Dist: dist2,
	}); err != nil {
		t.Fatal(err)
	}
	if got := keysUnder(t, d, ctx, bucket, "pr-1"); len(got) != 1 || got[0] != "pr-1/index.html" {
		t.Fatalf("stale objects not deleted: %v", got)
	}

	// down でスタックもオブジェクトも消える。2 回目も成功(冪等)
	if err := d.Down(ctx, stackName, bucket, "pr-1"); err != nil {
		t.Fatal(err)
	}
	if got := keysUnder(t, d, ctx, bucket, "pr-1"); len(got) != 0 {
		t.Fatalf("objects remain after down: %v", got)
	}
	if err := d.Down(ctx, stackName, bucket, "pr-1"); err != nil {
		t.Fatalf("down should be idempotent: %v", err)
	}
}

func TestStaticUpValidation(t *testing.T) {
	d, ctx := testDriver(t)
	base := UpInput{UpInput: stack.UpInput{StackName: "x", Name: "n"}, Bucket: "b", Dist: "d"}

	noBucket := base
	noBucket.Bucket = ""
	if _, err := d.Up(ctx, noBucket); err == nil {
		t.Error("bucket 未指定はエラーのはず")
	}
	missingDist := base
	missingDist.Dist = filepath.Join(t.TempDir(), "nope")
	if _, err := d.Up(ctx, missingDist); err == nil {
		t.Error("dist が無いディレクトリはエラーのはず")
	}
	emptyDist := base
	emptyDist.Dist = t.TempDir()
	emptyDist.Bucket = "kagerou-test-static-empty"
	makeBucket(t, d, ctx, emptyDist.Bucket)
	t.Cleanup(func() { _ = d.Down(ctx, "x", emptyDist.Bucket, "n") })
	if _, err := d.Up(ctx, emptyDist); err == nil || !strings.Contains(err.Error(), "no files") {
		t.Errorf("空 dist はビルド忘れとして落とすはず: %v", err)
	}
}

func TestContentType(t *testing.T) {
	cases := map[string]string{
		"a/index.html": "text/html",
		"a/app.js":     "javascript", // text/javascript か application/javascript(環境差)
		"a/app.css":    "text/css",
		"a/x.json":     "application/json",
		"a/bin":        "application/octet-stream",
	}
	for path, want := range cases {
		got := *contentType(path)
		if !strings.Contains(got, want) {
			t.Errorf("contentType(%q) = %q, want to contain %q", path, got, want)
		}
	}
}
