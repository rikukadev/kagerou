package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rikukadev/kagerou/internal/config"
	"github.com/rikukadev/kagerou/internal/driver/stack"
)

// cmdServe は読み取り専用 HTTP を立てる(CONTRACT §6・#16)。Backstage 等が
// 環境一覧を読む口。書き込み(down / TTL 延長)は提供しない — それは CI の
// workflow_dispatch 経由に留め、権限もロジックも CI 側に置く。
// Lambda function URL では AWS Lambda Web Adapter(LWA)越しにそのまま動く
// (PORT を見る)。案 A(薄い HTTP)のまま、案 B(常駐)への移行路にもなる。
func cmdServe(args []string, out *os.File) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultFile, "config file")
	addr := fs.String("addr", defaultServeAddr(), "listen address (default: :$PORT or :8080)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*cfgPath)
	if err != nil {
		return err
	}
	drv, ctx, err := newDriver(cfg)
	if err != nil {
		return err
	}

	h := &serveHandler{lister: drv, ctx: ctx}
	if _, err := fmt.Fprintf(out, "kagerou serve: listening on %s (read-only: GET /environments, /environments/{name}, /healthz)\n", *addr); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           h.mux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

// defaultServeAddr は LWA / コンテナ流儀に合わせ、PORT があればそれを使う。
func defaultServeAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

// lister は serve が必要とする driver の一部(読み取りのみ)。テストで差し替える。
type lister interface {
	List(ctx context.Context) ([]*stack.Info, error)
}

type serveHandler struct {
	lister lister
	ctx    context.Context
}

func (h *serveHandler) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/environments/", h.getOne) // /environments/<name>
	mux.HandleFunc("/environments", h.listAll)
	return mux
}

// environments は list 相当の Environment JSON(CONTRACT §3)を全プロジェクト分返す。
// serve は横断ビュー用なので既定で全件(list の --all-projects 相当)。
func (h *serveHandler) environments() ([]map[string]any, error) {
	infos, err := h.lister.List(h.ctx)
	if err != nil {
		return nil, err
	}
	envs := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		envs = append(envs, environmentJSON(info.Tags[stack.TagName], info))
	}
	return envs, nil
}

func (h *serveHandler) listAll(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) {
		return
	}
	envs, err := h.environments()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// ?project= で 1 プロジェクトに絞れる(Backstage の Component 紐付け用)。
	if p := r.URL.Query().Get("project"); p != "" {
		filtered := make([]map[string]any, 0, len(envs))
		for _, e := range envs {
			if e["project"] == p {
				filtered = append(filtered, e)
			}
		}
		envs = filtered
	}
	writeJSON(w, http.StatusOK, envs)
}

func (h *serveHandler) getOne(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/environments/")
	if name == "" || strings.Contains(name, "/") {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}
	envs, err := h.environments()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	for _, e := range envs {
		if e["name"] == name {
			writeJSON(w, http.StatusOK, e)
			return
		}
	}
	writeErr(w, http.StatusNotFound, fmt.Errorf("environment %q not found", name))
}

func (h *serveHandler) healthz(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// readOnly は GET 以外を 405 で弾く。書き込みは CI の workflow_dispatch 経由。
func readOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeErr(w, http.StatusMethodNotAllowed,
			fmt.Errorf("read-only: use GET (writes go through CI workflow_dispatch)"))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}
