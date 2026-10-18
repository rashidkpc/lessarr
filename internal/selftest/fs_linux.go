//go:build linux

package selftest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func devOf(fi os.FileInfo) (uint64, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), true
	}
	return 0, false
}

func uidGidOf(fi os.FileInfo) (uint32, uint32, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid, st.Gid, true
	}
	return 0, 0, false
}

func statFree(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}

func sameDevice(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	da, oka := devOf(fa)
	db, okb := devOf(fb)
	if !oka || !okb {
		return false, fmt.Errorf("cannot read device ids")
	}
	return da == db, nil
}

// mountInfo finds the mount that contains path, taking the longest matching
// mount point so nested mounts resolve correctly.
func mountInfo(path string) (mountPoint, fsType, options string, err error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", err
	}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()

	best := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		mp := strings.ReplaceAll(fields[1], `\040`, " ")
		if abs == mp || strings.HasPrefix(abs, strings.TrimSuffix(mp, "/")+"/") {
			if len(mp) > len(best) {
				best, fsType, options = mp, fields[2], fields[3]
			}
		}
	}
	if best == "" {
		return "", "", "", fmt.Errorf("no mount contains %s", abs)
	}
	return best, fsType, options, scanner.Err()
}
