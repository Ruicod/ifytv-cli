// Package skills contains the offline IFYTV Skill distributed with the CLI.
package skills

import "embed"

// Files contains the same resources as the standalone Skill ZIP.
//
//go:embed ifytv/SKILL.md ifytv/examples/h3.json
//go:embed ifytv/references/assets.md ifytv/references/interfaces.md ifytv/references/recovery.md
var Files embed.FS

// ResourcePaths lists the published files relative to the Skill directory.
func ResourcePaths() []string {
	return []string{
		"SKILL.md",
		"examples/h3.json",
		"references/assets.md",
		"references/interfaces.md",
		"references/recovery.md",
	}
}
