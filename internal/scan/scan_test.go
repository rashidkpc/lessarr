package scan

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rashidkpc/lessarr/internal/config"
)

func TestFilesSkipsMetadataDirectoriesAndNonMedia(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"movie.mkv", "episode.MP4", "notes.txt", "@eaDir/thumb.jpg", ".hidden/a.mkv", "#recycle/b.mkv"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, errs := Files(root, nil)
	want := []string{filepath.Join(root, "episode.MP4"), filepath.Join(root, "movie.mkv")}
	if errs != 0 {
		t.Fatalf("errs = %d", errs)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Files() = %v, want %v", got, want)
	}
}

func TestFilesAppliesLibraryIgnore(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"movie.mkv", "movie.sample.mkv"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{Libraries: []config.Library{{Name: "Movies", Path: root, Ignore: []string{"*.sample.mkv"}}}}
	got, _ := Files(root, cfg)
	if len(got) != 1 || filepath.Base(got[0]) != "movie.mkv" {
		t.Fatalf("Files() = %v", got)
	}
}
