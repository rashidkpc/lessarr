package selftest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/ffmpeg"
)

func TestReportCounters(t *testing.T) {
	var r Report
	r.OK("a", "fine")
	r.Warn("b", "meh")
	r.Fail("c", "broken")
	r.Skip("d", "n/a")
	r.Fail("e", "also broken")
	if got := r.Failures(); got != 2 {
		t.Errorf("Failures = %d, want 2", got)
	}
	if got := r.Warnings(); got != 1 {
		t.Errorf("Warnings = %d, want 1", got)
	}
	if len(r.Checks) != 5 {
		t.Errorf("Checks = %d, want 5", len(r.Checks))
	}
}

func TestDeviceCheckMissingDevice(t *testing.T) {
	c := deviceCheck(filepath.Join(t.TempDir(), "nope"))
	if c.Status != StatusFail {
		t.Errorf("status = %s, want fail", c.Status)
	}
	if len(c.Extra) == 0 {
		t.Error("a missing device should come with remediation hints")
	}
}

func TestDeviceCheckPresentDevice(t *testing.T) {
	// A plain file stands in for the render node: the check cares that the path
	// exists and can be opened, which is exactly what goes wrong in a container.
	path := filepath.Join(t.TempDir(), "renderD128")
	if err := os.WriteFile(path, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	c := deviceCheck(path)
	if c.Status != StatusOK {
		t.Errorf("status = %s (%s), want ok", c.Status, c.Detail)
	}
}

// TestWriteProbeCleansUp guards the only thing selftest writes.
func TestWriteProbeCleansUp(t *testing.T) {
	dir := t.TempDir()
	detail, err := writeProbe(dir)
	if err != nil {
		t.Fatalf("writeProbe: %v", err)
	}
	if detail == "" {
		t.Error("expected a description of what worked")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("writeProbe left files behind: %v", names)
	}
}

func TestFilesystemChecksOnRealDirectory(t *testing.T) {
	dir := t.TempDir()
	check := filesystemChecks(Options{MediaPath: dir, WorkDir: dir})
	if check.Status != StatusOK {
		t.Fatalf("status = %s: %s", check.Status, check.Detail)
	}
	if len(check.Extra) == 0 {
		t.Error("expected filesystem details")
	}
	// Same directory means the same device, so no atomicity warning.
	for _, line := range check.Extra {
		if len(line) > 8 && line[:8] == "WARNING:" {
			t.Errorf("unexpected warning for a single directory: %s", line)
		}
	}
}

// TestRunDegradesGracefully ensures a host with no ffmpeg produces a report
// rather than a panic.
func TestRunDegradesGracefully(t *testing.T) {
	rep := Run(context.Background(), Options{
		Runner:       ffmpeg.Runner{FFmpeg: "/nonexistent/ffmpeg", FFProbe: "/nonexistent/ffprobe"},
		Device:       "/dev/dri/not-here",
		MediaPath:    t.TempDir(),
		TrialSeconds: 1,
		Timeout:      5 * time.Second,
		Backends:     []string{"software"},
	})
	if len(rep.Checks) == 0 {
		t.Fatal("expected a report")
	}
	if rep.Failures() == 0 {
		t.Error("a host with no ffmpeg and no device must report failures")
	}
}

func TestRunSkipsTrialWhenEncoderAbsent(t *testing.T) {
	// /bin/true emits no encoder list, so no backend should be attempted.
	rep := Run(context.Background(), Options{
		Runner:       ffmpeg.Runner{FFmpeg: "/bin/true", FFProbe: "/bin/true"},
		Device:       "",
		TrialSeconds: 1,
		Timeout:      5 * time.Second,
	})
	for _, c := range rep.Checks {
		if c.Name == "trial vaapi" && c.Status == StatusFail {
			t.Errorf("should have skipped rather than failed: %s", c.Detail)
		}
	}
}
