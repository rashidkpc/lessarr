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
	cfg     *config.Config
	store   *queue.Store
	backend string
	version string
	page    *template.Template
}

func New(cfg *config.Config, store *queue.Store, backend, version string) *Server {
	return &Server{
		cfg: cfg, store: store, backend: backend, version: version,
		page: template.Must(template.New("page").Funcs(template.FuncMap{
			"bytes": humanBytes,
			"when":  humanWhen,
			"saved": savedPercent,
		}).Parse(pageHTML)),
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
	case r.URL.Path == "/" && r.Method == http.MethodGet:
		s.statusPage(w)
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
	if err := s.page.Execute(w, status); err != nil {
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
	Version   string
	Backend   string
	Device    string
	Schedule  string
	Paused    bool
	ScanEvery string
	Counts    queue.Counts
	Libraries []LibraryStatus
	Current   *queue.Job
	Recent    []queue.Job
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
	status := Status{
		Version: s.version, Backend: s.backend, Device: s.cfg.Encoder.Device,
		Schedule: s.cfg.Encoder.Schedule, Paused: paused,
		ScanEvery: s.cfg.Server.ScanInterval.Std().String(),
		Counts:    counts, Current: current, Recent: recent,
	}
	for _, l := range s.cfg.Libraries {
		ls := LibraryStatus{Name: l.Name, Path: l.Path, Write: l.Write, Paused: paused}
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

func savedPercent(before, after int64) string {
	if before <= 0 || after <= 0 || after >= before {
		return "—"
	}
	return formatFloat(float64(before-after)/float64(before)*100) + "%"
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>lessarr</title>
<meta http-equiv="refresh" content="5">
<style>
:root { color-scheme: dark; }
body { font: 15px/1.45 system-ui, sans-serif; margin: 2rem auto; max-width: 1100px; padding: 0 1rem; background:#111; color:#ddd; }
h1 { font-size: 1.4rem; margin: 0 0 .25rem; }
h2 { font-size: 1rem; margin: 2rem 0 .5rem; color:#aaa; text-transform: uppercase; letter-spacing:.06em; }
a { color:#8ab4f8; }
table { width:100%; border-collapse: collapse; }
th, td { text-align:left; padding:.4rem .5rem; border-bottom:1px solid #2a2a2a; vertical-align: top; }
th { color:#888; font-weight:500; }
.muted { color:#888; }
.badge { display:inline-block; padding:.1rem .45rem; border-radius:4px; background:#263238; margin-right:.3rem; }
.ok { background:#1b3a1b; }
.warn { background:#4a3410; }
.err { background:#4a1b1b; }
.grid { display:grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap:.6rem; }
.card { background:#1a1a1a; border:1px solid #2a2a2a; border-radius:6px; padding:.7rem; }
.card b { display:block; font-size:1.35rem; }
button { background:#2d4a7a; color:#fff; border:0; border-radius:4px; padding:.45rem .8rem; cursor:pointer; }
code { color:#b7d6a8; }
</style>
</head>
<body>
<h1>lessarr <span class="muted">v{{.Version}}</span></h1>
<div class="muted">
{{if .Paused}}<span class="badge warn">paused</span>{{else}}<span class="badge ok">running</span>{{end}}
{{if .Schedule}}schedule <code>{{.Schedule}}</code>{{end}}
&nbsp; backend <code>{{.Backend}}</code> on <code>{{.Device}}</code>
&nbsp; scan every {{.ScanEvery}}
</div>
<form method="post" action="{{if .Paused}}/api/resume{{else}}/api/pause{{end}}" style="margin-top:.8rem">
<button type="submit">{{if .Paused}}Resume{{else}}Pause{{end}}</button>
</form>

<h2>Queue</h2>
<div class="grid">
<div class="card"><span class="muted">pending</span><b>{{.Counts.Pending}}</b></div>
<div class="card"><span class="muted">running</span><b>{{.Counts.Running}}</b></div>
<div class="card"><span class="muted">done</span><b>{{.Counts.Done}}</b></div>
<div class="card"><span class="muted">failed</span><b>{{.Counts.Failed}}</b></div>
<div class="card"><span class="muted">skipped</span><b>{{.Counts.Skipped}}</b></div>
<div class="card"><span class="muted">reclaimed</span><b>{{bytes .Counts.Saved}}</b></div>
</div>

{{if .Current}}
<h2>Current job</h2>
<div class="card">
<div><code>{{.Current.Path}}</code></div>
<div class="muted">{{.Current.Action}} · {{.Current.Library}} · started {{when .Current.StartedAt}}</div>
<div>{{.Current.Reason}}</div>
</div>
{{end}}

<h2>Libraries</h2>
<table>
<tr><th>Library</th><th>Mode</th><th>Files</th><th>Size</th><th>Actions</th></tr>
{{range .Libraries}}
<tr>
<td><div>{{.Name}}</div><div class="muted"><code>{{.Path}}</code></div></td>
<td>{{if .Write}}<span class="badge ok">write</span>{{else}}<span class="badge">observe</span>{{end}}</td>
<td>{{.Files}}</td><td>{{bytes .Bytes}}</td>
<td>{{range $action, $n := .Actions}}<span class="badge">{{$action}} {{$n}}</span>{{end}}</td>
</tr>
{{end}}
</table>

<h2>Recent jobs</h2>
<table>
<tr><th>#</th><th>File</th><th>State</th><th>Size</th><th>When</th></tr>
{{range .Recent}}
<tr>
<td>{{.ID}}</td>
<td><div><code>{{.Path}}</code></div><div class="muted">{{.Reason}}{{if .Error}} · <span class="err">{{.Error}}</span>{{end}}</div></td>
<td><span class="badge {{if eq .StateName "done"}}ok{{else if eq .StateName "failed"}}err{{else if eq .StateName "running"}}warn{{end}}">{{.StateName}}</span></td>
<td>{{bytes .SizeBefore}} → {{bytes .SizeAfter}}{{if gt .SizeAfter 0}} <span class="muted">({{saved .SizeBefore .SizeAfter}})</span>{{end}}</td>
<td class="muted">{{when .UpdatedAt}}</td>
</tr>
{{else}}
<tr><td colspan="5" class="muted">No jobs yet.</td></tr>
{{end}}
</table>
</body>
</html>
`
