//go:build !linux

package daemon

func acquireProcessLock(string) (func(), error) {
	return func() {}, nil
}
