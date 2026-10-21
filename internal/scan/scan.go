// Package scan walks media libraries. It is polling-only on purpose: the
// library lives on a remote share, where inotify does not receive changes.
package scan

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rashidkpc/lessarr/internal/config"
)

var mediaExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".ts": true,
	".m2ts": true, ".mov": true, ".wmv": true, ".webm": true, ".mpg": true,
	".mpeg": true, ".divx": true, ".flv": true, ".vob": true,
}

var skipDirs = map[string]bool{
	"@eaDir": true, "@tmp": true, "#recycle": true, "#snapshot": true,
	".@__thumb": true, "@appstore": true, ".Trash": true, ".Trashes": true,
	"lost+found": true,
}

// Files returns every media file under root, skipping tool metadata and hidden
// directories. errs counts directories that could not be read.
func Files(root string, cfg *config.Config) ([]string, int) {
	var paths []string
	errs := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs++
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		// Temp outputs are created beside the source and end in .mkv/.mp4.
		// Never let a scan enqueue its own in-progress file or trash entry.
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !mediaExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if cfg != nil {
			if l, ok := cfg.LibraryFor(path); ok && Ignored(path, l.Ignore) {
				return nil
			}
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths, errs
}

func Ignored(path string, patterns []string) bool {
	base := filepath.Base(path)
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
		if ok, _ := filepath.Match(p, path); ok {
			return true
		}
	}
	return false
}
