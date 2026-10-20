package validate

import (
	"testing"
	"time"
)

func TestSampleWindows(t *testing.T) {
	windows := sampleWindows(100 * time.Second)
	if len(windows) != sampleCount {
		t.Fatalf("got %d windows, want %d", len(windows), sampleCount)
	}
	wantStarts := []time.Duration{0, 20 * time.Second, 40 * time.Second, 60 * time.Second, 80 * time.Second}
	for i, want := range wantStarts {
		if windows[i].Start != want {
			t.Errorf("window %d starts at %s, want %s", i, windows[i].Start, want)
		}
		if windows[i].Start+windows[i].Len > 100*time.Second {
			t.Errorf("window %d extends past the end", i)
		}
	}
	if got := sampleWindows(30 * time.Second); got != nil {
		t.Errorf("short files should use a full decode, got %v", got)
	}
}

func TestSampleWindowsStayInsideLongFile(t *testing.T) {
	duration := 2*time.Hour + 17*time.Minute
	windows := sampleWindows(duration)
	if len(windows) != sampleCount {
		t.Fatalf("got %d windows", len(windows))
	}
	for i, window := range windows {
		if window.Start < 0 || window.Start+window.Len > duration {
			t.Errorf("window %d out of range: start %s len %s", i, window.Start, window.Len)
		}
	}
}
