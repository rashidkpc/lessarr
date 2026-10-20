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
</style>
</head>
<body>{{end}}
{{define "nav"}}<nav><a href="/">Dashboard</a><a href="/files">Files</a></nav>{{end}}`

const pageHTML = `{{define "page"}}{{template "head"}}
<h1>lessarr <span class="muted">v{{.Version}}</span></h1>
{{template "nav"}}
<div class="muted">
{{if .Paused}}<span class="badge warn">paused</span>{{else}}<span class="badge ok">running</span>{{end}}
{{if .Schedule}}schedule <code>{{.Schedule}}</code>{{end}}
&nbsp; backend <code>{{.Backend}}</code> on <code>{{.Device}}</code>
&nbsp; scan every {{.ScanEvery}}
</div>
<form method="post" action="{{if .Paused}}/api/resume{{else}}/api/pause{{end}}" style="margin-top:.8rem">
<button type="submit">{{if .Paused}}Resume{{else}}Pause{{end}}</button>
</form>

{{if and (gt .Counts.Pending 0) (not .WritesEnabled)}}
<div class="card notice">
<strong>{{.Counts.Pending}} job(s) queued.</strong>
Writes are disabled, so the worker will not claim them. <a href="/files?action=transcode">Review the files</a>.
</div>
{{end}}

<h2>Queue</h2>
<div class="grid">
<a class="card" href="/files?queue=pending" style="color:inherit"><span class="muted">pending</span><b>{{.Counts.Pending}}</b></a>
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

<h2>Libraries <a class="muted" style="font-size:.8rem" href="/files">view files</a></h2>
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
<td>{{if .QueueLabel}}<span class="badge {{.QueueClass}}">{{.QueueLabel}}</span>{{end}}</td>
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
