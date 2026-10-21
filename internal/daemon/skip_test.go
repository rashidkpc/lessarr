package daemon

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

func TestSkipAndUnskip(t *testing.T) {
	dir := t.TempDir()
	library := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(library, "Movie.mkv")
	if err := os.WriteFile(path, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "lessarr.db")
	store, err := queue.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Default()
	cfg.Server.DB = dbPath
	cfg.Libraries = []config.Library{{Name: "Movies", Path: library, Write: true}}
	if _, err := store.Enqueue(queue.Job{Path: path, Library: "Movies", Action: "transcode", SizeBefore: 5}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := store.Fail(job.ID, 3, 3, "failed", 0); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: &cfg, store: store, log: io.Discard}
	if err := d.Skip(job.ID); err != nil {
		t.Fatal(err)
	}
	skipped, err := store.Job(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.State != queue.Skipped {
		t.Fatalf("state = %s", skipped.State)
	}
	if reason, err := store.SuppressionReason(path); err != nil || reason != queue.OperatorSkipReason {
		t.Fatalf("suppression = %q, %v", reason, err)
	}
	if err := d.Unskip(path); err != nil {
		t.Fatal(err)
	}
	requeued, err := store.Job(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.State != queue.Pending {
		t.Fatalf("state after unskip = %s", requeued.State)
	}
	if reason, err := store.SuppressionReason(path); err != nil || reason != "" {
		t.Fatalf("suppression after unskip = %q, %v", reason, err)
	}
}
