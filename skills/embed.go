// Package skills contains the offline IFYTV Skill distributed with the CLI.
package skills

import "embed"

// Files contains the same two resources as the standalone Skill ZIP.
//
//go:embed ifytv/SKILL.md ifytv/examples/h3.json
var Files embed.FS
