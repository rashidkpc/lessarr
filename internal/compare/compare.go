// Package compare renders split-frame comparisons between a retained original
// and its replacement. It is intentionally stills-only: five frames are cheap
// enough to keep for every transcode and expose softness at the seam.
package compare

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/rashidkpc/lessarr/internal/ffmpeg"
)

const (
	sampleCount = 5
	timeout     = 5 * time.Minute
)

// Task is one completed transcode with a retained original.
type Task struct {
	ID     int64
	Source string
	Output string
}

// Dir is the shared comparison root, kept beside the SQLite database.
func Dir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "comparisons")
}

type PruneStats struct {
	Deleted int
	Freed   int64
	Kept    int
}

type gallery struct {
	path   string
	size   int64
	newest time.Time
}

// Prune removes galleries older than retention and then, if the total still
// exceeds maxBytes, the oldest galleries. The newest gallery is never removed
// by the size cap.
func Prune(root string, retention time.Duration, maxBytes int64, now time.Time) (PruneStats, error) {
	var stats PruneStats
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	galleries := make([]gallery, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(entry.Name(), 10, 64); err != nil {
			continue
		}
		g := gallery{path: filepath.Join(root, entry.Name())}
		_ = filepath.WalkDir(g.path, func(path string, item fs.DirEntry, err error) error {
			if err != nil || item.IsDir() {
				return nil
			}
			info, err := item.Info()
			if err != nil {
				return nil
			}
			g.size += info.Size()
			if info.ModTime().After(g.newest) {
				g.newest = info.ModTime()
			}
			return nil
		})
		galleries = append(galleries, g)
	}

	kept := galleries[:0]
	for _, g := range galleries {
		if retention > 0 && !g.newest.IsZero() && now.Sub(g.newest) > retention {
			if err := os.RemoveAll(g.path); err != nil {
				return stats, err
			}
			stats.Deleted++
			stats.Freed += g.size
			continue
		}
		kept = append(kept, g)
	}
	galleries = kept

	sort.Slice(galleries, func(i, j int) bool {
		return galleries[i].newest.Before(galleries[j].newest)
	})
	var total int64
	for _, g := range galleries {
		total += g.size
	}
	if maxBytes > 0 {
		for total > maxBytes && len(galleries) > 1 {
			g := galleries[0]
			if err := os.RemoveAll(g.path); err != nil {
				return stats, err
			}
			galleries = galleries[1:]
			total -= g.size
			stats.Deleted++
			stats.Freed += g.size
		}
	}
	stats.Kept = len(galleries)
	return stats, nil
}

// Generate writes five full-frame original/new pairs and an interactive wipe
// page. It is idempotent once the first original frame exists.
func Generate(ctx context.Context, runner ffmpeg.Runner, duration float64, task Task, root string) error {
	outDir := filepath.Join(root, strconv.FormatInt(task.ID, 10))
	if _, err := os.Stat(filepath.Join(outDir, "index.html")); err == nil {
		if _, err := os.Stat(filepath.Join(outDir, "01-original.png")); err == nil {
			return nil
		}
	}
	if duration <= 0 {
		return fmt.Errorf("duration is unknown")
	}
	if _, err := os.Stat(task.Source); err != nil {
		return fmt.Errorf("original unavailable: %w", err)
	}
	if _, err := os.Stat(task.Output); err != nil {
		return fmt.Errorf("output unavailable: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	// Older split-only galleries are replaced by full frames.
	if old, err := filepath.Glob(filepath.Join(outDir, "*-split.png")); err == nil {
		for _, path := range old {
			_ = os.Remove(path)
		}
	}
	for i, at := range sampleTimes(duration) {
		original := filepath.Join(outDir, fmt.Sprintf("%02d-original.png", i+1))
		replacement := filepath.Join(outDir, fmt.Sprintf("%02d-new.png", i+1))
		if err := extract(ctx, runner, task.Source, at, original); err != nil {
			return fmt.Errorf("extract original at %ds: %w", int(at), err)
		}
		if err := extract(ctx, runner, task.Output, at, replacement); err != nil {
			return fmt.Errorf("extract new at %ds: %w", int(at), err)
		}
	}
	return writeIndex(outDir, task, duration)
}

func sampleTimes(duration float64) []float64 {
	times := make([]float64, 0, sampleCount)
	for i := 0; i < sampleCount; i++ {
		fraction := 0.1 + 0.8*float64(i)/float64(sampleCount-1)
		at := fraction * duration
		if at > duration-1 {
			at = duration - 1
		}
		if at < 0 {
			at = 0
		}
		times = append(times, at)
	}
	return times
}

func extract(ctx context.Context, runner ffmpeg.Runner, path string, at float64, out string) error {
	_, err := runner.RunCapture(ctx, timeout,
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(at, 'f', 3, 64),
		"-i", path, "-frames:v", "1", "-vf", "format=yuv420p", out)
	return err
}

func writeIndex(dir string, task Task, duration float64) error {
	file, err := os.Create(filepath.Join(dir, "index.html"))
	if err != nil {
		return err
	}
	defer file.Close()
	type sample struct {
		Index int
		At    float64
	}
	var samples []sample
	for i, at := range sampleTimes(duration) {
		samples = append(samples, sample{Index: i + 1, At: at})
	}
	page := template.Must(template.New("compare").Parse(compareHTML))
	return page.Execute(file, struct {
		Task     Task
		Duration float64
		Samples  []sample
	}{Task: task, Duration: duration, Samples: samples})
}

const compareHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>lessarr comparison</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
body{font:15px/1.5 ui-sans-serif,system-ui,sans-serif;background:#f6f3ec;color:#1f1f1f;margin:2rem auto;max-width:1200px;padding:0 1rem 3rem}
a{color:#d71920;text-decoration:none}a:hover{text-decoration:underline}
h1{font-size:1.35rem;margin:0 0 .3rem}
figure{margin:0 0 2rem}
.compare{position:relative;line-height:0;border:1px solid #e4ddd0;border-radius:12px;overflow:hidden;background:#fffefa;user-select:none;box-shadow:0 1px 2px rgba(15,23,42,.05)}
.compare>img{display:block;width:100%;height:auto}
.after{position:absolute;inset:0;overflow:hidden;clip-path:inset(0 0 0 50%)}
.after img{position:absolute;top:0;left:0;width:100%;height:auto}
.divider{position:absolute;top:0;bottom:0;left:50%;width:2px;background:#fffefa;box-shadow:0 0 6px rgba(15,23,42,.8);pointer-events:none}
.divider:after{content:"";position:absolute;top:50%;left:50%;width:30px;height:30px;border-radius:50%;background:#fffefa;border:2px solid #d71920;transform:translate(-50%,-50%);box-shadow:0 1px 6px rgba(15,23,42,.35)}
.compare input{position:absolute;inset:0;width:100%;height:100%;margin:0;opacity:0;cursor:ew-resize}
figcaption{color:#6f6a61;margin-top:.4rem;line-height:1.4}
.legend{color:#6f6a61;margin:0 0 .8rem}
</style></head><body>
<h1>Job {{.Task.ID}} comparison</h1>
<p><a href="/jobs/{{.Task.ID}}">← job details</a> · duration {{printf "%.1f" .Duration}}s</p>
<p class="legend">Drag the white bar: left reveals the original, right reveals the new file.</p>
{{range .Samples}}
<figure>
<div class="compare">
<img src="{{printf "%02d" .Index}}-original.png" alt="original">
<div class="after"><img src="{{printf "%02d" .Index}}-new.png" alt="new"></div>
<div class="divider"></div>
<input type="range" min="0" max="100" value="50" aria-label="Reveal original or new">
</div>
<figcaption>{{printf "%02d" .Index}} · {{printf "%.0f" .At}}s · drag to compare</figcaption>
</figure>
{{end}}
<script>
document.querySelectorAll(".compare").forEach(function (el) {
  var range = el.querySelector("input");
  var after = el.querySelector(".after");
  var divider = el.querySelector(".divider");
  function update() {
    var v = Number(range.value);
    after.style.clipPath = "inset(0 0 0 " + v + "%)";
    divider.style.left = v + "%";
  }
  range.addEventListener("input", update);
  update();
});
</script>
</body></html>`
