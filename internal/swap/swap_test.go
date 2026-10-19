package swap

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplacePreservesMetadataAndTrashBackup(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "episode.mkv")
	output := filepath.Join(dir, ".lessarr-output.tmp.mkv")
	original := []byte("original media bytes")
	replacement := []byte("new encoded media bytes")
	if err := os.WriteFile(source, original, 0o640); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(source, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, replacement, 0o600); err != nil {
		t.Fatal(err)
	}

	backup, err := Replace(source, output)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(backup)) != dir || filepath.Base(filepath.Dir(backup)) != TrashDir {
		t.Fatalf("backup is not under source-local trash: %s", backup)
	}
	gotSource, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSource, replacement) {
		t.Error("source does not contain replacement")
	}
	gotBackup, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBackup, original) {
		t.Error("trash does not preserve original bytes")
	}
	fi, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %04o, want 0640", fi.Mode().Perm())
	}
	if !fi.ModTime().Equal(oldTime) {
		t.Errorf("mtime = %s, want %s", fi.ModTime(), oldTime)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Errorf("temp output should have been renamed, stat err=%v", err)
	}
}

func TestPrepareSupportsReadOnlySourceMode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mkv")
	output := filepath.Join(dir, "output.tmp.mkv")
	if err := os.WriteFile(source, []byte("source"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(source, output); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o444 {
		t.Errorf("mode = %04o, want 0444", fi.Mode().Perm())
	}
}

func TestReplaceRejectsDifferentDirectory(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	tempDir := filepath.Join(root, "temp")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "source.mkv")
	output := filepath.Join(tempDir, "output.mkv")
	_ = os.WriteFile(source, []byte("source"), 0o644)
	_ = os.WriteFile(output, []byte("output"), 0o644)
	if _, err := ReplacePrepared(source, output); err == nil {
		t.Fatal("expected cross-directory replacement to be rejected")
	}
}
