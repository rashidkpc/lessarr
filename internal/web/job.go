package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rashidkpc/lessarr/internal/analysis"
	"github.com/rashidkpc/lessarr/internal/compare"
	"github.com/rashidkpc/lessarr/internal/queue"
)

type JobPage struct {
	*queue.Job
	Analysis      *analysis.Result
	HasComparison bool
	CanRetry      bool
}

func (s *Server) jobPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/jobs/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	job, err := s.store.Job(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil {
		http.NotFound(w, r)
		return
	}
	page := JobPage{Job: job}
	if _, err := os.Stat(filepath.Join(compare.Dir(s.cfg.Server.DB), strconv.FormatInt(job.ID, 10), "index.html")); err == nil {
		page.HasComparison = true
	}
	if job.Backup != "" {
		if fi, err := os.Lstat(job.Backup); err == nil && fi.Mode().IsRegular() {
			if lib, ok := s.cfg.LibraryFor(job.Path); ok && lib.Write {
				page.CanRetry = true
			}
		}
	}
	if data, err := s.store.Analysis(id); err == nil && data != "" {
		var result analysis.Result
		if json.Unmarshal([]byte(data), &result) == nil {
			page.Analysis = &result
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "job", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
