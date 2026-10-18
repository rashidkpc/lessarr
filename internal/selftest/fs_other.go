//go:build !linux

package selftest

import (
	"fmt"
	"os"
)

// The filesystem checks are Linux-specific (procfs, statfs). Elsewhere they
// degrade to "unknown" rather than breaking the build; lessarr's target is
// Linux and that is what CI exercises.

func devOf(os.FileInfo) (uint64, bool)            { return 0, false }
func uidGidOf(os.FileInfo) (uint32, uint32, bool) { return 0, 0, false }

func statFree(string) (uint64, uint64, error) {
	return 0, 0, fmt.Errorf("unsupported platform")
}

func sameDevice(string, string) (bool, error) {
	return false, fmt.Errorf("unsupported platform")
}

func mountInfo(string) (string, string, string, error) {
	return "", "", "", fmt.Errorf("unsupported platform")
}
