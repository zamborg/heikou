// Package learnheikou exposes the agent-readable onboarding skill to the
// installed Heikou binary.
package learnheikou

import _ "embed"

// Instructions is the canonical onboarding skill used by h quickstart.
//
//go:embed SKILL.md
var Instructions string
