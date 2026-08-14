// Package brief assembles the one-line summary a session row shows between its
// state and its runtime — the cell that answers "what is this session?".
//
// It is a package rather than a few functions in the UI because filling that
// cell can mean running a program, and process execution does not belong in the
// package that draws frames. What remains in internal/ui is the rendering: the
// width budgets, the separator, and the mark an unproven fragment carries.
package brief

import (
	"strings"
	"time"

	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/format"
)

// SourceID names a source in configuration and in provenance.
type SourceID string

const (
	// SourceTitle is the durable title the user gave the session.
	SourceTitle SourceID = "title"
	// SourcePrompt is the immutable task the session was launched with.
	SourcePrompt SourceID = "prompt"
	// SourceLatest is the most recent message routed through Heikou. Text typed
	// directly into an attached native TUI is not observable and never appears.
	SourceLatest SourceID = "latest"
	// SourceActivity is the last thing the runner recorded the session doing.
	//
	// It is the first built-in that observes the agent rather than restating
	// something Heikou already knew, and it is the first that cannot prove what
	// it says: the phrase is derived from a record another program wrote, so it
	// renders with the approximate mark. Its text is filled by an Observer, and
	// a session with nothing cached falls through to the next source.
	SourceActivity SourceID = "activity"
	// SourceRunner is the last-resort label for a session with no other text.
	SourceRunner SourceID = "runner"
)

// A Fragment is one source's contribution to a Brief.
type Fragment struct {
	Text   string
	Source SourceID
	// Proven marks text Heikou can defend from durable state or a tmux
	// observation. A source that derives, summarizes, guesses, or asks another
	// program must leave it false, and the row then marks the text approximate.
	//
	// Configured command sources are never proven. The command may well be
	// reporting ground truth from the runner, but Heikou cannot check that, and
	// the distinction the mark draws is between what Heikou observed and what it
	// was told. Reporting an exit code tmux cannot prove as zero would be the
	// same claim in a different field.
	Proven bool
}

func (f Fragment) Empty() bool { return strings.TrimSpace(f.Text) == "" }

// Label names a source for surfaces with a whole line to spend. Rows
// deliberately do not use these: "latest via Heikou · " is twenty columns
// before a single character of message.
func (id SourceID) Label() string {
	switch id {
	case SourceLatest:
		return "latest via Heikou"
	case SourcePrompt:
		return "initial task"
	case SourceTitle:
		return "title"
	case SourceActivity:
		return "runner activity"
	case SourceRunner:
		return "runner"
	default:
		return string(id)
	}
}

func (f Fragment) Label() string { return f.Source.Label() }

// A Brief has two slots. The lead is always rendered; the detail sits behind a
// separator and yields first when the row is narrow.
type Brief struct {
	Lead   Fragment
	Detail Fragment
}

// A Source contributes at most one fragment for a session. Empty text means the
// source has nothing to say about this session right now, and the slot falls
// through to the next source in the layout.
//
// Fragment must not block. It is called for every visible row on every frame,
// and the dashboard refreshes once a second. A source backed by a subprocess or
// a network call reads a cache here and does its work in an Observer, which
// owns its own cadence.
type Source interface {
	ID() SourceID
	Fragment(control.Session) Fragment
}

// Registry resolves a source name to the source that fills it.
type Registry map[SourceID]Source

// sourceFunc adapts a plain text function to Source, mirroring the
// AuthorizeFunc and ResolveCommandFunc adapters in internal/control.
type sourceFunc struct {
	id     SourceID
	proven bool
	text   func(control.Session) string
}

func (s sourceFunc) ID() SourceID { return s.id }

func (s sourceFunc) Fragment(session control.Session) Fragment {
	return Fragment{Text: briefText(s.text(session)), Source: s.id, Proven: s.proven}
}

// observedSource reads whatever an Observer last recorded. It never runs
// anything itself, which is what keeps a configured command out of the render
// path.
type observedSource struct {
	id           SourceID
	observations Observations
}

func (s observedSource) ID() SourceID { return s.id }

func (s observedSource) Fragment(session control.Session) Fragment {
	observation, ok := s.observations[Key{Session: session.ID, Source: s.id}]
	if !ok || strings.TrimSpace(observation.Text) == "" {
		return Fragment{}
	}
	return Fragment{Text: observation.Text, Source: s.id}
}

// A Layout is the ordered preference for each slot: the first source with
// something to say fills it.
type Layout struct {
	Lead   []SourceID
	Detail []SourceID
}

func LayoutFrom(settings config.BriefConfig) Layout {
	return Layout{Lead: toSourceIDs(settings.Lead), Detail: toSourceIDs(settings.Detail)}
}

func toSourceIDs(names []string) []SourceID {
	ids := make([]SourceID, 0, len(names))
	for _, name := range names {
		ids = append(ids, SourceID(name))
	}
	return ids
}

// NewRegistry builds the sources a layout can draw on: the built-ins, plus one
// cache-reading source for every configured command. Configuration has already
// rejected a slot naming anything that is not in one of those two sets.
func NewRegistry(settings config.BriefConfig, observations Observations) Registry {
	registry := Registry{
		SourceTitle: sourceFunc{id: SourceTitle, proven: true,
			text: func(session control.Session) string { return session.Record.Title }},
		SourcePrompt: sourceFunc{id: SourcePrompt, proven: true,
			text: func(session control.Session) string { return session.Prompt }},
		SourceLatest: sourceFunc{id: SourceLatest, proven: true,
			text: func(session control.Session) string { return session.LastUserMessage }},
		SourceRunner: sourceFunc{id: SourceRunner, proven: true,
			text: func(session control.Session) string { return string(session.Backend) + " session" }},
		// The activity source reads the same cache a configured command does.
		// That is the whole reason the cache exists: a render must not be the
		// thing that opens a file, and a source with nothing cached must fall
		// through rather than render blank.
		SourceActivity: observedSource{id: SourceActivity, observations: observations},
	}
	for name := range settings.Sources {
		id := SourceID(name)
		registry[id] = observedSource{id: id, observations: observations}
	}
	return registry
}

// Resolve walks each slot in order and takes the first source that produces
// text.
//
// A source already spent on the lead is skipped in the detail. That one rule
// replaces the special case the row used to carry — "show the initial task as
// detail, but only when a title exists" — which was that rule written out for
// the single case that existed.
func (l Layout) Resolve(session control.Session, registry Registry) Brief {
	lead := firstFragment(session, registry, l.Lead, "")
	return Brief{
		Lead:   lead,
		Detail: firstFragment(session, registry, l.Detail, lead.Source),
	}
}

func firstFragment(session control.Session, registry Registry, order []SourceID, spent SourceID) Fragment {
	for _, id := range order {
		if id == spent {
			continue
		}
		source, ok := registry[id]
		if !ok {
			continue
		}
		if fragment := source.Fragment(session); !fragment.Empty() {
			return fragment
		}
	}
	return Fragment{}
}

// Key identifies one source's observation of one session.
type Key struct {
	Session string
	Source  SourceID
}

// An Observation is what a configured command last reported.
type Observation struct {
	Text       string
	ObservedAt time.Time
	// ActivityAt is the session's terminal activity at the moment this was
	// recorded. It is the change-detection signal: a session that has not done
	// anything since is not worth asking about again.
	ActivityAt time.Time
}

// Observations is an immutable snapshot, replaced wholesale rather than
// mutated, so a source that captured one can be read from the render path
// without a lock.
type Observations map[Key]Observation

// briefText is format.OneLine bounded to what a row can hold.
func briefText(value string) string {
	value = format.OneLine(value)
	if len([]rune(value)) <= maxTextRunes {
		return value
	}
	return string([]rune(value)[:maxTextRunes])
}
