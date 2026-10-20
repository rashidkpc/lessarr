package compare

import "testing"

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
