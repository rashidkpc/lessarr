package worker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
)

func makeH264Fixture(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(dir, "fixture.mkv")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-t", "6", "-i", "testsrc2=size=640x480:rate=24",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create source fixture: %v: %s", err, out)
	}
	return path
}

func makeMP4Fixture(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	sub := filepath.Join(dir, "captions.srt")
	if err := os.WriteFile(sub, []byte("1\n00:00:00,000 --> 00:00:05,000\nhello\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-t", "6", "-i", "testsrc2=size=640x480:rate=24",
		"-f", "lavfi", "-t", "6", "-i", "sine=frequency=440:sample_rate=48000",
		"-i", sub, "-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-c:s", "mov_text", "-metadata:s:a:0", "language=eng",
		"-metadata:s:s:0", "language=eng", "-t", "6", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create MP4 source fixture: %v: %s", err, out)
	}
	return path
}

func testOptions() Options {
	lib := config.DefaultLibrary()
	lib.Write = false
	lib.MinSizeMB = 0
	lib.MinDuration = 0
	lib.MinLongEdge = 100
	lib.Preset = "ultrafast"
	return Options{
		Runner:  ffmpeg.NewRunner(),
		Prober:  probe.New(),
		Backend: ffmpeg.Software(),
		Library: lib,
		Policy:  decision.Policy{Audio: config.Default().Audio, Subtitles: config.Default().Subtitles},
		Timeout: 2 * time.Minute,
	}
}

func TestTargetContainerPreservesSupportedExtensions(t *testing.T) {
	for _, tc := range []struct {
		path, ext, format string
	}{
		{"movie.mkv", ".mkv", "matroska"},
		{"movie.mp4", ".mp4", "mp4"},
		{"movie.m4v", ".m4v", "mp4"},
		{"movie.mov", ".mov", "mp4"},
	} {
		ext, format, err := TargetContainer(tc.path)
		if err != nil || ext != tc.ext || format != tc.format {
			t.Errorf("targetContainer(%q) = %q,%q,%v", tc.path, ext, format, err)
		}
	}
	if _, _, err := TargetContainer("movie.avi"); err == nil {
		t.Error("in-place extension change from AVI should be refused")
	}
}

func TestOnceObserveOnlyDoesNotModify(t *testing.T) {
	dir := t.TempDir()
	path := makeH264Fixture(t, dir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Once(context.Background(), path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Withheld {
		t.Fatal("write=false should withhold the encode")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("observe-only run modified the source")
	}
}

func TestOnceEncodesValidatesSwapsAndPreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := makeH264Fixture(t, dir)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	o := testOptions()
	o.Force = true
	var progressCount int
	o.Progress = func(ffmpeg.Progress) { progressCount++ }
	result, err := Once(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Withheld || result.Backup == "" {
		t.Fatalf("expected replacement and trash backup, got %+v", result)
	}
	if progressCount == 0 {
		t.Error("expected at least one ffmpeg progress event")
	}

	current, err := probe.New().Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Video.Codec != "hevc" {
		t.Errorf("replacement codec = %q, want hevc", current.Video.Codec)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %04o, want 0640", fi.Mode().Perm())
	}
	if !fi.ModTime().Equal(oldTime) {
		t.Errorf("mtime = %s, want %s", fi.ModTime(), oldTime)
	}
	backup, err := os.ReadFile(result.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, original) {
		t.Fatal("trash backup is not byte-identical to the original")
	}
	if !strings.Contains(result.Backup, filepath.Join(dir, swapDirName())) {
		t.Errorf("backup is outside trash dir: %s", result.Backup)
	}

	// The embedded provenance marker makes repeat processing a hard skip.
	second, err := Once(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision.Action != decision.Skip || !second.Decision.Has(decision.CodeProvenance) {
		t.Errorf("second run should skip tagged output, got %s %v", second.Decision.Action, second.Decision.Codes)
	}
}

func TestCheckSavings(t *testing.T) {
	lib := config.DefaultLibrary()
	lib.MinSavingsPercent = 25
	lib.MinRemuxBytes = 100
	mb := config.MB
	cases := []struct {
		name     string
		before   int64
		after    int64
		d        decision.Decision
		wantErr  bool
		wantSkip bool
	}{
		{"transcode passes", 1000 * mb, 700 * mb, decision.Decision{Action: decision.Transcode}, false, false},
		{"transcode below bar", 1000 * mb, 900 * mb, decision.Decision{Action: decision.Transcode}, true, true},
		{"output grew", 1000 * mb, 1001 * mb, decision.Decision{Action: decision.Transcode}, true, true},
		{"remux passes", 1000 * mb, 800 * mb, decision.Decision{Action: decision.Remux}, false, false},
		{"remux below bar", 1000 * mb, 950 * mb, decision.Decision{Action: decision.Remux}, true, true},
		{"container remux may not shrink", 1000 * mb, 1001 * mb, decision.Decision{
			Action: decision.Remux, Codes: []decision.Code{decision.CodeContainerRemux},
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSavings(tc.before, tc.after, tc.d, lib)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkSavings() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				var skip *SkipError
				var permanent *PermanentError
				if tc.wantSkip && !errors.As(err, &skip) {
					t.Errorf("error should be a skip: %v", err)
				}
				if !tc.wantSkip && !errors.As(err, &permanent) {
					t.Errorf("error should be permanent: %v", err)
				}
			}
		})
	}
}

func TestOnceRejectsOutputBelowSavingsBar(t *testing.T) {
	dir := t.TempDir()
	path := makeH264Fixture(t, dir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	o := testOptions()
	o.Force = true
	o.Library.MinSavingsPercent = 99
	_, err = Once(context.Background(), path, o)
	var skip *SkipError
	if !errors.As(err, &skip) {
		t.Fatalf("expected a policy skip, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed savings check modified the source")
	}
}

func TestOnceMP4KeepsExtensionAndMOVText(t *testing.T) {
	dir := t.TempDir()
	path := makeMP4Fixture(t, dir)
	o := testOptions()
	o.Force = true
	result, err := Once(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Backup == "" {
		t.Fatal("expected original backup")
	}
	if ext := filepath.Ext(path); ext != ".mp4" {
		t.Fatalf("source path extension changed to %s", ext)
	}
	info, err := probe.New().Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Container, "mp4") || info.Video.Codec != "hevc" {
		t.Fatalf("output should remain HEVC MP4, got container=%q codec=%q", info.Container, info.Video.Codec)
	}
	if len(info.Subs) != 1 || info.Subs[0].Codec != "mov_text" {
		t.Fatalf("MP4 text subtitle should remain mov_text, got %+v", info.Subs)
	}
	if tag := info.Tags[ffmpeg.ProvenanceTag]; tag != "1" {
		t.Fatalf("MP4 provenance tag = %q, want 1", tag)
	}
	second, err := Once(context.Background(), path, o)
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision.Action != decision.Skip || !second.Decision.Has(decision.CodeProvenance) {
		t.Errorf("second MP4 run should skip via format tag, got %s %v", second.Decision.Action, second.Decision.Codes)
	}
}

func TestOnceInjectedFailuresPreserveSource(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, stage := range []string{"encode", "validate", "fsync", "rename"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := makeH264Fixture(t, dir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			o := testOptions()
			o.Force = true
			if stage == "encode" {
				t.Setenv("LESSARR_FAIL_AFTER", stage)
			} else {
				o.FailAfter = stage
			}
			if _, err := Once(context.Background(), path, o); err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("expected injected %s failure, got %v", stage, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failure changed original bytes")
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
				t.Errorf("failure left temp/trash artifacts: %v", entries)
			}
		})
	}
}

func swapDirName() string { return ".lessarr-trash" }
