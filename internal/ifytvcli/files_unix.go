//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ifytvcli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func tryFileLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func publishFile(temp, path string, replace bool) error {
	var err error
	if replace {
		err = os.Rename(temp, path)
	} else {
		// A hard link atomically claims the final name, including against
		// another writer creating it after the download started.
		err = os.Link(temp, path)
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
