// Package swap preserves the original, prepares replacement metadata and
// atomically installs a validated temp file on the source filesystem.
package swap

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const TrashDir = ".lessarr-trash"

// Prepare copies the source's permissions, ownership and mtime to the output,
// then fsyncs the output file. It must run before ReplacePrepared.
func Prepare(source, output string) error {
	srcInfo, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	outInfo, err := os.Stat(output)
	if err != nil {
		return fmt.Errorf("stat output: %w", err)
	}
	if !srcInfo.Mode().IsRegular() || !outInfo.Mode().IsRegular() {
		return fmt.Errorf("source and output must be regular files")
	}
	outFile, err := os.OpenFile(output, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open output before preserving metadata: %w", err)
	}
	defer outFile.Close()

	srcStat, srcOK := srcInfo.Sys().(*syscall.Stat_t)
	outStat, outOK := outInfo.Sys().(*syscall.Stat_t)
	if srcOK && outOK && (srcStat.Uid != outStat.Uid || srcStat.Gid != outStat.Gid) {
		if err := os.Chown(output, int(srcStat.Uid), int(srcStat.Gid)); err != nil {
			return fmt.Errorf("preserve source owner: %w", err)
		}
	}
	if err := os.Chmod(output, srcInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("preserve source mode: %w", err)
	}
	if err := os.Chtimes(output, srcInfo.ModTime(), srcInfo.ModTime()); err != nil {
		return fmt.Errorf("preserve source mtime: %w", err)
	}
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("fsync output: %w", err)
	}
	return nil
}

// ReplacePrepared first hardlinks the original into .lessarr-trash, then
// atomically renames the prepared output over it. The backup remains available
// until an operator removes it.
func ReplacePrepared(source, output string) (string, error) {
	if filepath.Clean(filepath.Dir(source)) != filepath.Clean(filepath.Dir(output)) {
		return "", fmt.Errorf("source and output must share a directory for atomic replacement")
	}

	trashDir := filepath.Join(filepath.Dir(source), TrashDir)
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return "", fmt.Errorf("create trash directory: %w", err)
	}
	backup := filepath.Join(trashDir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(source)))
	if err := os.Link(source, backup); err != nil {
		return "", fmt.Errorf("preserve original in trash: %w", err)
	}
	if err := SyncDir(trashDir); err != nil {
		_ = os.Remove(backup)
		return "", fmt.Errorf("sync trash directory: %w", err)
	}

	if err := os.Rename(output, source); err != nil {
		_ = os.Remove(backup)
		_ = SyncDir(trashDir)
		return "", fmt.Errorf("atomic replacement: %w", err)
	}
	if err := SyncDir(filepath.Dir(source)); err != nil {
		return backup, fmt.Errorf("replacement completed but directory fsync failed; original is preserved at %s: %w", backup, err)
	}
	return backup, nil
}

// Replace is the complete safe replacement operation.
func Replace(source, output string) (string, error) {
	if err := Prepare(source, output); err != nil {
		return "", err
	}
	return ReplacePrepared(source, output)
}

func SyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open output for fsync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync output: %w", err)
	}
	return nil
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for fsync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync directory: %w", err)
	}
	return nil
}
