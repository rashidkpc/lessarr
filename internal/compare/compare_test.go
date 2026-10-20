package compare

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
