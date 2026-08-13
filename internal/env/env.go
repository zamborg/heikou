// Package env names every environment variable Heikou reads or sets.
//
// The names were previously declared wherever they happened to be needed:
// HEIKOU_CLAUDE_BIN was a constant in internal/config, another constant in
// internal/runner, and a string literal in internal/supervisor's list of
// variables the tmux server must refresh. HEIKOU_STATE and HEIKOU_DATA were
// declared in internal/workstream and written out again in internal/home's
// migration table.
//
// Three copies of a name is not a style problem. It means the answer to "which
// variables does Heikou honour" is assembled from five files, so a new variable
// can be added to one surface and silently missed by the tmux refresh list —
// which is how a stale credential leaks into a later session. It also means a
// hermetic test cannot know what to blank without reading the source.
//
// This package is a leaf: it imports nothing else in the module, so any layer
// can name a variable without a dependency detour. An architecture test asserts
// that no HEIKOU_ literal is written anywhere else in non-test source.
package env

import (
	"os"
	"strings"
)

const (
	// Home overrides the single directory holding every Heikou file. Setting it
	// also suppresses the one-time migration off the pre-0.4 XDG layout: a
	// caller that chose a location owns it.
	Home = "HEIKOU_HOME"
	// Config, State and Data override individual paths inside the home
	// directory. They predate the consolidation and are still honoured.
	Config = "HEIKOU_CONFIG"
	State  = "HEIKOU_STATE"
	Data   = "HEIKOU_DATA"

	// TmuxSocket names the private tmux server. Tests rely on it to keep a run
	// off the developer's own sessions.
	TmuxSocket = "HEIKOU_TMUX_SOCKET"

	// DefaultRunner, CodexBinary and ClaudeBinary override settings that
	// otherwise come from config.json.
	DefaultRunner = "HEIKOU_DEFAULT_RUNNER"
	CodexBinary   = "HEIKOU_CODEX_BIN"
	ClaudeBinary  = "HEIKOU_CLAUDE_BIN"

	// SessionID is set by Heikou into an agent's environment rather than read
	// from the user, so a running agent can identify its own session.
	SessionID = "HEIKOU_SESSION_ID"

	// SessionRunner, SessionState, SessionRoot and SessionTitle describe one
	// session to a configured brief source. They are written outward only, into
	// that command's environment, and are never read back.
	//
	// The set is deliberately small. A brief source is told which session it is
	// describing, not what was said in it: the initial prompt and the messages
	// are the user's content, and wanting a status line in a row is not a reason
	// to hand what someone typed to another program on a timer.
	SessionRunner = "HEIKOU_SESSION_RUNNER"
	SessionState  = "HEIKOU_SESSION_STATE"
	SessionRoot   = "HEIKOU_SESSION_ROOT"
	SessionTitle  = "HEIKOU_SESSION_TITLE"
)

// Names lists every variable above, so that a test can assert this file is the
// only place in non-test source where a HEIKOU_ name is written, and a reader
// can answer "which variables does Heikou honour" from one list.
//
// It is deliberately not the tmux server's environment-refresh list. Those are
// different questions: that list is about which values must be re-read from an
// attaching client, and answering it wholesale from here would refresh
// HEIKOU_SESSION_ID, replacing a session's identity with the attacher's.
var Names = []string{
	Home, Config, State, Data,
	TmuxSocket,
	DefaultRunner, CodexBinary, ClaudeBinary,
	SessionID, SessionRunner, SessionState, SessionRoot, SessionTitle,
}

// Value reads a variable and trims it, treating whitespace as unset. Every
// caller wanted this; several wrote it out by hand.
func Value(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// ValueOr reads a variable, falling back when it is unset or blank.
func ValueOr(name, fallback string) string {
	if value := Value(name); value != "" {
		return value
	}
	return fallback
}
