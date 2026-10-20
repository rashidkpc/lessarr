package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	store, err := queue.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Libraries = []config.Library{{Name: "Movies", Path: "/media/movies"}}
	return New(&cfg, store, "vaapi", "test")
}

func TestStatusPageRenders(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"lessarr", "Movies", "observe", "Queue"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var status Status
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Backend != "vaapi" || len(status.Libraries) != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestPauseEndpoint(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pause", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	paused, err := s.store.Paused()
	if err != nil || !paused {
		t.Fatalf("Paused = %v, %v", paused, err)
	}
}
