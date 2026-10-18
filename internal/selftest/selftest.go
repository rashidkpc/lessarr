// Package selftest answers the questions that decide whether lessarr can work on
// a given host, by trying things instead of assuming them.
//
// It is deliberately the first thing to run on a new machine: it touches no
// existing file, and every claim in its report came from an actual attempt.
package selftest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/ffmpeg"
)

type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

type Check struct {
	Name   string
	Status Status
	Detail string
	Extra  []string
}

type Report struct {
	Checks []Check
}

func (r *Report) add(status Status, name, detail string, extra ...string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail, Extra: extra})
}

func (r *Report) addCheck(c Check) { r.Checks = append(r.Checks, c) }

func (r *Report) OK(name, detail string, extra ...string) { r.add(StatusOK, name, detail, extra...) }
func (r *Report) Warn(name, detail string, extra ...string) {
	r.add(StatusWarn, name, detail, extra...)
}
func (r *Report) Fail(name, detail string, extra ...string) {
	r.add(StatusFail, name, detail, extra...)
}
func (r *Report) Skip(name, detail string, extra ...string) {
	r.add(StatusSkip, name, detail, extra...)
}

func (r Report) Failures() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			n++
		}
	}
	return n
}

func (r Report) Warnings() int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == StatusWarn {
			n++
		}
	}
	return n
}

type Options struct {
	Runner       ffmpeg.Runner
	Device       string
	MediaPath    string
	WorkDir      string
	TrialSeconds int
	Timeout      time.Duration
	Backends     []string
}

// Run performs every check it can, degrading to a warning or skip rather than
// aborting, so one report shows the whole picture.
func Run(ctx context.Context, o Options) Report {
	var rep Report
	if o.TrialSeconds <= 0 {
		o.TrialSeconds = 3
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if len(o.Backends) == 0 {
		o.Backends = []string{"vaapi", "qsv", "software"}
	}
	if o.WorkDir == "" {
		o.WorkDir = os.TempDir()
	}

	rep.OK("platform", fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH), kernelVersion())

	r := o.Runner
	if r.FFmpeg == "" {
		r = ffmpeg.NewRunner()
	}

	ffmpegVersion, ffErr := r.Version(ctx)
	if ffErr != nil {
		rep.Fail("ffmpeg", "not usable: "+ffErr.Error())
	} else {
		rep.OK("ffmpeg", ffmpegVersion)
	}
	if v, err := r.ProbeVersion(ctx); err != nil {
		rep.Fail("ffprobe", "not usable: "+err.Error())
	} else {
		rep.OK("ffprobe", v)
	}

	// Device.
	if o.Device == "" {
		rep.Skip("device", "no device configured")
	} else {
		rep.addCheck(deviceCheck(o.Device))
	}

	// Hardware acceleration methods and decoders.
	decoders, decErr := r.Decoders(ctx)
	if decErr != nil {
		rep.Warn("hw decode", "could not list decoders: "+decErr.Error())
	} else {
		support := ffmpeg.HardwareDecodeSupport(decoders)
		var have, missing []string
		for _, codec := range ffmpeg.SourceCodecsWithHWDecode() {
			if support[codec] {
				have = append(have, codec)
			} else {
				missing = append(missing, codec)
			}
		}
		if len(have) > 0 {
			rep.OK("hw decode", "hardware decoders: "+strings.Join(have, ", "))
		}
		if len(missing) > 0 {
			rep.Warn("hw decode gaps",
				"no hardware decoder for: "+strings.Join(missing, ", "),
				"These decode on the CPU. The encoder filter chain for them must upload frames to the GPU rather than assuming hardware frames, otherwise ffmpeg fails with 'Impossible to convert between the formats'.")
		}
	}

	if accels, err := r.HWAccels(ctx); err != nil {
		rep.Warn("hwaccels", "could not list: "+err.Error())
	} else if len(accels) == 0 {
		rep.Warn("hwaccels", "none compiled in")
	} else {
		rep.OK("hwaccels", strings.Join(accels, " "))
	}

	// Encoders and trial encodes.
	encoders, encErr := r.Encoders(ctx)
	if encErr != nil {
		rep.Fail("encoders", "could not list: "+encErr.Error())
	} else {
		var present []string
		for _, name := range []string{"hevc_vaapi", "hevc_qsv", "libx265", "eac3"} {
			if encoders[name] {
				present = append(present, name)
			}
		}
		if len(present) == 0 {
			rep.Fail("encoders", "no usable HEVC encoder found")
		} else {
			rep.OK("encoders", "available: "+strings.Join(present, ", "))
		}

		for _, name := range o.Backends {
			b, err := ffmpeg.ByName(name, o.Device)
			if err != nil {
				rep.Skip("trial "+name, err.Error())
				continue
			}
			if name != "software" && !encoders[b.Encoder] {
				rep.Skip("trial "+name, b.Encoder+" is not compiled into this ffmpeg")
				continue
			}
			trial := r.TrialEncode(ctx, b, o.Device, o.WorkDir, o.TrialSeconds, o.Timeout)
			switch {
			case trial.OK && trial.Decodes:
				rep.OK("trial "+name, fmt.Sprintf("%.0f fps, %s, decodes cleanly",
					trial.FPS, humanBytes(trial.Bytes)))
			case trial.OK && !trial.Decodes:
				rep.Warn("trial "+name, "encoded but the output did not decode cleanly")
			default:
				rep.Fail("trial "+name, firstLine(trial.Err))
			}
		}
	}

	// Filesystem checks against the media path.
	if o.MediaPath == "" {
		rep.Skip("filesystem", "no media path given")
		return rep
	}
	rep.addCheck(filesystemChecks(o))
	return rep
}

func deviceCheck(device string) Check {
	check := Check{Name: "device"}
	fi, err := os.Stat(device)
	if err != nil {
		check.Status = StatusFail
		check.Detail = device + " not found"
		check.Extra = []string{
			"If this is a container, /dev/dri is not mapped. Add `devices: [\"/dev/dri:/dev/dri\"]` to the service.",
			"On the host, check that the iGPU is enabled: ls -l /dev/dri",
		}
		return check
	}
	uid, gid, _ := uidGidOf(fi)
	mode := fi.Mode().Perm()

	f, openErr := os.Open(device)
	if openErr != nil {
		check.Status = StatusFail
		check.Detail = fmt.Sprintf("%s exists (mode %04o uid=%d gid=%d) but cannot be opened: %v",
			device, mode, uid, gid, openErr)
		check.Extra = []string{"Run the container as root, or add the device's group."}
		return check
	}
	_ = f.Close()

	check.Status = StatusOK
	check.Detail = fmt.Sprintf("%s mode=%04o uid=%d gid=%d, openable", device, mode, uid, gid)
	return check
}

func filesystemChecks(o Options) Check {
	check := Check{Name: "filesystem"}
	mountPoint, fsType, opts, err := mountInfo(o.MediaPath)
	if err != nil {
		check.Status = StatusWarn
		check.Detail = "could not inspect mounts: " + err.Error()
		return check
	}
	readOnly := false
	for _, opt := range strings.Split(opts, ",") {
		if opt == "ro" {
			readOnly = true
		}
	}

	lines := []string{fmt.Sprintf("%s is %s, mounted at %s", o.MediaPath, fsType, mountPoint)}
	if strings.Contains(fsType, "btrfs") {
		lines = append(lines, "btrfs: snapshots are available, which is the safest rollback before a live run")
	}

	if free, total, err := statFree(o.MediaPath); err == nil {
		lines = append(lines, fmt.Sprintf("free %s of %s", humanBytes(int64(free)), humanBytes(int64(total))))
	}

	if readOnly {
		check.Status = StatusWarn
		check.Detail = "media is mounted read-only"
		check.Extra = append(lines, "Correct for a stats-only run. Writing needs this mount to be read-write.")
		return check
	}

	detail, writeErr := writeProbe(o.MediaPath)
	if writeErr != nil {
		check.Status = StatusFail
		check.Detail = "cannot write to the media path: " + writeErr.Error()
		check.Extra = lines
		return check
	}
	check.Status = StatusOK
	check.Detail = detail
	check.Extra = lines

	if same, err := sameDevice(o.MediaPath, o.WorkDir); err == nil && !same {
		check.Status = StatusWarn
		check.Extra = append(check.Extra, fmt.Sprintf(
			"WARNING: %s and %s are on different filesystems, so a temp file there cannot be renamed into place atomically",
			o.WorkDir, o.MediaPath))
	}
	return check
}

// writeProbe creates, renames and removes a file in the target directory. It is
// the only thing selftest writes, and it cleans up after itself.
func writeProbe(dir string) (string, error) {
	name := fmt.Sprintf(".lessarr-selftest-%d", os.Getpid())
	tmp := filepath.Join(dir, name+".tmp")
	done := filepath.Join(dir, name+".done")

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(make([]byte, 64*1024)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	if err := os.Rename(tmp, done); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("rename (the atomic swap step) failed: %w", err)
	}
	fi, err := os.Stat(done)
	if err != nil {
		return "", err
	}
	uid, gid, _ := uidGidOf(fi)
	if err := os.Remove(done); err != nil {
		return "", fmt.Errorf("cleanup failed: %w", err)
	}
	return fmt.Sprintf("create, fsync and rename all work; resulting file uid=%d gid=%d mode=%04o",
		uid, gid, fi.Mode().Perm()), nil
}

func kernelVersion() string {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return runtime.GOOS
	}
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		return "kernel " + fields[2]
	}
	return "unknown kernel"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if line == "" {
		return "failed with no output"
	}
	return line
}
