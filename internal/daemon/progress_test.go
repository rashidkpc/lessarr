package daemon

import (
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
)

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

func TestSettled(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)
	lib := config.DefaultLibrary()
	lib.SkipIfNewerThan = config.Duration(10 * time.Minute)
	if settled(now.Add(-time.Minute), lib, now) {
		t.Error("a file modified one minute ago should not be settled")
	}
	if !settled(now.Add(-11*time.Minute), lib, now) {
		t.Error("a file modified eleven minutes ago should be settled")
	}
	lib.SkipIfNewerThan = 0
	if !settled(now, lib, now) {
		t.Error("zero settle window should always be settled")
	}
}
