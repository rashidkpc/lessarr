// Package calibrate measures VAAPI CQP size and quality on short real-media
// samples. Source files are read-only; all generated files live in local scratch
// and are removed before Run returns.
package calibrate

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
)

var DefaultQPs = []int{18, 22, 26, 30}

const (
	defaultSegments       = 3
	defaultSegmentSeconds = 10
	defaultTimeout        = 5 * time.Minute
	maxSegments           = 5
	maxQPs                = 8
)

type Prober interface {
	Probe(context.Context, string) (*probe.MediaInfo, error)
}

type Runner interface {
	Encoders(context.Context) (map[string]bool, error)
	RunCapture(context.Context, time.Duration, ...string) (string, error)
}

type Options struct {
	Runner         Runner
	Prober         Prober
	File           string
	Device         string
	QPs            []int
	SegmentCount   int
	SegmentLength  time.Duration
	WorkDir        string
	CommandTimeout time.Duration
	Progress       func(segment, segmentCount, qp int)
}

type Segment struct {
	Index    int
	Start    time.Duration
	Duration time.Duration
}

type Measurement struct {
	Segment       Segment
	QP            int
	Bytes         int64
	SSIM          float64
	XPSNR         float64
	EncodeElapsed time.Duration
}

type Report struct {
	File           string
	Device         string
	Width          int
	Height         int
	SourceDuration time.Duration
	Segments       []Segment
	QPs            []int
	Measurements   []Measurement
	XPSNRAvailable bool
}

var (
	ssimValue  = regexp.MustCompile(`\bAll:([0-9]+(?:\.[0-9]+)?)`)
	xpsnrValue = regexp.MustCompile(`\(minimum:\s*([0-9]+(?:\.[0-9]+)?)\)`)
)

// ParseQPs parses a comma-delimited list of VAAPI CQP values (1..51).
func ParseQPs(value string) ([]int, error) {
	var qps []int
	seen := map[int]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("empty QP in %q", value)
		}
		qp, err := strconv.Atoi(item)
		if err != nil || qp < 1 || qp > 51 {
			return nil, fmt.Errorf("QP %q must be an integer from 1 to 51", item)
		}
		if seen[qp] {
			return nil, fmt.Errorf("duplicate QP %d", qp)
		}
		seen[qp] = true
		qps = append(qps, qp)
	}
	if len(qps) == 0 || len(qps) > maxQPs {
		return nil, fmt.Errorf("choose between 1 and %d QPs", maxQPs)
	}
	return qps, nil
}

// Run samples a single progressive SDR source at evenly spaced points,
// encodes each sample at each requested VAAPI QP, measures SSIM/XPSNR, and
// removes all scratch files on every return path.
func Run(ctx context.Context, o Options) (Report, error) {
	var report Report
	if o.File == "" {
		return report, fmt.Errorf("source file is required")
	}
	if o.Device == "" {
		o.Device = "/dev/dri/renderD128"
	}
	if len(o.QPs) == 0 {
		o.QPs = append([]int(nil), DefaultQPs...)
	}
	if _, err := ParseQPs(intsToString(o.QPs)); err != nil {
		return report, err
	}
	if o.SegmentCount == 0 {
		o.SegmentCount = defaultSegments
	}
	if o.SegmentCount < 1 || o.SegmentCount > maxSegments {
		return report, fmt.Errorf("segment count must be between 1 and %d", maxSegments)
	}
	if o.SegmentLength == 0 {
		o.SegmentLength = defaultSegmentSeconds * time.Second
	}
	if o.SegmentLength < time.Second || o.SegmentLength > time.Minute {
		return report, fmt.Errorf("segment length must be between 1s and 60s")
	}
	if o.CommandTimeout <= 0 {
		o.CommandTimeout = defaultTimeout
	}
	if o.WorkDir == "" {
		o.WorkDir = os.TempDir()
	}
	workInfo, err := os.Stat(o.WorkDir)
	if err != nil {
		return report, fmt.Errorf("scratch directory %s: %w", o.WorkDir, err)
	}
	if !workInfo.IsDir() {
		return report, fmt.Errorf("scratch path %s is not a directory", o.WorkDir)
	}
	if remote, fsType, err := isRemoteFilesystem(o.WorkDir); err != nil {
		return report, fmt.Errorf("inspect scratch filesystem: %w", err)
	} else if remote {
		return report, fmt.Errorf("scratch directory %s is on a %s filesystem; choose local scratch", o.WorkDir, fsType)
	}
	if fi, err := os.Stat(o.File); err != nil {
		return report, fmt.Errorf("source file: %w", err)
	} else if !fi.Mode().IsRegular() {
		return report, fmt.Errorf("source must be a regular file")
	}

	runner := o.Runner
	if runner == nil {
		runner = ffmpeg.NewRunner()
	}
	prober := o.Prober
	if prober == nil {
		prober = probe.New()
	}
	info, err := prober.Probe(ctx, o.File)
	if err != nil {
		return report, err
	}
	if info.Video.HDR.Any() {
		return report, fmt.Errorf("HDR/DoVi sources are excluded from calibration; choose an SDR source")
	}
	if info.Video.Interlaced() {
		return report, fmt.Errorf("interlaced sources are not supported by calibration yet; choose progressive video")
	}
	if info.Video.Width <= 0 || info.Video.Height <= 0 || info.Video.Width%2 != 0 || info.Video.Height%2 != 0 {
		return report, fmt.Errorf("source dimensions must be positive and even for VAAPI 4:2:0 encoding")
	}
	if info.Duration <= 0 {
		return report, fmt.Errorf("source duration is unknown")
	}
	encoders, err := runner.Encoders(ctx)
	if err != nil {
		return report, fmt.Errorf("list FFmpeg encoders: %w", err)
	}
	if !encoders["hevc_vaapi"] {
		return report, fmt.Errorf("FFmpeg does not provide hevc_vaapi")
	}

	sourceDuration := time.Duration(info.Duration * float64(time.Second))
	segments, err := makeSegments(sourceDuration, o.SegmentCount, o.SegmentLength)
	if err != nil {
		return report, err
	}
	work, err := os.MkdirTemp(o.WorkDir, "lessarr-calibrate-")
	if err != nil {
		return report, fmt.Errorf("create local calibration scratch: %w", err)
	}
	defer os.RemoveAll(work)

	report = Report{
		File:           o.File,
		Device:         o.Device,
		Width:          info.Video.Width,
		Height:         info.Video.Height,
		SourceDuration: sourceDuration,
		Segments:       segments,
		QPs:            append([]int(nil), o.QPs...),
		XPSNRAvailable: true,
	}
	for _, segment := range segments {
		reference := filepath.Join(work, fmt.Sprintf("segment-%02d-reference.mkv", segment.Index))
		if _, err := runner.RunCapture(ctx, o.CommandTimeout, referenceArgs(o.File, reference, info.Video.Index, segment)...); err != nil {
			return Report{}, fmt.Errorf("extract reference segment %d: %w", segment.Index, err)
		}
		for _, qp := range o.QPs {
			if o.Progress != nil {
				o.Progress(segment.Index, len(segments), qp)
			}
			output := filepath.Join(work, fmt.Sprintf("segment-%02d-qp-%02d.mkv", segment.Index, qp))
			started := time.Now()
			if _, err := runner.RunCapture(ctx, o.CommandTimeout, encodeArgs(reference, output, o.Device, qp)...); err != nil {
				return Report{}, fmt.Errorf("encode segment %d at QP %d: %w", segment.Index, qp, err)
			}
			elapsed := time.Since(started)
			fi, err := os.Stat(output)
			if err != nil {
				return Report{}, fmt.Errorf("stat QP %d output: %w", qp, err)
			}
			if fi.Size() <= 0 {
				return Report{}, fmt.Errorf("QP %d produced an empty output", qp)
			}
			ssim, err := measure(ctx, runner, "ssim", reference, output, o.CommandTimeout)
			if err != nil {
				return Report{}, fmt.Errorf("measure SSIM for segment %d at QP %d: %w", segment.Index, qp, err)
			}
			var xpsnr float64
			if report.XPSNRAvailable {
				xpsnr, err = measure(ctx, runner, "xpsnr", reference, output, o.CommandTimeout)
				if err != nil {
					if strings.Contains(err.Error(), "No such filter: 'xpsnr'") {
						report.XPSNRAvailable = false
					} else {
						return Report{}, fmt.Errorf("measure XPSNR for segment %d at QP %d: %w", segment.Index, qp, err)
					}
				}
			}
			report.Measurements = append(report.Measurements, Measurement{
				Segment: segment, QP: qp, Bytes: fi.Size(), SSIM: ssim, XPSNR: xpsnr, EncodeElapsed: elapsed,
			})
			if err := os.Remove(output); err != nil {
				return Report{}, fmt.Errorf("remove temporary QP %d encode: %w", qp, err)
			}
		}
		if err := os.Remove(reference); err != nil {
			return Report{}, fmt.Errorf("remove temporary reference segment: %w", err)
		}
	}
	return report, nil
}

func makeSegments(total time.Duration, count int, length time.Duration) ([]Segment, error) {
	if total < time.Duration(count)*length {
		return nil, fmt.Errorf("source duration %s is shorter than %d × %s calibration segments", total.Round(time.Second), count, length)
	}
	segments := make([]Segment, count)
	for i := range segments {
		fraction := 0.5
		if count > 1 {
			fraction = 0.1 + 0.8*float64(i)/float64(count-1)
		}
		start := time.Duration(fraction*float64(total)) - length/2
		if start < 0 {
			start = 0
		}
		if latest := total - length; start > latest {
			start = latest
		}
		segments[i] = Segment{Index: i + 1, Start: start, Duration: length}
	}
	return segments, nil
}

func referenceArgs(source, output string, videoIndex int, segment Segment) []string {
	return []string{
		"-hide_banner", "-nostdin", "-y",
		"-ss", seconds(segment.Start), "-i", source,
		"-map", fmt.Sprintf("0:%d", videoIndex),
		"-t", seconds(segment.Duration),
		"-an", "-sn", "-c:v", "ffv1", "-level", "3",
		"-fps_mode", "passthrough", "-map_metadata", "-1",
		"-f", "matroska", output,
	}
}

func encodeArgs(source, output, device string, qp int) []string {
	return []string{
		"-hide_banner", "-nostdin", "-y",
		"-init_hw_device", "vaapi=va:" + device,
		"-filter_hw_device", "va",
		"-i", source,
		"-map", "0:v:0",
		"-vf", "format=nv12,hwupload",
		"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", strconv.Itoa(qp),
		"-fps_mode", "passthrough", "-an", "-sn", "-map_metadata", "-1",
		"-f", "matroska", output,
	}
}

func measure(ctx context.Context, runner Runner, metric, reference, encoded string, timeout time.Duration) (float64, error) {
	if metric != "ssim" && metric != "xpsnr" {
		return 0, fmt.Errorf("unsupported metric %q", metric)
	}
	graph := fmt.Sprintf("[0:v:0]format=yuv420p[ref];[1:v:0]format=yuv420p[dist];[ref][dist]%s[metric]", metric)
	log, err := runner.RunCapture(ctx, timeout,
		"-hide_banner", "-nostdin", "-v", "info", "-nostats",
		"-i", reference, "-i", encoded, "-filter_complex", graph,
		"-map", "[metric]", "-an", "-sn", "-f", "null", "-",
	)
	if err != nil {
		return 0, err
	}
	pattern := ssimValue
	if metric == "xpsnr" {
		pattern = xpsnrValue
	}
	matches := pattern.FindStringSubmatch(log)
	if len(matches) != 2 {
		return 0, fmt.Errorf("FFmpeg %s output did not contain a final metric", strings.ToUpper(metric))
	}
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid %s value %q", strings.ToUpper(metric), matches[1])
	}
	return value, nil
}

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', 3, 64) }

func intsToString(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}
