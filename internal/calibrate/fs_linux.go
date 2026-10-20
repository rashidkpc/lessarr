//go:build linux

package calibrate

import (
	"fmt"
	"syscall"
)

const (
	cifsSuperMagic = 0xFF534D42
	nfsSuperMagic  = 0x6969
	sshfsMagic     = 0x65735546
)

func isRemoteFilesystem(path string) (bool, string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return false, "", fmt.Errorf("statfs %s: %w", path, err)
	}
	switch uint64(stat.Type) {
	case cifsSuperMagic:
		return true, "CIFS", nil
	case nfsSuperMagic:
		return true, "NFS", nil
	case sshfsMagic:
		return true, "FUSE/SSHFS", nil
	default:
		return false, "", nil
	}
}
