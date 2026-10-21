// Package web is the small read-mostly status server. It intentionally has no
// build step and no external assets so the binary is still one file.
package web

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/compare"
	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
	"github.com/rashidkpc/lessarr/internal/trash"
)

type Server struct {
	cfg       *config.Config
	store     *queue.Store
	version   string
	progress  func() *Progress
	retry     func(int64) error
	restore   func(int64) error
	templates *template.Template
}

type Step struct {
	Label   string
	At      time.Time
	Done    bool
	Current bool
}

// Progress is a snapshot of the currently running job's encoder progress.
type Progress struct {
	JobID       int64
	Phase       string
	Frame       int64
	OutTime     time.Duration
	Duration    time.Duration
	Speed       string
	TotalSize   int64
	Percent     float64
	Stage       string
	Steps       []Step
	SampleDone  int
	SampleTotal int
	SamplePct   float64
	Started     time.Time
	Updated     time.Time
}

func New(cfg *config.Config, store *queue.Store, version string, progress func() *Progress, retry, restore func(int64) error) *Server {
	return &Server{
		cfg: cfg, store: store, version: version, progress: progress, retry: retry, restore: restore,
		templates: template.Must(template.New("root").Funcs(template.FuncMap{
			"bytes":        humanBytes,
			"when":         humanWhen,
			"saved":        savedPercent,
			"sizePair":     sizePair,
			"sizeEstimate": sizeEstimate,
			"dur":          humanDuration,
			"sbytes":       signedBytes,
		}).Parse(sharedHTML + dashboardHTML + pageHTML + filesHTML + jobHTML + trashHTML + metricsHTML)),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" && !s.authorized(w, r) {
		return
	}
	if r.Method == http.MethodPost && !sameOrigin(r) {
		http.Error(w, "cross-origin POST refused", http.StatusForbidden)
		return
	}
	switch {
	case r.URL.Path == "/healthz":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	case r.URL.Path == "/api/status":
		s.statusJSON(w)
	case r.URL.Path == "/api/pause" && r.Method == http.MethodPost:
		s.setPaused(w, r, true)
	case r.URL.Path == "/api/resume" && r.Method == http.MethodPost:
		s.setPaused(w, r, false)
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		s.statusPage(w, r)
	case r.URL.Path == "/fragment" && r.Method == http.MethodGet:
		s.fragmentPage(w, r)
	case r.URL.Path == "/files" && r.Method == http.MethodGet:
		s.filesPage(w, r)
	case r.URL.Path == "/trash" && r.Method == http.MethodGet:
		s.trashPage(w, r)
	case r.URL.Path == "/metrics" && r.Method == http.MethodGet:
		s.metricsPage(w, r)
	case r.URL.Path == "/api/trash":
		s.trashJSON(w)
	case r.URL.Path == "/api/trash/delete" && r.Method == http.MethodPost:
		s.deleteOrphan(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/jobs/") && strings.HasSuffix(r.URL.Path, "/retry") && r.Method == http.MethodPost:
		s.retryJob(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/jobs/") && strings.HasSuffix(r.URL.Path, "/restore") && r.Method == http.MethodPost:
		s.restoreJob(w, r)
	case strings.HasPrefix(r.URL.Path, "/jobs/") && r.Method == http.MethodGet:
		s.jobPage(w, r)
	case strings.HasPrefix(r.URL.Path, "/comparisons/"):
		http.StripPrefix("/comparisons/", http.FileServer(http.Dir(compare.Dir(s.cfg.Server.DB)))).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) statusJSON(w http.ResponseWriter) {
	status, err := s.collect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(status)
}

func (s *Server) prepareStatus(r *http.Request) (Status, error) {
	status, err := s.collect()
	if err != nil {
		return Status{}, err
	}
	status.Page = "dashboard"
	status.Tab = r.URL.Query().Get("tab")
	switch status.Tab {
	case "running", "queued", "done", "failed":
	default:
		status.Tab = "running"
	}
	status.Tabs = []TabLink{
		{Key: "running", Label: "Running", Count: status.Counts.Running, Href: "/?tab=running", Active: status.Tab == "running"},
		{Key: "queued", Label: "Queued", Count: status.Counts.Pending, Href: "/?tab=queued", Active: status.Tab == "queued"},
		{Key: "done", Label: "Done", Count: status.Counts.Done, Href: "/?tab=done", Active: status.Tab == "done"},
		{Key: "failed", Label: "Failed", Count: status.Counts.Failed, Href: "/?tab=failed", Active: status.Tab == "failed"},
	}
	return status, nil
}

type TrashPage struct {
	Version    string
	Page       string
	Report     trash.Report
	TrashLimit int64
	Retention  string
}

func (s *Server) loadTrashReport() (trash.Report, error) {
	return trash.BuildReport(s.store, s.cfg.Libraries)
}

func (s *Server) trashJSON(w http.ResponseWriter) {
	report, err := s.loadTrashReport()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
}

func (s *Server) deleteOrphan(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := trash.DeleteOrphan(s.store, s.cfg.Libraries, path); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/trash", http.StatusSeeOther)
}

func (s *Server) trashPage(w http.ResponseWriter, r *http.Request) {
	report, err := s.loadTrashReport()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := TrashPage{
		Version: s.version, Page: "trash", Report: report,
		TrashLimit: s.cfg.Trash.MaxSizeGB,
		Retention:  s.cfg.Trash.Retention.Std().String(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "trash", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := s.store.SetPaused(paused); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Server.Username == "" && s.cfg.Server.Password == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if ok {
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Server.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Server.Password)) == 1
		if userOK && passOK {
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="lessarr"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return false
}

func sameOrigin(r *http.Request) bool {
	target := r.Header.Get("Origin")
	if target == "" {
		target = r.Header.Get("Referer")
	}
	if target == "" {
		return true
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) restoreJob(w http.ResponseWriter, r *http.Request) {
	idText := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/restore"), "/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	if s.restore == nil {
		http.Error(w, "restore is unavailable", http.StatusInternalServerError)
		return
	}
	if err := s.restore(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", id), http.StatusSeeOther)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	idText := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/retry"), "/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid job ID", http.StatusBadRequest)
		return
	}
	if s.retry == nil {
		http.Error(w, "retry is unavailable", http.StatusInternalServerError)
		return
	}
	if err := s.retry(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	tab := r.FormValue("tab")
	if tab == "" {
		if referer, err := url.Parse(r.Referer()); err == nil {
			tab = referer.Query().Get("tab")
		}
	}
	switch tab {
	case "running", "queued", "done", "failed":
	default:
		tab = "queued"
	}
	http.Redirect(w, r, "/?tab="+url.QueryEscape(tab), http.StatusSeeOther)
}

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	status, err := s.prepareStatus(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "page", status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) fragmentPage(w http.ResponseWriter, r *http.Request) {
	status, err := s.prepareStatus(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.templates.ExecuteTemplate(w, "dashboard", status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type TabLink struct {
	Key    string
	Label  string
	Count  int
	Href   string
	Active bool
}

type Status struct {
	Version       string
	Page          string
	Paused        bool
	WritesEnabled bool
	Tab           string
	Tabs          []TabLink
	Counts        queue.Counts
	Current       *queue.Job
	Progress      *Progress
	Queued        []queue.Job
	UpNext        []queue.Job
	Done          []queue.Job
	Failed        []queue.Job
	LibraryFiles  int
	LibraryBytes  int64
	TrashFiles    int
	TrashBytes    int64
	TrashLimitGB  int64
	Saved30d      int64
	Done30d       int
	Ready         int
}

func (s *Server) collect() (Status, error) {
	paused, err := s.store.Paused()
	if err != nil {
		return Status{}, err
	}
	counts, err := s.store.Counts()
	if err != nil {
		return Status{}, err
	}
	current, err := s.store.Current()
	if err != nil {
		return Status{}, err
	}
	queued, err := s.store.ByState(queue.Pending, 25)
	if err != nil {
		return Status{}, err
	}
	done, err := s.store.ByState(queue.Done, 20)
	if err != nil {
		return Status{}, err
	}
	failed, err := s.store.ByState(queue.Failed, 10)
	if err != nil {
		return Status{}, err
	}
	for i := range done {
		done[i].Retryable = s.retryable(&done[i])
		done[i].Restorable = s.restorable(&done[i])
	}
	for i := range failed {
		failed[i].Retryable = s.retryable(&failed[i])
	}
	trashStats, err := trash.Summary(s.store)
	if err != nil {
		return Status{}, err
	}
	now := time.Now()
	saved30d, done30d, err := s.store.SavedSince(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		return Status{}, err
	}
	libraryFiles, libraryBytes, err := s.store.LibraryTotals()
	if err != nil {
		return Status{}, err
	}
	if queued == nil {
		queued = []queue.Job{}
	}
	if done == nil {
		done = []queue.Job{}
	}
	if failed == nil {
		failed = []queue.Job{}
	}
	writable := make([]string, 0, len(s.cfg.Libraries))
	for _, lib := range s.cfg.Libraries {
		if lib.Write {
			writable = append(writable, lib.Name)
		}
	}
	now = time.Now()
	upNext, err := s.store.NextQueued(now, 5, writable...)
	if err != nil {
		return Status{}, err
	}
	if upNext == nil {
		upNext = []queue.Job{}
	}
	ready, err := s.store.CountReady(now, writable...)
	if err != nil {
		return Status{}, err
	}
	var progress *Progress
	if s.progress != nil {
		progress = s.progress()
	}
	status := Status{
		Version: s.version, Paused: paused, Counts: counts,
		Current: current, Progress: progress, Queued: queued, UpNext: upNext,
		Done: done, Failed: failed,
		LibraryFiles: libraryFiles, LibraryBytes: libraryBytes,
		TrashFiles: trashStats.Files, TrashBytes: trashStats.Bytes, TrashLimitGB: s.cfg.Trash.MaxSizeGB,
		Saved30d: saved30d, Done30d: done30d, Ready: ready, WritesEnabled: len(writable) > 0,
	}
	return status, nil
}

func sizePair(before, after int64) string {
	if after > 0 {
		return humanBytes(before) + " → " + humanBytes(after)
	}
	return humanBytes(before)
}

func sizeEstimate(before, estimated int64, unmeasured bool) string {
	if unmeasured {
		return humanBytes(before) + " → unmeasured"
	}
	if estimated > 0 {
		return humanBytes(before) + " → " + humanBytes(estimated)
	}
	return humanBytes(before)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func signedBytes(n int64) string {
	if n < 0 {
		return "-" + humanBytes(-n)
	}
	return humanBytes(n)
}

func humanWhen(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return t.Format("2006-01-02 15:04")
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func savedPercent(before, after int64) string {
	if before <= 0 || after <= 0 || after >= before {
		return "—"
	}
	return formatFloat(float64(before-after)/float64(before)*100) + "%"
}
