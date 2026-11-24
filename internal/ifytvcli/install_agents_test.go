package ifytvcli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstallSkillRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("IFYTV_INSTALL_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	path := func(rel string) string { return filepath.Join(home, filepath.FromSlash(rel)) }
	dest := func(rel string) string { return filepath.Join(path(rel), "skills", "ifytv") }
	base := []string{dest(".agents"), dest(".claude")}
	check := func(agent string, want []string) {
		t.Helper()
		got, err := installSkillRoots(home, agent)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("agent %q returned %v, %v; want %v", agent, got, err, want)
		}
	}
	check("", base)
	check("codex", base[:1])
	check("claude-code", base[1:])
	for _, rel := range []string{".cursor", ".gemini", ".copilot", ".config/opencode", ".codeium/windsurf", ".kiro", ".moltbot"} {
		if err := os.MkdirAll(path(rel), 0700); err != nil {
			t.Fatal(err)
		}
	}
	check("both", base)
	check("auto", append(base, dest(".cursor"), dest(".gemini"), dest(".copilot"), dest(".config/opencode"), dest(".codeium/windsurf"), dest(".kiro"), dest(".moltbot")))
	check("openclaw", []string{dest(".moltbot")})
	check("windsurf", []string{dest(".codeium/windsurf")})
	if _, err := installSkillRoots(home, "../invalid"); err == nil {
		t.Fatal("invalid Agent accepted")
	}
}

func TestInstallSkillRootsConfiguredDirectories(t *testing.T) {
	home := t.TempDir()
	configured := t.TempDir()
	t.Setenv("IFYTV_INSTALL_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", configured)
	t.Setenv("XDG_CONFIG_HOME", configured)
	for agent, want := range map[string]string{
		"claude-code": filepath.Join(configured, "skills", "ifytv"),
		"opencode":    filepath.Join(configured, "opencode", "skills", "ifytv"),
	} {
		got, err := installSkillRoots(home, agent)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("%s => %v, %v; want %s", agent, got, err, want)
		}
	}
}
