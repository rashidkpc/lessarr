package web

import (
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/queue"
)

func TestBuildChartsUseZeroBaseline(t *testing.T) {
	now := time.Now()
	points := []queue.Metric{
		{At: now, Pending: 55, TrashBytes: 100, SavedBytes: 200},
		{At: now.Add(5 * time.Minute), Pending: 55, TrashBytes: 100, SavedBytes: 200},
	}
	charts := buildCharts(points)
	if len(charts) != 4 {
		t.Fatalf("charts = %d", len(charts))
	}
	if charts[0].MinLabel != "0" || charts[0].MaxLabel != "55" {
		t.Errorf("queue axis = %s..%s, want 0..55", charts[0].MinLabel, charts[0].MaxLabel)
	}
	if charts[2].MinLabel != "0 B" {
		t.Errorf("trash axis minimum = %q, want 0 B", charts[2].MinLabel)
	}
}
