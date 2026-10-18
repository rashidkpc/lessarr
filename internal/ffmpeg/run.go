package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Runner executes the ffmpeg family. Everything that shells out goes through
// here so that capability probing and real work share one code path.
type Runner struct {
	FFmpeg  string
	FFProbe string
}

func NewRunner() Runner { return Runner{FFmpeg: "ffmpeg", FFProbe: "ffprobe"} }

func (r Runner) ffmpegBin() string {
	if r.FFmpeg != "" {
		return r.FFmpeg
	}
	return "ffmpeg"
}

func (r Runner) ffprobeBin() string {
	if r.FFProbe != "" {
		return r.FFProbe
	}
	return "ffprobe"
}

// Version returns the first line of `ffmpeg -version`.
func (r Runner) Version(ctx context.Context) (string, error) {
	out, err := r.output(ctx, r.ffmpegBin(), "-hide_banner", "-version")
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return line, nil
}

func (r Runner) ProbeVersion(ctx context.Context) (string, error) {
	out, err := r.output(ctx, r.ffprobeBin(), "-hide_banner", "-version")
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return line, nil
}

// Encoders lists the encoder names this build actually provides. A name being
// listed does not mean the hardware supports it; only a trial encode proves
// that.
func (r Runner) Encoders(ctx context.Context) (map[string]bool, error) {
	out, err := r.output(ctx, r.ffmpegBin(), "-hide_banner", "-encoders")
	if err != nil {
		return nil, err
	}
	return parseCodecList(out), nil
}

// Decoders lists available decoders, used to spot source codecs that will fall
// back to software decode.
func (r Runner) Decoders(ctx context.Context) (map[string]bool, error) {
	out, err := r.output(ctx, r.ffmpegBin(), "-hide_banner", "-decoders")
	if err != nil {
		return nil, err
	}
	return parseCodecList(out), nil
}

// HWAccels lists the hardware acceleration methods compiled in.
func (r Runner) HWAccels(ctx context.Context) ([]string, error) {
	out, err := r.output(ctx, r.ffmpegBin(), "-hide_banner", "-hwaccels")
	if err != nil {
		return nil, err
	}
	var accels []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Hardware acceleration") {
			continue
		}
		accels = append(accels, line)
	}
	return accels, nil
}

// parseCodecList reads ffmpeg's `-encoders` / `-decoders` table. The flag column
// is six characters of [VASD.F.], and the legacy header rows use "=" where a
// codec name would be.
func parseCodecList(out string) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		flags, name := fields[0], fields[1]
		if len(flags) != 6 || !strings.ContainsAny(flags, "VASD") {
			continue
		}
		if name == "=" {
			continue
		}
		names[name] = true
	}
	return names
}

// TrialResult is the outcome of one synthetic encode.
type TrialResult struct {
	Backend string
	OK      bool
	Err     string
	Wall    time.Duration
	FPS     float64
	Bytes   int64
	Decodes bool
	Command []string
	Output  string
}

// TrialEncode runs a short synthetic encode through a backend.
//
// The hardware backends are driven from a lavfi source, so the frames start in
// system memory and have to be uploaded. That exercises device creation, the
// upload path and the encoder together — which is the combination that actually
// breaks on this class of hardware.
func (r Runner) TrialEncode(ctx context.Context, b Backend, device, dir string, seconds int, timeout time.Duration) TrialResult {
	res := TrialResult{Backend: b.Name, Output: filepath.Join(dir, "lessarr-trial-"+b.Name+".mkv")}
	args := trialArgs(b, device, res.Output, seconds)
	res.Command = args

	start := time.Now()
	_, err := r.runTimeout(ctx, timeout, args...)
	res.Wall = time.Since(start)
	if err != nil {
		res.Err = err.Error()
		return res
	}

	res.OK = true
	if fi, statErr := os.Stat(res.Output); statErr == nil {
		res.Bytes = fi.Size()
	}
	if res.Wall > 0 {
		res.FPS = float64(seconds*25) / res.Wall.Seconds()
	}
	res.Decodes = r.decodes(ctx, res.Output, timeout)
	return res
}

func trialArgs(b Backend, device, out string, seconds int) []string {
	base := []string{"-hide_banner", "-nostdin", "-y"}
	if b.Name == "software" {
		return append(base,
			"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25",
			"-t", strconv.Itoa(seconds),
			"-c:v", "libx265", "-crf", "24", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-progress", "pipe:1", "-nostats", out)
	}
	upload := "hwupload"
	enc := []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", "24"}
	devices := []string{"-init_hw_device", "vaapi=va:" + device, "-filter_hw_device", "va"}
	if b.Name == "qsv" {
		upload = "hwupload=extra_hw_frames=64"
		enc = []string{"-c:v", "hevc_qsv", "-preset", "slow", "-global_quality", "24"}
		devices = []string{"-init_hw_device", "qsv=hw:" + device, "-filter_hw_device", "hw"}
	}
	args := append(base, devices...)
	args = append(args,
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25",
		"-t", strconv.Itoa(seconds),
		"-vf", "format=nv12,"+upload)
	args = append(args, enc...)
	return append(args, "-progress", "pipe:1", "-nostats", out)
}

// decodes does a null decode to confirm the produced file is actually readable,
// not merely present.
func (r Runner) decodes(ctx context.Context, path string, timeout time.Duration) bool {
	_, err := r.runTimeout(ctx, timeout, "-hide_banner", "-v", "error", "-i", path, "-f", "null", "-")
	return err == nil
}

func (r Runner) output(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", filepath.Base(bin), lastLines(msg, 3))
	}
	return stdout.String(), nil
}

func (r Runner) runTimeout(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, r.ffmpegBin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if cctx.Err() == context.DeadlineExceeded {
		return msg, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return msg, fmt.Errorf("%s", lastLines(msg, 4))
	}
	return msg, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// hwDecoderCandidates maps a source codec to the hardware decoder names that
// could handle it. MPEG-4 Part 2 (Xvid) has no hardware decoder on any
// consumer Intel generation, which is why it appears here.
var hwDecoderCandidates = map[string][]string{
	"h264":       {"h264_vaapi", "h264_qsv"},
	"hevc":       {"hevc_vaapi", "hevc_qsv"},
	"mpeg2video": {"mpeg2_vaapi", "mpeg2_qsv"},
	"vc1":        {"vc1_vaapi", "vc1_qsv"},
	"mpeg4":      {"mpeg4_vaapi", "mpeg4_qsv"},
	"vp9":        {"vp9_vaapi", "vp9_qsv"},
}

// HardwareDecodeSupport reports, per source codec, whether any hardware decoder
// is available. The rest fall back to software decode, which changes the filter
// chain the encoder needs.
func HardwareDecodeSupport(decoders map[string]bool) map[string]bool {
	support := map[string]bool{}
	for codec, candidates := range hwDecoderCandidates {
		for _, c := range candidates {
			if decoders[c] {
				support[codec] = true
				break
			}
		}
	}
	return support
}

// SourceCodecsWithHWDecode is the list of codecs HardwareDecodeSupport knows
// about, in a stable (sorted) order so reports do not shuffle between runs.
func SourceCodecsWithHWDecode() []string {
	return []string{"h264", "hevc", "mpeg2video", "mpeg4", "vc1", "vp9"}
}
