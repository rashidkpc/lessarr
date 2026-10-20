// Package web is the small read-mostly status server. It intentionally has no
// build step and no external assets so the binary is still one file.
package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

type Server struct {
	cfg         *config.Config
	store       *queue.Store
	backend     string
	version     string
	progress    func() *Progress
	scan        func() *ScanStatus
	triggerScan func()
	templates   *template.Template
}

type ScanStatus struct {
	Scanning bool
	Since    time.Time
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

func New(cfg *config.Config, store *queue.Store, backend, version string,
	progress func() *Progress, scan func() *ScanStatus, triggerScan func()) *Server {
	return &Server{
		cfg: cfg, store: store, backend: backend, version: version, progress: progress, scan: scan, triggerScan: triggerScan,
		templates: template.Must(template.New("root").Funcs(template.FuncMap{
			"bytes": humanBytes,
			"when":  humanWhen,
			"saved": savedPercent,
			"dur":   humanDuration,
		}).Parse(sharedHTML + pageHTML + filesHTML)),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	case r.URL.Path == "/api/scan" && r.Method == http.MethodPost:
		if s.triggerScan != nil {
			s.triggerScan()
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		s.statusPage(w)
	case r.URL.Path == "/files" && r.Method == http.MethodGet:
		s.filesPage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := s.store.SetPaused(paused); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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

func (s *Server) statusPage(w http.ResponseWriter) {
	status, err := s.collect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "page", status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type LibraryStatus struct {
	Name    string
	Path    string
	Write   bool
	Paused  bool
	Files   int
	Bytes   int64
	Actions map[string]int
}

type Status struct {
	Version       string
	Backend       string
	Device        string
	Schedule      string
	Paused        bool
	WritesEnabled bool
	ScanEvery     string
	Counts        queue.Counts
	Libraries     []LibraryStatus
	Current       *queue.Job
	Progress      *Progress
	Scan          *ScanStatus
	LastScan      *queue.ScanInfo
	Recent        []queue.Job
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
	files, err := s.store.FilesSummary()
	if err != nil {
		return Status{}, err
	}
	current, err := s.store.Current()
	if err != nil {
		return Status{}, err
	}
	recent, err := s.store.Recent(25)
	if err != nil {
		return Status{}, err
	}
	var progress *Progress
	if s.progress != nil {
		progress = s.progress()
	}
	lastScan, err := s.store.LastScan()
	if err != nil {
		return Status{}, err
	}
	var scan *ScanStatus
	if s.scan != nil {
		scan = s.scan()
	}
	status := Status{
		Version: s.version, Backend: s.backend, Device: s.cfg.Encoder.Device,
		Schedule: s.cfg.Encoder.Schedule, Paused: paused,
		ScanEvery: s.cfg.Server.ScanInterval.Std().String(),
		Counts:    counts, Current: current, Progress: progress, Scan: scan, LastScan: lastScan, Recent: recent,
	}
	for _, l := range s.cfg.Libraries {
		ls := LibraryStatus{Name: l.Name, Path: l.Path, Write: l.Write, Paused: paused}
		if l.Write {
			status.WritesEnabled = true
		}
		if sum, ok := files[l.Name]; ok {
			ls.Files, ls.Bytes, ls.Actions = sum.Files, sum.Bytes, sum.Actions
		}
		status.Libraries = append(status.Libraries, ls)
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
