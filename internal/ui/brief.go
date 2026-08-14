package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/zamborg/heikou/internal/brief"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/format"
)

const (
	briefSeparator       = " ↳ "
	briefApproximateMark = "~"
	briefMinLeadWidth    = 8
	briefMinDetailWidth  = 12
)

// displayFragment is a fragment as it appears on screen, carrying its own
// provenance. The mark is a glyph rather than a dim style because a row has to
// stay readable in a terminal with no color.
func displayFragment(fragment brief.Fragment) string {
	if fragment.Proven || fragment.Empty() {
		return fragment.Text
	}
	return briefApproximateMark + fragment.Text
}

// renderBrief lays the two slots against separate budgets.
//
// Concatenating them into one truncation was what made the cell hard to read:
// the detail could be cut to a meaningless fragment while the lead still had
// room, and what you would actually see could not be predicted from the row
// width. Here the lead may take what it needs, but never so much that the
// detail drops below a useful minimum, and when there is no room for both the
// lead wins outright rather than showing a stub of the second.
func renderBrief(item brief.Brief, width int) string {
	if width <= 0 {
		return ""
	}
	lead := displayFragment(item.Lead)
	if item.Detail.Empty() {
		return truncatePlain(lead, width)
	}
	separator := lipgloss.Width(briefSeparator)
	leadBudget := width - separator - briefMinDetailWidth
	if leadBudget < briefMinLeadWidth {
		return truncatePlain(lead, width)
	}
	rendered := truncatePlain(lead, leadBudget)
	remaining := width - lipgloss.Width(rendered) - separator
	if remaining < briefMinDetailWidth {
		return truncatePlain(lead, width)
	}
	return rendered + briefSeparator + truncatePlain(displayFragment(item.Detail), remaining)
}

// briefLayout and briefRegistry are derived on demand rather than cached on the
// Model. Both are pure functions of settings and the current observations,
// which the Model already holds, and building them costs a few hundred
// nanoseconds against a one-second refresh. Caching them bought nothing and
// added the one failure this feature does not need: a cache that can disagree
// with the settings it came from.
func (m Model) briefLayout() brief.Layout { return brief.LayoutFrom(m.settings.Brief) }

func (m Model) briefRegistry() brief.Registry {
	return brief.NewRegistry(m.settings.Brief, m.briefObservations)
}

func (m Model) sessionBrief(session control.Session) brief.Brief {
	return m.briefLayout().Resolve(session, m.briefRegistry())
}

// sessionDisplayTitle is the brief's lead on its own, for surfaces that name a
// session in running text rather than lay out a row.
func (m Model) sessionDisplayTitle(session control.Session) string {
	return displayFragment(m.sessionBrief(session).Lead)
}

// sessionSecondaryDetail is the brief's detail with its source named, for the
// details pane, which has a whole line to spend and should be precise. The
// label follows whichever source filled the slot, so the pane cannot drift from
// the row beside it.
func (m Model) sessionSecondaryDetail(session control.Session) string {
	detail := m.sessionBrief(session).Detail
	if detail.Empty() {
		return ""
	}
	return detail.Label() + " · " + displayFragment(detail)
}

func (m Model) sessionRowSummary(session control.Session, width int) string {
	return renderBrief(m.sessionBrief(session), width)
}

// briefSlotSummary describes a slot's preference order in the words the help
// glossary uses. The help screen generates its entry from the live layout for
// the same reason it generates composer keys from settings: a glossary that
// restates configurable behaviour in prose is a glossary that goes stale.
func briefSlotSummary(order []brief.SourceID) string {
	if len(order) == 0 {
		return "nothing"
	}
	labels := make([]string, 0, len(order))
	for _, id := range order {
		labels = append(labels, id.Label())
	}
	if len(labels) == 1 {
		return labels[0]
	}
	return strings.Join(labels[:len(labels)-1], ", ") + ", then " + labels[len(labels)-1]
}

// briefHealthLines reports what the last observation pass did that a user might
// need to act on. It returns nothing when the sources are behaving, so a
// healthy configuration reads as quiet rather than as a status block.
func (m Model) briefHealthLines() []string {
	var lines []string
	if m.briefReport.Deferred > 0 {
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("   %d session updates deferred to the next pass",
			m.briefReport.Deferred)))
	}
	for _, failure := range m.briefReport.Failures {
		lines = append(lines, failedStyle.Render("   "+truncatePlain(format.OneLine(failure.Error()), max(1, m.width-4))))
	}
	return lines
}

func (m Model) briefGlossaryDescription() string {
	layout := m.briefLayout()
	description := "The one-line summary in the middle of a session row. Its lead is the first of " +
		briefSlotSummary(layout.Lead) + " that has something to say"
	if len(layout.Detail) == 0 {
		description += ", and this configuration shows no detail behind it"
	} else {
		description += "; the text after " + strings.TrimSpace(briefSeparator) + " is the first of " +
			briefSlotSummary(layout.Detail)
	}
	return description + ". A leading " + briefApproximateMark +
		" marks text Heikou was told rather than observed. Set \"brief\" in settings to change what fills either slot."
}

// briefObservationTimeout bounds a whole observation pass. Each configured
// source already carries its own timeout; this is the backstop that keeps one
// wedged pass from holding the single-flight slot forever.
const briefObservationTimeout = 60 * time.Second

type briefMsg struct {
	generation   uint64
	observations brief.Observations
	report       brief.Report
}

// briefFetchState is single-flight and generation-tagged like the snapshot and
// preview fetches in model.go. Configured sources run subprocesses, so a slow
// one must never be able to stack a second pass on top of the first.
type briefFetchState struct {
	generation       uint64
	activeGeneration uint64
	queued           bool
}

func (m *Model) requestBrief() tea.Cmd {
	// Asked of the observer rather than of the settings, because a layout can
	// now ask for work without defining a command: naming the built-in activity
	// source is what turns transcript reads on.
	if !brief.NewObserver(m.settings.Brief).Configured() {
		return nil
	}
	if m.briefFetch.activeGeneration != 0 {
		m.briefFetch.queued = true
		return nil
	}
	m.briefFetch.generation++
	m.briefFetch.activeGeneration = m.briefFetch.generation
	return m.observeBriefCmd(m.briefFetch.activeGeneration)
}

func (m *Model) finishBrief(generation uint64) (bool, tea.Cmd) {
	if generation == 0 || generation != m.briefFetch.activeGeneration {
		return false, nil
	}
	m.briefFetch.activeGeneration = 0
	queued := m.briefFetch.queued
	m.briefFetch.queued = false
	if queued {
		return true, m.requestBrief()
	}
	return true, nil
}

func (m Model) observeBriefCmd(generation uint64) tea.Cmd {
	observer := brief.NewObserver(m.settings.Brief)
	sessions := append(append([]control.Session(nil), m.snapshot.Sessions...), m.snapshot.Orphans...)
	previous := m.briefObservations
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), briefObservationTimeout)
		defer cancel()
		observations, report := observer.Observe(ctx, sessions, previous)
		return briefMsg{generation: generation, observations: observations, report: report}
	}
}

// briefSettingsLines renders the active layout and each configured source in
// the settings pane.
func (m Model) briefSettingsLines() []string {
	layout := m.briefLayout()
	lines := []string{"", lipgloss.NewStyle().Bold(true).Render(" brief"),
		mutedStyle.Render(" lead   ") + truncatePlain(briefSlotSummary(layout.Lead), max(1, m.width-9)),
		mutedStyle.Render(" detail ") + truncatePlain(briefSlotSummary(layout.Detail), max(1, m.width-9))}
	for _, name := range slices.Sorted(maps.Keys(m.settings.Brief.Sources)) {
		source := m.settings.Brief.Sources[name]
		lines = append(lines,
			" "+padPlain(name, 9)+truncatePlain(jsonCommand(source.Command), max(1, m.width-11)),
			mutedStyle.Render(fmt.Sprintf("   every %ds, %ds timeout, only after terminal activity",
				source.IntervalSeconds, source.TimeoutSeconds)))
	}
	// Source health lives here rather than on the notice line: this is where
	// someone comes to fix a source, and a background pass must not overwrite
	// the feedback from whatever the user last did.
	return append(lines, m.briefHealthLines()...)
}
