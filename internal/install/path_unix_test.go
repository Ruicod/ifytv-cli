//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginShellPrioritizesNewCLIWithoutDuplicatingOrLosingPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "my [home]")
	bin := filepath.Join(home, ".local", "bin")
	old := filepath.Join(home, "old-go-bin")
	put(t, filepath.Join(bin, "ifytv"), "#!/bin/sh\nexit 0\n")
	put(t, filepath.Join(old, "ifytv"), "#!/bin/sh\nexit 1\n")
	for _, dir := range []string{bin, old} {
		if err := os.Chmod(filepath.Join(dir, "ifytv"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	profiles, err := shellProfiles(home, "sh", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyFiles([]string{home}, profiles, nil); err != nil {
		t.Fatal(err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, initial := range []string{old + ":" + bin, old + ":" + bin + "::/bin:", bin + ":" + old, old + ":/bin"} {
		cmd := exec.Command(sh, "-c", `. "$HOME/.profile"; . "$HOME/.profile"; command -v ifytv; printf '%s' "$PATH"`)
		cmd.Env = []string{"HOME=" + home, "PATH=" + initial}
		out, err := cmd.CombinedOutput()
		entries := []string{bin}
		for _, entry := range strings.Split(initial, ":") {
			if entry != bin {
				entries = append(entries, entry)
			}
		}
		want := filepath.Join(bin, "ifytv") + "\n" + strings.Join(entries, ":")
		if err != nil || string(out) != want {
			t.Errorf("PATH %q: got %q, want %q (%v)", initial, out, want, err)
		}
	}
}

func TestSharedHomeDotfileLinkIsPreservedAndExternalLinkIsRejected(t *testing.T) {
	home := t.TempDir()
	dotfile := filepath.Join(home, "dotfiles", "bash-env")
	put(t, dotfile, "# private settings\n")
	for _, name := range []string{".profile", ".bashrc"} {
		if err := os.Symlink(filepath.Join("dotfiles", "bash-env"), filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := shellProfiles(home, "bash", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyFiles([]string{home}, files, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".profile", ".bashrc"} {
		if _, err := os.Readlink(filepath.Join(home, name)); err != nil {
			t.Fatalf("dotfile link was replaced: %v", err)
		}
	}
	b := contents(t, dotfile)
	if !strings.HasPrefix(b, "# private settings\n") || strings.Count(b, "# IFYTV CLI") != 1 {
		t.Fatal("shared profile was lost or installed twice", b)
	}
	external := filepath.Join(t.TempDir(), "elsewhere")
	put(t, external, "untouched")
	bad := filepath.Join(home, ".zshrc")
	if err := os.Symlink(external, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFiles([]string{home}, []targetFile{{path: bad, body: []byte(shPath), mode: 0644, profile: true}}, nil); err == nil || contents(t, external) != "untouched" {
		t.Fatal("wrote to an external profile")
	}
}
