package ifytvcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallIsOfflineAndIncludesPublishedSkill(t *testing.T) {
	home := filepath.Join(t.TempDir(), "安装 home")
	t.Setenv("IFYTV_INSTALL_HOME", home)
	t.Setenv("IFYTV_BASE_URL", "invalid URL")
	// Offline install must report the exact paths even with unrelated auth
	// configuration left over from an earlier session.
	t.Setenv("IFYTV_API_KEY", ".local")
	t.Setenv("IFYTV_STATE_DIR", filepath.Join(home, "untouched-state"))
	result := invoke(context.Background(), "install")
	if result.code != 0 {
		t.Fatalf("install: %+v", result)
	}
	name := "ifytv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(home, ".local", "bin", name)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if !bytes.Equal(read(self), read(executable)) || !strings.Contains(result.out, executable) {
		t.Fatal("installed executable or reported path differs")
	}
	for _, agent := range []string{".agents", ".claude"} {
		root := filepath.Join(home, agent, "skills", "ifytv")
		if !strings.Contains(result.out, root) {
			t.Fatalf("missing installed Skill path %s in %s", root, result.out)
		}
		for _, file := range []string{"SKILL.md", "examples/h3.json"} {
			want := read(filepath.Join("..", "..", "skills", "ifytv", file))
			got := read(filepath.Join(root, file))
			if !bytes.Equal(want, got) || bytes.Contains(got, []byte("\r\n")) {
				t.Fatalf("installed %s is not the published LF file", file)
			}
		}
	}
	for _, path := range []string{"untouched-state", ".profile", ".bashrc", ".zshrc"} {
		if _, err := os.Lstat(filepath.Join(home, path)); !os.IsNotExist(err) {
			t.Fatalf("unexpected change to %s: %v", path, err)
		}
	}
}

func TestInstallHelpAndInvalidAgentHaveNoSideEffects(t *testing.T) {
	home := filepath.Join(t.TempDir(), "untouched")
	t.Setenv("IFYTV_INSTALL_HOME", home)
	t.Setenv("IFYTV_BASE_URL", "invalid URL")
	for _, args := range [][]string{{"--help"}, {"install", "--help"}} {
		if got := invoke(context.Background(), args...); got.code != 0 || !strings.Contains(got.out, "ifytv install") {
			t.Fatalf("help: %+v", got)
		}
	}
	if got := invoke(context.Background(), "install", "--agent", "unknown"); got.code != 2 {
		t.Fatalf("invalid agent: %+v", got)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatal("invalid or informational command modified install home", err)
	}
}
