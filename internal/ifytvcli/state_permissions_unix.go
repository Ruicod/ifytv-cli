//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ifytvcli

import (
	"errors"
	"os"
	"syscall"
)

func ensureStateDir(dir string) error { return os.MkdirAll(dir, 0700) }

func openStateLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
}

func openStateRecord(path string) (*os.File, error) { return os.Open(path) }

func createStateTemp(dir string) (*os.File, error) { return os.CreateTemp(dir, ".run-*") }

func ensureDeviceRoot(dir string) error {
	if err := ensureStateDir(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) || info.Mode().Perm()&0022 != 0 {
		return errUnsafeStateACL
	}
	return nil
}

func ensureDeviceDir(dir string) error {
	if err := ensureStateDir(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !privateDeviceObject(info) {
		return errUnsafeStateACL
	}
	return nil
}

func privateDeviceObject(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0
}

func openDeviceLock(path string) (*os.File, error) {
	return openDeviceFile(path, os.O_RDWR|os.O_CREATE)
}

func openDeviceRecord(path string) (*os.File, error) {
	return openDeviceFile(path, os.O_RDONLY)
}

func openDeviceFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if errors.Is(err, syscall.ELOOP) {
		return nil, errUnsafeStateACL
	}
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || !privateDeviceObject(info) {
		f.Close()
		return nil, errUnsafeStateACL
	}
	return f, nil
}
