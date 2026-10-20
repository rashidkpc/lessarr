// Package web is the small read-mostly status server. It intentionally has no
// build step and no external assets so the binary is still one file.
package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

type Server struct {
	cfg       *config.Config
	store     *queue.Store
	version   string
	progress  func() *Progress
	templates *template.Template
}

// Progress is a snapshot of the currently running job's encoder progress.
type Progress struct {
	JobID     int64
	Phase     string
	Frame     int64
	OutTime   time.Duration
	Duration  time.Duration
	Speed     string
	TotalSize int64
	Percent   float64
	Started   time.Time
	Updated   time.Time
}

func New(cfg *config.Config, store *queue.Store, version string, progress func() *Progress) *Server {
	return &Server{
		cfg: cfg, store: store, version: version, progress: progress,
		templates: template.Must(template.New("root").Funcs(template.FuncMap{
			"bytes": humanBytes,
			"when":  humanWhen,
			"saved": savedPercent,
			"dur":   humanDuration,
		}).Parse(sharedHTML + dashboardHTML + pageHTML + filesHTML + jobHTML)),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	case r.URL.Path == "/api/status":
		s.statusJSON(w)
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		s.statusPage(w, r)
	case r.URL.Path == "/fragment" && r.Method == http.MethodGet:
		s.fragmentPage(w, r)
	case r.URL.Path == "/files" && r.Method == http.MethodGet:
		s.filesPage(w, r)
	case strings.HasPrefix(r.URL.Path, "/jobs/") && r.Method == http.MethodGet:
		s.jobPage(w, r)
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
	Paused        bool
	WritesEnabled bool
	Tab           string
	Tabs          []TabLink
	Counts        queue.Counts
	Current       *queue.Job
	Progress      *Progress
	Queued        []queue.Job
	Done          []queue.Job
	Failed        []queue.Job
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
	if queued == nil {
		queued = []queue.Job{}
	}
	if done == nil {
		done = []queue.Job{}
	}
	if failed == nil {
		failed = []queue.Job{}
	}
	var progress *Progress
	if s.progress != nil {
		progress = s.progress()
	}
	status := Status{
		Version: s.version, Paused: paused, Counts: counts,
		Current: current, Progress: progress, Queued: queued, Done: done, Failed: failed,
	}
	for _, l := range s.cfg.Libraries {
		if l.Write {
			status.WritesEnabled = true
		}
	}
	return status, nil
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
