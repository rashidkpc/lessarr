package daemon

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
	"github.com/rashidkpc/lessarr/internal/swap"
)

func TestRestorePutsOriginalBackAndSuppresses(t *testing.T) {
	dir := t.TempDir()
	library := filepath.Join(dir, "Movies")
	if err := os.MkdirAll(filepath.Join(library, swap.TrashDir), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(library, "Movie.mkv")
	backup := filepath.Join(library, swap.TrashDir, "1-Movie.mkv")
	if err := os.WriteFile(source, []byte("transcoded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("original"), 0o644); err != nil {
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
	if _, err := store.Enqueue(queue.Job{Path: source, Library: "Movies", Action: "transcode", SizeBefore: 100}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Claim(time.Now())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err := store.Complete(job.ID, queue.Done, 50, backup); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: &cfg, store: store, log: io.Discard}
	if err := d.Restore(job.ID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("source = %q, want original", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup still exists")
	}
	restored, err := store.Job(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != queue.Failed || !restored.Restored {
		t.Fatalf("job = %+v", restored)
	}
	fi, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if suppressed, err := store.Suppressed(source, fi.Size(), fi.ModTime().Unix()); err != nil || !suppressed {
		t.Fatalf("suppression = %v, %v", suppressed, err)
	}
}
