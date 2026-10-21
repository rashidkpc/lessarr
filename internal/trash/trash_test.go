package trash

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

type fakeStore struct {
	jobs    []queue.Job
	cleared map[int64]bool
}

func (f *fakeStore) Backups() ([]queue.Job, error) { return f.jobs, nil }
func (f *fakeStore) ClearBackup(id int64) error {
	if f.cleared == nil {
		f.cleared = map[int64]bool{}
	}
	f.cleared[id] = true
	return nil
}

func backupFile(t *testing.T, dir string, name string, size int) string {
	t.Helper()
	trashDir := filepath.Join(dir, ".lessarr-trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(trashDir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPruneKeepsNewestPerPath(t *testing.T) {
	dir := t.TempDir()
	old := backupFile(t, dir, "1-episode.mkv", 10)
	newer := backupFile(t, dir, "2-episode.mkv", 10)
	now := time.Now()
	store := &fakeStore{jobs: []queue.Job{
		{ID: 1, Path: "/media/episode.mkv", Backup: old, FinishedAt: now.Add(-2 * time.Hour)},
		{ID: 2, Path: "/media/episode.mkv", Backup: newer, FinishedAt: now.Add(-time.Hour)},
	}}
	stats, err := Prune(context.Background(), store, config.Trash{Retention: config.Duration(48 * time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeletedFiles != 1 || stats.Freed != 10 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("older backup still exists")
	}
	if _, err := os.Stat(newer); err != nil {
		t.Error("newest backup should remain")
	}
	if !store.cleared[1] {
		t.Error("old job backup was not cleared")
	}
}

func TestPruneDeletesExpired(t *testing.T) {
	dir := t.TempDir()
	old := backupFile(t, dir, "1-old.mkv", 10)
	now := time.Now()
	store := &fakeStore{jobs: []queue.Job{
		{ID: 1, Path: "/media/old.mkv", Backup: old, FinishedAt: now.Add(-72 * time.Hour)},
	}}
	stats, err := Prune(context.Background(), store, config.Trash{Retention: config.Duration(48 * time.Hour)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeletedFiles != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("expired backup still exists")
	}
}

func TestPruneRefusesNonTrashPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-trash.mkv")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{jobs: []queue.Job{{ID: 1, Path: "/media/x.mkv", Backup: path, FinishedAt: time.Now().Add(-72 * time.Hour)}}}
	if _, err := Prune(context.Background(), store, config.Trash{Retention: config.Duration(48 * time.Hour)}, time.Now()); err == nil {
		t.Fatal("expected refusal to delete a non-trash path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("non-trash file was removed")
	}
}

func TestPruneCapUsesGigabytes(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 3)
	now := time.Now()
	jobs := make([]queue.Job, 3)
	for i := range paths {
		paths[i] = backupFile(t, dir, string(rune('a'+i))+"-file.mkv", 0)
		f, err := os.OpenFile(paths[i], os.O_RDWR, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(100 * config.MB); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		jobs[i] = queue.Job{ID: int64(i + 1), Path: "/media/" + string(rune('a'+i)) + ".mkv",
			Backup: paths[i], FinishedAt: now.Add(time.Duration(i) * time.Minute)}
	}
	store := &fakeStore{jobs: jobs}
	stats, err := Prune(context.Background(), store, config.Trash{
		Retention: config.Duration(48 * time.Hour), MaxSizeGB: 1,
	}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeletedFiles != 0 {
		t.Fatalf("deleted %d files, want 0 (cap is 1 GiB, files are 100 MiB each)", stats.DeletedFiles)
	}
	if stats.Files != 3 {
		t.Fatalf("remaining %d files, want 3", stats.Files)
	}
}

func TestBuildReportFindsOrphansAndMissing(t *testing.T) {
	lib := t.TempDir()
	tracked := backupFile(t, lib, "tracked.mkv", 10)
	orphan := backupFile(t, lib, "orphan.mkv", 20)
	missing := filepath.Join(lib, ".lessarr-trash", "missing.mkv")
	store := &fakeStore{jobs: []queue.Job{
		{ID: 1, Path: "/media/tracked.mkv", Backup: tracked},
		{ID: 2, Path: "/media/missing.mkv", Backup: missing},
	}}
	report, err := BuildReport(store, []config.Library{{Name: "Movies", Path: lib}})
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedDirs != 1 || report.TrackedFiles != 1 || report.TrackedBytes != 10 {
		t.Fatalf("report counts = %+v", report)
	}
	if len(report.OrphanFiles) != 1 || report.OrphanFiles[0].Path != orphan || report.OrphanBytes != 20 {
		t.Fatalf("orphans = %+v", report.OrphanFiles)
	}
	if len(report.Missing) != 1 || report.Missing[0].JobID != 2 || report.Missing[0].Backup != missing {
		t.Fatalf("missing = %+v", report.Missing)
	}
}

func TestDeleteOrphanRefusesTrackedAndRemovesOrphan(t *testing.T) {
	lib := t.TempDir()
	tracked := backupFile(t, lib, "tracked.mkv", 10)
	orphan := backupFile(t, lib, "orphan.mkv", 20)
	store := &fakeStore{jobs: []queue.Job{{ID: 1, Path: "/media/tracked.mkv", Backup: tracked}}}
	if err := DeleteOrphan(store, []config.Library{{Name: "Movies", Path: lib}}, tracked); err == nil {
		t.Fatal("deleted a tracked backup")
	}
	if err := DeleteOrphan(store, []config.Library{{Name: "Movies", Path: lib}}, orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan still exists")
	}
}
