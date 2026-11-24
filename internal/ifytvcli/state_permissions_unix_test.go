//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ifytvcli

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixDeviceSecretsStayInPrivateRegularFiles(t *testing.T) {
	testSite(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, challengeJSON) })
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	files, err := filepath.Glob(filepath.Join(os.Getenv("IFYTV_STATE_DIR"), "auth", "*", "session.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing device state", err)
	}
	file, dir := files[0], filepath.Dir(files[0])
	for path, mode := range map[string]os.FileMode{dir: 0700, file: 0600, filepath.Join(dir, "session.lock"): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("insecure device storage", path, err)
		}
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	expectError(t, authCommand("status").result(t, false), "unsafe_state_permissions")
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	expectError(t, authCommand("status").result(t, false), "unsafe_state_permissions")
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file, file+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file+".original", file); err != nil {
		t.Fatal(err)
	}
	expectError(t, authCommand("status").result(t, false), "unsafe_state_permissions")
}

func TestUnixDeviceRejectsWritableOrLinkedParents(t *testing.T) {
	for _, scenario := range []string{"writable root", "writable auth", "linked auth", "readable root"} {
		t.Run(scenario, func(t *testing.T) {
			testSite(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, challengeJSON) })
			t.Setenv("IFYTV_API_KEY", "")
			root := os.Getenv("IFYTV_STATE_DIR")
			dir := filepath.Join(root, "auth")
			switch scenario {
			case "writable root":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
			case "writable auth":
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0777); err != nil {
					t.Fatal(err)
				}
			case "linked auth":
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Fatal(err)
				}
			case "readable root":
				if err := os.Chmod(root, 0755); err != nil {
					t.Fatal(err)
				}
				authResult(t, authCommand("login"), true, "authorization_required")
				return
			}
			expectError(t, authCommand("login").result(t, false), "unsafe_state_permissions")
		})
	}
}

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
