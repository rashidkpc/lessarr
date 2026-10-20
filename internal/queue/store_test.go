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
	if err := s.ResetRunning(); err != nil {
		t.Fatal(err)
	}
	if current, err := s.Current(); err != nil || current != nil {
		t.Fatalf("Current after reset = %v, %v", current, err)
	}
}
