//go:build !linux

package calibrate

func isRemoteFilesystem(string) (bool, string, error) { return false, "", nil }
