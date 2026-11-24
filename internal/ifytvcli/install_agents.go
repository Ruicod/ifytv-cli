package ifytvcli

import (
	"fmt"
	"os"
	"path/filepath"
)

func installSkillRoots(home, agent string) ([]string, error) {
	configHome := filepath.Join(home, ".config")
	claudeHome := filepath.Join(home, ".claude")
	// A portable installation must keep all Agent files under its chosen home.
	if os.Getenv("IFYTV_INSTALL_HOME") == "" {
		if path := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(path) {
			configHome = path
		}
		if path := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(path) {
			claudeHome = path
		}
	}
	openClaw := filepath.Join(home, ".openclaw")
	for _, name := range []string{".openclaw", ".clawdbot", ".moltbot"} {
		if path := filepath.Join(home, name); isAgentDirectory(path) {
			openClaw = path
			break
		}
	}
	agents := []struct {
		name string
		dir  string
	}{
		{"codex", filepath.Join(home, ".agents")},
		{"claude-code", claudeHome},
		{"cursor", filepath.Join(home, ".cursor")},
		{"gemini-cli", filepath.Join(home, ".gemini")},
		{"github-copilot", filepath.Join(home, ".copilot")},
		{"opencode", filepath.Join(configHome, "opencode")},
		{"windsurf", filepath.Join(home, ".codeium", "windsurf")},
		{"kiro-cli", filepath.Join(home, ".kiro")},
		{"openclaw", openClaw},
	}
	var roots []string
	for _, a := range agents {
		defaultAgent := a.name == "codex" || a.name == "claude-code"
		selected := a.name == agent
		switch agent {
		case "", "auto":
			selected = defaultAgent || isAgentDirectory(a.dir)
		case "both":
			selected = defaultAgent
		}
		if selected {
			roots = append(roots, filepath.Join(a.dir, "skills", "ifytv"))
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	return roots, nil
}

func isAgentDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
