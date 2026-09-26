package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rikukadev/kagerou/internal/driver/stack"
)

type fakeLister struct {
	infos []*stack.Info
	err   error
	// gotProject は List に渡された project を記録する。serve は横断ビューなので
	// 空文字(全 project)で呼ぶことを固定する(#243)。
	gotProject *string
}

func (f fakeLister) List(_ context.Context, project string) ([]*stack.Info, error) {
	if f.gotProject != nil {
		*f.gotProject = project
	}
	return f.infos, f.err
}

func info(name, project, status string) *stack.Info {
	return &stack.Info{
		StackName:    "kagerou-" + name,
		Status:       status,
		CreationTime: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		Tags: map[string]string{
			stack.TagName:    name,
			stack.TagProject: project,
		},
	}
}

func newTestServer(l lister) *serveHandler {
	return &serveHandler{lister: l, ctx: context.Background()}
}

func doGET(t *testing.T, h *serveHandler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServeListAll(t *testing.T) {
	h := newTestServer(fakeLister{infos: []*stack.Info{
		info("pr-1", "todo", "CREATE_COMPLETE"),
		info("pr-2", "shop", "UPDATE_COMPLETE"),
	}})
	rec := doGET(t, h, "/environments")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 envs, got %d", len(got))
	}
	if got[0]["name"] != "pr-1" || got[0]["project"] != "todo" || got[0]["state"] != "ready" {
		t.Fatalf("unexpected env[0]: %v", got[0])
	}
}

func TestServeFilterByProject(t *testing.T) {
	h := newTestServer(fakeLister{infos: []*stack.Info{
		info("pr-1", "todo", "CREATE_COMPLETE"),
		info("pr-2", "shop", "CREATE_COMPLETE"),
		info("pr-3", "todo", "CREATE_COMPLETE"),
	}})
	rec := doGET(t, h, "/environments?project=todo")
	var got []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("project=todo は 2 件のはず: %d", len(got))
	}
	for _, e := range got {
		if e["project"] != "todo" {
			t.Fatalf("foreign project leaked: %v", e)
		}
	}
}

func TestServeGetOne(t *testing.T) {
	h := newTestServer(fakeLister{infos: []*stack.Info{
		info("pr-1", "todo", "CREATE_COMPLETE"),
	}})
	rec := doGET(t, h, "/environments/pr-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "pr-1" {
		t.Fatalf("unexpected: %v", got)
	}
	if rec := doGET(t, h, "/environments/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing は 404 のはず: %d", rec.Code)
	}
}

func TestServeReadOnly(t *testing.T) {
	h := newTestServer(fakeLister{})
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/environments", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST は 405 のはず: %d", rec.Code)
	}
	if rec.Header().Get("Allow") != "GET" {
		t.Fatalf("Allow: GET を返すはず: %q", rec.Header().Get("Allow"))
	}
}

func TestServeHealthz(t *testing.T) {
	h := newTestServer(fakeLister{})
	rec := doGET(t, h, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
}

func TestServeListerError(t *testing.T) {
	h := newTestServer(fakeLister{err: errors.New("aws down")})
	rec := doGET(t, h, "/environments")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("driver エラーは 502 のはず: %d", rec.Code)
	}
}

// TestServeListsAllProjects は serve が横断ビューとして **project を絞らずに**
// List を呼ぶことを固定する(#243)。ここで project を渡してしまうと、
// 他プロジェクトの環境が一覧から消える。
func TestServeListsAllProjects(t *testing.T) {
	var got string
	h := newTestServer(fakeLister{gotProject: &got, infos: []*stack.Info{
		info("pr-1", "todo", "CREATE_COMPLETE"),
	}})
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/environments", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if got != "" {
		t.Errorf("serve が project を絞って List を呼んだ: %q (want \"\")", got)
	}
}
