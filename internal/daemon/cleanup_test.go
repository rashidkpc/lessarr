package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
)

func TestCleanupStaleTemps(t *testing.T) {
	lib := t.TempDir()
	compareRoot := t.TempDir()
	old := filepath.Join(lib, ".lessarr-old.tmp.mkv")
	recent := filepath.Join(lib, ".lessarr-new.tmp.mkv")
	for _, path := range []string{old, recent} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(compareRoot, ".work-123")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(work, past, past); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(lib, ".lessarr-trash")
	if err := os.MkdirAll(tracked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tracked, "keep.mkv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := cleanupStaleTemps([]config.Library{{Name: "Movies", Path: lib}}, compareRoot, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old temp was not removed")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("recent temp was removed")
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Error("stale comparison work dir was not removed")
	}
	if _, err := os.Stat(filepath.Join(tracked, "keep.mkv")); err != nil {
		t.Error("trash content was removed")
	}
}

func TestCheckLibraries(t *testing.T) {
	dir := t.TempDir()
	if err := checkLibraries([]config.Library{{Name: "Movies", Path: dir}}); err != nil {
		t.Fatal(err)
	}
	if err := checkLibraries([]config.Library{{Name: "Missing", Path: filepath.Join(dir, "nope")}}); err == nil {
		t.Fatal("missing library should fail the mount check")
	}
}
