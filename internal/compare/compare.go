// Package compare renders split-frame comparisons between a retained original
// and its replacement. It is intentionally stills-only: five frames are cheap
// enough to keep for every transcode and expose softness at the seam.
package compare

import (
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
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

// Generate writes five split images and an index.html. It is idempotent: once
// index.html exists, the job is skipped.
func Generate(ctx context.Context, runner ffmpeg.Runner, duration float64, task Task, root string) error {
	outDir := filepath.Join(root, strconv.FormatInt(task.ID, 10))
	if _, err := os.Stat(filepath.Join(outDir, "index.html")); err == nil {
		return nil
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
	work, err := os.MkdirTemp(outDir, ".work-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	font := findFont()
	for i, at := range sampleTimes(duration) {
		original := filepath.Join(work, fmt.Sprintf("%d-original.png", i))
		replacement := filepath.Join(work, fmt.Sprintf("%d-new.png", i))
		if err := extract(ctx, runner, task.Source, at, original); err != nil {
			return fmt.Errorf("extract original at %ds: %w", int(at), err)
		}
		if err := extract(ctx, runner, task.Output, at, replacement); err != nil {
			return fmt.Errorf("extract new at %ds: %w", int(at), err)
		}
		split := filepath.Join(outDir, fmt.Sprintf("%02d-split.png", i+1))
		if err := compose(ctx, runner, original, replacement, split, font); err != nil {
			return fmt.Errorf("compose split at %ds: %w", int(at), err)
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

func compose(ctx context.Context, runner ffmpeg.Runner, original, replacement, out, font string) error {
	left := "[0:v]crop=iw/2:ih:0:0"
	right := "[1:v]crop=iw/2:ih:iw/2:0"
	if font != "" {
		left += ",drawtext=fontfile=" + font + ":text='ORIGINAL':x=18:y=18:fontsize=22:fontcolor=white:box=1:boxcolor=black@0.45:boxborderw=6"
		right += ",drawtext=fontfile=" + font + ":text='NEW':x=18:y=18:fontsize=22:fontcolor=white:box=1:boxcolor=black@0.45:boxborderw=6"
	}
	graph := left + "[l];" + right + "[r];[l][r]hstack=inputs=2," +
		"drawbox=x=iw/2-1:y=0:w=2:h=ih:color=white@0.8:t=fill"
	_, err := runner.RunCapture(ctx, timeout,
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", original, "-i", replacement,
		"-filter_complex", graph, "-frames:v", "1", out)
	return err
}

func findFont() string {
	for _, path := range []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
		"/usr/share/fonts/truetype/liberation/LiberationSans-Bold.ttf",
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
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
body{font:15px/1.5 system-ui,sans-serif;background:#111;color:#ddd;margin:2rem auto;max-width:1200px;padding:0 1rem}
a{color:#8ab4f8}figure{margin:0 0 2rem}img{max-width:100%;height:auto;border:1px solid #333}
figcaption{color:#999;margin-top:.4rem}
</style></head><body>
<h1>Job {{.Task.ID}} comparison</h1>
<p><a href="/jobs/{{.Task.ID}}">← job details</a> · left = original, right = new · duration {{printf "%.1f" .Duration}}s</p>
{{range .Samples}}
<figure>
<img src="{{printf "%02d" .Index}}-split.png" alt="split comparison">
<figcaption>{{printf "%02d" .Index}} · {{printf "%.0f" .At}}s · left original / right new</figcaption>
</figure>
{{end}}
</body></html>`
