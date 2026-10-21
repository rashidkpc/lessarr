package compare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSampleTimes(t *testing.T) {
	times := sampleTimes(1000)
	want := []float64{100, 300, 500, 700, 900}
	if len(times) != len(want) {
		t.Fatalf("got %d times", len(times))
	}
	for i := range want {
		if diff := times[i] - want[i]; diff > 0.001 || diff < -0.001 {
			t.Errorf("time %d = %.1f, want %.1f", i, times[i], want[i])
		}
	}
	if got := sampleTimes(0.5); got[0] != 0 {
		t.Errorf("short duration should clamp to zero, got %v", got)
	}
}

func TestWriteIndexHasWipeSlider(t *testing.T) {
	dir := t.TempDir()
	if err := writeIndex(dir, Task{ID: 7}, 100); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`type="range"`, "01-original.png", "01-new.png", "clip-path", "drag to compare"} {
		if !strings.Contains(body, want) {
			t.Errorf("comparison page missing %q", want)
		}
	}
}

func TestPruneByAgeAndSize(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	makeGallery := func(name string, age time.Duration, size int) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "01-original.png")
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	old := makeGallery("1", 48*time.Hour, 10)
	recent := makeGallery("2", time.Hour, 10)
	newest := makeGallery("3", 0, 10)
	work := filepath.Join(root, ".work-incomplete")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	stats, err := Prune(root, 24*time.Hour, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 || stats.Freed != 10 {
		t.Fatalf("age prune stats = %+v", stats)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old gallery was not removed")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("recent gallery was removed")
	}
	if _, err := os.Stat(work); err != nil {
		t.Error("work directory should not be touched by Prune")
	}

	stats, err = Prune(root, 0, 15, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 || stats.Kept != 1 {
		t.Fatalf("size prune stats = %+v", stats)
	}
	if _, err := os.Stat(recent); !os.IsNotExist(err) {
		t.Error("older gallery should be removed by the size cap")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Error("newest gallery must be kept")
	}
}
