package calibrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/probe"
)

func TestParseQPs(t *testing.T) {
	got, err := ParseQPs("18, 22,30")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{18, 22, 30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseQPs() = %v, want %v", got, want)
	}
	for _, input := range []string{"", "18,,22", "0", "52", "abc", "18,18", "1,2,3,4,5,6,7,8,9"} {
		if _, err := ParseQPs(input); err == nil {
			t.Errorf("ParseQPs(%q) unexpectedly succeeded", input)
		}
	}
}

func TestMakeSegmentsCentersAtTenFiftyNinetyPercent(t *testing.T) {
	got, err := makeSegments(100*time.Second, 3, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{5 * time.Second, 45 * time.Second, 85 * time.Second}
	for i := range want {
		if got[i].Start != want[i] {
			t.Errorf("segment %d starts at %s, want %s", i+1, got[i].Start, want[i])
		}
	}
	if _, err := makeSegments(29*time.Second, 3, 10*time.Second); err == nil {
		t.Fatal("expected short source to be rejected")
	}
}

func TestRunCleansLocalScratchAndMeasuresEveryCombination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mkv")
	if err := os.WriteFile(source, []byte("read-only source fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "scratch")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	report, err := Run(context.Background(), Options{
		Runner: runner,
		Prober: fakeProber{info: &probe.MediaInfo{
			Path: source, Duration: 50, Video: probe.Video{Index: 0, Codec: "h264", Width: 1920, Height: 1080},
		}},
		File: source, Device: "/dev/dri/renderD128", QPs: []int{20, 24},
		SegmentCount: 2, SegmentLength: 10 * time.Second, WorkDir: work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Measurements) != 4 {
		t.Fatalf("got %d measurements, want 4", len(report.Measurements))
	}
	if report.Measurements[0].SSIM != 0.987654 || report.Measurements[0].XPSNR != 36.1234 {
		t.Fatalf("unexpected parsed metrics: %+v", report.Measurements[0])
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch artifacts remain: %v", entries)
	}
	contents, err := os.ReadFile(source)
	if err != nil || string(contents) != "read-only source fixture" {
		t.Fatalf("source changed: %q, %v", contents, err)
	}
	if got := len(runner.commands); got != 14 { // 2 references, 4 encodes, and 8 metric passes.
		t.Errorf("ran %d ffmpeg commands, want 14", got)
	}
}

func TestRunRemovesScratchAfterEncodeError(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mkv")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "scratch")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{failOn: 2} // Reference extraction succeeds; VAAPI encode fails.
	_, err := Run(context.Background(), Options{
		Runner: runner,
		Prober: fakeProber{info: &probe.MediaInfo{Duration: 50, Video: probe.Video{Width: 1280, Height: 720}}},
		File:   source, QPs: []int{24}, SegmentCount: 1, SegmentLength: 10 * time.Second, WorkDir: work,
	})
	if err == nil || !strings.Contains(err.Error(), "encode segment") {
		t.Fatalf("Run() error = %v, want encode error", err)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch artifacts remain after failure: %v", entries)
	}
}

func TestEncodeArgsUseVAAPICQPAndVideoOnly(t *testing.T) {
	args := encodeArgs("input.mkv", "output.mkv", "/dev/dri/renderD128", 24)
	joined := strings.Join(args, " ")
	for _, want := range []string{"vaapi=va:/dev/dri/renderD128", "-map 0:v:0", "format=nv12,hwupload", "hevc_vaapi", "-rc_mode CQP", "-qp 24", "-an", "-sn", "output.mkv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("encode args %q missing %q", joined, want)
		}
	}
}

func TestReferenceArgsUseLosslessVideoOnlySegment(t *testing.T) {
	args := referenceArgs("input.mkv", "reference.mkv", 4,
		Segment{Start: 5 * time.Second, Duration: 10 * time.Second})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-map 0:4", "-ss 5.000", "-t 10.000", "-c:v ffv1", "-an", "-sn", "reference.mkv"} {
		if !strings.Contains(joined, want) {
			t.Errorf("reference args %q missing %q", joined, want)
		}
	}
}

type fakeProber struct{ info *probe.MediaInfo }

func (p fakeProber) Probe(context.Context, string) (*probe.MediaInfo, error) { return p.info, nil }

type fakeRunner struct {
	commands [][]string
	failOn   int
}

func (r *fakeRunner) Encoders(context.Context) (map[string]bool, error) {
	return map[string]bool{"hevc_vaapi": true}, nil
}

func (r *fakeRunner) RunCapture(_ context.Context, _ time.Duration, args ...string) (string, error) {
	r.commands = append(r.commands, append([]string(nil), args...))
	if r.failOn > 0 && len(r.commands) == r.failOn {
		return "", errors.New("synthetic ffmpeg failure")
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-filter_complex") {
		if strings.Contains(joined, "ssim") {
			return "[Parsed_ssim_0] SSIM Y:0.99 U:0.99 V:0.99 All:0.987654 (20.0)\n", nil
		}
		return "[Parsed_xpsnr_0] XPSNR y:36.1234 u:39.0 v:40.0 (minimum: 36.1234)\n", nil
	}
	output := args[len(args)-1]
	if err := os.WriteFile(output, []byte("encoded"), 0o600); err != nil {
		return "", err
	}
	return "", nil
}
