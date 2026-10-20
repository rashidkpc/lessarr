// Package daemon is the long-running lessarr process: it scans libraries,
// persists decisions, queues work, and executes one job at a time.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
	"github.com/rashidkpc/lessarr/internal/queue"
	"github.com/rashidkpc/lessarr/internal/scan"
	"github.com/rashidkpc/lessarr/internal/web"
	"github.com/rashidkpc/lessarr/internal/worker"
)

const (
	maxAttempts  = 3
	retryDelay   = 10 * time.Minute
	rescanAfter  = 6 * time.Hour
	workerPoll   = 2 * time.Second
	scanProgress = 100
	scanInterval = 5 * time.Minute
)

type Options struct {
	Config  *config.Config
	Store   *queue.Store
	Runner  ffmpeg.Runner
	Prober  probe.Prober
	Backend ffmpeg.Backend
	Version string
	Log     io.Writer
	Once    bool
}

type Daemon struct {
	cfg        *config.Config
	store      *queue.Store
	runner     ffmpeg.Runner
	prober     probe.Prober
	backend    ffmpeg.Backend
	hwDecode   map[string]bool
	writable   []string
	log        io.Writer
	policy     decision.Policy
	progressMu sync.Mutex
	progress   web.Progress
}

// Run blocks until ctx is cancelled. With Once set it performs a single scan
// and returns, which makes deployment checks cheap.
func Run(ctx context.Context, o Options) error {
	if o.Config == nil || o.Store == nil {
		return errors.New("daemon requires a config and store")
	}
	if o.Log == nil {
		o.Log = os.Stderr
	}
	if o.Runner.FFmpeg == "" {
		o.Runner = ffmpeg.NewRunner()
	}
	if o.Prober.FFProbe == "" {
		o.Prober = probe.New()
	}
	d := &Daemon{
		cfg:     o.Config,
		store:   o.Store,
		runner:  o.Runner,
		prober:  o.Prober,
		backend: o.Backend,
		log:     o.Log,
		policy:  decision.Policy{Audio: o.Config.Audio, Subtitles: o.Config.Subtitles},
	}
	for _, lib := range o.Config.Libraries {
		if lib.Write {
			d.writable = append(d.writable, lib.Name)
		}
	}
	if o.Once {
		return d.sweep(ctx, true)
	}

	if err := d.store.ResetInterrupted(); err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}
	selection, err := ffmpeg.Select(ctx, d.cfg.Encoder.Backend, d.cfg.Encoder.Device, "", d.runner)
	if err != nil {
		return fmt.Errorf("select encoder: %w", err)
	}
	d.backend = selection.Backend
	if decoders, err := d.runner.Decoders(ctx); err == nil {
		d.hwDecode = ffmpeg.HardwareDecodeSupport(decoders)
	} else {
		d.hwDecode = map[string]bool{}
	}
	fmt.Fprintf(d.log, "backend: %s (%s)\n", selection.Backend.Name, selection.Reason)

	handler := web.New(d.cfg, d.store, o.Version, d.currentProgress)
	server := &http.Server{
		Addr:              d.cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		fmt.Fprintf(d.log, "listening on http://%s\n", d.cfg.Server.Listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(d.log, "http server: %v\n", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.scanLoop(ctx)
	}()
	workers := max(1, d.cfg.Encoder.Workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			d.worker(ctx, n)
		}(i + 1)
	}

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	wg.Wait()
	return nil
}

func (d *Daemon) scanLoop(ctx context.Context) {
	d.runScan(ctx, true)
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runScan(ctx, false)
		}
	}
}

func (d *Daemon) runScan(ctx context.Context, force bool) {
	if err := d.sweep(ctx, force); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(d.log, "scan: %v\n", err)
	}
}

func (d *Daemon) sweep(ctx context.Context, force bool) error {
	started := time.Now()
	total, queued, probed := 0, 0, 0
	for _, lib := range d.cfg.Libraries {
		paths, errs := scan.Files(lib.Path, d.cfg)
		if errs > 0 {
			fmt.Fprintf(d.log, "scan %s: %d unreadable directories\n", lib.Name, errs)
		}
		fmt.Fprintf(d.log, "scan %s: %d media files\n", lib.Name, len(paths))
		for i, path := range paths {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			total++
			fi, err := os.Stat(path)
			if err != nil {
				fmt.Fprintf(d.log, "scan: stat %s: %v\n", path, err)
				continue
			}
			if !force {
				old, ok, err := d.store.File(path)
				if err == nil && ok && old.Size == fi.Size() && old.MTime.Unix() == fi.ModTime().Unix() &&
					time.Since(old.CheckedAt) < rescanAfter {
					continue
				}
			}
			info, err := d.prober.Probe(ctx, path)
			if err != nil {
				fmt.Fprintf(d.log, "scan: probe %s: %v\n", path, err)
				continue
			}
			dec := decision.Decide(info, lib, d.policy, decision.Options{
				Tagged:       info.Tags[ffmpeg.ProvenanceTag] != "",
				OriginalLang: probe.OriginalLanguageFromFilename(path),
			})
			codes := make([]string, len(dec.Codes))
			for j, c := range dec.Codes {
				codes[j] = string(c)
			}
			probed++
			if err := d.store.PutFile(queue.File{
				Path: path, Library: lib.Name, Size: fi.Size(), MTime: fi.ModTime(),
				Action: string(dec.Action), Codes: strings.Join(codes, ","),
				Reason: strings.Join(dec.Reasons, "; "), Codec: info.Video.Codec,
				CheckedAt: time.Now(),
			}); err != nil {
				fmt.Fprintf(d.log, "scan: record %s: %v\n", path, err)
				continue
			}
			if dec.Action == decision.Skip {
				continue
			}
			if _, _, err := worker.TargetContainer(path); err != nil {
				fmt.Fprintf(d.log, "scan: cannot replace %s in place: %v\n", path, err)
				continue
			}
			added, err := d.store.Enqueue(queue.Job{
				Path: path, Library: lib.Name, Action: string(dec.Action),
				Codec: info.Video.Codec, Reason: strings.Join(dec.Reasons, "; "),
				SizeBefore: fi.Size(),
			})
			if err != nil {
				fmt.Fprintf(d.log, "scan: queue %s: %v\n", path, err)
			} else if added {
				queued++
			}
			if i > 0 && i%scanProgress == 0 {
				fmt.Fprintf(d.log, "scan %s: %d/%d\n", lib.Name, i, len(paths))
			}
		}
	}
	elapsed := time.Since(started)
	fmt.Fprintf(d.log, "scan complete: %d files, %d probed, %d queued in %s\n",
		total, probed, queued, elapsed.Round(time.Second))
	return nil
}

func (d *Daemon) worker(ctx context.Context, n int) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(workerPoll):
		}
		paused, err := d.store.Paused()
		if err != nil {
			fmt.Fprintf(d.log, "worker %d: pause state: %v\n", n, err)
			continue
		}
		if paused {
			continue
		}
		// Claim treats an empty filter as "any library". Observe-only mode must
		// leave candidates pending, not execute and withhold them.
		if len(d.writable) == 0 {
			continue
		}
		job, err := d.store.Claim(time.Now(), d.writable...)
		if err != nil {
			fmt.Fprintf(d.log, "worker %d: claim: %v\n", n, err)
			continue
		}
		if job == nil {
			continue
		}
		d.runJob(ctx, n, job)
	}
}

func (d *Daemon) runJob(ctx context.Context, n int, job *queue.Job) {
	lib, ok := d.cfg.LibraryByName(job.Library)
	if !ok {
		_ = d.store.Fail(job.ID, job.Attempts, maxAttempts, "library no longer configured", retryDelay)
		return
	}
	if !lib.Write {
		_ = d.store.Complete(job.ID, queue.Withheld, job.SizeBefore, "")
		fmt.Fprintf(d.log, "job %d withheld: %s\n", job.ID, job.Path)
		return
	}
	fmt.Fprintf(d.log, "job %d start: %s (%s)\n", job.ID, job.Path, job.Action)
	d.startProgress(job.ID)
	defer d.clearProgress()
	result, err := worker.Once(ctx, job.Path, worker.Options{
		Runner:         d.runner,
		Prober:         d.prober,
		Backend:        d.backend,
		HardwareDecode: d.supportsHardwareDecode(job.Codec),
		Library:        lib,
		Policy:         d.policy,
		OriginalLang:   probe.OriginalLanguageFromFilename(job.Path),
		Progress:       d.updateProgress,
		OnInfo:         d.setProgressInfo,
		Phase:          d.setProgressPhase,
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		var permanent *worker.PermanentError
		if errors.As(err, &permanent) {
			_ = d.store.Fail(job.ID, maxAttempts, maxAttempts, err.Error(), 0)
			fmt.Fprintf(d.log, "job %d failed permanently: %v\n", job.ID, err)
			return
		}
		_ = d.store.Fail(job.ID, job.Attempts, maxAttempts, err.Error(), retryDelay)
		fmt.Fprintf(d.log, "job %d failed (attempt %d/%d): %v\n", job.ID, job.Attempts, maxAttempts, err)
		return
	}
	if result.Withheld {
		_ = d.store.Complete(job.ID, queue.Withheld, job.SizeBefore, "")
		fmt.Fprintf(d.log, "job %d withheld: %s\n", job.ID, job.Path)
		return
	}
	if result.Decision.Action == decision.Skip {
		_ = d.store.Complete(job.ID, queue.Skipped, job.SizeBefore, "")
		fmt.Fprintf(d.log, "job %d skipped: %s\n", job.ID, result.Decision.String())
		return
	}
	after := job.SizeBefore
	if fi, err := os.Stat(job.Path); err == nil {
		after = fi.Size()
	}
	if err := d.store.Complete(job.ID, queue.Done, after, result.Backup); err != nil {
		fmt.Fprintf(d.log, "job %d: record completion: %v\n", job.ID, err)
		return
	}
	fmt.Fprintf(d.log, "job %d done: %s -> %s in %s\n", job.ID,
		humanBytes(job.SizeBefore), humanBytes(after), result.Elapsed.Round(time.Second))
}

func (d *Daemon) supportsHardwareDecode(codec string) bool {
	if d.hwDecode == nil {
		return false
	}
	return d.hwDecode[codec]
}

func (d *Daemon) startProgress(jobID int64) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	now := time.Now()
	d.progress = web.Progress{JobID: jobID, Phase: "probing", Started: now, Updated: now}
}

func (d *Daemon) clearProgress() {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	d.progress = web.Progress{}
}

func (d *Daemon) setProgressInfo(info *probe.MediaInfo) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	if info != nil && info.Duration > 0 {
		d.progress.Duration = time.Duration(info.Duration * float64(time.Second))
	}
	d.progress.Updated = time.Now()
}

func (d *Daemon) setProgressPhase(phase string) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	d.progress.Phase = phase
	if phase != "encoding" {
		d.progress.Percent = 100
	}
	d.progress.Updated = time.Now()
}

func (d *Daemon) updateProgress(p ffmpeg.Progress) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	d.progress.Frame = p.Frame
	d.progress.OutTime = p.OutTime
	d.progress.Speed = p.Speed
	d.progress.TotalSize = p.TotalSize
	if d.progress.Duration > 0 {
		pct := float64(p.OutTime) / float64(d.progress.Duration) * 100
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		d.progress.Percent = pct
	}
	d.progress.Updated = time.Now()
}

func (d *Daemon) currentProgress() *web.Progress {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	if d.progress.JobID == 0 {
		return nil
	}
	copy := d.progress
	return &copy
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
