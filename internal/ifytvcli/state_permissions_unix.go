//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ifytvcli

import "os"

func ensureStateDir(dir string) error { return os.MkdirAll(dir, 0700) }

func openStateLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
}

func openStateRecord(path string) (*os.File, error) { return os.Open(path) }

func createStateTemp(dir string) (*os.File, error) { return os.CreateTemp(dir, ".run-*") }
