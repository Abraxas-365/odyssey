// Package skills holds the agent skills Odicea ships and serves: today only
// "odicea", which teaches a coding assistant (Claude Code, Codex, VS Code,
// Antigravity) to prepare an app and publish it through Odicea's MCP tools.
// The files under odicea/ are the single source; Bundle packs them into the
// tar.gz the API serves at GET /skills/odicea.tar.gz.
package skills

import "embed"

// Name is the skill's name: its directory, its frontmatter name, and the
// top directory of the archive.
const Name = "odicea"

// FS holds the skill's files (odicea/SKILL.md, odicea/references/*.md).
//
//go:embed odicea
var FS embed.FS
