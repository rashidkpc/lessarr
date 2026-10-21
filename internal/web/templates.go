package web

const sharedHTML = `{{define "head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>lessarr</title>
<style>
:root {
  color-scheme: light;
  --bg:#f6f3ec; --surface:#fffefa; --border:#e4ddd0; --text:#1f1f1f; --muted:#6f6a61;
  --accent:#d71920; --accent-soft:#ffe8cc;
  --ok:#1f6b3a; --ok-bg:#e3f4e9; --warn:#8a4b00; --warn-bg:#fff1c7; --err:#b3261e; --err-bg:#fde8e6;
}
* { box-sizing:border-box; }
body { font:15px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif; background:var(--bg); color:var(--text); margin:0; padding:2rem 1.25rem 4rem; }
.wrap { max-width:1200px; margin:0 auto; }
.top { display:flex; align-items:center; justify-content:space-between; gap:1rem; margin-bottom:1.6rem; }
.brand { font-size:1.4rem; font-weight:700; letter-spacing:-.02em; }
.brand span { font-size:.8rem; color:var(--muted); font-weight:400; margin-left:.4rem; }
nav { display:flex; gap:.2rem; background:#ece6da; padding:.25rem; border-radius:10px; }
nav a { padding:.35rem .75rem; border-radius:7px; color:#3f3a33; font-weight:500; }
nav a:hover { background:#fffdf9; color:var(--text); text-decoration:none; }
a { color:var(--accent); text-decoration:none; }
a:hover { text-decoration:underline; }
h1 { font-size:1.35rem; margin:0 0 .3rem; }
h2 { font-size:.78rem; text-transform:uppercase; letter-spacing:.07em; color:var(--muted); margin:2rem 0 .6rem; }
.muted { color:var(--muted); }
.panel { background:var(--surface); border:1px solid var(--border); border-radius:12px; padding:1rem; box-shadow:0 1px 2px rgba(15,23,42,.04); }
.stats { display:grid; grid-template-columns:repeat(auto-fit,minmax(180px,1fr)); gap:.8rem; margin:1rem 0 1.4rem; }
.stat { background:var(--surface); border:1px solid var(--border); border-top:3px solid #d71920; border-radius:12px; padding:.9rem 1rem; box-shadow:0 1px 2px rgba(15,23,42,.04); }
.stat:nth-child(2) { border-top-color:#f47b20; }
.stat:nth-child(3) { border-top-color:#f2c200; }
.stat:nth-child(4) { border-top-color:#1f1f1f; }
.stat .label { font-size:.76rem; color:var(--muted); text-transform:uppercase; letter-spacing:.05em; font-weight:600; }
.stat .value { font-size:1.5rem; font-weight:700; margin-top:.25rem; }
.stat .sub { font-size:.82rem; color:var(--muted); margin-top:.1rem; }
.tabs { display:flex; gap:.2rem; background:#ece6da; padding:.25rem; border-radius:10px; margin:0 0 1rem; width:max-content; max-width:100%; overflow-x:auto; }
.tab { padding:.42rem .8rem; border-radius:7px; color:#6f6a61; font-weight:500; white-space:nowrap; }
.tab:hover { color:var(--text); text-decoration:none; }
.tab.active { background:#fffdf9; color:var(--text); box-shadow:0 1px 2px rgba(15,23,42,.1); }
.tab .count { color:#9a9185; margin-left:.3rem; font-weight:400; }
.tab.active .count { color:var(--muted); }
.table-wrap { background:var(--surface); border:1px solid var(--border); border-radius:12px; overflow:hidden; box-shadow:0 1px 2px rgba(15,23,42,.04); }
table { width:100%; border-collapse:collapse; }
th, td { text-align:left; padding:.65rem .8rem; border-bottom:1px solid var(--border); vertical-align:middle; }
th { font-size:.72rem; text-transform:uppercase; letter-spacing:.05em; color:#9a9185; background:#f7f2e9; font-weight:600; }
tr:last-child td { border-bottom:0; }
tbody tr:hover td { background:#f7f2e9; }
.badge { display:inline-block; padding:.12rem .5rem; border-radius:999px; background:#ece6da; color:#3f3a33; font-size:.8rem; font-weight:500; margin-right:.3rem; }
.ok { background:var(--ok-bg); color:var(--ok); }
.warn { background:var(--warn-bg); color:var(--warn); }
.err { background:var(--err-bg); color:var(--err); }
button { background:#1f1f1f; color:#fff; border:0; border-radius:8px; padding:.45rem .85rem; font:inherit; font-weight:500; cursor:pointer; }
button:hover { background:var(--accent); }
button.secondary { background:#fffdf9; color:#1f1f1f; border:1px solid #1f1f1f; }
button.secondary:hover { background:#fff1c7; }
button.small { padding:.22rem .55rem; font-size:.8rem; border-radius:6px; }
input[type=search] { background:#fffdf9; color:var(--text); border:1px solid var(--border); border-radius:8px; padding:.45rem .7rem; min-width:min(360px,100%); }
code { background:#f1ece2; color:#1f1f1f; border-radius:4px; padding:.08rem .3rem; font-size:.9em; word-break:break-word; }
.path { word-break:break-all; }
.nowrap { white-space:nowrap; }
.chips { margin:.55rem 0; }
.chip { display:inline-block; padding:.2rem .6rem; border:1px solid var(--border); border-radius:999px; margin:0 .35rem .35rem 0; color:#3f3a33; background:#fffdf9; font-size:.85rem; }
.chip:hover { text-decoration:none; border-color:#9a9185; }
.chip.active { background:var(--accent); color:#fff; border-color:var(--accent); }
.notice { margin:1rem 0; border-color:#fde68a; background:#fffbeb; color:#92400e; }
.paused { background:var(--warn-bg); color:var(--warn); border:1px solid #fde68a; border-radius:8px; padding:.5rem .7rem; margin-bottom:1rem; font-size:.9rem; }
.bar { height:10px; background:#e9e1d3; border-radius:999px; overflow:hidden; margin:.7rem 0 .4rem; }
.bar span { display:block; height:100%; background:var(--accent); border-radius:999px; }
.stage { font-size:1rem; font-weight:600; margin:.7rem 0 .25rem; }
.stages { margin-top:.6rem; }
.stages summary { cursor:pointer; color:var(--accent); font-size:.88rem; }
.subbar { height:6px; width:min(360px,100%); background:#e9e1d3; border-radius:999px; overflow:hidden; margin:.6rem 0 .2rem; }
.subbar span { display:block; height:100%; background:#f2c200; border-radius:999px; }
.steps { display:flex; flex-wrap:wrap; gap:.35rem; margin:.7rem 0 .2rem; }
.step { padding:.12rem .5rem; border-radius:999px; background:#f1ece2; color:#6f6a61; font-size:.82rem; }
.step.done { background:var(--ok-bg); color:var(--ok); }
.step.current { background:var(--accent-soft); color:#7a4a00; }
.step.pending { background:#fffdf9; color:#9a9185; }
.toolbar { display:flex; align-items:flex-start; justify-content:space-between; gap:1rem; margin-bottom:.4rem; }
@media (max-width:640px) {
  body { padding:1.25rem .75rem 3rem; }
  .top { flex-wrap:wrap; }
  th, td { padding:.5rem .55rem; }
  .toolbar { flex-direction:column; }
}
</style>
</head>
<body><div class="wrap">
<div class="top">
  <div class="brand">lessarr <span>v{{.Version}}</span></div>
  {{template "nav"}}
</div>{{end}}
{{define "nav"}}<nav><a href="/">Dashboard</a><a href="/files">Files</a></nav>{{end}}`

const dashboardHTML = `{{define "dashboard"}}
<div class="toolbar">
<div><h1>Dashboard</h1><div class="muted">Encoding activity and recent savings</div></div>
<form method="post" action="{{if .Paused}}/api/resume{{else}}/api/pause{{end}}">
<button class="secondary" type="submit" title="{{if .Paused}}Resume claiming jobs{{else}}Finish the current job, then stop claiming new jobs{{end}}">{{if .Paused}}Resume{{else}}Pause{{end}}</button>
</form>
</div>
{{if .Paused}}<div class="paused">Paused. The current job will finish; new jobs will not be claimed until resumed.</div>{{end}}

<div class="stats">
<div class="stat"><div class="label">Reclaimed · 24h</div><div class="value">{{bytes .Saved24h}}</div><div class="sub">{{.Done24h}} job(s)</div></div>
<div class="stat"><div class="label">Reclaimed · 7d</div><div class="value">{{bytes .Saved7d}}</div><div class="sub">{{.Done7d}} job(s)</div></div>
<div class="stat"><div class="label">Total reclaimed</div><div class="value">{{bytes .SavedTotal}}</div><div class="sub">{{.DoneTotal}} file(s)</div></div>
<div class="stat"><div class="label">Trash</div><div class="value">{{bytes .TrashBytes}}</div><div class="sub">{{.TrashFiles}} file(s){{if gt .TrashLimitGB 0}} · cap {{.TrashLimitGB}} GB{{end}}</div></div>
</div>

<div class="tabs">
{{range .Tabs}}
<a class="tab {{if .Active}}active{{end}}" href="{{.Href}}">{{.Label}} <span class="count">{{.Count}}</span></a>
{{end}}
</div>

{{if eq .Tab "running"}}
{{if .Current}}
<div class="panel">
<div class="path"><a href="/jobs/{{.Current.ID}}"><code>{{.Current.Path}}</code></a></div>
<div class="muted">{{.Current.Action}} · {{.Current.Library}} · started {{when .Current.StartedAt}}</div>
<div style="margin-top:.45rem">{{.Current.Reason}}</div>
{{if .Progress}}
<div class="stage">{{if .Progress.Stage}}{{.Progress.Stage}}{{else}}{{.Progress.Phase}}{{end}}</div>
{{if gt .Progress.Frame 0}}
<div class="bar"><span style="width:{{printf "%.1f" .Progress.Percent}}%"></span></div>
<div class="muted">
{{printf "%.0f%%" .Progress.Percent}} · {{.Progress.Frame}} frames · {{dur .Progress.OutTime}} / {{dur .Progress.Duration}}{{if .Progress.Speed}} · speed {{.Progress.Speed}}{{end}} · {{bytes .Progress.TotalSize}} written · updated {{when .Progress.Updated}}
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
{{range .Progress.Steps}}<span class="step {{if .Done}}done{{else if .Current}}current{{else}}pending{{end}}">{{.Label}}</span>{{end}}
</div>
</details>
{{end}}
{{end}}
</div>
{{else}}
<div class="panel muted">Nothing is running.</div>
{{end}}

{{else if eq .Tab "queued"}}
<div class="table-wrap"><table>
<tr><th>#</th><th>File</th><th>Library</th><th>Action</th><th class="nowrap">Size</th><th class="nowrap">Estimate</th><th class="nowrap">Queued</th></tr>
{{range .Queued}}
<tr>
<td><a href="/jobs/{{.ID}}">{{.ID}}</a></td>
<td class="path"><a href="/jobs/{{.ID}}"><code>{{.Path}}</code></a></td>
<td>{{.Library}}</td>
<td><span class="badge">{{.Action}}</span></td>
<td class="nowrap">{{bytes .SizeBefore}}</td>
<td class="nowrap">{{if .SavingsUnmeasured}}<span class="muted">unmeasured</span>{{else if gt .EstimatedBytes 0}}~{{bytes .EstimatedBytes}}{{else}}—{{end}}</td>
<td class="muted nowrap">{{when .CreatedAt}}</td>
</tr>
{{else}}
<tr><td colspan="7" class="muted">Queue is empty.</td></tr>
{{end}}
</table></div>
{{if gt .Counts.Pending (len .Queued)}}<div class="muted" style="margin-top:.8rem"><a href="/files?queue=pending">View all queued files</a></div>{{end}}

{{else if eq .Tab "done"}}
<div class="table-wrap"><table>
<tr><th>#</th><th>File</th><th>Library</th><th>Action</th><th class="nowrap">Result</th><th class="nowrap">When</th><th></th></tr>
{{range .Done}}
<tr>
<td><a href="/jobs/{{.ID}}">{{.ID}}</a></td>
<td class="path"><a href="/jobs/{{.ID}}"><code>{{.Path}}</code></a></td>
<td>{{.Library}}</td>
<td><span class="badge">{{.Action}}</span></td>
<td class="nowrap">{{bytes .SizeBefore}}{{if gt .SizeAfter 0}} → {{bytes .SizeAfter}} <span class="muted">({{saved .SizeBefore .SizeAfter}})</span>{{end}}</td>
<td class="muted nowrap">{{when .FinishedAt}}</td>
<td>{{if .Retryable}}<form method="post" action="/api/jobs/{{.ID}}/retry" onsubmit="return confirm('Restore the original and re-transcode with current settings?')"><button class="secondary small" type="submit">Retry</button></form>{{end}}</td>
</tr>
{{else}}
<tr><td colspan="7" class="muted">Nothing completed yet.</td></tr>
{{end}}
</table></div>

{{else if eq .Tab "failed"}}
<div class="table-wrap"><table>
<tr><th>#</th><th>File</th><th>Library</th><th>Error</th><th>When</th><th></th></tr>
{{range .Failed}}
<tr>
<td><a href="/jobs/{{.ID}}">{{.ID}}</a></td>
<td class="path"><a href="/jobs/{{.ID}}"><code>{{.Path}}</code></a></td>
<td>{{.Library}}</td>
<td><span class="err">{{.Error}}</span></td>
<td class="muted">{{when .UpdatedAt}}</td>
<td>{{if .Retryable}}<form method="post" action="/api/jobs/{{.ID}}/retry" onsubmit="return confirm('Retry this job with current settings?')"><button class="secondary small" type="submit">Retry</button></form>{{end}}</td>
</tr>
{{else}}
<tr><td colspan="6" class="muted">No failures.</td></tr>
{{end}}
</table></div>
{{end}}
{{end}}`

const pageHTML = `{{define "page"}}{{template "head"}}
<div id="live" data-tab="{{.Tab}}">
{{template "dashboard" .}}
</div>
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
</body></html>{{end}}`

const filesHTML = `{{define "files"}}{{template "head"}}
<div class="toolbar">
<div><h1>Files</h1><div class="muted">{{.Total}} file(s) match · {{.Counts.Pending}} queued</div></div>
</div>

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

<div class="table-wrap"><table>
<tr><th>#</th><th>File</th><th>Library</th><th>Action</th><th>Size</th><th>Queue</th><th>Decision</th><th>Checked</th></tr>
{{range .Rows}}
<tr>
<td>{{if .JobID}}<a href="/jobs/{{.JobID}}">{{.JobID}}</a>{{else}}—{{end}}</td>
<td class="path"><code>{{.Path}}</code></td>
<td>{{.Library}}</td>
<td><span class="badge">{{.Action}}</span></td>
<td>{{bytes .Size}}</td>
<td>{{if .JobID}}<a href="/jobs/{{.JobID}}"><span class="badge {{.QueueClass}}">{{.QueueLabel}}</span></a>{{else if .QueueLabel}}<span class="badge {{.QueueClass}}">{{.QueueLabel}}</span>{{end}}</td>
<td class="muted">{{.Reason}}</td>
<td class="muted">{{when .CheckedAt}}</td>
</tr>
{{else}}
<tr><td colspan="8" class="muted">No files match this filter.</td></tr>
{{end}}
</table></div>

<div class="muted" style="margin-top:1rem">
Showing {{.Start}}–{{.End}} of {{.Total}}
{{if .PrevHref}} · <a href="{{.PrevHref}}">Previous</a>{{end}}
{{if .NextHref}} · <a href="{{.NextHref}}">Next</a>{{end}}
</div>
</div>
</body></html>{{end}}`

const jobHTML = `{{define "job"}}{{template "head"}}
<h1>Job #{{.ID}} <span class="badge {{if eq .StateName "done"}}ok{{else if eq .StateName "failed"}}err{{else if eq .StateName "running"}}warn{{end}}">{{.StateName}}</span></h1>
<div class="muted">
{{if eq .StateName "pending"}}<a href="/?tab=queued">← Queued</a>{{else if eq .StateName "done"}}<a href="/?tab=done">← Done</a>{{else if eq .StateName "failed"}}<a href="/?tab=failed">← Failed</a>{{else}}<a href="/?tab=running">← Running</a>{{end}}
&nbsp; <a href="/files?q={{urlquery .Path}}">file decisions</a>
{{if .HasComparison}}&nbsp; <a href="/comparisons/{{.ID}}/">compare original vs new</a>{{end}}
</div>
{{if .CanRetry}}
<form method="post" action="/api/jobs/{{.ID}}/retry" style="margin:.8rem 0" onsubmit="return confirm('Restore the original and re-transcode with current settings?')">
<button type="submit">Retry with current settings</button>
</form>
{{end}}

<h2>File</h2>
<div class="panel">
<div class="path"><code>{{.Path}}</code></div>
<div class="muted">{{.Library}} · {{.Action}} · {{.Codec}}</div>
</div>

<h2>Result</h2>
<div class="table-wrap"><table>
<tr><th>Size</th><td>{{bytes .SizeBefore}}{{if and (eq .StateName "done") (gt .SizeAfter 0)}} → {{bytes .SizeAfter}} <span class="muted">({{saved .SizeBefore .SizeAfter}} smaller)</span>{{end}}</td></tr>
<tr><th>Attempts</th><td>{{.Attempts}}</td></tr>
<tr><th>Created</th><td>{{.CreatedAt.Format "2006-01-02 15:04:05"}} <span class="muted">({{when .CreatedAt}})</span></td></tr>
{{if not .StartedAt.IsZero}}<tr><th>Started</th><td>{{.StartedAt.Format "2006-01-02 15:04:05"}}</td></tr>{{end}}
{{if not .FinishedAt.IsZero}}<tr><th>Finished</th><td>{{.FinishedAt.Format "2006-01-02 15:04:05"}} <span class="muted">({{when .FinishedAt}})</span></td></tr>{{end}}
{{if .Backup}}<tr><th>Original</th><td class="path"><code>{{.Backup}}</code></td></tr>{{end}}
</table></div>

{{if .Analysis}}
<h2>Savings breakdown</h2>
<div class="table-wrap"><table>
<tr><th>Part</th><th>Before</th><th>After</th><th>Saved</th></tr>
<tr><td>Video</td><td>{{bytes .Analysis.Video.Before}}</td><td>{{bytes .Analysis.Video.After}}</td><td>{{sbytes .Analysis.Video.Saved}}</td></tr>
<tr><td>Audio</td><td>{{bytes .Analysis.Audio.Before}}</td><td>{{bytes .Analysis.Audio.After}}</td><td>{{sbytes .Analysis.Audio.Saved}}</td></tr>
<tr><td>Subtitles</td><td>{{bytes .Analysis.Subtitles.Before}}</td><td>{{bytes .Analysis.Subtitles.After}}</td><td>{{sbytes .Analysis.Subtitles.Saved}}</td></tr>
<tr><td>Container / other</td><td>{{bytes .Analysis.Other.Before}}</td><td>{{bytes .Analysis.Other.After}}</td><td>{{sbytes .Analysis.Other.Saved}}</td></tr>
<tr><th>Total</th><th>{{bytes .Analysis.Total.Before}}</th><th>{{bytes .Analysis.Total.After}}</th><th>{{sbytes .Analysis.Total.Saved}}</th></tr>
</table></div>
<p class="muted">Measured from packet payload sizes. Container/other includes mux overhead, metadata, and attachments.</p>

{{if .Analysis.SSIM}}
<h2>Quality (sampled SSIM)</h2>
<div class="table-wrap"><table>
<tr><th>At</th><th>SSIM</th></tr>
{{range .Analysis.SSIM}}<tr><td>{{printf "%.0f" .AtSeconds}}s</td><td>{{printf "%.4f" .SSIM}}</td></tr>{{end}}
</table></div>
<p class="muted">Compared against the retained original; higher is better.</p>
{{end}}
{{if .Analysis.SSIMError}}<p class="muted">SSIM unavailable: {{.Analysis.SSIMError}}</p>{{end}}
{{else}}
<p class="muted">No savings analysis stored yet. Run <code>lessarr analyze --config FILE {{.ID}}</code>.</p>
{{end}}

<h2>Decision</h2>
<div class="panel" style="white-space:pre-wrap">{{.Reason}}</div>

{{if .Error}}
<h2>Error</h2>
<div class="panel" style="white-space:pre-wrap;background:#fef2f2;border-color:#fecaca;color:#991b1b">{{.Error}}</div>
{{end}}
</div>
</body></html>{{end}}`
