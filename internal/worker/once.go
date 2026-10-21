// Package worker executes one already-decided media job. M1 deliberately has a
// single-file entry point; the watcher and queue arrive later.
package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
	"github.com/rashidkpc/lessarr/internal/swap"
	"github.com/rashidkpc/lessarr/internal/validate"
)

type Options struct {
	Runner         ffmpeg.Runner
	Prober         probe.Prober
	Backend        ffmpeg.Backend
	HardwareDecode bool
	Library        config.Library
	Policy         decision.Policy
	OriginalLang   string
	Force          bool
	Timeout        time.Duration
	Progress       func(ffmpeg.Progress)
	OnInfo         func(*probe.MediaInfo)
	Phase          func(string)
	ValidateStep   func(string)
	DecodeProgress func(ffmpeg.Progress)
	FailAfter      string // test hook; normally set by LESSARR_FAIL_AFTER
}

type Result struct {
	Decision decision.Decision
	Withheld bool
	Source   string
	Output   *probe.MediaInfo
	Backup   string
	Elapsed  time.Duration
}

// PermanentError marks a failure that retrying cannot fix, such as an output
// that misses the library's savings bar.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Once decides and, only when enabled or explicitly forced, processes one file.
// --force bypasses the per-library write=false guard, not safety or policy
// decisions: a Skip remains a Skip.
func Once(ctx context.Context, path string, o Options) (Result, error) {
	started := time.Now()
	if o.Runner.FFmpeg == "" {
		o.Runner = ffmpeg.NewRunner()
	}
	if o.Prober.FFProbe == "" {
		o.Prober = probe.New()
	}
	if o.Timeout <= 0 {
		o.Timeout = 12 * time.Hour
	}

	info, err := o.Prober.Probe(ctx, path)
	if err != nil {
		return Result{}, err
	}
	before, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("stat source: %w", err)
	}
	lstat, err := os.Lstat(path)
	if err != nil {
		return Result{}, fmt.Errorf("lstat source: %w", err)
	}
	if !before.Mode().IsRegular() || !lstat.Mode().IsRegular() {
		return Result{}, fmt.Errorf("source must be a regular file, not a symlink or special file")
	}
	if o.OnInfo != nil {
		o.OnInfo(info)
	}

	d := decision.Decide(info, o.Library, o.Policy, decision.Options{
		Tagged:       tagValue(info.Tags, ffmpeg.ProvenanceTag) != "",
		OriginalLang: o.OriginalLang,
	})
	result := Result{Decision: d, Source: path}
	if d.Action == decision.Skip {
		result.Elapsed = time.Since(started)
		return result, nil
	}
	if !o.Library.Write && !o.Force {
		result.Withheld = true
		result.Elapsed = time.Since(started)
		return result, nil
	}
	if o.Backend.Encoder == "" {
		return result, fmt.Errorf("no encoder backend was selected")
	}
	targetExt, outputFormat, err := TargetContainer(path)
	if err != nil {
		return result, err
	}
	if outputFormat == "mp4" {
		for _, subIndex := range d.SubsKeep {
			for _, sub := range info.Subs {
				if sub.Index == subIndex && sub.Image() {
					return result, fmt.Errorf("MP4 cannot safely retain image subtitles; an MKV extension change is required, so refusing in-place replacement")
				}
			}
		}
	}

	free, err := freeBytes(filepath.Dir(path))
	if err != nil {
		return result, fmt.Errorf("check free space: %w", err)
	}
	need := int64(float64(before.Size()) * o.Library.FreeSpaceFactor)
	if free < need {
		return result, fmt.Errorf("insufficient free space: have %s, require %s",
			humanBytes(free), humanBytes(need))
	}
	if err := swap.Preflight(path); err != nil {
		return result, fmt.Errorf("replacement preflight failed (check write access and hardlinks): %w", err)
	}

	// CreateTemp uses the source directory so the final rename cannot cross a
	// filesystem. Retain the .mkv suffix so FFmpeg selects the Matroska muxer.
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".lessarr-*.tmp"+targetExt)
	if err != nil {
		return result, fmt.Errorf("create same-directory temp output: %w", err)
	}
	temp := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(temp)
		return result, fmt.Errorf("close temp output: %w", err)
	}
	defer os.Remove(temp)

	args, err := ffmpeg.BuildWithHardwareDecodeAndContainer(o.Backend, path, temp, info, d, o.Library, o.HardwareDecode, outputFormat)
	if err != nil {
		return result, err
	}
	if o.Phase != nil {
		o.Phase("encoding")
	}
	if err := o.Runner.Encode(ctx, args, o.Timeout, o.Progress); err != nil {
		return result, err
	}
	if o.Phase != nil {
		o.Phase("validating")
	}
	if err := failAt(o.FailAfter, "encode"); err != nil {
		return result, err
	}
	if err := sourceUnchanged(path, before); err != nil {
		return result, err
	}

	validator := validate.Validator{Prober: o.Prober, Runner: o.Runner}
	output, err := validator.OutputWithProgress(ctx, temp, info, d, o.Timeout, outputFormat, o.Library.Validation, o.ValidateStep, o.DecodeProgress)
	if err != nil {
		return result, err
	}
	result.Output = output
	if err := failAt(o.FailAfter, "validate"); err != nil {
		return result, err
	}
	if err := sourceUnchanged(path, before); err != nil {
		return result, err
	}
	if err := checkSavings(before.Size(), output.Size, d, o.Library); err != nil {
		return result, err
	}

	if err := swap.Prepare(path, temp); err != nil {
		return result, err
	}
	if err := failAt(o.FailAfter, "fsync"); err != nil {
		return result, err
	}
	if err := failAt(o.FailAfter, "rename"); err != nil {
		return result, err
	}
	backup, err := swap.ReplacePrepared(path, temp)
	if err != nil {
		return result, err
	}
	result.Backup = backup
	result.Elapsed = time.Since(started)
	return result, nil
}

// checkSavings refuses a validated output that misses the configured bar. The
// decision engine estimates; this check measures. A miss is permanent because
// re-encoding the same source would miss the same bar.
func checkSavings(before, after int64, d decision.Decision, lib config.Library) error {
	if after <= 0 {
		return &PermanentError{Err: fmt.Errorf("output is empty; refusing to replace")}
	}
	// A container-fix remux is about track/container correctness, not bytes.
	if d.Action == decision.Remux && d.Has(decision.CodeContainerRemux) {
		return nil
	}
	saved := before - after
	if saved < 0 {
		return &PermanentError{Err: fmt.Errorf("output grew by %s; refusing to replace", humanBytes(-saved))}
	}
	switch d.Action {
	case decision.Transcode:
		need := int64(float64(before) * lib.MinSavingsPercent / 100)
		if saved < need {
			return &PermanentError{Err: fmt.Errorf(
				"output saves %s (%.1f%%), below min_savings_percent %.0f%%; refusing to replace",
				humanBytes(saved), float64(saved)/float64(before)*100, lib.MinSavingsPercent)}
		}
	case decision.Remux:
		need := lib.MinRemuxBytes * config.MB
		if saved < need {
			return &PermanentError{Err: fmt.Errorf(
				"output saves %s, below the %d MB remux bar; refusing to replace",
				humanBytes(saved), lib.MinRemuxBytes)}
		}
	}
	return nil
}

// TargetContainer returns an in-place-safe extension/muxer pair. Formats that
// would require renaming the media path are rejected rather than writing a
// Matroska file under an .avi/.ts extension.
func TargetContainer(path string) (extension, format string, err error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv":
		return ".mkv", "matroska", nil
	case ".mp4":
		return ".mp4", "mp4", nil
	case ".m4v":
		return ".m4v", "mp4", nil
	case ".mov":
		return ".mov", "mp4", nil
	default:
		return "", "", fmt.Errorf("in-place once does not support %s: output would need a new extension; use an MKV/MP4 copy until extension-changing swaps are implemented", filepath.Ext(path))
	}
}

func sourceUnchanged(path string, before os.FileInfo) error {
	lstat, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("source disappeared during processing: %w", err)
	}
	if !lstat.Mode().IsRegular() {
		return errors.New("source changed to a symlink or special file during processing")
	}
	after, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("source disappeared during processing: %w", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("source changed during processing; refusing to replace it")
	}
	return nil
}

func failAt(option, stage string) error {
	if option == "" {
		option = os.Getenv("LESSARR_FAIL_AFTER")
	}
	if strings.EqualFold(option, stage) {
		return fmt.Errorf("injected failure after %s", stage)
	}
	return nil
}

func tagValue(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
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
