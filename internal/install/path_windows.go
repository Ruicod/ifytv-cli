package install

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"
)

var (
	registry      = syscall.NewLazyDLL("advapi32.dll")
	createKey     = registry.NewProc("RegCreateKeyExW")
	setValue      = registry.NewProc("RegSetValueExW")
	expandEnv     = syscall.NewLazyDLL("kernel32.dll").NewProc("ExpandEnvironmentStringsW")
	notifySetting = syscall.NewLazyDLL("user32.dll").NewProc("SendMessageTimeoutW")
)

func planPath(home string) ([]targetFile, func() error, error) {
	dir := filepath.Join(home, ".local", "bin")
	return nil, func() error { return persistUserPath(dir) }, nil
}

func expandWindowsEnv(raw string) (string, error) {
	src, err := syscall.UTF16PtrFromString(raw)
	if err != nil {
		return "", err
	}
	var buf []uint16
	var dst *uint16
	for {
		n, _, callErr := expandEnv.Call(uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)), uintptr(len(buf)))
		if n == 0 {
			return "", callErr
		}
		if int(n) <= len(buf) {
			return syscall.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n)
		dst = &buf[0]
	}
}

func updatedUserPath(raw string, expandable bool, dir string) (string, error) {
	if strings.Contains(dir, ";") {
		return "", errors.New("the user home contains the PATH separator ';'; use IFYTV_INSTALL_HOME for a portable install")
	}
	list := raw
	entry := dir
	if expandable {
		var err error
		if list, err = expandWindowsEnv(raw); err != nil {
			return "", err
		}
		// Referencing USERPROFILE keeps percent characters inside a home directory
		// literal while preserving all other expandable registry entries.
		const fromProfile = `%USERPROFILE%\.local\bin`
		profile, err := expandWindowsEnv(fromProfile)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(filepath.Clean(profile), filepath.Clean(dir)) {
			entry = fromProfile
		} else if strings.Contains(dir, "%") {
			return "", errors.New("the expandable user PATH cannot represent this home directory")
		}
	}
	matches := func(path string) bool { return strings.EqualFold(filepath.Clean(path), filepath.Clean(dir)) }
	if parts := filepath.SplitList(list); len(parts) > 0 && matches(parts[0]) {
		return raw, nil
	}
	if raw == "" {
		return entry, nil
	}
	// Move a later dedicated entry to the front without rewriting the spelling
	// of anything else. A variable can expand to several directories: keep
	// such expressions intact even if they also contain the installation.
	kept := []string{entry}
	for _, original := range splitRawPath(raw) {
		value := original
		if expandable {
			var err error
			if value, err = expandWindowsEnv(value); err != nil {
				return "", err
			}
		}
		parts := filepath.SplitList(value)
		if len(parts) == 1 && matches(parts[0]) {
			kept[0] = original
		} else {
			kept = append(kept, original)
		}
	}
	return strings.Join(kept, ";"), nil
}

func splitRawPath(raw string) []string {
	var parts []string
	start, quoted := 0, false
	for i, c := range raw {
		switch c {
		case '"':
			quoted = !quoted
		case ';':
			if !quoted {
				parts = append(parts, raw[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, raw[start:])
}

func readRegistryPath(key syscall.Handle, name *uint16) (string, uint32, error) {
	var kind, size uint32
	err := syscall.RegQueryValueEx(key, name, nil, &kind, nil, &size)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return "", syscall.REG_EXPAND_SZ, nil
	}
	for err == nil || errors.Is(err, syscall.ERROR_MORE_DATA) {
		buf := make([]uint16, (size+1)/2+1)
		n := uint32(len(buf) * 2)
		err = syscall.RegQueryValueEx(key, name, nil, &kind, (*byte)(unsafe.Pointer(&buf[0])), &n)
		size = n
		if err != nil {
			continue
		}
		if kind != syscall.REG_EXPAND_SZ && kind != syscall.REG_SZ {
			return "", 0, errors.New("user Path registry value must be REG_SZ or REG_EXPAND_SZ")
		}
		if n%2 != 0 {
			return "", 0, errors.New("invalid UTF-16 in user Path registry value")
		}
		chars := buf[:n/2]
		if len(chars) > 0 && chars[len(chars)-1] == 0 {
			chars = chars[:len(chars)-1]
		}
		for _, c := range chars {
			if c == 0 {
				return "", 0, errors.New("unexpected NUL within the user Path registry value")
			}
		}
		raw := syscall.UTF16ToString(chars)
		encoded, err := syscall.UTF16FromString(raw)
		if err != nil || !slices.Equal(chars, encoded[:len(encoded)-1]) {
			return "", 0, errors.New("invalid UTF-16 in user Path registry value")
		}
		return raw, kind, nil
	}
	return "", 0, err
}

func persistUserPath(dir string) error {
	envKey, _ := syscall.UTF16PtrFromString("Environment")
	name, _ := syscall.UTF16PtrFromString("Path")
	var key syscall.Handle
	code, _, _ := createKey.Call(syscall.HKEY_CURRENT_USER, uintptr(unsafe.Pointer(envKey)), 0, 0, 0,
		syscall.KEY_QUERY_VALUE|syscall.KEY_SET_VALUE, 0, uintptr(unsafe.Pointer(&key)), 0)
	if code != 0 {
		return fmt.Errorf("open HKCU\\Environment: %w", syscall.Errno(code))
	}
	defer syscall.RegCloseKey(key)
	raw, kind, err := readRegistryPath(key, name)
	if err != nil {
		return err
	}
	next, err := updatedUserPath(raw, kind == syscall.REG_EXPAND_SZ, dir)
	if err != nil || raw == next {
		return err
	}
	data, err := syscall.UTF16FromString(next)
	if err != nil {
		return err
	}
	current, currentKind, err := readRegistryPath(key, name)
	if err != nil {
		return err
	}
	if current != raw || currentKind != kind {
		return errors.New("user Path changed during installation; rerun to preserve the new value")
	}
	code, _, _ = setValue.Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, uintptr(kind),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	if code != 0 {
		return fmt.Errorf("write HKCU\\Environment\\Path: %w", syscall.Errno(code))
	}
	// New processes started by Explorer inherit the updated environment.
	const hwndBroadcast, wmSettingChange, abortIfHung = 0xffff, 0x001a, 0x0002
	notifySetting.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(envKey)), abortIfHung, 250, 0)
	return nil
}
