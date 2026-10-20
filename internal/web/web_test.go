package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	return New(&cfg, store, "test", nil)
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
	for _, want := range []string{"lessarr", "Running", "Queued", "Done", "observe only"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestStatusPageTabs(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/?tab=queued", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="tab active" href="/?tab=queued"`) {
		t.Error("queued tab should be active")
	}
	if strings.Count(body, "tab active") != 1 {
		t.Errorf("expected exactly one active tab, got %d", strings.Count(body, "tab active"))
	}
}

func TestJobPage(t *testing.T) {
	s := testServer(t)
	path := "/media/movies/Example.mkv"
	if err := s.store.PutFile(queue.File{Path: path, Library: "Movies", Size: 1234,
		Action: "transcode", Reason: "h264 video", Codec: "h264"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode",
		Reason: "h264 video", SizeBefore: 1234}); err != nil {
		t.Fatal(err)
	}
	job, err := s.store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.FormatInt(job.ID, 10), nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Example.mkv", "Decision", "h264 video", "running"} {
		if !strings.Contains(body, want) {
			t.Errorf("job page missing %q", want)
		}
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs/9999", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing job status = %d", w.Code)
	}
}

func TestFragmentUpdatesWithoutPageShell(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/fragment?tab=queued", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<script") {
		t.Error("fragment should not include the page shell")
	}
	if !strings.Contains(body, "Queued") || !strings.Contains(body, `href="/?tab=queued"`) {
		t.Error("fragment should render the tab bar")
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestPagePollsFragmentInsteadOfReloading(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `id="live"`) || !strings.Contains(body, "/fragment?tab=") {
		t.Error("page should poll the live fragment endpoint")
	}
	if strings.Contains(body, "location.reload") {
		t.Error("page should not reload the whole document")
	}
}

func TestSamplingProgressIsCollapsedWithSubBar(t *testing.T) {
	store, err := queue.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Libraries = []config.Library{{Name: "Movies", Path: "/media/movies"}}
	if _, err := store.Enqueue(queue.Job{Path: "/media/movies/a.mkv", Library: "Movies", Action: "remux", SizeBefore: 123}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s := New(&cfg, store, "test", func() *Progress {
		return &Progress{JobID: 1, Phase: "validating", Stage: "sampling 3/5",
			SampleDone: 2, SampleTotal: 5, SamplePct: 40, Started: now, Updated: now,
			Steps: []Step{
				{Label: "validating", Done: true},
				{Label: "scanning packets", Done: true},
				{Label: "sampling 1/5", Done: true},
				{Label: "sampling 2/5", Done: true},
				{Label: "sampling 3/5"},
			}}
	})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	for _, want := range []string{"sampling 3/5", `details class="stages"`, "sampling 2/5", "width:40.0%"} {
		if !strings.Contains(body, want) {
			t.Errorf("sampling progress missing %q", want)
		}
	}
}

func TestPauseAndResumeEndpoint(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/pause", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("pause status = %d", w.Code)
	}
	if paused, err := s.store.Paused(); err != nil || !paused {
		t.Fatalf("Paused = %v, %v", paused, err)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/resume", nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("resume status = %d", w.Code)
	}
	if paused, err := s.store.Paused(); err != nil || paused {
		t.Fatalf("Paused after resume = %v, %v", paused, err)
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
	if status.Version != "test" || status.Counts.Pending != 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestFilesPageShowsQueuedCandidate(t *testing.T) {
	s := testServer(t)
	path := "/media/movies/Example.mkv"
	if err := s.store.PutFile(queue.File{Path: path, Library: "Movies", Size: 1234,
		Action: "transcode", Codes: "codec_gap", Reason: "h264 video", Codec: "h264"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/files?action=transcode", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Example.mkv", "pending", "observe only", "candidate job"} {
		if !strings.Contains(body, want) {
			t.Errorf("files page missing %q", want)
		}
	}
}

func TestProgressAppearsInStatusAndPage(t *testing.T) {
	store, err := queue.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Libraries = []config.Library{{Name: "Movies", Path: "/media/movies"}}
	if _, err := store.Enqueue(queue.Job{Path: "/media/movies/a.mkv", Library: "Movies", Action: "transcode", SizeBefore: 123}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s := New(&cfg, store, "test", func() *Progress {
		return &Progress{JobID: 7, Phase: "encoding", Stage: "encoding", Percent: 42, Frame: 1234,
			OutTime: 30 * time.Second, Duration: 2 * time.Minute, Speed: "2.4x",
			TotalSize: 100 << 20, Started: now, Updated: now,
			Steps: []Step{{Label: "preparing", Done: true}, {Label: "encoding"}}}
	})
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var status Status
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Progress == nil || status.Progress.Percent != 42 || status.Progress.Phase != "encoding" {
		t.Fatalf("progress = %+v", status.Progress)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	body := w.Body.String()
	for _, want := range []string{"preparing", "encoding", "42%", "2.4x", "0:30", "2:00"} {
		if !strings.Contains(body, want) {
			t.Errorf("progress page missing %q", want)
		}
	}
}
