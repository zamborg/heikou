// Package manageheikou exposes the pilot's operating instructions to the
// installed Heikou binary, so a fresh installation can write them into the
// Heikou home directory without a network fetch or a checkout.
package manageheikou

import _ "embed"

// Agents is the generic entry-point instruction file. It is written to
// AGENTS.md, which both Codex and Claude Code read, so the pilot does not
// depend on a single vendor's convention.
//
//go:embed AGENTS.md
var Agents string

// Skill is the full command reference the instructions point at.
//
//go:embed SKILL.md
var Skill string
