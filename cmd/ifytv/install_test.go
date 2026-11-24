package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstalledCLIReportsVersionAndReinstallsFromItsOwnPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "portable home")
	t.Setenv("IFYTV_INSTALL_HOME", home)
	t.Setenv("IFYTV_TEST_PROCESS", "child")
	t.Setenv("IFYTV_BASE_URL", "invalid URL")
	t.Setenv("IFYTV_STATE_DIR", filepath.Join(home, "credentials"))
	run := func(program string, args ...string) string {
		t.Helper()
		args = append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)
		out, err := exec.Command(program, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("command failed: %s (%v)", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run(os.Args[0], "install", "--agent", "codex")
	claude := filepath.Join(home, ".claude", "skills", "ifytv", "SKILL.md")
	if _, err := os.Stat(claude); !os.IsNotExist(err) {
		t.Fatal("unexpected Claude installation", err)
	}
	name := "ifytv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	installed := filepath.Join(home, ".local", "bin", name)
	if got := run(installed, "--version"); got != version {
		t.Fatalf("--version: %q", got)
	}
	if got := run(installed, "install", "--help"); !strings.Contains(got, "--agent") {
		t.Fatalf("missing install help: %q", got)
	}
	run(installed, "install", "--agent", "claude-code")
	if _, err := os.Stat(claude); err != nil {
		t.Fatal("missing Claude Skill", err)
	}
	if _, err := os.Stat(filepath.Join(home, "credentials")); !os.IsNotExist(err) {
		t.Fatal("install touched credential storage", err)
	}
}
