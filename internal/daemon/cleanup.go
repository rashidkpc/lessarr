package daemon

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
)

const staleTempAge = time.Hour

// cleanupStaleTemps removes hidden temp files left by a crash and abandoned
// comparison work directories. It never follows or deletes library content.
func cleanupStaleTemps(libs []config.Library, compareRoot string, now time.Time) (int, error) {
	removed := 0
	for _, lib := range libs {
		err := filepath.WalkDir(lib.Path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if path != lib.Path && strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			name := entry.Name()
			if !strings.HasPrefix(name, ".lessarr-") {
				return nil
			}
			if !strings.Contains(name, ".tmp.") && !strings.HasPrefix(name, ".lessarr-preflight-") {
				return nil
			}
			info, err := entry.Info()
			if err != nil || now.Sub(info.ModTime()) < staleTempAge {
				return nil
			}
			if err := os.Remove(path); err == nil {
				removed++
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return removed, err
		}
	}
	if compareRoot != "" {
		_ = filepath.WalkDir(compareRoot, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".work-") {
				return nil
			}
			info, err := entry.Info()
			if err != nil || now.Sub(info.ModTime()) < staleTempAge {
				return nil
			}
			if err := os.RemoveAll(path); err == nil {
				removed++
			}
			return filepath.SkipDir
		})
	}
	return removed, nil
}
