package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestShellPathPreservesChosenProfilesAndAddsDirectoryOnce(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "sh"} {
		t.Run(shell, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "config with spaces")
			zdir := filepath.Join(home, ".zdot")
			want := []string{filepath.Join(home, ".profile")}
			switch shell {
			case "bash":
				put(t, filepath.Join(home, ".bash_login"), "only this login profile wins")
				want = []string{filepath.Join(home, ".bash_login"), filepath.Join(home, ".bashrc")}
			case "zsh":
				want = []string{filepath.Join(zdir, ".zprofile"), filepath.Join(zdir, ".zshrc")}
			case "fish":
				want = []string{filepath.Join(config, "fish", "conf.d", "ifytv-path.fish")}
			}
			for _, path := range want {
				put(t, path, "# unrelated line")
			}
			files, err := shellProfiles(home, shell, config, zdir)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := applyFiles([]string{home}, files, nil); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range want {
				text := contents(t, path)
				if !strings.HasPrefix(text, "# unrelated line\n") || strings.Count(text, "# IFYTV CLI") != 1 || !strings.Contains(text, "$HOME/.local/bin") {
					t.Errorf("unexpected profile %s: %q", path, text)
				}
				info, err := os.Stat(path)
				if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
					t.Errorf("profile permissions changed: %v, %v", info, err)
				}
			}
		})
	}
}
