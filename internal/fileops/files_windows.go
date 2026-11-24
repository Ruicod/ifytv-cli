package fileops

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32   = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx = kernel32.NewProc("LockFileEx")
	moveFileEx = kernel32.NewProc("MoveFileExW")
)

func TryLock(file *os.File) (bool, error) {
	const exclusiveFailImmediately = 0x02 | 0x01
	const lockViolation syscall.Errno = 33
	var offset syscall.Overlapped
	ok, _, err := lockFileEx.Call(file.Fd(), exclusiveFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&offset)))
	if ok != 0 {
		return true, nil
	}
	if err == lockViolation {
		return false, nil
	}
	return false, err
}

func Publish(temp, path string, replace bool) error {
	from, err := syscall.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	const writeThrough = 0x08
	const replaceExisting = 0x01
	flags := uintptr(writeThrough)
	if replace {
		flags |= replaceExisting
	}
	ok, _, moveErr := moveFileEx.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags)
	if ok == 0 {
		return moveErr
	}
	return nil
}
