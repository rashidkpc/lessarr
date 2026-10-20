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
