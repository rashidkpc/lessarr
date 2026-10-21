// Package trash bounds the retained originals created by swap. It keeps the
// newest backup per source path, then enforces an age and a hard size cap.
package trash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/rashidkpc/lessarr/internal/config"
	"github.com/rashidkpc/lessarr/internal/queue"
)

type Store interface {
	Backups() ([]queue.Job, error)
	ClearBackup(int64) error
}

type Stats struct {
	Files        int
	Bytes        int64
	DeletedFiles int
	Freed        int64
}

// Entry is one file found in a .lessarr-trash directory.
type Entry struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// Location is one .lessarr-trash directory on disk.
type Location struct {
	Path     string
	Files    int
	Bytes    int64
	Tracked  int
	Orphans  int
	Modified time.Time
}

// Missing is a database reference whose retained original is gone.
type Missing struct {
	JobID  int64
	Source string
	Backup string
	Reason string
}

// Report describes the difference between retained originals on disk and the
// backups recorded in the database. Orphans are never deleted automatically:
// after database loss they may be the only recoverable copy.
type Report struct {
	ScannedDirs  int
	Locations    []Location
	TrackedFiles int
	TrackedBytes int64
	OrphanFiles  []Entry
	OrphanBytes  int64
	Missing      []Missing
	Errors       []string
}

const trashDirName = ".lessarr-trash"

// BuildReport scans configured libraries for trash directories and compares
// their contents with the jobs that still claim a retained original.
func BuildReport(store Store, libraries []config.Library) (Report, error) {
	jobs, err := store.Backups()
	if err != nil {
		return Report{}, err
	}
	report := Report{}
	known := make(map[string]queue.Job, len(jobs))
	for _, job := range jobs {
		path := filepath.Clean(job.Backup)
		known[path] = job
		fi, err := os.Lstat(path)
		switch {
		case err == nil && fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0:
			report.TrackedFiles++
			report.TrackedBytes += fi.Size()
		case errors.Is(err, fs.ErrNotExist):
			report.Missing = append(report.Missing, Missing{
				JobID: job.ID, Source: job.Path, Backup: path, Reason: "file is missing",
			})
		case err != nil:
			report.Missing = append(report.Missing, Missing{
				JobID: job.ID, Source: job.Path, Backup: path, Reason: err.Error(),
			})
		default:
			report.Missing = append(report.Missing, Missing{
				JobID: job.ID, Source: job.Path, Backup: path, Reason: "not a regular file",
			})
		}
	}

	seenDirs := map[string]bool{}
	seenOrphans := map[string]bool{}
	for _, lib := range libraries {
		err := filepath.WalkDir(lib.Path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !entry.IsDir() || entry.Name() != trashDirName {
				return nil
			}
			clean := filepath.Clean(path)
			if seenDirs[clean] {
				return filepath.SkipDir
			}
			seenDirs[clean] = true
			report.ScannedDirs++
			loc := Location{Path: clean}
			if info, err := entry.Info(); err == nil {
				loc.Modified = info.ModTime()
			}
			entries, err := os.ReadDir(clean)
			if err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", clean, err))
				report.Locations = append(report.Locations, loc)
				return filepath.SkipDir
			}
			for _, item := range entries {
				if item.IsDir() {
					continue
				}
				path := filepath.Join(clean, item.Name())
				fi, err := os.Lstat(path)
				if err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
					continue
				}
				loc.Files++
				loc.Bytes += fi.Size()
				if fi.ModTime().After(loc.Modified) {
					loc.Modified = fi.ModTime()
				}
				if _, ok := known[path]; ok {
					loc.Tracked++
					continue
				}
				if seenOrphans[path] {
					continue
				}
				seenOrphans[path] = true
				loc.Orphans++
				report.OrphanFiles = append(report.OrphanFiles, Entry{
					Path: path, Size: fi.Size(), ModTime: fi.ModTime(),
				})
				report.OrphanBytes += fi.Size()
			}
			report.Locations = append(report.Locations, loc)
			return filepath.SkipDir
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", lib.Path, err))
		}
	}
	sort.Slice(report.Locations, func(i, j int) bool {
		return report.Locations[i].Path < report.Locations[j].Path
	})
	sort.Slice(report.OrphanFiles, func(i, j int) bool {
		return report.OrphanFiles[i].ModTime.After(report.OrphanFiles[j].ModTime)
	})
	sort.Slice(report.Missing, func(i, j int) bool {
		return report.Missing[i].JobID < report.Missing[j].JobID
	})
	return report, nil
}

// Summary reports the existing backups without deleting anything.
func Summary(store Store) (Stats, error) {
	jobs, err := store.Backups()
	if err != nil {
		return Stats{}, err
	}
	var stats Stats
	for _, job := range jobs {
		fi, err := os.Lstat(job.Backup)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		stats.Files++
		stats.Bytes += fi.Size()
	}
	return stats, nil
}

// Prune keeps the newest backup per source path, deletes backups older than the
// retention, then deletes the oldest until the total is under the size cap. A
// single oversized backup is kept rather than leaving no rollback at all.
func Prune(ctx context.Context, store Store, cfg config.Trash, now time.Time) (Stats, error) {
	jobs, err := store.Backups()
	if err != nil {
		return Stats{}, err
	}
	byPath := map[string][]queue.Job{}
	for _, job := range jobs {
		fi, err := os.Lstat(job.Backup)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				_ = store.ClearBackup(job.ID)
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		byPath[job.Path] = append(byPath[job.Path], job)
	}

	stats := Stats{}
	var keep []queue.Job
	for _, group := range byPath {
		sort.Slice(group, func(i, j int) bool {
			ti, tj := backupTime(group[i]), backupTime(group[j])
			if ti.Equal(tj) {
				return group[i].ID > group[j].ID
			}
			return ti.After(tj)
		})
		keep = append(keep, group[0])
		for _, old := range group[1:] {
			if err := deleteBackup(store, old, &stats); err != nil {
				return stats, err
			}
		}
	}

	var retained []queue.Job
	retention := cfg.Retention.Std()
	for _, job := range keep {
		if retention > 0 && now.Sub(backupTime(job)) > retention {
			if err := deleteBackup(store, job, &stats); err != nil {
				return stats, err
			}
			continue
		}
		retained = append(retained, job)
	}

	sort.Slice(retained, func(i, j int) bool {
		return backupTime(retained[i]).Before(backupTime(retained[j]))
	})
	var total int64
	for _, job := range retained {
		if fi, err := os.Lstat(job.Backup); err == nil && fi.Mode().IsRegular() {
			total += fi.Size()
		}
	}
	limit := cfg.MaxSizeGB * config.GB
	if limit > 0 {
		for len(retained) > 1 && total > limit {
			oldest := retained[0]
			fi, err := os.Lstat(oldest.Backup)
			if err == nil && fi.Mode().IsRegular() {
				total -= fi.Size()
			}
			if err := deleteBackup(store, oldest, &stats); err != nil {
				return stats, err
			}
			retained = retained[1:]
		}
	}

	for _, job := range retained {
		fi, err := os.Lstat(job.Backup)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		stats.Files++
		stats.Bytes += fi.Size()
	}
	select {
	case <-ctx.Done():
		return stats, ctx.Err()
	default:
	}
	return stats, nil
}

// DeleteOrphan removes one reviewed orphan file. It re-checks that the path is
// still an orphan and is not a symlink at the moment of deletion.
func DeleteOrphan(store Store, libraries []config.Library, path string) error {
	clean := filepath.Clean(path)
	report, err := BuildReport(store, libraries)
	if err != nil {
		return err
	}
	found := false
	for _, orphan := range report.OrphanFiles {
		if filepath.Clean(orphan.Path) == clean {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%s is not an orphan in a configured trash directory", clean)
	}
	jobs, err := store.Backups()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if filepath.Clean(job.Backup) == clean {
			return fmt.Errorf("%s is still referenced by job %d", clean, job.ID)
		}
	}
	fi, err := os.Lstat(clean)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a regular file", clean)
	}
	if err := os.Remove(clean); err != nil {
		return err
	}
	_ = os.Remove(filepath.Dir(clean))
	return nil
}

func backupTime(job queue.Job) time.Time {
	if !job.FinishedAt.IsZero() {
		return job.FinishedAt
	}
	if !job.UpdatedAt.IsZero() {
		return job.UpdatedAt
	}
	return job.CreatedAt
}

func deleteBackup(store Store, job queue.Job, stats *Stats) error {
	path := filepath.Clean(job.Backup)
	if filepath.Base(filepath.Dir(path)) != ".lessarr-trash" {
		return fmt.Errorf("refusing to delete non-trash path %s", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return store.ClearBackup(job.ID)
		}
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to delete non-regular trash path %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	stats.DeletedFiles++
	stats.Freed += fi.Size()
	_ = os.Remove(filepath.Dir(path))
	return store.ClearBackup(job.ID)
}
