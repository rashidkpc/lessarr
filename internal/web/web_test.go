package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/compare"
	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := queue.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Server.DB = dbPath
	cfg.Libraries = []config.Library{{Name: "Movies", Path: "/media/movies"}}
	return New(&cfg, store, "test", nil, nil, nil)
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
	for _, want := range []string{"lessarr", "Dashboard", "Queued", "Done", "Reclaimed"} {
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
	if err := s.store.SaveAnalysis(job.ID, `{"video":{"before":1000,"after":500,"saved":500},"total":{"before":1000,"after":500,"saved":500}}`); err != nil {
		t.Fatal(err)
	}
	compareDir := filepath.Join(compare.Dir(s.cfg.Server.DB), strconv.FormatInt(job.ID, 10))
	if err := os.MkdirAll(compareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(compareDir, "index.html"), []byte("comparison"), 0o644); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.FormatInt(job.ID, 10), nil))
	body := w.Body.String()
	for _, want := range []string{"Example.mkv", "Decision", "h264 video", "running", "Savings breakdown", "compare original vs new"} {
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
	}, nil, nil)
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

func TestRetryEndpoint(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := queue.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Server.DB = dbPath
	cfg.Libraries = []config.Library{{Name: "Movies", Path: "/media/movies", Write: true}}
	var called int64
	s := New(&cfg, store, "test", nil, func(id int64) error { called = id; return nil }, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs/42/retry?tab=done", nil))
	if w.Code != http.StatusSeeOther || called != 42 {
		t.Fatalf("retry status=%d called=%d", w.Code, called)
	}
	if got := w.Header().Get("Location"); got != "/?tab=done" {
		t.Errorf("retry redirect = %q, want done tab", got)
	}
}

func TestJobPageShowsRetryWhenOriginalExists(t *testing.T) {
	s := testServer(t)
	s.cfg.Libraries[0].Write = true
	path := "/media/movies/Example.mkv"
	if _, err := s.store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	job, err := s.store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), ".lessarr-trash", "original.mkv")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Complete(job.ID, queue.Done, 50, backup); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.FormatInt(job.ID, 10), nil))
	if body := w.Body.String(); !strings.Contains(body, "Retry with current settings") {
		t.Error("job page should offer retry when the original exists")
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
	for _, want := range []string{"Example.mkv", "pending", "Queue", "All queues", "All libraries", "value=\"transcode\" selected", "Movies · transcode", "file-filters", "files-summary", "files-results", "new DOMParser()"} {
		if !strings.Contains(body, want) {
			t.Errorf("files page missing %q", want)
		}
	}
	if strings.Contains(body, ">Apply<") {
		t.Error("files page still has an Apply button")
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
	}, nil, nil)
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

func TestBasicAuthRequired(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := queue.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Server.DB = dbPath
	cfg.Server.Username = "admin"
	cfg.Server.Password = "secret"
	cfg.Libraries = []config.Library{{Name: "Movies", Path: "/media/movies"}}
	s := New(&cfg, store, "test", nil, nil, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d", w.Code)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pause", nil)
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", w.Code)
	}
}

func TestTrashReportPageAndAPI(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "movies")
	trashDir := filepath.Join(lib, ".lessarr-trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(trashDir, "orphan.mkv")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	store, err := queue.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Server.DB = dbPath
	cfg.Libraries = []config.Library{{Name: "Movies", Path: lib}}
	s := New(&cfg, store, "test", nil, nil, nil)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/trash", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("trash page = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Retained originals on disk") || !strings.Contains(body, "Trash locations") || !strings.Contains(body, orphan) {
		t.Errorf("trash page missing report content")
	}

	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/trash", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("trash api = %d", w.Code)
	}
	var report struct {
		OrphanFiles []struct {
			Path string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.OrphanFiles) != 1 || report.OrphanFiles[0].Path != orphan {
		t.Fatalf("api orphans = %+v", report.OrphanFiles)
	}

	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/trash/delete?path="+url.QueryEscape(orphan), nil))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete orphan = %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan was not deleted")
	}
}

func TestRestoreEndpointAndBadge(t *testing.T) {
	s := testServer(t)
	var called int64
	s.restore = func(id int64) error { called = id; return nil }
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/jobs/42/restore", nil))
	if w.Code != http.StatusSeeOther || called != 42 {
		t.Fatalf("status=%d called=%d", w.Code, called)
	}

	path := "/media/movies/Restored.mkv"
	if _, err := s.store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	job, err := s.store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.store.Fail(job.ID, 3, 3, queue.RestoredReason, 0); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs/"+strconv.FormatInt(job.ID, 10), nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "restored") {
		t.Fatalf("restored badge missing: %d %s", w.Code, w.Body.String())
	}
}

func TestMetricsPage(t *testing.T) {
	s := testServer(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.store.RecordMetric(queue.Metric{At: now.Add(time.Duration(i) * time.Minute), Pending: i, Failed: 1, TrashBytes: 100, SavedBytes: 200}); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("metrics page = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"Metrics", "Queue depth", "Failures", "Trash", "Reclaimed", "<svg", "y-axis"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics page missing %q", want)
		}
	}
}

func TestRetryHiddenWhileJobIsQueued(t *testing.T) {
	s := testServer(t)
	lib := t.TempDir()
	s.cfg.Libraries = []config.Library{{Name: "Movies", Path: lib, Write: true}}
	path := filepath.Join(lib, "Movie.mkv")
	if err := os.WriteFile(path, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 5}); err != nil {
		t.Fatal(err)
	}
	job, err := s.store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.store.Fail(job.ID, 3, 3, "failed", 0); err != nil {
		t.Fatal(err)
	}
	job, err = s.store.Job(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !s.retryable(job) {
		t.Fatal("retry should be available before the item is queued")
	}
	if _, err := s.store.EnqueueRetry(queue.Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 5}); err != nil {
		t.Fatal(err)
	}
	if s.retryable(job) {
		t.Fatal("retry should be unavailable while the item is queued")
	}
}

func TestNavHighlightsCurrentScreen(t *testing.T) {
	s := testServer(t)
	for path, href := range map[string]string{
		"/": "/", "/files": "/files", "/trash": "/trash", "/metrics": "/metrics",
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, w.Code)
		}
		want := `class="active" href="` + href + `"`
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s missing active nav %q", path, want)
		}
	}
}

func TestRunningTabShowsUpNext(t *testing.T) {
	s := testServer(t)
	s.cfg.Libraries[0].Write = true
	path := "/media/movies/Next.mkv"
	if _, err := s.store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Up next") || !strings.Contains(body, path) {
		t.Errorf("running tab missing up next: %s", body)
	}
}
