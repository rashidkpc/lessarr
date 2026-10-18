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
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
	"github.com/rashidkpc/lessarr/internal/selftest"
)

var version = "dev"

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
	case "selftest":
		return cmdSelftest(ctx, args[1:], stdout, stderr)
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
  lessarr probe [--config FILE] [--accurate] [--backend NAME] [--json] <file>
  lessarr stats [--config FILE] [--top N] [--workers N] [--reasons] <path>
  lessarr selftest [--config FILE] [--device PATH] [--seconds N] [path]
  lessarr config-validate [--config FILE]
  lessarr version

probe    explain what lessarr would do with one file, and why. Writes nothing.
stats    walk a path, decide every file, report the projected outcome.
selftest check this host: GPU, drivers, encoders, permissions. Writes nothing
         except a temporary file it removes again.
config   load and validate a config file.

M0 has no write path: run, scan and once arrive in M1/M2.
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
		fmt.Fprintf(stdout, "  %-12s %s  (%s)\n", l.Name, l.Path, l.Mode)
	}
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
		argv, err = ffmpeg.Build(b, path, path+".lessarr.tmp.mkv", info, d, lib)
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
		fmt.Fprintf(w, "projected:  %s\n", humanBytes(d.EstimatedOutputBytes))
		fmt.Fprintf(w, "backend:    %s\n", backend)
		if d.ScaleTo != nil {
			fmt.Fprintf(w, "scale:      %dx%d\n", d.ScaleTo.W, d.ScaleTo.H)
		}
		if d.Deinterlace {
			fmt.Fprintf(w, "deinterlace: yes\n")
		}
		fmt.Fprintf(w, "command:    ffmpeg %s\n", strings.Join(argv, " "))
	}
	fmt.Fprintf(w, "policy:     icq=%d preset=%s min_savings=%.0f%% min_remux=%dMB min_size=%dMB\n",
		lib.ICQ, lib.Preset, lib.MinSavingsPercent, lib.MinRemuxBytes, lib.MinSizeMB)
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

	root := fs.Arg(0)
	if root == "" && cfg != nil && len(cfg.Libraries) == 1 {
		root = cfg.Libraries[0].Path
	}
	if root == "" {
		fmt.Fprintln(stderr, "lessarr stats: a path is required")
		return 2
	}
	if _, err := os.Stat(root); err != nil {
		fmt.Fprintln(stderr, "lessarr:", err)
		return 1
	}

	paths, walkErrs := collectMedia(root, cfg)
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
			results = append(results, statResult{lib: name, size: info.Size, out: out, act: d.Action, code: code, path: path})
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

	printStats(stdout, results, *topN, *reasons)
	if failed > 0 {
		fmt.Fprintf(stderr, "lessarr: %d file(s) could not be probed\n", failed)
	}
	return 0
}

// statResult is one file's outcome during a stats walk.
type statResult struct {
	lib  string
	size int64
	out  int64
	act  decision.Action
	code decision.Code
	path string
}

func printStats(w io.Writer, results []statResult, topN int, showReasons bool) {
	// Group by library.
	type bucket struct {
		files                 int
		bytes                 int64
		skipN, remuxN, transN int
		skipB, remuxB, transB int64
		outB                  int64
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
		fmt.Fprintf(w, "%s\n", name)
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

	if topN > 0 {
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

var mediaExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true,
	".m2ts": true, ".mov": true, ".wmv": true, ".webm": true, ".mpg": true,
	".mpeg": true, ".divx": true, ".flv": true, ".vob": true,
}

var skipDirs = map[string]bool{
	"@eaDir": true, "@tmp": true, "#recycle": true, "#snapshot": true,
	".@__thumb": true, "@appstore": true, ".Trash": true, ".Trashes": true,
	"lost+found": true,
}

func collectMedia(root string, cfg *config.Config) ([]string, int) {
	var paths []string
	errs := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !mediaExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if cfg != nil {
			if l, ok := cfg.LibraryFor(path); ok && ignored(path, l.Ignore) {
				return nil
			}
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths, errs
}

func ignored(path string, patterns []string) bool {
	base := filepath.Base(path)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		if ok, _ := filepath.Match(p, path); ok {
			return true
		}
	}
	return false
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
