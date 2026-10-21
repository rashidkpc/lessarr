// Package daemon is the long-running lessarr process: it scans libraries,
// persists decisions, queues work, and executes one job at a time.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rashidkpc/lessarr/internal/compare"
	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/decision"
	"github.com/rashidkpc/lessarr/internal/ffmpeg"
	"github.com/rashidkpc/lessarr/internal/probe"
	"github.com/rashidkpc/lessarr/internal/queue"
	"github.com/rashidkpc/lessarr/internal/scan"
	"github.com/rashidkpc/lessarr/internal/swap"
	"github.com/rashidkpc/lessarr/internal/trash"
	"github.com/rashidkpc/lessarr/internal/web"
	"github.com/rashidkpc/lessarr/internal/worker"
)

const (
	maxAttempts      = 3
	retryDelay       = 10 * time.Minute
	rescanAfter      = 6 * time.Hour
	workerPoll       = 2 * time.Second
	scanProgress     = 100
	scanInterval     = 5 * time.Minute
	mountInterval    = time.Minute
	metricsInterval  = 5 * time.Minute
	metricsRetention = 30 * 24 * time.Hour
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
	cfg         *config.Config
	store       *queue.Store
	runner      ffmpeg.Runner
	prober      probe.Prober
	backend     ffmpeg.Backend
	hwDecode    map[string]bool
	writable    []string
	log         io.Writer
	policy      decision.Policy
	progressMu  sync.Mutex
	progress    web.Progress
	compareCh   chan int64
	trashMu     sync.Mutex
	mountMu     sync.Mutex
	mountDown   bool
	mountPaused bool
}

// Run blocks until ctx is cancelled. With Once set it performs a single scan
// and returns, which makes deployment checks cheap.
func Run(ctx context.Context, o Options) error {
	if o.Config == nil || o.Store == nil {
		return errors.New("daemon requires a config and store")
	}
	release, err := acquireProcessLock(filepath.Join(filepath.Dir(o.Config.Server.DB), "lessarr.lock"))
	if err != nil {
		return err
	}
	defer release()
	if o.Log == nil {
		o.Log = os.Stderr
	}
	if err := o.Store.IntegrityCheck(); err != nil {
		return fmt.Errorf("database integrity check failed: %w", err)
	}
	if err := o.Store.Backup(o.Config.Server.DB + ".bak"); err != nil {
		fmt.Fprintf(o.Log, "database backup: %v\n", err)
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

	d.checkMounts()
	if err := d.store.ResetInterrupted(); err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}
	if err := d.store.ClearRestoreInProgress(); err != nil {
		return fmt.Errorf("clear stale restore markers: %w", err)
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

	handler := web.New(d.cfg, d.store, o.Version, d.currentProgress, d.Retry, d.Restore)
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
	d.compareCh = make(chan int64, 128)
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.compareWorker(ctx)
	}()
	d.backfillComparisons()
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.trashLoop(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.watchLoop(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.metricsLoop(ctx)
	}()

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
			// A nested library owns its paths outright. Without this, the
			// broader library scans the same file and can queue it under the
			// wrong (possibly observe-only) policy.
			if best, ok := d.cfg.LibraryFor(path); ok && best.Name != lib.Name {
				continue
			}
			total++
			fi, err := os.Stat(path)
			if err != nil {
				fmt.Fprintf(d.log, "scan: stat %s: %v\n", path, err)
				continue
			}
			if !settled(fi.ModTime(), lib, time.Now()) {
				// Sonarr/nzbget may still be moving or renaming this file.
				continue
			}
			if suppressed, err := d.store.Suppressed(path, fi.Size(), fi.ModTime().Unix()); err == nil && suppressed {
				// Operator restored the original and opted out of this generation.
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
				SizeBefore: fi.Size(), EstimatedBytes: dec.EstimatedOutputBytes,
				SavingsUnmeasured: dec.VideoSavingsUnmeasured,
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
		d.notifyFailure(job, "library no longer configured", true)
		return
	}
	if !lib.Write {
		_ = d.store.Complete(job.ID, queue.Withheld, job.SizeBefore, "")
		fmt.Fprintf(d.log, "job %d withheld: %s\n", job.ID, job.Path)
		return
	}
	fmt.Fprintf(d.log, "job %d start: %s (%s)\n", job.ID, job.Path, job.Action)
	d.startProgress(job.ID, job.Action, lib.Validation)
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
		ValidateStep:   d.addProgressStep,
		DecodeProgress: d.updateProgress,
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		var permanent *worker.PermanentError
		if errors.As(err, &permanent) {
			_ = d.store.Fail(job.ID, maxAttempts, maxAttempts, err.Error(), 0)
			fmt.Fprintf(d.log, "job %d failed permanently: %v\n", job.ID, err)
			d.notifyFailure(job, err.Error(), true)
			return
		}
		_ = d.store.Fail(job.ID, job.Attempts, maxAttempts, err.Error(), retryDelay)
		fmt.Fprintf(d.log, "job %d failed (attempt %d/%d): %v\n", job.ID, job.Attempts, maxAttempts, err)
		if job.Attempts >= maxAttempts {
			d.notifyFailure(job, err.Error(), true)
		}
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
	if job.Action == string(decision.Transcode) && result.Backup != "" {
		d.enqueueCompare(job.ID)
	}
	d.pruneTrash(ctx)
}

// Restore puts the retained original back and opts the path out of automatic
// processing. Retry is the explicit way to opt back in.
func (d *Daemon) Restore(jobID int64) error {
	d.trashMu.Lock()
	defer d.trashMu.Unlock()
	job, err := d.store.Job(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job %d not found", jobID)
	}
	if job.State != queue.Done || job.Backup == "" {
		return fmt.Errorf("job %d has no retained original", jobID)
	}
	lib, ok := d.cfg.LibraryFor(job.Path)
	if !ok {
		return fmt.Errorf("file is outside configured libraries")
	}
	if !lib.Write {
		return fmt.Errorf("library %q is observe-only; enable write before restoring", lib.Name)
	}
	backup := filepath.Clean(job.Backup)
	if filepath.Base(filepath.Dir(backup)) != ".lessarr-trash" {
		return fmt.Errorf("refusing to restore non-trash path")
	}
	fi, err := os.Lstat(backup)
	if err != nil {
		return fmt.Errorf("original is unavailable: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("original is not a regular file")
	}
	size, mtime := fi.Size(), fi.ModTime().Unix()
	// Suppress before touching the filesystem so a concurrent scan cannot
	// enqueue the path, and Claim refuses suppressed paths. Recheck for a job
	// that was claimed just before the marker landed.
	if err := d.store.Suppress(job.Path, queue.RestoreInProgressReason, size, mtime); err != nil {
		return err
	}
	active, err := d.store.ActiveJob(job.Path)
	if err != nil {
		_ = d.store.ClearSuppression(job.Path)
		return err
	}
	if active != nil {
		_ = d.store.ClearSuppression(job.Path)
		return fmt.Errorf("file has an active job; wait for it to finish before restoring")
	}
	if err := swap.Restore(job.Path, backup); err != nil {
		_ = d.store.ClearSuppression(job.Path)
		return err
	}
	if err := d.store.MarkRestored(job.ID, job.Path, queue.RestoredReason, size, mtime); err != nil {
		return fmt.Errorf("original restored, but recording the undo failed: %w", err)
	}
	_ = os.RemoveAll(filepath.Join(compare.Dir(d.cfg.Server.DB), fmt.Sprintf("%d", job.ID)))
	fmt.Fprintf(d.log, "job %d restored: %s; automatic processing suppressed\n", job.ID, job.Path)
	return nil
}

// Retry restores a retained original over the current output and queues a
// fresh job under the current library settings.
func (d *Daemon) Retry(jobID int64) error {
	d.trashMu.Lock()
	defer d.trashMu.Unlock()
	job, err := d.store.Job(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("job %d not found", jobID)
	}
	lib, ok := d.cfg.LibraryFor(job.Path)
	if !ok {
		return fmt.Errorf("file is outside configured libraries")
	}
	if !lib.Write {
		return fmt.Errorf("library %q is observe-only; enable write before retrying", lib.Name)
	}
	active, err := d.store.ActiveJob(job.Path)
	if err != nil {
		return err
	}
	if active != nil && active.State == queue.Running {
		return fmt.Errorf("file is currently being processed")
	}

	restore := false
	source := ""
	if job.Backup != "" {
		backup := filepath.Clean(job.Backup)
		if filepath.Base(filepath.Dir(backup)) != ".lessarr-trash" {
			return fmt.Errorf("refusing to restore non-trash path")
		}
		if fi, err := os.Lstat(backup); err == nil && fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
			restore = true
			source = backup
		}
	}
	if !restore {
		if job.State != queue.Failed {
			return fmt.Errorf("original is no longer available")
		}
		fi, err := os.Lstat(job.Path)
		if err != nil {
			return fmt.Errorf("original is unavailable: %w", err)
		}
		if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("original is not a regular file")
		}
		source = job.Path
	}
	if restore {
		if out, err := os.Lstat(job.Path); err == nil {
			if !out.Mode().IsRegular() || out.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("current output is not a regular file")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	info, err := d.prober.Probe(probeCtx, source)
	if err != nil {
		return fmt.Errorf("probe original: %w", err)
	}
	if restore {
		if err := os.Rename(source, job.Path); err != nil {
			return fmt.Errorf("restore original: %w", err)
		}
		if err := d.store.ClearBackup(job.ID); err != nil {
			fmt.Fprintf(d.log, "retry %d: clear backup: %v\n", job.ID, err)
		}
	}
	if active != nil {
		if err := d.store.ResetPendingForRetry(active.ID, lib.Name, info.Video.Codec, info.Size); err != nil {
			return err
		}
		fmt.Fprintf(d.log, "job %d retry: reset pending job %d\n", job.ID, active.ID)
		return nil
	}
	added, err := d.store.EnqueueRetry(queue.Job{
		Path: job.Path, Library: lib.Name, Action: string(decision.Transcode),
		Codec: info.Video.Codec, Reason: "retry requested from retained original",
		SizeBefore: info.Size,
	})
	if err != nil {
		return err
	}
	if !added {
		return fmt.Errorf("file already has an active job")
	}
	fmt.Fprintf(d.log, "job %d retry: restored original and queued again\n", job.ID)
	return nil
}

func (d *Daemon) trashLoop(ctx context.Context) {
	d.cleanupTemps()
	d.pruneComparisons()
	d.pruneTrash(ctx)
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.cleanupTemps()
			d.pruneComparisons()
			d.pruneTrash(ctx)
			if err := d.store.Backup(d.cfg.Server.DB + ".bak"); err != nil {
				fmt.Fprintf(d.log, "database backup: %v\n", err)
			}
		}
	}
}

func (d *Daemon) metricsLoop(ctx context.Context) {
	d.recordMetrics()
	ticker := time.NewTicker(metricsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.recordMetrics()
		}
	}
}

func (d *Daemon) recordMetrics() {
	counts, err := d.store.Counts()
	if err != nil {
		fmt.Fprintf(d.log, "metrics: counts: %v\n", err)
		return
	}
	trashStats, err := trash.Summary(d.store)
	if err != nil {
		fmt.Fprintf(d.log, "metrics: trash: %v\n", err)
		return
	}
	saved, _, err := d.store.SavedSince(time.Time{})
	if err != nil {
		fmt.Fprintf(d.log, "metrics: savings: %v\n", err)
		return
	}
	now := time.Now()
	if err := d.store.RecordMetric(queue.Metric{
		At: now, Pending: counts.Pending, Running: counts.Running, Failed: counts.Failed,
		TrashFiles: trashStats.Files, TrashBytes: trashStats.Bytes, SavedBytes: saved,
	}); err != nil {
		fmt.Fprintf(d.log, "metrics: record: %v\n", err)
		return
	}
	_ = d.store.PruneMetrics(now.Add(-metricsRetention))
}

func (d *Daemon) watchLoop(ctx context.Context) {
	ticker := time.NewTicker(mountInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkMounts()
		}
	}
}

// checkMounts pauses claiming when a library becomes unreadable. It resumes
// only a pause that it created itself, and reports transitions via the webhook.
func (d *Daemon) checkMounts() {
	err := checkLibraries(d.cfg.Libraries)
	d.mountMu.Lock()
	defer d.mountMu.Unlock()
	if err != nil {
		if d.mountDown {
			return
		}
		d.mountDown = true
		fmt.Fprintf(d.log, "mount check failed: %v; pausing queue\n", err)
		paused, perr := d.store.Paused()
		if perr == nil && !paused {
			if err := d.store.SetPaused(true); err == nil {
				d.mountPaused = true
			}
		}
		d.notifyEvent(map[string]any{
			"event": "mount_failed", "error": err.Error(),
			"time": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	if !d.mountDown {
		return
	}
	d.mountDown = false
	fmt.Fprintf(d.log, "mount check recovered\n")
	if d.mountPaused {
		if err := d.store.SetPaused(false); err == nil {
			d.mountPaused = false
		}
	}
	d.notifyEvent(map[string]any{
		"event": "mount_recovered", "time": time.Now().UTC().Format(time.RFC3339),
	})
}

func checkLibraries(libs []config.Library) error {
	for _, lib := range libs {
		f, err := os.Open(lib.Path)
		if err != nil {
			return fmt.Errorf("%s: %w", lib.Name, err)
		}
		_, readErr := f.Readdirnames(1)
		_ = f.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("%s: %w", lib.Name, readErr)
		}
	}
	return nil
}

func (d *Daemon) notifyFailure(job *queue.Job, message string, permanent bool) {
	d.notifyEvent(map[string]any{
		"event":     "job_failed",
		"permanent": permanent,
		"id":        job.ID,
		"path":      job.Path,
		"library":   job.Library,
		"action":    job.Action,
		"attempts":  job.Attempts,
		"error":     message,
		"time":      time.Now().UTC().Format(time.RFC3339),
	})
}

func (d *Daemon) notifyEvent(payload map[string]any) {
	if d.cfg.Server.WebhookURL == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(d.log, "webhook: %v\n", err)
		return
	}
	url := d.cfg.Server.WebhookURL
	label, _ := payload["event"].(string)
	go func() {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(d.log, "webhook %s: %v\n", label, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(d.log, "webhook %s: %v\n", label, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fmt.Fprintf(d.log, "webhook %s: status %s\n", label, resp.Status)
		}
	}()
}

func (d *Daemon) pruneComparisons() {
	stats, err := compare.Prune(
		compare.Dir(d.cfg.Server.DB),
		d.cfg.Comparisons.Retention.Std(),
		d.cfg.Comparisons.MaxSizeGB*config.GB,
		time.Now(),
	)
	if err != nil {
		fmt.Fprintf(d.log, "comparisons: %v\n", err)
		return
	}
	if stats.Deleted > 0 {
		fmt.Fprintf(d.log, "comparisons: removed %d gallery/galleries, freed %s; %d kept\n",
			stats.Deleted, humanBytes(stats.Freed), stats.Kept)
	}
}

func (d *Daemon) cleanupTemps() {
	// Never race an active encode: its temp file is the only copy of the work.
	d.progressMu.Lock()
	active := d.progress.JobID != 0
	d.progressMu.Unlock()
	if active {
		return
	}
	n, err := cleanupStaleTemps(d.cfg.Libraries, compare.Dir(d.cfg.Server.DB), time.Now())
	if err != nil {
		fmt.Fprintf(d.log, "temp cleanup: %v\n", err)
		return
	}
	if n > 0 {
		fmt.Fprintf(d.log, "temp cleanup: removed %d stale item(s)\n", n)
	}
}

func (d *Daemon) pruneTrash(ctx context.Context) {
	d.trashMu.Lock()
	defer d.trashMu.Unlock()
	stats, err := trash.Prune(ctx, d.store, d.cfg.Trash, time.Now())
	if err != nil {
		fmt.Fprintf(d.log, "trash: %v\n", err)
		return
	}
	if stats.DeletedFiles > 0 || stats.Files > 0 {
		fmt.Fprintf(d.log, "trash: %d files, %s remaining; deleted %d, freed %s\n",
			stats.Files, humanBytes(stats.Bytes), stats.DeletedFiles, humanBytes(stats.Freed))
	}
}

func (d *Daemon) enqueueCompare(jobID int64) {
	if d.compareCh == nil {
		return
	}
	select {
	case d.compareCh <- jobID:
	default:
		fmt.Fprintf(d.log, "compare: queue full, skipping job %d for now\n", jobID)
	}
}

func (d *Daemon) backfillComparisons() {
	jobs, err := d.store.DoneTranscodes()
	if err != nil {
		fmt.Fprintf(d.log, "compare backfill: %v\n", err)
		return
	}
	root := compare.Dir(d.cfg.Server.DB)
	queued := 0
	for _, job := range jobs {
		if _, err := os.Stat(job.Backup); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("%d", job.ID), "index.html")); err == nil {
			if _, err := os.Stat(filepath.Join(root, fmt.Sprintf("%d", job.ID), "01-original.png")); err == nil {
				continue
			}
		}
		d.enqueueCompare(job.ID)
		queued++
	}
	if queued > 0 {
		fmt.Fprintf(d.log, "compare backfill: queued %d job(s)\n", queued)
	}
}

func (d *Daemon) compareWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case jobID := <-d.compareCh:
			job, err := d.store.Job(jobID)
			if err != nil {
				fmt.Fprintf(d.log, "compare %d: %v\n", jobID, err)
				continue
			}
			if job == nil || job.State != queue.Done || job.Action != string(decision.Transcode) || job.Backup == "" {
				continue
			}
			if _, err := os.Stat(job.Backup); err != nil {
				continue
			}
			info, err := d.prober.Probe(ctx, job.Path)
			if err != nil {
				fmt.Fprintf(d.log, "compare %d: probe: %v\n", jobID, err)
				continue
			}
			err = compare.Generate(ctx, d.runner, info.Duration, compare.Task{
				ID: job.ID, Source: job.Backup, Output: job.Path,
			}, compare.Dir(d.cfg.Server.DB))
			if err != nil {
				fmt.Fprintf(d.log, "compare %d: %v\n", jobID, err)
				continue
			}
			fmt.Fprintf(d.log, "compare %d: generated\n", jobID)
		}
	}
}

func (d *Daemon) supportsHardwareDecode(codec string) bool {
	if d.hwDecode == nil {
		return false
	}
	return d.hwDecode[codec]
}

func (d *Daemon) startProgress(jobID int64, action, validation string) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	now := time.Now()
	d.progress = web.Progress{JobID: jobID, Phase: "preparing", Started: now, Updated: now}
	for _, label := range planStages(action, validation) {
		d.progress.Steps = append(d.progress.Steps, web.Step{Label: label})
	}
	d.addProgressStepLocked("preparing")
}

func planStages(action, validation string) []string {
	stages := []string{
		"preparing", "encoding", "validating",
		"probing output", "checking stream counts", "checking container",
		"checking video codec", "checking dimensions", "checking duration",
		"checking audio tracks",
	}
	if action == string(decision.Remux) && validation != "full" {
		stages = append(stages, "scanning packets")
	}
	if validation == "full" {
		stages = append(stages, "decoding output")
	} else {
		for i := 1; i <= 5; i++ {
			stages = append(stages, fmt.Sprintf("sampling %d/5", i))
		}
	}
	return append(stages, "validated", "installing")
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
	if d.progress.Phase != phase {
		d.progress.Phase = phase
		d.progress.Frame = 0
		d.progress.OutTime = 0
		d.progress.Speed = ""
		d.progress.TotalSize = 0
		d.progress.Percent = 0
		d.addProgressStepLocked(phase)
	}
	if phase == "installing" {
		d.progress.Percent = 100
	}
	d.progress.Updated = time.Now()
}

func (d *Daemon) addProgressStep(label string) {
	d.progressMu.Lock()
	defer d.progressMu.Unlock()
	d.addProgressStepLocked(label)
}

func (d *Daemon) addProgressStepLocked(label string) {
	now := time.Now()
	idx := -1
	for i := range d.progress.Steps {
		if d.progress.Steps[i].Label == label {
			idx = i
			break
		}
	}
	if idx == -1 {
		current := -1
		for i := range d.progress.Steps {
			if d.progress.Steps[i].Current {
				current = i
			}
		}
		idx = current + 1
		if idx < 0 || idx > len(d.progress.Steps) {
			idx = len(d.progress.Steps)
		}
		d.progress.Steps = append(d.progress.Steps, web.Step{})
		copy(d.progress.Steps[idx+1:], d.progress.Steps[idx:])
		d.progress.Steps[idx] = web.Step{Label: label}
	}
	for i := range d.progress.Steps {
		step := &d.progress.Steps[i]
		step.Done = i < idx
		step.Current = i == idx
		if step.Current {
			step.At = now
		}
	}
	d.progress.Stage = label
	d.progress.SampleDone = 0
	d.progress.SampleTotal = 0
	for _, step := range d.progress.Steps {
		var index, total int
		if _, err := fmt.Sscanf(step.Label, "sampling %d/%d", &index, &total); err != nil || total <= 0 {
			continue
		}
		d.progress.SampleTotal = total
		if step.Done {
			d.progress.SampleDone++
		}
	}
	if d.progress.SampleTotal > 0 {
		d.progress.SamplePct = float64(d.progress.SampleDone) / float64(d.progress.SampleTotal) * 100
	}
	d.progress.Updated = now
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

// settled reports whether a file has been untouched long enough to process.
// Importers commonly move/rename files for several minutes after download.
func settled(mtime time.Time, lib config.Library, now time.Time) bool {
	if lib.SkipIfNewerThan <= 0 {
		return true
	}
	return now.Sub(mtime) >= lib.SkipIfNewerThan.Std()
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
