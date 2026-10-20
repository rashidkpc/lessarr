package daemon

import "testing"

func TestSamplingStepsUpdateSubProgress(t *testing.T) {
	d := &Daemon{}
	d.startProgress(1)
	d.addProgressStep("validating")
	d.addProgressStep("scanning packets")
	d.addProgressStep("sampling 1/5")
	d.addProgressStep("sampling 2/5")
	p := d.currentProgress()
	if p == nil {
		t.Fatal("expected progress")
	}
	if p.Stage != "sampling 2/5" {
		t.Errorf("stage = %q", p.Stage)
	}
	if p.SampleDone != 1 || p.SampleTotal != 5 || p.SamplePct != 20 {
		t.Errorf("sample progress = %d/%d %.1f, want 1/5 20.0", p.SampleDone, p.SampleTotal, p.SamplePct)
	}
}
