package install

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func put(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func contents(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpgradeRetainsEarlierFilesAndRerunDoesNotReplace(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "skills", "ifytv", "SKILL.md")
	custom := filepath.Join(filepath.Dir(target), "custom.txt")
	put(t, target, "previous manual installation")
	put(t, custom, "keep me")
	list := []targetFile{{path: target, body: []byte("published"), mode: 0644}}
	backups, err := applyFiles([]string{home}, list, nil)
	if err != nil || len(backups) != 1 {
		t.Fatal("upgrade did not retain prior file", backups, err)
	}
	if contents(t, backups[0]) != "previous manual installation" || contents(t, target) != "published" || contents(t, custom) != "keep me" {
		t.Fatal("wrong contents after upgrade")
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	backups, err = applyFiles([]string{home}, list, nil)
	after, statErr := os.Stat(target)
	if err != nil || statErr != nil || len(backups) != 0 || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("rerun rewrote an identical file", backups, err, statErr)
	}
}

func TestFailureRestoresPriorFilesAndNewFilesAreRemoved(t *testing.T) {
	home := t.TempDir()
	old := filepath.Join(home, "old")
	newFile := filepath.Join(home, "new")
	put(t, old, "original")
	injected := errors.New("cannot update user PATH")
	_, err := applyFiles([]string{home}, []targetFile{
		{path: old, body: []byte("upgrade"), mode: 0644},
		{path: newFile, body: []byte("new"), mode: 0644},
	}, func() error {
		if contents(t, old) != "upgrade" || contents(t, newFile) != "new" {
			t.Fatal("failure hook did not run after file installation")
		}
		return injected
	})
	if !errors.Is(err, injected) || contents(t, old) != "original" {
		t.Fatal("original contents lost", err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatal("partial install", err)
	}
}

func TestInstallerRefusesLinkedTargetAndDirectoryCollision(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "untouched")
	put(t, outside, "original")
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			bad := filepath.Join(home, kind)
			if kind == "directory" {
				if err := os.Mkdir(bad, 0755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(outside, bad); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			first := filepath.Join(home, "first")
			_, err := applyFiles([]string{home}, []targetFile{
				{path: first, body: []byte("first"), mode: 0644},
				{path: bad, body: []byte("bad"), mode: 0644},
			}, nil)
			if err == nil || contents(t, outside) != "original" {
				t.Fatal("accepted an unsafe install target")
			}
			if _, err := os.Stat(first); !os.IsNotExist(err) {
				t.Fatal("install partially published", err)
			}
		})
	}
}

func TestPortableHomeMustBeExplicitAbsoluteDirectory(t *testing.T) {
	for _, value := range []string{"", "relative"} {
		t.Setenv("IFYTV_INSTALL_HOME", value)
		if _, _, err := UserHome(); err == nil || !strings.Contains(err.Error(), "IFYTV_INSTALL_HOME") {
			t.Fatalf("unexpectedly accepted override %q: %v", value, err)
		}
	}
}

func TestConcurrentInstallerCannotEnterUntilFirstCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.lock")
	first, err := lockInstall(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := lockInstall(path); err == nil {
		second.Close()
		t.Fatal("second installer acquired the same lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	retry, err := lockInstall(path)
	if err != nil {
		t.Fatal("normal retry could not acquire released lock", err)
	}
	retry.Close()
}

func TestStagingCleanupKeepsReplacementDirectoryAndItsContents(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "SKILL.md")
	put(t, target, "old")
	staged, err := stage(targetFile{path: target, body: []byte("new"), mode: 0644})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged.dir, filepath.Join(home, "moved-stage")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(staged.dir, "next")
	put(t, other, "a different directory now occupies this name")
	if err := staged.clean(false); err == nil {
		t.Fatal("cleanup acted on a different directory")
	}
	if contents(t, other) != "a different directory now occupies this name" {
		t.Fatal("unrelated file changed")
	}
}

func linkDir(t *testing.T, dest, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dest); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	// Junctions are available without the Windows symlink privilege.
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"$ErrorActionPreference = 'Stop'; New-Item -ItemType Junction -Path $env:IFYTV_TEST_LINK -Target $env:IFYTV_TEST_TARGET | Out-Null")
	cmd.Env = append(os.Environ(), "IFYTV_TEST_LINK="+dest, "IFYTV_TEST_TARGET="+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
}

func TestWholeSkillAliasResolvesOnlyRecognizedAgentDestination(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".agents", "skills", "ifytv")
	claude := filepath.Join(home, ".claude", "skills", "ifytv")
	other := filepath.Join(home, ".other", "skills", "ifytv")
	external := t.TempDir()
	if err := os.MkdirAll(shared, 0755); err != nil {
		t.Fatal(err)
	}
	linkDir(t, claude, shared)
	linkDir(t, other, external)
	for _, dirs := range [][]string{{shared, claude}, {claude}} {
		got, err := physicalSkillDirs([]string{home}, home, dirs)
		if err != nil || len(got) != 1 || pathKey(got[0]) != pathKey(shared) {
			t.Fatalf("failed shared Skill: %q, %v", got, err)
		}
	}
	if _, err := physicalSkillDirs([]string{home}, home, []string{other}); err == nil {
		t.Fatal("accepted external Skill link")
	}
	ancestor := filepath.Join(home, "linked-parent")
	linkDir(t, ancestor, external)
	dest := filepath.Join(ancestor, "new", "SKILL.md")
	if _, err := applyFiles([]string{home}, []targetFile{{path: dest, body: []byte("oops"), mode: 0644}}, nil); err == nil {
		t.Fatal("traversed linked parent")
	}
	if entries, err := os.ReadDir(external); err != nil || len(entries) != 0 {
		t.Fatal("external target altered", entries, err)
	}
}
