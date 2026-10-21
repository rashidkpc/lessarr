package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rashidkpc/lessarr/internal/queue"
)

const metricsWindow = 30 * 24 * time.Hour

type MetricsPage struct {
	Version string
	Charts  []Chart
}

type YTick struct {
	Y     string
	Label string
}

type Chart struct {
	Title      string
	Subtitle   string
	Path       string
	Area       string
	MinLabel   string
	MaxLabel   string
	LastLabel  string
	FirstAt    string
	LastAt     string
	LastX      string
	LastY      string
	AxisLeft   string
	AxisRight  string
	AxisTop    string
	AxisBottom string
	AxisLabelX string
	YTicks     []YTick
	HasData    bool
}

func (s *Server) metricsPage(w http.ResponseWriter, r *http.Request) {
	points, err := s.store.MetricsSince(time.Now().Add(-metricsWindow))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	page := MetricsPage{Version: s.version, Charts: buildCharts(points)}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "metrics", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func buildCharts(points []queue.Metric) []Chart {
	if len(points) == 0 {
		return nil
	}
	points = downsample(points, 240)
	specs := []struct {
		title    string
		subtitle string
		value    func(queue.Metric) int64
		bytes    bool
	}{
		{"Queue depth", "pending jobs", func(m queue.Metric) int64 { return int64(m.Pending) }, false},
		{"Failures", "failed and restored jobs", func(m queue.Metric) int64 { return int64(m.Failed) }, false},
		{"Trash", "retained original bytes", func(m queue.Metric) int64 { return m.TrashBytes }, true},
		{"Reclaimed", "total completed savings", func(m queue.Metric) int64 { return m.SavedBytes }, true},
	}
	charts := make([]Chart, 0, len(specs))
	for _, spec := range specs {
		charts = append(charts, buildChart(points, spec.title, spec.subtitle, spec.value, spec.bytes))
	}
	return charts
}

func buildChart(points []queue.Metric, title, subtitle string, value func(queue.Metric) int64, bytes bool) Chart {
	const width, height = 640.0, 180.0
	const left, right, top, bottom = 64.0, 18.0, 12.0, 20.0
	chart := Chart{Title: title, Subtitle: subtitle}
	minT, maxT := points[0].At, points[len(points)-1].At
	if !maxT.After(minT) {
		maxT = minT.Add(time.Second)
	}
	// Charts start at zero so a flat series does not imply a narrow range
	// between two arbitrary adjacent values.
	minV := int64(0)
	maxV := value(points[0])
	for _, p := range points[1:] {
		if v := value(p); v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		maxV = 1
	}
	format := func(v int64) string {
		if bytes {
			return humanBytes(v)
		}
		return fmt.Sprintf("%d", v)
	}
	xFor := func(t time.Time) float64 {
		return left + (float64(t.Sub(minT))/float64(maxT.Sub(minT)))*(width-left-right)
	}
	yFor := func(v int64) float64 {
		return height - bottom - (float64(v-minV)/float64(maxV-minV))*(height-top-bottom)
	}

	var path string
	for i, p := range points {
		x, y := xFor(p.At), yFor(value(p))
		if i == 0 {
			path += fmt.Sprintf("M %.1f %.1f", x, y)
		} else {
			path += fmt.Sprintf(" L %.1f %.1f", x, y)
		}
	}
	firstX, firstY := xFor(points[0].At), yFor(value(points[0]))
	last := points[len(points)-1]
	lastX, lastY := xFor(last.At), yFor(value(last))
	chart.Path = path
	chart.Area = fmt.Sprintf("M %.1f %.1f L %.1f %.1f L %.1f %.1f L %.1f %.1f Z",
		firstX, height-bottom, firstX, firstY, lastX, lastY, lastX, height-bottom)
	chart.MinLabel = format(minV)
	chart.MaxLabel = format(maxV)
	chart.LastLabel = format(value(last))
	chart.FirstAt = axisTime(points[0].At)
	chart.LastAt = axisTime(last.At)
	chart.LastX = fmt.Sprintf("%.1f", lastX)
	chart.LastY = fmt.Sprintf("%.1f", lastY)
	chart.AxisLeft = fmt.Sprintf("%.1f", left)
	chart.AxisRight = fmt.Sprintf("%.1f", width-right)
	chart.AxisTop = fmt.Sprintf("%.1f", top)
	chart.AxisBottom = fmt.Sprintf("%.1f", height-bottom)
	chart.AxisLabelX = fmt.Sprintf("%.1f", left-6)
	for i := 0; i <= 4; i++ {
		v := maxV * int64(i) / 4
		chart.YTicks = append(chart.YTicks, YTick{Y: fmt.Sprintf("%.1f", yFor(v)), Label: format(v)})
	}
	chart.HasData = true
	return chart
}

// downsample keeps chart SVGs small without changing the stored samples.
func downsample(points []queue.Metric, max int) []queue.Metric {
	if len(points) <= max {
		return points
	}
	out := make([]queue.Metric, 0, max)
	step := float64(len(points)-1) / float64(max-1)
	for i := 0; i < max; i++ {
		idx := int(float64(i)*step + 0.5)
		if idx >= len(points) {
			idx = len(points) - 1
		}
		out = append(out, points[idx])
	}
	out[len(out)-1] = points[len(points)-1]
	return out
}

func axisTime(t time.Time) string {
	if time.Since(t) < 48*time.Hour {
		return t.Format("15:04")
	}
	return t.Format("Jan 2")
}
