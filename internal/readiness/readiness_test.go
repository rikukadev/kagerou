package readiness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitImmediate200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	if err := Wait(context.Background(), srv.URL, "/healthz", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestWaitEventually200(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 2 { // 1 回目は 503(cold start 想定)
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := Wait(context.Background(), srv.URL, "healthz", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if n.Load() < 2 {
		t.Fatalf("should have polled at least twice, got %d", n.Load())
	}
}

func TestWaitTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	err := Wait(context.Background(), srv.URL, "/healthz", 1*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("want timeout error, got %v", err)
	}
}
