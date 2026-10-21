package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rashidkpc/lessarr/internal/queue"
)

type FileView struct {
	queue.FileRow
	QueueLabel string
	QueueClass string
	CanUnskip  bool
}

type FilterOption struct {
	Value    string
	Label    string
	Selected bool
}

type FilesPage struct {
	Status
	Query          queue.FileQuery
	Rows           []FileView
	Total          int
	Limit          int
	Offset         int
	Start          int
	End            int
	PrevHref       string
	NextHref       string
	QueueOptions   []FilterOption
	ActionOptions  []FilterOption
	LibraryOptions []FilterOption
	ActiveFilters  int
	HasFilters     bool
}

func (s *Server) filesPage(w http.ResponseWriter, r *http.Request) {
	status, err := s.collect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	values := r.URL.Query()
	q := queue.FileQuery{
		Library: strings.TrimSpace(values.Get("library")),
		Action:  strings.TrimSpace(values.Get("action")),
		Queue:   strings.TrimSpace(values.Get("queue")),
		Search:  strings.TrimSpace(values.Get("q")),
		Limit:   100,
	}
	switch q.Queue {
	case "pending", "running", "none", "skipped":
	default:
		q.Queue = ""
	}
	if n, err := strconv.Atoi(values.Get("offset")); err == nil && n > 0 {
		q.Offset = n
	}
	rows, total, err := s.store.Files(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writable := map[string]bool{}
	for _, lib := range s.cfg.Libraries {
		writable[lib.Name] = lib.Write
	}
	page := FilesPage{Status: status, Query: q, Total: total, Limit: q.Limit, Offset: q.Offset}
	page.Page = "files"
	for _, row := range rows {
		view := FileView{FileRow: row}
		switch {
		case row.SuppressedReason == queue.OperatorSkipReason:
			view.QueueLabel = "skip"
			view.QueueClass = "warn"
			view.CanUnskip = writable[row.Library]
		case row.SuppressedReason == queue.RestoredReason:
			view.QueueLabel = "restored"
			view.QueueClass = "warn"
		case row.SuppressedReason != "":
			view.QueueLabel = "suppressed"
			view.QueueClass = "warn"
		case row.JobID > 0:
			view.QueueLabel = row.JobState
			if row.JobState == string(queue.Running) {
				view.QueueClass = "ok"
			} else {
				view.QueueClass = "warn"
			}
		case row.Action == "skip":
			view.QueueLabel = "skip"
			view.CanUnskip = writable[row.Library] && row.SkippedJobID > 0
		case writable[row.Library]:
			view.QueueLabel = "not queued"
		default:
			view.QueueLabel = "would queue"
			view.QueueClass = "warn"
		}
		page.Rows = append(page.Rows, view)
	}
	page.Start = q.Offset + 1
	page.End = q.Offset + len(rows)
	if total == 0 {
		page.Start, page.End = 0, 0
	}
	if q.Offset > 0 {
		prev := q.Offset - q.Limit
		if prev < 0 {
			prev = 0
		}
		page.PrevHref = fileHref(q, q.Library, q.Action, q.Queue, prev)
	}
	if q.Offset+len(rows) < total {
		page.NextHref = fileHref(q, q.Library, q.Action, q.Queue, q.Offset+q.Limit)
	}
	page.QueueOptions = []FilterOption{{Label: "All queues", Selected: q.Queue == ""}}
	for _, item := range []struct{ value, label string }{
		{"pending", "Queued"}, {"running", "Running"}, {"none", "Not queued"}, {"skipped", "Skipped"},
	} {
		page.QueueOptions = append(page.QueueOptions, FilterOption{
			Value: item.value, Label: item.label, Selected: q.Queue == item.value,
		})
	}
	page.ActionOptions = []FilterOption{{Label: "All actions", Selected: q.Action == ""}}
	for _, action := range []struct{ value, label string }{
		{"transcode", "Transcode"}, {"remux", "Remux"}, {"skip", "Skip"},
	} {
		page.ActionOptions = append(page.ActionOptions, FilterOption{
			Value: action.value, Label: action.label, Selected: q.Action == action.value,
		})
	}
	page.LibraryOptions = []FilterOption{{Label: "All libraries", Selected: q.Library == ""}}
	for _, lib := range s.cfg.Libraries {
		page.LibraryOptions = append(page.LibraryOptions, FilterOption{
			Value: lib.Name, Label: lib.Name, Selected: q.Library == lib.Name,
		})
	}
	for _, value := range []string{q.Library, q.Action, q.Queue, q.Search} {
		if value != "" {
			page.ActiveFilters++
		}
	}
	page.HasFilters = page.ActiveFilters > 0
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "files", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func fileHref(q queue.FileQuery, library, action, queueName string, offset int) string {
	values := url.Values{}
	if library != "" {
		values.Set("library", library)
	}
	if action != "" {
		values.Set("action", action)
	}
	if queueName != "" {
		values.Set("queue", queueName)
	}
	if q.Search != "" {
		values.Set("q", q.Search)
	}
	if offset > 0 {
		values.Set("offset", strconv.Itoa(offset))
	}
	if values.Encode() == "" {
		return "/files"
	}
	return "/files?" + values.Encode()
}
