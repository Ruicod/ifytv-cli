//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ifytvcli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixPrivateRunPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	scope := filepath.Join(root, "runs", "scope")
	if err := ensureStateDir(scope); err != nil {
		t.Fatal(err)
	}
	lock, err := openStateLock(filepath.Join(scope, "shot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	temp, err := createStateTemp(scope)
	if err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(scope, "shot.json")
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publishFile(temp.Name(), final, true); err != nil {
		t.Fatal(err)
	}
	r, err := openStateRecord(final)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for path, mode := range map[string]os.FileMode{root: 0755, scope: 0700, lock.Name(): 0600, final: 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode %o, want %o", filepath.Base(path), info.Mode().Perm(), mode)
		}
	}
}
