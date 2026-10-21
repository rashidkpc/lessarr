package queue

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFileRoundTrip(t *testing.T) {
	s := openTest(t)
	want := File{Path: "/media/a.mkv", Library: "TV", Size: 123, MTime: time.Unix(100, 0),
		Action: "transcode", Codes: "codec_gap", Reason: "h264", Codec: "h264", CheckedAt: time.Unix(200, 0)}
	if err := s.PutFile(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.File(want.Path)
	if err != nil || !ok {
		t.Fatalf("File() = %v, %v", ok, err)
	}
	if got.Size != want.Size || got.Action != want.Action || got.MTime.Unix() != want.MTime.Unix() {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestEnqueueIsIdempotentWhileActive(t *testing.T) {
	s := openTest(t)
	j := Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode", Codec: "h264", Reason: "h264", SizeBefore: 100}
	added, err := s.Enqueue(j)
	if err != nil || !added {
		t.Fatalf("first Enqueue = %v, %v", added, err)
	}
	added, err = s.Enqueue(j)
	if err != nil || added {
		t.Fatalf("second Enqueue = %v, %v; want false", added, err)
	}
	if _, err := s.Claim(time.Now()); err != nil {
		t.Fatal(err)
	}
	// A running job also blocks a duplicate.
	added, err = s.Enqueue(j)
	if err != nil || added {
		t.Fatalf("Enqueue while running = %v, %v; want false", added, err)
	}
}

func TestClaimCompleteAndCounts(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode", SizeBefore: 1000}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatalf("Claim = %v, %v", job, err)
	}
	if job.State != Running || job.Attempts != 1 {
		t.Fatalf("claimed job = %+v", job)
	}
	if err := s.Complete(job.ID, Done, 400, "/media/.trash/a"); err != nil {
		t.Fatal(err)
	}
	counts, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Done != 1 || counts.Running != 0 || counts.Saved != 600 {
		t.Fatalf("counts = %+v", counts)
	}
	recent, err := s.Recent(5)
	if err != nil || len(recent) != 1 || recent[0].State != Done {
		t.Fatalf("Recent = %+v, %v", recent, err)
	}
}

func TestFailRequeuesThenStops(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.Fail(job.ID, 1, 2, "temporary", 0); err != nil {
		t.Fatal(err)
	}
	again, err := s.Claim(time.Now())
	if err != nil || again == nil {
		t.Fatalf("requeued job was not claimable: %v, %v", again, err)
	}
	if err := s.Fail(again.ID, 2, 2, "final", 0); err != nil {
		t.Fatal(err)
	}
	counts, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Failed != 1 {
		t.Fatalf("failed count = %d, want 1", counts.Failed)
	}
}

func TestEnqueueSkipsRecentlyFailedPath(t *testing.T) {
	s := openTest(t)
	j := Job{Path: "/media/a.mkv", Library: "TV", Action: "remux", SizeBefore: 1000}
	if added, err := s.Enqueue(j); err != nil || !added {
		t.Fatalf("first Enqueue = %v, %v", added, err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.Fail(job.ID, 1, 1, "will not shrink", 0); err != nil {
		t.Fatal(err)
	}
	if added, err := s.Enqueue(j); err != nil || added {
		t.Fatalf("Enqueue after failure = %v, %v; want false", added, err)
	}
}

func TestClaimOnlyTakesWritableLibraries(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/tv.mkv", Library: "TV", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Job{Path: "/media/movie.mkv", Library: "Movies", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	if job, err := s.Claim(time.Now(), "Movies"); err != nil || job == nil || job.Library != "Movies" {
		t.Fatalf("Claim(writable Movies) = %+v, %v", job, err)
	}
	if job, err := s.Claim(time.Now(), "Movies"); err != nil || job != nil {
		t.Fatalf("second Claim = %+v, %v; want none", job, err)
	}
	// The observe-only TV job is still pending and visible in the counters.
	counts, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pending != 1 || counts.Running != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestFilesJoinActiveJobAndFilter(t *testing.T) {
	s := openTest(t)
	for _, f := range []File{
		{Path: "/media/tv/ep1.mkv", Library: "TV", Size: 100, Action: "transcode", Reason: "h264", CheckedAt: time.Now()},
		{Path: "/media/tv/ep2.mkv", Library: "TV", Size: 200, Action: "skip", Reason: "compliant", CheckedAt: time.Now()},
		{Path: "/media/movies/film.mkv", Library: "Movies", Size: 300, Action: "remux", Reason: "audio", CheckedAt: time.Now()},
	} {
		if err := s.PutFile(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Enqueue(Job{Path: "/media/tv/ep1.mkv", Library: "TV", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.Files(FileQuery{Library: "TV", Action: "transcode"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].JobState != "pending" || rows[0].JobID == 0 {
		t.Fatalf("Files() = %+v, total %d", rows, total)
	}
	rows, total, err = s.Files(FileQuery{Search: "film"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Path != "/media/movies/film.mkv" {
		t.Fatalf("search Files() = %+v, total %d", rows, total)
	}
	if _, total, err = s.Files(FileQuery{Queue: "pending"}); err != nil || total != 1 {
		t.Fatalf("pending queue total = %d, err %v", total, err)
	}
	if _, total, err = s.Files(FileQuery{Queue: "none"}); err != nil || total != 2 {
		t.Fatalf("not-queued total = %d, err %v", total, err)
	}
}

func TestJobByID(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode", Reason: "h264"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(time.Now())
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	got, err := s.Job(claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Path != "/media/a.mkv" || got.State != Running {
		t.Fatalf("Job() = %+v", got)
	}
	if missing, err := s.Job(9999); err != nil || missing != nil {
		t.Fatalf("missing job = %+v, %v", missing, err)
	}
}

func TestPauseAndResetRunning(t *testing.T) {
	s := openTest(t)
	if paused, err := s.Paused(); err != nil || paused {
		t.Fatalf("initial Paused = %v, %v", paused, err)
	}
	if err := s.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if paused, err := s.Paused(); err != nil || !paused {
		t.Fatalf("Paused after set = %v, %v", paused, err)
	}
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetInterrupted(); err != nil {
		t.Fatal(err)
	}
	if current, err := s.Current(); err != nil || current != nil {
		t.Fatalf("Current after reset = %v, %v", current, err)
	}
}

func TestEnqueueReassignsPendingJobToSpecificLibrary(t *testing.T) {
	s := openTest(t)
	path := "/media/tv/Show/Season 1/episode.mkv"
	if _, err := s.Enqueue(Job{Path: path, Library: "TV", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET next_attempt_at=? WHERE path=?`, time.Now().Add(time.Hour).Unix(), path); err != nil {
		t.Fatal(err)
	}
	if added, err := s.Enqueue(Job{Path: path, Library: "Show S1", Action: "transcode", SizeBefore: 100}); err != nil || added {
		t.Fatalf("reassign Enqueue = %v, %v", added, err)
	}
	job, err := s.Claim(time.Now(), "Show S1")
	if err != nil || job == nil || job.Library != "Show S1" {
		t.Fatalf("Claim = %+v, %v", job, err)
	}
}

func TestDoneTranscodes(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode", Backup: ""}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.Complete(job.ID, Done, 500, "/media/.lessarr-trash/a.mkv"); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.DoneTranscodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("DoneTranscodes = %+v", jobs)
	}
}

func TestSavedSince(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "TV", Action: "transcode", SizeBefore: 1000}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.Complete(job.ID, Done, 400, "/media/.lessarr-trash/a"); err != nil {
		t.Fatal(err)
	}
	saved, n, err := s.SavedSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if saved != 600 || n != 1 {
		t.Fatalf("SavedSince = %d, %d", saved, n)
	}
	if saved, n, err := s.SavedSince(time.Now().Add(time.Hour)); err != nil || saved != 0 || n != 0 {
		t.Fatalf("future cutoff = %d, %d, %v", saved, n, err)
	}
}

func TestSavedSinceUsesNewestPerPath(t *testing.T) {
	s := openTest(t)
	path := "/media/a.mkv"
	if _, err := s.Enqueue(Job{Path: path, Library: "TV", Action: "transcode", SizeBefore: 1000}); err != nil {
		t.Fatal(err)
	}
	job, _ := s.Claim(time.Now())
	if err := s.Complete(job.ID, Done, 400, "/media/.lessarr-trash/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Job{Path: path, Library: "TV", Action: "transcode", SizeBefore: 400}); err != nil {
		t.Fatal(err)
	}
	job2, _ := s.Claim(time.Now())
	if err := s.Complete(job2.ID, Done, 300, "/media/.lessarr-trash/b"); err != nil {
		t.Fatal(err)
	}
	saved, n, err := s.SavedSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if saved != 100 || n != 1 {
		t.Fatalf("SavedSince = %d, %d; want 100, 1", saved, n)
	}
}

func TestEnqueueRetryBypassesFailureCooldown(t *testing.T) {
	s := openTest(t)
	j := Job{Path: "/media/retry.mkv", Library: "TV", Action: "transcode", SizeBefore: 100}
	if _, err := s.Enqueue(j); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := s.Fail(job.ID, 1, 1, "boom", 0); err != nil {
		t.Fatal(err)
	}
	if added, err := s.Enqueue(j); err != nil || added {
		t.Fatalf("normal Enqueue = %v, %v; want false", added, err)
	}
	if added, err := s.EnqueueRetry(j); err != nil || !added {
		t.Fatalf("EnqueueRetry = %v, %v; want true", added, err)
	}
}

func TestSavedSinceDoesNotGoNegative(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/big.mkv", Library: "TV", Action: "remux", SizeBefore: 1000}); err != nil {
		t.Fatal(err)
	}
	job, _ := s.Claim(time.Now())
	if err := s.Complete(job.ID, Done, 1200, "/media/.lessarr-trash/big"); err != nil {
		t.Fatal(err)
	}
	saved, n, err := s.SavedSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if saved != 0 || n != 1 {
		t.Fatalf("SavedSince = %d, %d; want 0, 1", saved, n)
	}
}

func TestActiveJobAndResetForRetry(t *testing.T) {
	s := openTest(t)
	path := "/media/active.mkv"
	if _, err := s.Enqueue(Job{Path: path, Library: "TV", Action: "remux", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveJob(path)
	if err != nil || active == nil {
		t.Fatalf("ActiveJob = %+v, %v", active, err)
	}
	if err := s.ResetPendingForRetry(active.ID, "TV", "h264", 200); err != nil {
		t.Fatal(err)
	}
	job, err := s.Job(active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != Pending || job.Attempts != 0 || job.Action != "transcode" || job.Codec != "h264" || job.SizeBefore != 200 {
		t.Fatalf("reset job = %+v", job)
	}
}

func TestIntegrityAndBackup(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/media/a.mkv", Library: "Movies", Action: "transcode"}); err != nil {
		t.Fatal(err)
	}
	if err := s.IntegrityCheck(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	counts, err := restored.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pending != 1 {
		t.Fatalf("backup pending = %d, want 1", counts.Pending)
	}
}

func TestSuppressionBlocksEnqueueAndRetryClears(t *testing.T) {
	s := openTest(t)
	path := "/media/movies/restored.mkv"
	if _, err := s.Enqueue(Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.Complete(job.ID, Done, 50, "/media/movies/.lessarr-trash/1-restored.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRestored(job.ID, path, RestoredReason, 100, 1000); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(job.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.State != Failed || !got.Restored || got.Backup != "" {
		t.Fatalf("restored job = %+v", got)
	}
	if suppressed, err := s.Suppressed(path, 100, 1000); err != nil || !suppressed {
		t.Fatalf("suppressed = %v, %v", suppressed, err)
	}
	added, err := s.Enqueue(Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100})
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("automatic enqueue ignored suppression")
	}
	added, err = s.EnqueueRetry(Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("explicit retry did not enqueue")
	}
	if suppressed, err := s.Suppressed(path, 100, 1000); err != nil || suppressed {
		t.Fatalf("suppression survived retry: %v, %v", suppressed, err)
	}
}

func TestSuppressionExpiresWhenFileChanges(t *testing.T) {
	s := openTest(t)
	path := "/media/movies/replaced.mkv"
	if err := s.Suppress(path, RestoredReason, 100, 1000); err != nil {
		t.Fatal(err)
	}
	if suppressed, err := s.Suppressed(path, 100, 1000); err != nil || !suppressed {
		t.Fatalf("same generation suppressed = %v, %v", suppressed, err)
	}
	if suppressed, err := s.Suppressed(path, 101, 1000); err != nil || suppressed {
		t.Fatalf("new generation suppressed = %v, %v", suppressed, err)
	}
	if suppressed, err := s.Suppressed(path, 100, 1000); err != nil || suppressed {
		t.Fatalf("expired suppression returned = %v, %v", suppressed, err)
	}
}

func TestSuppressionBlocksClaim(t *testing.T) {
	s := openTest(t)
	path := "/media/movies/claimed.mkv"
	if _, err := s.Enqueue(Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.Suppress(path, RestoreInProgressReason, 100, 1000); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if job != nil {
		t.Fatalf("claimed suppressed job %d", job.ID)
	}
}

func TestMetricsRoundTripAndPrune(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.RecordMetric(Metric{At: now.Add(time.Duration(i) * time.Minute), Pending: i, TrashBytes: int64(i * 10)}); err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.MetricsSince(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 || points[2].Pending != 2 {
		t.Fatalf("points = %+v", points)
	}
	if err := s.PruneMetrics(now.Add(90 * time.Second)); err != nil {
		t.Fatal(err)
	}
	points, err = s.MetricsSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("after prune = %d points", len(points))
	}
}

func TestNextQueuedOnlyWritableLibraries(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/tv/a.mkv", Library: "TV", Action: "transcode", SizeBefore: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Job{Path: "/movies/b.mkv", Library: "Movies", Action: "transcode", SizeBefore: 20}); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.NextQueued(time.Now(), 5, "Movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Library != "Movies" {
		t.Fatalf("next = %+v", jobs)
	}
}

func TestLibraryTotalsAndReadyCount(t *testing.T) {
	s := openTest(t)
	if err := s.PutFile(File{Path: "/movies/a.mkv", Library: "Movies", Size: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFile(File{Path: "/movies/b.mkv", Library: "Movies", Size: 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Job{Path: "/tv/a.mkv", Library: "TV", Action: "transcode", SizeBefore: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Job{Path: "/movies/a.mkv", Library: "Movies", Action: "transcode", SizeBefore: 10}); err != nil {
		t.Fatal(err)
	}
	files, bytes, err := s.LibraryTotals()
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 30 {
		t.Fatalf("library totals = %d files, %d bytes", files, bytes)
	}
	ready, err := s.CountReady(time.Now(), "Movies")
	if err != nil {
		t.Fatal(err)
	}
	if ready != 1 {
		t.Fatalf("ready = %d, want 1", ready)
	}
}

func TestPrioritizeMovesPendingJobToFront(t *testing.T) {
	s := openTest(t)
	if _, err := s.Enqueue(Job{Path: "/movies/a.mkv", Library: "Movies", Action: "transcode", SizeBefore: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue(Job{Path: "/movies/b.mkv", Library: "Movies", Action: "transcode", SizeBefore: 10}); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.NextQueued(time.Now(), 5, "Movies")
	if err != nil || len(jobs) != 2 {
		t.Fatal(err)
	}
	second := jobs[1]
	if err := s.Prioritize(second.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	jobs, err = s.NextQueued(time.Now(), 5, "Movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != second.ID {
		t.Fatalf("order after prioritize = %+v", jobs)
	}
	claimed, err := s.Claim(time.Now(), "Movies")
	if err != nil || claimed == nil || claimed.ID != second.ID {
		t.Fatalf("claimed = %+v, %v", claimed, err)
	}
}
