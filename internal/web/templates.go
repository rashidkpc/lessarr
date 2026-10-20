package web

const sharedHTML = `{{define "head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>lessarr</title>
<style>
:root { color-scheme: dark; }
body { font: 15px/1.45 system-ui, sans-serif; margin: 2rem auto; max-width: 1250px; padding: 0 1rem; background:#111; color:#ddd; }
h1 { font-size: 1.4rem; margin: 0 0 .25rem; }
h2 { font-size: 1rem; margin: 2rem 0 .5rem; color:#aaa; text-transform: uppercase; letter-spacing:.06em; }
a { color:#8ab4f8; text-decoration:none; }
a:hover { text-decoration:underline; }
nav { margin:.8rem 0 1.2rem; padding-bottom:.6rem; border-bottom:1px solid #2a2a2a; }
nav a { margin-right:1rem; }
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
input[type=search] { background:#1a1a1a; color:#ddd; border:1px solid #333; border-radius:4px; padding:.4rem .6rem; min-width:320px; }
code { color:#b7d6a8; }
.chips { margin:.6rem 0; }
.chip { display:inline-block; padding:.2rem .55rem; border:1px solid #333; border-radius:999px; margin:0 .35rem .35rem 0; color:#bbb; }
.chip.active { background:#2d4a7a; color:#fff; border-color:#2d4a7a; }
.notice { margin:1rem 0; border-color:#4a3410; }
.bar { height:10px; background:#222; border-radius:999px; overflow:hidden; margin:.7rem 0 .4rem; }
.bar span { display:block; height:100%; background:#2d7a4a; }
.stage { font-size:1.05rem; margin:.7rem 0 .2rem; color:#eee; }
.stages { margin-top:.6rem; }
.stages summary { cursor:pointer; color:#8ab4f8; }
.subbar { height:6px; width:min(360px,100%); background:#222; border-radius:999px; overflow:hidden; margin:.6rem 0 .2rem; }
.subbar span { display:block; height:100%; background:#2d4a7a; }
.steps { display:flex; flex-wrap:wrap; gap:.35rem; margin:.7rem 0 .2rem; }
.step { padding:.12rem .45rem; border-radius:999px; background:#222; color:#777; font-size:.85rem; }
.step.done { background:#1b3a1b; color:#9c9; }
.step.current { background:#4a3410; color:#fff; }
.tabs { display:flex; gap:.2rem; border-bottom:1px solid #2a2a2a; margin:1rem 0 1.2rem; }
.tab { padding:.5rem .8rem; color:#999; border-bottom:2px solid transparent; }
.tab.active { color:#fff; border-color:#8ab4f8; }
.tab .count { color:#777; margin-left:.2rem; }
.tab.active .count { color:#bbb; }
</style>
</head>
<body>{{end}}
{{define "nav"}}<nav><a href="/">Dashboard</a><a href="/files">Files</a></nav>{{end}}`

const dashboardHTML = `{{define "dashboard"}}
<div style="display:flex;justify-content:space-between;align-items:center;gap:1rem;margin-bottom:.8rem">
<div class="muted">
{{if .Paused}}<span class="badge warn">paused</span>{{else}}<span class="badge ok">running</span>{{end}}
{{if .WritesEnabled}}<span class="badge ok">writes enabled</span>{{else}}<span class="badge">observe only</span>{{end}}
</div>
<form method="post" action="{{if .Paused}}/api/resume{{else}}/api/pause{{end}}">
<button type="submit" title="{{if .Paused}}Resume claiming jobs{{else}}Finish the current job, then stop claiming new jobs{{end}}">{{if .Paused}}Resume{{else}}Pause{{end}}</button>
</form>
</div>
<div class="tabs">
{{range .Tabs}}
<a class="tab {{if .Active}}active{{end}}" href="{{.Href}}">{{.Label}} <span class="count">{{.Count}}</span></a>
{{end}}
</div>

{{if eq .Tab "running"}}
{{if .Current}}
<div class="card">
<div><code>{{.Current.Path}}</code></div>
<div class="muted">{{.Current.Action}} · {{.Current.Library}} · started {{when .Current.StartedAt}}</div>
<div>{{.Current.Reason}}</div>
{{if .Progress}}
<div class="stage">{{if .Progress.Stage}}{{.Progress.Stage}}{{else}}{{.Progress.Phase}}{{end}}</div>
{{if gt .Progress.Frame 0}}
<div class="bar"><span style="width:{{printf "%.1f" .Progress.Percent}}%"></span></div>
<div class="muted">
{{printf "%.0f%%" .Progress.Percent}}
· {{.Progress.Frame}} frames
· {{dur .Progress.OutTime}} / {{dur .Progress.Duration}}
{{if .Progress.Speed}} · speed {{.Progress.Speed}}{{end}}
· {{bytes .Progress.TotalSize}} written
· updated {{when .Progress.Updated}}
</div>
{{end}}
{{if .Progress.Steps}}
<details class="stages">
<summary>All stages <span class="muted">({{len .Progress.Steps}})</span></summary>
{{if and (gt .Progress.SampleTotal 0) (lt .Progress.SampleDone .Progress.SampleTotal)}}
<div class="subbar"><span style="width:{{printf "%.1f" .Progress.SamplePct}}%"></span></div>
<div class="muted">sampling {{.Progress.SampleDone}}/{{.Progress.SampleTotal}}</div>
{{end}}
<div class="steps">
{{range .Progress.Steps}}<span class="step {{if .Done}}done{{else}}current{{end}}">{{.Label}}</span>{{end}}
</div>
</details>
{{end}}
{{end}}
</div>
{{else}}
<div class="card muted">Nothing is running.</div>
{{end}}

{{else if eq .Tab "queued"}}
<table>
<tr><th>File</th><th>Library</th><th>Action</th><th>Size</th></tr>
{{range .Queued}}
<tr>
<td><a href="/jobs/{{.ID}}"><code>{{.Path}}</code></a></td>
<td>{{.Library}}</td>
<td><span class="badge">{{.Action}}</span></td>
<td>{{bytes .SizeBefore}}</td>
</tr>
{{else}}
<tr><td colspan="4" class="muted">Queue is empty.</td></tr>
{{end}}
</table>
{{if gt .Counts.Pending (len .Queued)}}<div class="muted" style="margin-top:.8rem"><a href="/files?queue=pending">View all queued files</a></div>{{end}}

{{else if eq .Tab "done"}}
<table>
<tr><th>File</th><th>Action</th><th>Size</th><th>When</th></tr>
{{range .Done}}
<tr>
<td><a href="/jobs/{{.ID}}"><code>{{.Path}}</code></a></td>
<td>{{.Action}}</td>
<td>{{bytes .SizeBefore}}{{if gt .SizeAfter 0}} → {{bytes .SizeAfter}} <span class="muted">({{saved .SizeBefore .SizeAfter}})</span>{{end}}</td>
<td class="muted">{{when .FinishedAt}}</td>
</tr>
{{else}}
<tr><td colspan="4" class="muted">Nothing completed yet.</td></tr>
{{end}}
</table>

{{else if eq .Tab "failed"}}
<table>
<tr><th>File</th><th>Error</th><th>When</th></tr>
{{range .Failed}}
<tr>
<td><a href="/jobs/{{.ID}}"><code>{{.Path}}</code></a></td>
<td><span class="err">{{.Error}}</span></td>
<td class="muted">{{when .UpdatedAt}}</td>
</tr>
{{else}}
<tr><td colspan="3" class="muted">No failures.</td></tr>
{{end}}
</table>
{{end}}
{{end}}`

const pageHTML = `{{define "page"}}{{template "head"}}
<h1>lessarr <span class="muted">v{{.Version}}</span></h1>
{{template "nav"}}

<div id="live" data-tab="{{.Tab}}">
{{template "dashboard" .}}
</div>

<script>
(function () {
  var live = document.getElementById("live");
  var tab = live.getAttribute("data-tab") || "running";
  live.addEventListener("toggle", function (e) {
    if (e.target && e.target.classList && e.target.classList.contains("stages")) {
      sessionStorage.setItem("lessarr-stages", e.target.open ? "1" : "0");
    }
  }, true);
  function refresh() {
    if (document.hidden) {
      setTimeout(refresh, 2000);
      return;
    }
    fetch("/fragment?tab=" + encodeURIComponent(tab), {cache: "no-store"})
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) {
        if (!html) return;
        live.innerHTML = html;
        var stages = live.querySelector("details.stages");
        if (stages) stages.open = sessionStorage.getItem("lessarr-stages") === "1";
      })
      .catch(function () {})
      .then(function () { setTimeout(refresh, 2000); });
  }
  setTimeout(refresh, 2000);
})();
</script>
{{end}}`

const filesHTML = `{{define "files"}}{{template "head"}}
<h1>Files <span class="muted">v{{.Version}}</span></h1>
{{template "nav"}}
<div class="muted">
{{if .Paused}}<span class="badge warn">paused</span>{{else}}<span class="badge ok">running</span>{{end}}
&nbsp; {{.Total}} file(s) match
&nbsp; {{.Counts.Pending}} queued
{{if .WritesEnabled}}<span class="badge ok">writes enabled</span>{{else}}<span class="badge warn">observe only</span>{{end}}
</div>

{{if and (gt .Counts.Pending 0) (not .WritesEnabled)}}
<div class="card notice">
<strong>{{.Counts.Pending}} candidate job(s) are queued.</strong>
No library has <code>write = true</code>, so the worker will not claim them yet.
</div>
{{end}}

<div class="chips"><span class="muted">Queue: </span>
{{range .QueueLinks}}<a class="chip {{if .Active}}active{{end}}" href="{{.Href}}">{{.Label}}</a>{{end}}
</div>
<div class="chips"><span class="muted">Action: </span>
{{range .ActionLinks}}<a class="chip {{if .Active}}active{{end}}" href="{{.Href}}">{{.Label}}</a>{{end}}
</div>
<div class="chips"><span class="muted">Library: </span>
{{range .LibraryLinks}}<a class="chip {{if .Active}}active{{end}}" href="{{.Href}}">{{.Label}}</a>{{end}}
</div>

<form method="get" action="/files" style="margin:1rem 0">
<input type="hidden" name="library" value="{{.Query.Library}}">
<input type="hidden" name="action" value="{{.Query.Action}}">
<input type="hidden" name="queue" value="{{.Query.Queue}}">
<input type="search" name="q" value="{{.Query.Search}}" placeholder="filter by path">
<button type="submit">Search</button>
</form>

<table>
<tr><th>File</th><th>Library</th><th>Action</th><th>Size</th><th>Queue</th><th>Decision</th><th>Checked</th></tr>
{{range .Rows}}
<tr>
<td><code>{{.Path}}</code></td>
<td>{{.Library}}</td>
<td><span class="badge">{{.Action}}</span></td>
<td>{{bytes .Size}}</td>
<td>{{if .JobID}}<a href="/jobs/{{.JobID}}"><span class="badge {{.QueueClass}}">{{.QueueLabel}}</span></a>{{else if .QueueLabel}}<span class="badge {{.QueueClass}}">{{.QueueLabel}}</span>{{end}}</td>
<td class="muted">{{.Reason}}</td>
<td class="muted">{{when .CheckedAt}}</td>
</tr>
{{else}}
<tr><td colspan="7" class="muted">No files match this filter.</td></tr>
{{end}}
</table>

<div class="muted" style="margin-top:1rem">
Showing {{.Start}}–{{.End}} of {{.Total}}
{{if .PrevHref}} · <a href="{{.PrevHref}}">Previous</a>{{end}}
{{if .NextHref}} · <a href="{{.NextHref}}">Next</a>{{end}}
</div>
{{end}}`

const jobHTML = `{{define "job"}}{{template "head"}}
<h1>Job #{{.ID}}
<span class="badge {{if eq .StateName "done"}}ok{{else if eq .StateName "failed"}}err{{else if eq .StateName "running"}}warn{{end}}">{{.StateName}}</span>
</h1>
{{template "nav"}}
<div class="muted">
{{if eq .StateName "pending"}}<a href="/?tab=queued">← Queued</a>{{else if eq .StateName "done"}}<a href="/?tab=done">← Done</a>{{else if eq .StateName "failed"}}<a href="/?tab=failed">← Failed</a>{{else}}<a href="/?tab=running">← Running</a>{{end}}
&nbsp; <a href="/files?q={{urlquery .Path}}">file decisions</a>
</div>

<h2>File</h2>
<div class="card">
<div style="word-break:break-all"><code>{{.Path}}</code></div>
<div class="muted">{{.Library}} · {{.Action}} · {{.Codec}}</div>
</div>

<h2>Result</h2>
<table>
<tr><th>Size</th><td>{{bytes .SizeBefore}}{{if and (eq .StateName "done") (gt .SizeAfter 0)}} → {{bytes .SizeAfter}} <span class="muted">({{saved .SizeBefore .SizeAfter}} smaller)</span>{{end}}</td></tr>
<tr><th>Attempts</th><td>{{.Attempts}}</td></tr>
<tr><th>Created</th><td>{{.CreatedAt.Format "2006-01-02 15:04:05"}} <span class="muted">({{when .CreatedAt}})</span></td></tr>
{{if not .StartedAt.IsZero}}<tr><th>Started</th><td>{{.StartedAt.Format "2006-01-02 15:04:05"}}</td></tr>{{end}}
{{if not .FinishedAt.IsZero}}<tr><th>Finished</th><td>{{.FinishedAt.Format "2006-01-02 15:04:05"}} <span class="muted">({{when .FinishedAt}})</span></td></tr>{{end}}
{{if .Backup}}<tr><th>Original</th><td style="word-break:break-all"><code>{{.Backup}}</code></td></tr>{{end}}
</table>

<h2>Decision</h2>
<div class="card" style="white-space:pre-wrap">{{.Reason}}</div>

{{if .Error}}
<h2>Error</h2>
<div class="card" style="white-space:pre-wrap;border-color:#4a1b1b">{{.Error}}</div>
{{end}}
{{end}}`
