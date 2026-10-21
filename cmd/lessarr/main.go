// Command lessarr decides what media needs re-encoding and (later) does it.
//
// M0 ships only the read-only commands: probe, stats and config-validate.
// Nothing here writes to a media file.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rashidkpc/lessarr/internal/analysis"
	"github.com/rashidkpc/lessarr/internal/calibrate"
	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/daemon"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
	"github.com/rashidkpc/lessarr/internal/queue"
	"github.com/rashidkpc/lessarr/internal/scan"
	"github.com/rashidkpc/lessarr/internal/selftest"
	"github.com/rashidkpc/lessarr/internal/trash"
	"github.com/rashidkpc/lessarr/internal/worker"
)

var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	var rev string
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			rev = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if rev == "" {
		return
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		rev += "-dirty"
	}
	version = rev
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	ctx := context.Background()
	switch args[0] {
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "lessarr %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "config-validate":
		return cmdConfigValidate(args[1:], stdout, stderr)
	case "run":
		return cmdRun(ctx, args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "pause":
		return cmdPause(args[1:], stdout, stderr, true)
	case "resume":
		return cmdPause(args[1:], stdout, stderr, false)
	case "analyze":
		return cmdAnalyze(ctx, args[1:], stdout, stderr)
	case "trash":
		return cmdTrash(args[1:], stdout, stderr)
	case "selftest":
		return cmdSelftest(ctx, args[1:], stdout, stderr)
	case "once":
		return cmdOnce(ctx, args[1:], stdout, stderr)
	case "calibrate":
		return cmdCalibrate(ctx, args[1:], stdout, stderr)
	case "probe":
		return cmdProbe(ctx, args[1:], stdout, stderr)
	case "stats":
		return cmdStats(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lessarr: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `lessarr — decide what media needs re-encoding

Usage:
  lessarr run [--config FILE] [--once]
  lessarr status [--config FILE]
  lessarr pause|resume [--config FILE]
  lessarr analyze [--config FILE] <job-id>
  lessarr trash stats|clean [--config FILE]
  lessarr probe [--config FILE] [--accurate] [--backend NAME] [--json] <file>
  lessarr stats [--config FILE] [--top N] [--workers N] [--reasons] <path>
  lessarr selftest [--config FILE] [--device PATH] [--seconds N] [path]
  lessarr once [--config FILE] [--backend auto|vaapi|qsv|software] [--force] <file>
  lessarr calibrate [--config FILE] [--device PATH] [--qps LIST] [--segments N] [--segment-seconds N] [--work-dir DIR] <file>
  lessarr config-validate [--config FILE]
  lessarr version

run      scan libraries, queue work, encode, and serve the status page.
status   print queue and library counters from the database.
pause    stop claiming new jobs; resume restarts them.
analyze  measure a completed job's video/audio/subtitle savings and sampled SSIM.
trash    report or prune retained originals under the configured retention and size cap.
probe    explain what lessarr would do with one file, and why. Writes nothing.
stats    walk a path, decide every file, report the projected outcome.
selftest check this host: GPU, drivers, encoders, permissions. Writes nothing
         except a temporary file it removes again.
once     decide, encode/remux, validate, and atomically replace one file.
calibrate offline VAAPI CQP sweep; reads the source and writes only temporary local samples.
config   load and validate a config file.

Scanning is polling-based because inotify does not observe changes made over
CIFS. write=false still records every decision and shows it on the dashboard,
but withholds replacement.
`)
}

func cmdConfigValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config-validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: ok — %d library/libraries, backend %s, decide_by %s\n",
		path, len(cfg.Libraries), cfg.Encoder.Backend, cfg.Encoder.DecideBy)
	for _, l := range cfg.Libraries {
		state := "observe only"
		if l.Write {
			state = "write enabled"
		}
		fmt.Fprintf(stdout, "  %-12s %-22s %-10s %s\n", l.Name, l.Path, l.Mode, state)
	}
	return 0
}

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	once := fs.Bool("once", false, "scan once and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	store, err := queue.Open(cfg.Server.DB)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	defer store.Close()

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "lessarr %s: %d libraries, database %s\n", version, len(cfg.Libraries), cfg.Server.DB)
	if err := daemon.Run(runCtx, daemon.Options{
		Config: cfg, Store: store, Version: version, Log: stderr, Once: *once,
	}); err != nil {
		fmt.Fprintln(stderr, "lessarr run:", err)
		return 1
	}
	return 0
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	store, err := queue.Open(cfg.Server.DB)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	defer store.Close()
	paused, err := store.Paused()
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	counts, err := store.Counts()
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	files, err := store.FilesSummary()
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	fmt.Fprintf(stdout, "database: %s\n", cfg.Server.DB)
	fmt.Fprintf(stdout, "paused: %s\n", yesNo(paused))
	fmt.Fprintf(stdout, "queue: pending=%d running=%d done=%d failed=%d skipped=%d withheld=%d\n",
		counts.Pending, counts.Running, counts.Done, counts.Failed, counts.Skipped, counts.Withheld)
	fmt.Fprintf(stdout, "reclaimed: %s\n", humanBytes(counts.Saved))
	if current, err := store.Current(); err == nil && current != nil {
		fmt.Fprintf(stdout, "current: %s (%s, started %s)\n", current.Path, current.Action, humanAgo(current.StartedAt))
	}
	fmt.Fprintln(stdout, "libraries:")
	for _, lib := range cfg.Libraries {
		state := "observe"
		if lib.Write {
			state = "write"
		}
		sum := files[lib.Name]
		fmt.Fprintf(stdout, "  %-14s %-8s %6d files  %10s  %s\n", lib.Name, state, sum.Files, humanBytes(sum.Bytes), actionCounts(sum.Actions))
	}
	recent, err := store.Recent(8)
	if err == nil && len(recent) > 0 {
		fmt.Fprintln(stdout, "recent:")
		for _, job := range recent {
			fmt.Fprintf(stdout, "  %-8s %-10s %s\n", job.StateName(), humanBytes(job.SizeBefore), filepath.Base(job.Path))
		}
	}
	return 0
}

func cmdPause(args []string, stdout, stderr io.Writer, paused bool) int {
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	store, err := queue.Open(cfg.Server.DB)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	defer store.Close()
	if err := store.SetPaused(paused); err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	if paused {
		fmt.Fprintln(stdout, "paused: no new jobs will be claimed")
	} else {
		fmt.Fprintln(stdout, "resumed: the worker will claim queued jobs")
	}
	return 0
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

func actionCounts(actions map[string]int) string {
	if len(actions) == 0 {
		return ""
	}
	keys := make([]string, 0, len(actions))
	for k := range actions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, actions[k]))
	}
	return strings.Join(parts, " ")
}

func cmdAnalyze(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "lessarr analyze: exactly one job ID is required")
		return 2
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(stderr, "lessarr analyze: invalid job ID")
		return 2
	}
	path, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	store, err := queue.Open(cfg.Server.DB)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	defer store.Close()
	job, err := store.Job(id)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	if job == nil {
		fmt.Fprintf(stderr, "lessarr analyze: job %d not found\n", id)
		return 1
	}
	if job.Backup == "" {
		fmt.Fprintln(stderr, "lessarr analyze: this job has no retained original")
		return 1
	}
	if _, err := os.Stat(job.Backup); err != nil {
		fmt.Fprintf(stderr, "lessarr analyze: original is no longer available: %v\n", err)
		return 1
	}
	p := probe.New()
	sourceInfo, err := p.Probe(ctx, job.Backup)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	outputInfo, err := p.Probe(ctx, job.Path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	result, err := analysis.Compare(actx, p, ffmpeg.NewRunner(), job.Backup, job.Path, sourceInfo, outputInfo)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr analyze:", err)
		return 1
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "lessarr analyze:", err)
		return 1
	}
	if err := store.SaveAnalysis(id, string(data)); err != nil {
		fmt.Fprintln(stderr, "lessarr analyze: save:", err)
		return 1
	}
	fmt.Fprintf(stdout, "job %d: %s\n\n", id, job.Path)
	fmt.Fprintf(stdout, "%-14s %12s %12s %12s\n", "part", "before", "after", "saved")
	printAnalysisRow(stdout, "video", result.Video)
	printAnalysisRow(stdout, "audio", result.Audio)
	printAnalysisRow(stdout, "subtitles", result.Subtitles)
	printAnalysisRow(stdout, "container/other", result.Other)
	printAnalysisRow(stdout, "total", result.Total)
	for _, sample := range result.SSIM {
		fmt.Fprintf(stdout, "ssim at %3.0fs: %.4f\n", sample.AtSeconds, sample.SSIM)
	}
	if result.SSIMError != "" {
		fmt.Fprintln(stdout, "ssim warning:", result.SSIMError)
	}
	return 0
}

func printAnalysisRow(w io.Writer, label string, totals analysis.Totals) {
	fmt.Fprintf(w, "%-14s %12s %12s %12s\n", label,
		humanBytes(totals.Before), humanBytes(totals.After), signedHumanBytes(totals.Saved))
}

func signedHumanBytes(n int64) string {
	if n < 0 {
		return "-" + humanBytes(-n)
	}
	return humanBytes(n)
}

func cmdTrash(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "stats" && args[0] != "clean") {
		fmt.Fprintln(stderr, "usage: lessarr trash stats|clean [--config FILE]")
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("trash "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	path, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	store, err := queue.Open(cfg.Server.DB)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	defer store.Close()

	if sub == "stats" {
		stats, err := trash.Summary(store)
		if err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
		fmt.Fprintf(stdout, "trash: %d files, %s\n", stats.Files, humanBytes(stats.Bytes))
		fmt.Fprintf(stdout, "policy: retention %s, cap %d GB\n", cfg.Trash.Retention.Std(), cfg.Trash.MaxSizeGB)
		return 0
	}
	stats, err := trash.Prune(context.Background(), store, cfg.Trash, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted: %d files, freed %s\n", stats.DeletedFiles, humanBytes(stats.Freed))
	fmt.Fprintf(stdout, "remaining: %d files, %s\n", stats.Files, humanBytes(stats.Bytes))
	return 0
}

func cmdProbe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	asJSON := fs.Bool("json", false, "emit JSON instead of text")
	accurate := fs.Bool("accurate", false, "measure video bitrate from real packets")
	backend := fs.String("backend", "", "override encoder backend (vaapi|qsv|software)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "lessarr probe: exactly one file is required")
		return 2
	}
	path := fs.Arg(0)

	cfg, lib, pol := resolveConfig(*cfgPath, path, stderr)
	if cfg == nil && *cfgPath != "" {
		return 1
	}

	p := probe.New()
	info, err := p.Probe(ctx, path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	if *accurate {
		if br, err := p.SampleVideoBitrate(ctx, path, info.Duration); err == nil {
			info.VideoBitrate = br
			info.BitrateSampled = true
		} else {
			fmt.Fprintf(stderr, "lessarr: warning: bitrate sampling failed: %v\n", err)
		}
	}

	tagged := info.Tags[ffmpeg.ProvenanceTag] != ""
	opts := decision.Options{
		Tagged:       tagged,
		OriginalLang: probe.OriginalLanguageFromFilename(path),
	}
	d := decision.Decide(info, lib, pol, opts)

	b, err := ffmpeg.ByName(backendFor(*backend, cfg), deviceFor(cfg))
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	var argv []string
	if d.Action != decision.Skip {
		tempExt, outputFormat, err := worker.TargetContainer(path)
		if err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
		selection, err := ffmpeg.Select(ctx, backendFor(*backend, cfg), deviceFor(cfg), info.Video.Codec, ffmpeg.NewRunner())
		if err != nil {
			fmt.Fprintln(stderr, "lessarr: select backend:", err)
			return 1
		}
		b = selection.Backend
		temp := strings.TrimSuffix(path, filepath.Ext(path)) + ".lessarr.tmp" + tempExt
		argv, err = ffmpeg.BuildWithHardwareDecodeAndContainer(b, path, temp, info, d, lib, selection.HardwareDecode, outputFormat)
		if err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
	}

	if *asJSON {
		out := probeJSON{Info: info, Decision: d, Argv: argv, Backend: b.Name}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
		return 0
	}

	printProbe(stdout, info, lib, d, b.Name, argv)
	return 0
}

func cmdOnce(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	backendName := fs.String("backend", "", "override encoder backend")
	force := fs.Bool("force", false, "explicitly allow writing when library write=false")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "lessarr once: exactly one file is required")
		return 2
	}
	path := fs.Arg(0)
	resolved, err := config.Find(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	lib, ok := cfg.LibraryFor(path)
	if !ok && len(cfg.Libraries) == 1 {
		lib, ok = cfg.Libraries[0], true
	}
	if !ok {
		fmt.Fprintf(stderr, "lessarr once: %s is outside configured libraries\n", path)
		return 1
	}
	policy := decision.Policy{Audio: cfg.Audio, Subtitles: cfg.Subtitles}
	p := probe.New()
	info, err := p.Probe(ctx, path)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	d := decision.Decide(info, lib, policy, decision.Options{
		Tagged:       info.Tags[ffmpeg.ProvenanceTag] != "",
		OriginalLang: probe.OriginalLanguageFromFilename(path),
	})
	fmt.Fprintf(stdout, "%s\n", d.String())
	if d.Action == decision.Skip {
		return 0
	}
	if !lib.Write && !*force {
		fmt.Fprintln(stdout, "observe only: library write=false; no file was modified")
		return 0
	}
	if *force && !lib.Write {
		fmt.Fprintln(stderr, "WARNING: --force overrides write=false for this one file")
	}

	requestedBackend := cfg.Encoder.Backend
	if *backendName != "" {
		requestedBackend = *backendName
	}
	selection, err := ffmpeg.Select(ctx, requestedBackend, cfg.Encoder.Device, info.Video.Codec, ffmpeg.NewRunner())
	if err != nil {
		fmt.Fprintln(stderr, "lessarr: select backend:", err)
		return 1
	}
	fmt.Fprintf(stdout, "backend: %s (%s)\n", selection.Backend.Name, selection.Reason)
	result, err := worker.Once(ctx, path, worker.Options{
		Runner:         ffmpeg.NewRunner(),
		Prober:         p,
		Backend:        selection.Backend,
		HardwareDecode: selection.HardwareDecode,
		Library:        lib,
		Policy:         policy,
		OriginalLang:   probe.OriginalLanguageFromFilename(path),
		Force:          *force,
		Progress: func(progress ffmpeg.Progress) {
			fmt.Fprintf(stderr, "\r  frame=%d time=%s speed=%s", progress.Frame, progress.OutTime.Round(time.Second), progress.Speed)
		},
		Phase: func(phase string) {
			fmt.Fprintf(stderr, "\r  phase: %-16s", phase)
		},
		ValidateStep: func(step string) {
			fmt.Fprintf(stderr, "\r  validate: %-24s", step)
		},
		DecodeProgress: func(progress ffmpeg.Progress) {
			fmt.Fprintf(stderr, "\r  decode: frame=%d time=%s speed=%s", progress.Frame, progress.OutTime.Round(time.Second), progress.Speed)
		},
	})
	fmt.Fprintln(stderr)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}
	if result.Withheld {
		fmt.Fprintln(stdout, "observe only: library write=false; no file was modified")
		return 0
	}
	if result.Backup != "" {
		fmt.Fprintf(stdout, "replaced: %s\noriginal: %s\nelapsed: %s\n",
			path, result.Backup, result.Elapsed.Round(time.Second))
	}
	return 0
}

func cmdCalibrate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file (uses encoder.device)")
	device := fs.String("device", "", "VAAPI render device")
	qpsFlag := fs.String("qps", "18,22,26,30", "VAAPI CQP values, comma-separated")
	segments := fs.Int("segments", 3, "number of evenly spaced source segments (1..5)")
	segmentSeconds := fs.Int("segment-seconds", 10, "duration of each segment (1..60 seconds)")
	workDir := fs.String("work-dir", "", "local scratch directory (default: system temp directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "lessarr calibrate: exactly one source file is required")
		return 2
	}
	qps, err := calibrate.ParseQPs(*qpsFlag)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr calibrate:", err)
		return 2
	}
	selectedDevice := *device
	if *cfgPath != "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
		if selectedDevice == "" {
			selectedDevice = cfg.Encoder.Device
		}
	}
	if *segmentSeconds < 1 || *segmentSeconds > 60 {
		fmt.Fprintln(stderr, "lessarr calibrate: segment-seconds must be between 1 and 60")
		return 2
	}
	if *segments < 1 || *segments > 5 {
		fmt.Fprintln(stderr, "lessarr calibrate: segments must be between 1 and 5")
		return 2
	}

	report, err := calibrate.Run(ctx, calibrate.Options{
		File:          fs.Arg(0),
		Device:        selectedDevice,
		QPs:           qps,
		SegmentCount:  *segments,
		SegmentLength: time.Duration(*segmentSeconds) * time.Second,
		WorkDir:       *workDir,
		Progress: func(segment, count, qp int) {
			fmt.Fprintf(stderr, "\rcalibrating segment %d/%d at QP %d...", segment, count, qp)
		},
	})
	fmt.Fprintln(stderr)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr calibrate:", err)
		return 1
	}
	printCalibration(stdout, report)
	return 0
}

func printCalibration(w io.Writer, report calibrate.Report) {
	fmt.Fprintf(w, "source:    %s\n", report.File)
	fmt.Fprintf(w, "video:     %dx%d, %s\n", report.Width, report.Height, humanDuration(report.SourceDuration.Seconds()))
	fmt.Fprintf(w, "encoder:   hevc_vaapi CQP, device %s\n", report.Device)
	fmt.Fprintf(w, "segments:  %d × %s\n\n", len(report.Segments), report.Segments[0].Duration)
	fmt.Fprintf(w, "%-7s %9s %-5s %12s %10s %11s %10s\n", "segment", "start", "QP", "size", "SSIM", "XPSNR", "encode")
	for _, result := range report.Measurements {
		fmt.Fprintf(w, "%-7d %9s %-5d %12s %10.6f %11s %10s\n",
			result.Segment.Index, humanDuration(result.Segment.Start.Seconds()), result.QP, humanBytes(result.Bytes), result.SSIM, xpsnrLabel(report.XPSNRAvailable, result.XPSNR), result.EncodeElapsed.Round(time.Millisecond))
	}
	fmt.Fprintln(w, "\nQP summary (mean quality across segments; total is sampled video bytes)")
	fmt.Fprintf(w, "%-5s %14s %12s %12s\n", "QP", "total size", "mean SSIM", "mean XPSNR")
	for _, qp := range report.QPs {
		var total int64
		var ssim, xpsnr float64
		count := 0
		for _, result := range report.Measurements {
			if result.QP != qp {
				continue
			}
			total += result.Bytes
			ssim += result.SSIM
			xpsnr += result.XPSNR
			count++
		}
		if count == 0 {
			continue
		}
		xpsnrText := "n/a"
		if report.XPSNRAvailable {
			xpsnrText = fmt.Sprintf("%.2f dB", xpsnr/float64(count))
		}
		fmt.Fprintf(w, "%-5d %14s %12.6f %12s\n", qp, humanBytes(total), ssim/float64(count), xpsnrText)
	}
	if !report.XPSNRAvailable {
		fmt.Fprintln(w, "\nXPSNR is unavailable in this FFmpeg build; SSIM is reported alone.")
	}
	fmt.Fprintln(w, "Temporary encodes were deleted; the source was not modified.")
}

func xpsnrLabel(available bool, value float64) string {
	if !available {
		return "n/a"
	}
	return fmt.Sprintf("%.2f dB", value)
}

func resolveConfig(cfgPath, path string, stderr io.Writer) (*config.Config, config.Library, decision.Policy) {
	def := config.Default()
	pol := decision.Policy{Audio: def.Audio, Subtitles: def.Subtitles}
	lib := config.DefaultLibrary()
	if cfgPath == "" {
		return nil, lib, pol
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return nil, lib, pol
	}
	pol = decision.Policy{Audio: cfg.Audio, Subtitles: cfg.Subtitles}
	if l, ok := cfg.LibraryFor(path); ok {
		lib = l
	} else if len(cfg.Libraries) == 1 {
		// Probing a file outside any configured library is common during
		// setup; with a single library its policy is the obvious intent.
		lib = cfg.Libraries[0]
	}
	return cfg, lib, pol
}

func backendFor(flagValue string, cfg *config.Config) string {
	if flagValue != "" {
		return flagValue
	}
	if cfg != nil {
		return cfg.Encoder.Backend
	}
	return "software"
}

func deviceFor(cfg *config.Config) string {
	if cfg != nil {
		return cfg.Encoder.Device
	}
	return "/dev/dri/renderD128"
}

func cmdSelftest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	device := fs.String("device", "", "render device, e.g. /dev/dri/renderD128")
	seconds := fs.Int("seconds", 3, "length of each trial encode")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := selftest.Options{TrialSeconds: *seconds}
	if *cfgPath != "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
		opts.Device = cfg.Encoder.Device
		switch cfg.Encoder.Backend {
		case "vaapi":
			opts.Backends = []string{"vaapi"}
		case "qsv":
			opts.Backends = []string{"qsv"}
		case "software":
			opts.Backends = []string{"software"}
		}
		if fs.NArg() == 0 && len(cfg.Libraries) > 0 {
			opts.MediaPath = cfg.Libraries[0].Path
		}
	}
	if *device != "" {
		opts.Device = *device
	}
	if fs.NArg() > 0 {
		opts.MediaPath = fs.Arg(0)
	}
	if opts.Device == "" {
		opts.Device = "/dev/dri/renderD128"
	}

	rep := selftest.Run(ctx, opts)
	printReport(stdout, rep)
	if rep.Failures() > 0 {
		return 1
	}
	return 0
}

func printReport(w io.Writer, rep selftest.Report) {
	for _, c := range rep.Checks {
		fmt.Fprintf(w, "%-4s %-17s %s\n", c.Status, c.Name, c.Detail)
		for _, extra := range c.Extra {
			fmt.Fprintf(w, "     %-17s   %s\n", "", extra)
		}
	}
	fmt.Fprintf(w, "\n%d failure(s), %d warning(s)\n", rep.Failures(), rep.Warnings())
}

type probeJSON struct {
	Info     *probe.MediaInfo  `json:"media"`
	Decision decision.Decision `json:"decision"`
	Backend  string            `json:"backend"`
	Argv     []string          `json:"argv,omitempty"`
}

func printProbe(w io.Writer, info *probe.MediaInfo, lib config.Library, d decision.Decision, backend string, argv []string) {
	fmt.Fprintf(w, "path:       %s\n", info.Path)
	fmt.Fprintf(w, "size:       %s\n", humanBytes(info.Size))
	fmt.Fprintf(w, "duration:   %s\n", humanDuration(info.Duration))
	fmt.Fprintf(w, "container:  %s\n", info.Container)

	fmt.Fprintf(w, "video:      %s %dx%d %.3gfps %d-bit bpp=%.3f %s",
		codec(info.Video.Codec), info.Video.Width, info.Video.Height, info.Video.FPS,
		info.Video.BitDepth, info.BPP(), hdrLabel(info.Video.HDR))
	if info.Video.EncoderTag != "" {
		fmt.Fprintf(w, " encoder=%q", info.Video.EncoderTag)
	}
	if info.Video.Interlaced() {
		fmt.Fprint(w, " interlaced")
	}
	if info.BitrateSampled {
		fmt.Fprint(w, " (sampled bitrate)")
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "audio:\n")
	if len(info.Audio) == 0 {
		fmt.Fprint(w, "  (none)\n")
	}
	for _, a := range info.Audio {
		flags := []string{}
		if a.Default {
			flags = append(flags, "default")
		}
		if a.Forced {
			flags = append(flags, "forced")
		}
		if a.Lossless() {
			flags = append(flags, "lossless")
		}
		fmt.Fprintf(w, "  #%d %s %s %s %s\n", a.Index, lang(a.Language), a.Codec,
			channels(a.Channels), strings.Join(flags, ","))
	}
	fmt.Fprintf(w, "subtitles:\n")
	if len(info.Subs) == 0 {
		fmt.Fprint(w, "  (none)\n")
	}
	for _, s := range info.Subs {
		flags := []string{}
		if s.Forced {
			flags = append(flags, "forced")
		}
		if s.Image() {
			flags = append(flags, "image")
		}
		fmt.Fprintf(w, "  #%d %s %s %s\n", s.Index, lang(s.Language), s.Codec, strings.Join(flags, ","))
	}

	fmt.Fprintf(w, "\ndecision:   %s\n", d.String())
	if d.Action != decision.Skip {
		if d.VideoSavingsUnmeasured {
			fmt.Fprintf(w, "projection: video savings unmeasured; audio-only estimate %s\n", humanBytes(d.EstimatedOutputBytes))
		} else {
			fmt.Fprintf(w, "projected:  %s\n", humanBytes(d.EstimatedOutputBytes))
		}
		fmt.Fprintf(w, "backend:    %s\n", backend)
		if d.ScaleTo != nil {
			fmt.Fprintf(w, "scale:      %dx%d\n", d.ScaleTo.W, d.ScaleTo.H)
		}
		if d.Deinterlace {
			fmt.Fprintf(w, "deinterlace: yes\n")
		}
		fmt.Fprintf(w, "command:    ffmpeg %s\n", strings.Join(argv, " "))
	}
	fmt.Fprintf(w, "policy:     icq=%d preset=%s min_savings=%.0f%% min_remux=%dMB min_size=%dMB min_duration=%s\n",
		lib.ICQ, lib.Preset, lib.MinSavingsPercent, lib.MinRemuxBytes, lib.MinSizeMB, lib.MinDuration.Std())
	fmt.Fprintf(w, "write:      %s\n", writeState(lib.Write))
}

func cmdStats(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file")
	topN := fs.Int("top", 15, "how many biggest wins to list")
	workers := fs.Int("workers", 4, "parallel probes")
	accurate := fs.Bool("accurate", false, "measure video bitrate from real packets (slow)")
	reasons := fs.Bool("reasons", false, "show a histogram of decision reasons")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	def := config.Default()
	pol := decision.Policy{Audio: def.Audio, Subtitles: def.Subtitles}
	var cfg *config.Config
	if *cfgPath != "" {
		var err error
		cfg, err = config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(stderr, "lessarr:", err)
			return 1
		}
		pol = decision.Policy{Audio: cfg.Audio, Subtitles: cfg.Subtitles}
	}

	// No path means "everything configured", which is the normal invocation
	// once more than one library exists. Walking one library at a time would
	// make the obvious command require an argument.
	var roots []string
	if arg := fs.Arg(0); arg != "" {
		roots = []string{arg}
	} else if cfg != nil {
		for _, l := range cfg.Libraries {
			roots = append(roots, l.Path)
		}
	}
	if len(roots) == 0 {
		fmt.Fprintln(stderr, "lessarr stats: no path given and no libraries configured")
		return 2
	}

	var paths []string
	walkErrs := 0
	seen := map[string]bool{}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			fmt.Fprintf(stderr, "lessarr: skipping %s: %v\n", root, err)
			walkErrs++
			continue
		}
		found, errs := scan.Files(root, cfg)
		walkErrs += errs
		for _, p := range found {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)

	// Zero files almost always means the configured paths are not visible
	// inside the container, which is the most common first-run mistake. Say so
	// instead of printing an empty report.
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "lessarr: no media files found under:")
		for _, root := range roots {
			fmt.Fprintf(stderr, "  %s\n", root)
		}
		fmt.Fprintln(stderr, "Check that the library paths in config.toml match the volume mounts.")
		return 1
	}
	if walkErrs > 0 {
		fmt.Fprintf(stderr, "lessarr: warning: %d unreadable directories skipped\n", walkErrs)
	}

	results := make([]statResult, 0, len(paths))
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan string)
	p := probe.New()

	var done, failed int
	var progMu sync.Mutex

	worker := func() {
		defer wg.Done()
		for path := range work {
			lib := config.DefaultLibrary()
			name := "(default)"
			if cfg != nil {
				if l, ok := cfg.LibraryFor(path); ok {
					lib, name = l, l.Name
				}
			}
			info, err := p.Probe(ctx, path)
			progMu.Lock()
			done++
			if err != nil {
				failed++
			}
			if done%50 == 0 {
				fmt.Fprintf(stderr, "\r  probed %d/%d", done, len(paths))
			}
			progMu.Unlock()
			if err != nil {
				continue
			}
			if *accurate {
				if br, err := p.SampleVideoBitrate(ctx, path, info.Duration); err == nil {
					info.VideoBitrate = br
				}
			}
			d := decision.Decide(info, lib, pol, decision.Options{
				Tagged:       info.Tags[ffmpeg.ProvenanceTag] != "",
				OriginalLang: probe.OriginalLanguageFromFilename(path),
			})
			out := d.EstimatedOutputBytes
			if d.Action == decision.Skip {
				out = info.Size
			}
			code := decision.CodeCompliant
			if len(d.Codes) > 0 {
				code = d.Codes[0]
			}
			mu.Lock()
			results = append(results, statResult{
				lib: name, size: info.Size, out: out, act: d.Action, code: code, path: path,
				videoSavingsUnmeasured: d.VideoSavingsUnmeasured,
			})
			mu.Unlock()
		}
	}

	wg.Add(*workers)
	for i := 0; i < *workers; i++ {
		go worker()
	}
	for _, path := range paths {
		work <- path
	}
	close(work)
	wg.Wait()
	fmt.Fprint(stderr, "\r")

	writeByLib := map[string]bool{}
	if cfg != nil {
		for _, l := range cfg.Libraries {
			writeByLib[l.Name] = l.Write
		}
	}
	printStats(stdout, results, *topN, *reasons, writeByLib)
	if failed > 0 {
		fmt.Fprintf(stderr, "lessarr: %d file(s) could not be probed\n", failed)
	}
	return 0
}

// statResult is one file's outcome during a stats walk.
type statResult struct {
	lib                    string
	size                   int64
	out                    int64
	act                    decision.Action
	code                   decision.Code
	path                   string
	videoSavingsUnmeasured bool
}

func printStats(w io.Writer, results []statResult, topN int, showReasons bool, writeByLib map[string]bool) {
	writable := false
	for _, on := range writeByLib {
		if on {
			writable = true
		}
	}
	if !writable {
		fmt.Fprint(w, "observe only: no library has write = true, so nothing will be modified\n")
		fmt.Fprint(w, "this report is a projection, not a record of work done\n\n")
	}
	// Group by library.
	type bucket struct {
		files                 int
		bytes                 int64
		skipN, remuxN, transN int
		skipB, remuxB, transB int64
		outB                  int64
		unmeasuredVideoN      int
		codes                 map[decision.Code]int
	}
	groups := map[string]*bucket{}
	for _, r := range results {
		b := groups[r.lib]
		if b == nil {
			b = &bucket{codes: map[decision.Code]int{}}
			groups[r.lib] = b
		}
		b.files++
		b.bytes += r.size
		b.codes[r.code]++
		if r.videoSavingsUnmeasured {
			b.unmeasuredVideoN++
		}
		switch r.act {
		case decision.Skip:
			b.skipN++
			b.skipB += r.size
			b.outB += r.size
		case decision.Remux:
			b.remuxN++
			b.remuxB += r.size
			b.outB += r.out
		case decision.Transcode:
			b.transN++
			b.transB += r.size
			b.outB += r.out
		}
	}

	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)

	var totalSize, totalOut int64
	for _, name := range names {
		b := groups[name]
		totalSize += b.bytes
		totalOut += b.outB
		fmt.Fprintf(w, "%s", name)
		if !writeByLib[name] {
			fmt.Fprint(w, "  (observe only)")
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  files        %d (%s)\n", b.files, humanBytes(b.bytes))
		fmt.Fprintf(w, "  skip         %d (%s)\n", b.skipN, humanBytes(b.skipB))
		fmt.Fprintf(w, "  remux        %d (%s)\n", b.remuxN, humanBytes(b.remuxB))
		fmt.Fprintf(w, "  transcode    %d (%s)\n", b.transN, humanBytes(b.transB))
		saved := b.bytes - b.outB
		pct := 0.0
		if b.bytes > 0 {
			pct = float64(saved) / float64(b.bytes) * 100
		}
		fmt.Fprintf(w, "  projected    %s -> %s  (saves %s, %.0f%%)\n",
			humanBytes(b.bytes), humanBytes(b.outB), humanBytes(saved), pct)
		if b.codes[decision.CodeCodecGap] > 0 {
			fmt.Fprintf(w, "  note         codec-gap video savings are modeled, not measured\n")
		}
		if b.unmeasuredVideoN > 0 {
			fmt.Fprintf(w, "  note         video savings unmeasured for %d transcode(s); excluded from projection\n", b.unmeasuredVideoN)
		}
		if showReasons {
			fmt.Fprintf(w, "  reasons:\n")
			type kv struct {
				c decision.Code
				n int
			}
			var kvs []kv
			for c, n := range b.codes {
				kvs = append(kvs, kv{c, n})
			}
			sort.Slice(kvs, func(i, j int) bool { return kvs[i].n > kvs[j].n })
			for _, e := range kvs {
				fmt.Fprintf(w, "    %-22s %d\n", e.c, e.n)
			}
		}
		fmt.Fprintln(w)
	}
	saved := totalSize - totalOut
	pct := 0.0
	if totalSize > 0 {
		pct = float64(saved) / float64(totalSize) * 100
	}
	fmt.Fprintf(w, "total          %s -> %s  (saves %s, %.0f%%)\n",
		humanBytes(totalSize), humanBytes(totalOut), humanBytes(saved), pct)
	totalCodecGap := 0
	totalUnmeasured := 0
	for _, b := range groups {
		totalCodecGap += b.codes[decision.CodeCodecGap]
		totalUnmeasured += b.unmeasuredVideoN
	}
	if totalCodecGap > 0 {
		fmt.Fprintln(w, "note           codec-gap video savings are modeled, not measured")
	}
	if totalUnmeasured > 0 {
		fmt.Fprintf(w, "note           video savings unmeasured for %d transcode(s); excluded from projection\n", totalUnmeasured)
	}

	if topN > 0 && saved > 0 {
		sorted := make([]statResult, len(results))
		copy(sorted, results)
		sort.Slice(sorted, func(i, j int) bool {
			return (sorted[i].size - sorted[i].out) > (sorted[j].size - sorted[j].out)
		})
		fmt.Fprintf(w, "\nbiggest wins\n")
		for i := 0; i < len(sorted) && i < topN; i++ {
			r := sorted[i]
			if r.size-r.out <= 0 {
				break
			}
			fmt.Fprintf(w, "  %-9s %8s -> %8s  %s\n", r.act,
				humanBytes(r.size), humanBytes(r.out), filepath.Base(r.path))
		}
	}
}

func codec(c string) string {
	switch c {
	case "mpeg2video":
		return "mpeg2"
	case "mpeg4":
		return "mpeg4"
	}
	return c
}

func lang(l string) string {
	if l == "" {
		return "und"
	}
	return l
}

func channels(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "2.0"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%dch", n)
}

func hdrLabel(h probe.HDR) string {
	switch {
	case h.DoVi && h.DoViProfile > 0:
		return fmt.Sprintf("dovi%d", h.DoViProfile)
	case h.DoVi:
		return "dovi"
	case h.HLG:
		return "hlg"
	case h.HDR10:
		return "hdr10"
	}
	return "sdr"
}

// writeState renders the per-library write switch, which defaults to off.
func writeState(on bool) string {
	if on {
		return "true (this library will be modified)"
	}
	return "false (observe only; nothing will be modified)"
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

func humanDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	if d <= 0 {
		return "unknown"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
