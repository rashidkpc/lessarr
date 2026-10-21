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

func (s *Server) retryable(job *queue.Job) bool {
	if job.State != queue.Done && job.State != queue.Failed {
		return false
	}
	if active, err := s.store.ActiveJob(job.Path); err == nil && active != nil {
		return false
	}
	lib, ok := s.cfg.LibraryFor(job.Path)
	if !ok || !lib.Write {
		return false
	}
	if job.Backup != "" {
		if fi, err := os.Lstat(job.Backup); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	if job.State == queue.Failed {
		if fi, err := os.Lstat(job.Path); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func (s *Server) restorable(job *queue.Job) bool {
	if job.State != queue.Done || job.Backup == "" {
		return false
	}
	if active, err := s.store.ActiveJob(job.Path); err == nil && active != nil {
		return false
	}
	lib, ok := s.cfg.LibraryFor(job.Path)
	if !ok || !lib.Write {
		return false
	}
	fi, err := os.Lstat(job.Backup)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0
}

type JobPage struct {
	*queue.Job
	Version       string
	Page          string
	Paused        bool
	Analysis      *analysis.Result
	HasComparison bool
	CanRetry      bool
	CanRestore    bool
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
	paused, err := s.store.Paused()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := JobPage{Job: job, Version: s.version, Page: "", Paused: paused}
	if _, err := os.Stat(filepath.Join(compare.Dir(s.cfg.Server.DB), strconv.FormatInt(job.ID, 10), "index.html")); err == nil {
		page.HasComparison = true
	}
	page.CanRetry = s.retryable(job)
	page.CanRestore = s.restorable(job)
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
