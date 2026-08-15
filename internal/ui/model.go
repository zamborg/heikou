package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/uax29/v2/graphemes"
	"github.com/zamborg/heikou/internal/brief"
	"github.com/zamborg/heikou/internal/config"
	"github.com/zamborg/heikou/internal/control"
	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/runner"
	"github.com/zamborg/heikou/internal/workstream"
)

const (
	refreshInterval = time.Second
	commandTimeout  = 8 * time.Second
	ungroupedKey    = "workstream:ungrouped"
	orphanedKey     = "workstream:orphaned"

	// A reply inherits the runner and root of the session it targets, so the
	// cycle keys have nothing to act on until the composer leaves reply mode.
	replyTargetFixedNotice = "reply uses the target's runner and root · Esc to compose a new session"

	// archiveChord archives the selected workstream. Every other letter in
	// "archive" was already spoken for — a is line start, r rename, c quit,
	// h backspace, i is Tab, e is line end — and v is the one that was free.
	//
	// It is a bare control chord rather than an Option or Command combination
	// on purpose. Ctrl-V arrives as a single C0 byte, so it needs none of the
	// enhanced key reporting that decides whether a modified arrow reaches
	// Heikou at all on macOS. The two chords that were also free, Ctrl-L and
	// Ctrl-Y, both lose to it: Ctrl-L is the key people press twice when a
	// screen looks wrong, which is the worst possible reflex to put behind a
	// press-twice confirmation, and Ctrl-Y is macOS's delayed-suspend
	// character. Pasting on macOS is Command-V and reaches Heikou as a paste
	// event, so Ctrl-V carries no paste reflex here either.
	archiveChord      = "ctrl+v"
	archiveChordLabel = "Ctrl-V"
	// archiveChordMinInterval is how long the arming press has to have been on
	// screen before a second press can act on it.
	//
	// It is not a timeout, and the difference matters: the arm never expires,
	// so a user who reads the notice and thinks about it for a minute still
	// gets the archive they asked for. What this rejects is the one press a
	// human cannot have made — holding the chord past the terminal's repeat
	// delay delivers arm and fire back to back with nothing in between, which
	// would archive a workstream off a key nobody meant to lean on. Repeat
	// rates run from about 15ms to 125ms apart, and a deliberate second press
	// takes longer than this, so the two do not overlap.
	archiveChordMinInterval = 250 * time.Millisecond
)

var (
	colorText       = adaptive("#1f2328", "#e6edf3")
	colorMuted      = adaptive("#656d76", "#7d8590")
	colorFaint      = adaptive("#afb8c1", "#30363d")
	colorSelection  = adaptive("#bfdbfe", "#264f78")
	colorCodex      = adaptive("#0969da", "#79c0ff")
	colorClaude     = adaptive("#8250df", "#d2a8ff")
	colorNoAgent    = adaptive("#57606a", "#c9d1d9")
	colorLive       = adaptive("#1a7f37", "#56d364")
	colorFailed     = adaptive("#cf222e", "#ff7b72")
	colorNotice     = adaptive("#9a6700", "#e3b341")
	textStyle       = lipgloss.NewStyle().Foreground(colorText)
	mutedStyle      = lipgloss.NewStyle().Foreground(colorMuted)
	faintStyle      = lipgloss.NewStyle().Foreground(colorFaint)
	selectedStyle   = lipgloss.NewStyle().Foreground(colorText).Background(colorSelection).Bold(true)
	modeBadgeStyle  = lipgloss.NewStyle().Foreground(adaptive("#ffffff", "#0d1117")).Background(colorCodex).Bold(true).Padding(0, 1)
	liveStyle       = lipgloss.NewStyle().Foreground(colorLive)
	failedStyle     = lipgloss.NewStyle().Foreground(colorFailed)
	noticeStyle     = lipgloss.NewStyle().Foreground(colorNotice)
	selectedKeyHint = lipgloss.NewStyle().Foreground(colorText)
)

type rowKind int

const (
	rowWorkstream rowKind = iota
	rowSession
	rowOrphanHeader
	rowOrphan
)

type listRow struct {
	key          string
	kind         rowKind
	workstreamID string
	sessionID    string
}

type screenKind uint8

const (
	screenDashboard screenKind = iota
	screenSettings
)

type overlayKind uint8

const (
	overlayNone overlayKind = iota
	overlayHelp
)

// composerEditMode names the organize destinations the composer can commit to.
// The composer already picks its destination before the draft is typed, so a
// rename is one more destination rather than a second text input with its own
// cursor, word motion, and paste handling.
type composerEditMode uint8

const (
	composerEditNone composerEditMode = iota
	composerEditCreateWorkstream
	composerEditRenameWorkstream
	composerEditSessionTitle
	composerEditRoot
)

type Model struct {
	controller control.Service
	root       string
	backend    heikou.Backend
	store      config.Store
	settings   config.Config

	snapshot      control.Snapshot
	overview      overviewModel
	cursor        int
	selected      string
	collapsed     map[string]bool
	rootIndex     map[string]int
	preview       string
	previewID     string
	width         int
	height        int
	busy          bool
	notice        string
	errorText     string
	confirmStop   string
	confirmDelete string
	// confirmArchive is the workstream a second archive chord archives. It is
	// the same arm-then-act gate the session lifecycle already uses, because
	// archiving is the one workstream verb that removes a row from the
	// dashboard and an unconfirmed keystroke must not reach durable state.
	confirmArchive string
	// archiveChordAt is when the archive chord last landed on that workstream.
	// A gate counts presses and cannot tell one held key from two decisions, so
	// this is the one thing it needs to know: how long ago the last press was.
	archiveChordAt time.Time
	snapshotFetch  snapshotFetchState
	previewFetch   previewFetchState

	briefObservations brief.Observations
	briefReport       brief.Report
	briefFetch        briefFetchState

	input          []string
	inputCursor    int
	inputColumn    int
	inputColumnSet bool

	// replyTarget is the session ID the composer commits to, pinned when the
	// reply key is pressed rather than read from the cursor at commit time, so
	// browsing the list while drafting cannot silently redirect the message.
	// Empty means the composer starts a new session.
	replyTarget string

	screen         screenKind
	overlay        overlayKind
	settingsOffset int
	helpOffset     int
	resizeMode     bool
	detailAdjust   int

	// markedSession is the session Ctrl-T pinned for a move. Selecting a
	// workstream and pressing Ctrl-T again completes it. Exactly one session is
	// markable: a batch move would be several non-atomic controller commands,
	// and a partial failure has no honest single-line outcome.
	markedSession string

	composerEdit       composerEditMode
	composerEditTarget string
	// composerRootSlot is which of the target workstream's roots the composer is
	// editing. It equals len(Roots) on the one slot past the end, which is how
	// adding a root is expressed as editing an empty one rather than as a
	// separate mode with its own chord.
	composerRootSlot int
	// composerRootOriginal is the path that slot held when the edit opened, and
	// is empty on the add slot. Replacing and removing both name the current
	// root, so committing against a remembered value rather than re-deriving
	// one keeps a root that moved underneath from being edited by position.
	composerRootOriginal string
	// confirmRootRemoval is the workstream and root a second Enter removes.
	// Emptying the field is a quiet gesture for a durable change, so it arms
	// rather than acts.
	confirmRootRemoval string
	pendingWorkstream  string
	artifactContext    artifactContextState

	// now is the clock the archive gate reads. It is a field so a test can say
	// how fast two presses arrived instead of racing a real one.
	now func() time.Time
}

func New(controller control.Service, root string, backend heikou.Backend, store config.Store, settings config.Config) Model {
	return Model{
		controller: controller, root: root, backend: backend, store: store, settings: settings,
		overview: newOverviewModel(control.Snapshot{}), collapsed: make(map[string]bool), rootIndex: make(map[string]int),
		now: time.Now,
	}
}

// clock reads the model's clock, defaulting to the real one so a Model built
// without New still behaves.
func (m Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// NewWithSelectedSession opens the dashboard with a durable session selected
// once the initial snapshot arrives. It is used by the guided quickstart after
// the user practices detaching from its directly attached native terminal.
func NewWithSelectedSession(controller control.Service, root string, backend heikou.Backend, store config.Store, settings config.Config, sessionID string) Model {
	model := New(controller, root, backend, store, settings)
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		model.selected = "session:" + sessionID
	}
	return model
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return initialSnapshotMsg{} }, tickCmd())
}

type initialSnapshotMsg struct{}

type snapshotMsg struct {
	generation uint64
	snapshot   control.Snapshot
	err        error
}

type previewMsg struct {
	generation uint64
	id         string
	text       string
	err        error
}

type snapshotFetchState struct {
	generation       uint64
	activeGeneration uint64
	queued           bool
}

type previewFetchState struct {
	generation       uint64
	activeGeneration uint64
	activeID         string
	queuedID         string
}

type startMsg struct {
	session control.Session
	err     error
}

type sendMsg struct {
	id  string
	err error
}

type stopMsg struct {
	id  string
	err error
}

type deleteMsg struct {
	id  string
	err error
}

type attachReadyMsg struct {
	command *exec.Cmd
	err     error
}

type attachMsg struct{ err error }

type settingsMsg struct {
	settings config.Config
	err      error
}

type settingsEditorMsg struct{ err error }

type workstreamMsg struct {
	action       string
	item         workstream.Workstream
	workstreamID string
	// name is carried rather than looked up, because an archived workstream is
	// gone from the next snapshot and the notice still has to name it.
	name      string
	sessionID string
	root      string
	delta     int
	moved     bool
	err       error
}

type sessionTitleMsg struct {
	id    string
	title string
	err   error
}

type externalMsg struct {
	label string
	err   error
}

type tickMsg time.Time

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		return m, nil

	case initialSnapshotMsg:
		return m, m.requestSnapshot()

	case tickMsg:
		return m, tea.Batch(m.requestSnapshot(), m.requestBrief(), tickCmd())

	case briefMsg:
		accepted, queued := m.finishBrief(message.generation)
		if !accepted {
			return m, nil
		}
		m.briefObservations = message.observations
		// Deliberately not written to the notice or error line. Those are
		// feedback for what the user just did and are only cleared by the next
		// keystroke, so a background pass reporting there would wipe the result
		// of their last action every interval. Source health belongs in the
		// settings pane, next to the sources it is about and where someone goes
		// to fix one. A failing source is also already visible in the rows: its
		// slot falls through to the next source.
		m.briefReport = message.report
		return m, queued

	case snapshotMsg:
		accepted, queuedSnapshot := m.finishSnapshot(message.generation)
		if !accepted {
			return m, nil
		}
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, queuedSnapshot
		}
		m.setSnapshot(message.snapshot)
		if m.markedSession != "" {
			if _, found := m.session(m.markedSession); !found {
				m.markedSession = ""
			}
		}
		// A rename target that disappeared can no longer receive the draft, and
		// the composer prefix would keep naming a destination that is gone.
		if m.composerEditTarget != "" && !m.composerEditTargetExists() {
			m.cancelComposerEdit()
			m.notice = "rename target is gone · composing a new session"
		}
		// A pinned target that died can no longer receive the draft, and leaving
		// the prefix pointing at it would promise delivery the composer cannot
		// keep. Drop back to new-session mode and say so once.
		if m.replyTarget != "" {
			if target, found := m.session(m.replyTarget); !found || !target.Alive() {
				m.replyTarget = ""
				m.notice = "reply target ended · composing a new session"
			}
		}
		m.restoreSelection()
		if selected, ok := m.selectedSession(); ok && selected.Available() {
			return m, tea.Batch(queuedSnapshot, m.requestPreview(selected.ID), m.artifactContextCmd(false))
		}
		m.previewFetch.queuedID = ""
		m.preview, m.previewID = "", ""
		return m, tea.Batch(queuedSnapshot, m.artifactContextCmd(false))

	case previewMsg:
		accepted, queuedID := m.finishPreview(message.generation, message.id)
		if !accepted {
			return m, nil
		}
		selected, ok := m.selectedSession()
		if ok && selected.Available() && message.id == selected.ID && message.err == nil {
			m.preview, m.previewID = message.text, message.id
		}
		if queuedID != "" && ok && selected.Available() && queuedID == selected.ID {
			return m, m.requestPreview(queuedID)
		}
		return m, nil

	case startMsg:
		m.busy = false
		if message.session.ID != "" {
			m.clearInput()
			m.selected = sessionRowKey(message.session)
			m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		}
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, m.requestSnapshot()
		}
		m.notice = fmt.Sprintf("started %s · %s", message.session.Backend, format.ShortID(message.session.ID))
		return m, m.requestSnapshot()

	case sendMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.clearInput()
		// The pin exists to protect one message in flight, so a delivered reply
		// consumes it. Holding the target past the send would aim the next Enter
		// at a conversation the user is done with, and staying is the mode that
		// needs the extra keystroke rather than leaving.
		m.replyTarget = ""
		m.notice = "message sent · " + format.ShortID(message.id) + " · composing a new session"
		m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		return m, tea.Batch(m.requestSnapshot(), m.requestPreview(message.id))

	case stopMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.notice = "stopped runtime · " + format.ShortID(message.id)
		m.confirmStop = ""
		if source, ok := m.session(message.id); ok && source.Orphaned && m.markedSession == message.id {
			m.markedSession = ""
		}
		return m, m.requestSnapshot()

	case deleteMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.notice = "deleted session record · " + format.ShortID(message.id)
		m.confirmDelete = ""
		if m.markedSession == message.id {
			m.markedSession = ""
		}
		if selected, ok := m.selectedSession(); ok && selected.ID == message.id {
			m.selected = ""
		}
		return m, m.requestSnapshot()

	case attachReadyMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = "attach: " + message.err.Error()
			return m, nil
		}
		m.notice = "attached · detach with Ctrl-\\ or Ctrl-b d"
		return m, tea.ExecProcess(message.command, func(err error) tea.Msg { return attachMsg{err: err} })

	case attachMsg:
		if message.err != nil {
			m.errorText = "attach: " + message.err.Error()
		} else {
			m.notice = "detached back to heikou"
		}
		return m, m.requestSnapshot()

	case settingsMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.settings = message.settings
		// Unlike launch commands, the brief takes effect on the next frame:
		// nothing is running yet that a changed layout would contradict. A
		// removed source's observations are dropped so its text cannot linger,
		// and a pass already in flight under the old configuration is abandoned
		// rather than allowed to land afterwards and repopulate them.
		m.briefObservations, m.briefReport = nil, brief.Report{}
		m.briefFetch.activeGeneration, m.briefFetch.queued = 0, false
		m.errorText = ""
		m.notice = "settings reloaded · brief applies now, commands to new sessions"
		return m, nil

	case settingsEditorMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = "edit settings: " + message.err.Error()
			return m, nil
		}
		m.notice = "editor closed · reloading settings…"
		return m, m.loadSettingsCmd()

	case workstreamMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		// A create, a rename or a root edit committed the composer's draft, so
		// finishing it releases the composer. Archiving never routed through the
		// composer, and taking an unrelated draft down with it would make one
		// chord have a second, silent outcome.
		if message.action != "archive" {
			m.cancelComposerEdit()
		}
		switch message.action {
		case "create":
			m.pendingWorkstream = message.item.ID
			m.notice = "created workstream · " + message.item.Name
		case "rename":
			m.notice = "renamed workstream"
		case "reorder":
			direction, boundary := "down", "last"
			if message.delta < 0 {
				direction, boundary = "up", "first"
			}
			if message.moved {
				m.notice = "moved workstream " + direction
			} else {
				m.notice = "workstream is already " + boundary
			}
		case "archive":
			// Its sessions are in Ungrouped now, so the cursor follows them there
			// rather than landing on whichever row inherited the vacated index.
			m.selected = ""
			m.notice = "archived " + message.name + " · its sessions are Ungrouped and still running"
		case "move":
			m.markedSession = ""
			m.notice = "moved " + format.ShortID(message.sessionID) + " · " + m.workstreamName(message.workstreamID)
		case "adopt":
			m.markedSession = ""
			m.notice = "adopted runtime " + format.ShortID(message.sessionID) + " · " + m.workstreamName(message.workstreamID)
		case "root_add":
			m.notice = "added root · " + format.CompactPath(message.root)
		case "root_replace":
			m.notice = "replaced root · " + format.CompactPath(message.root)
		case "root_remove":
			m.notice = "removed root · " + format.CompactPath(message.root) + " · files and sessions are untouched"
		}
		return m, m.requestSnapshot()

	case sessionTitleMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.cancelComposerEdit()
		if strings.TrimSpace(message.title) == "" {
			m.notice = "cleared session title · " + format.ShortID(message.id)
		} else {
			m.notice = "updated session title · " + format.ShortID(message.id)
		}
		return m, m.requestSnapshot()

	case externalMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.label + ": " + message.err.Error()
		} else {
			m.notice = message.label + " closed"
		}
		return m, tea.Batch(m.requestSnapshot(), m.artifactContextCmd(true))

	case artifactContextMsg:
		m.acceptArtifactContext(message)
		return m, nil

	case tea.PasteMsg:
		// A bracketed paste never reaches handleKey, so it would otherwise be
		// the one thing a user can do between the two archive presses that
		// leaves the second one looking like a confirmation of the first. Every
		// other input disarms; arriving by a different message is not a reason
		// to be the exception.
		m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		if m.busy || m.screen == screenSettings || m.overlay == overlayHelp {
			return m, nil
		}
		m.resizeMode = false
		// A rename or title is a single-line value, so a multiline clipboard is
		// flattened rather than committing line breaks into a name.
		if m.composerEdit != composerEditNone {
			m.insertText(normalizeInlinePaste(message.Content))
			m.notice = ""
			return m, nil
		}
		m.insertText(normalizeComposerPaste(message.Content))
		m.notice = ""
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}

func (m Model) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	bindingStroke := key.Keystroke()
	if stroke == "ctrl+c" {
		return m, tea.Quit
	}
	if m.overlay == overlayHelp {
		return m.handleHelpKey(stroke)
	}
	// While the composer is naming a workstream or a session, "?" is part of the
	// value being typed rather than a request for help.
	questionOpensHelp := stroke == "?" && (m.screen == screenSettings ||
		(m.screen == screenDashboard && m.inputValue() == "" && m.composerEdit == composerEditNone))
	if stroke == "f1" || questionOpensHelp {
		m.overlay = overlayHelp
		m.resizeMode = false
		m.helpOffset = 0
		m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		m.notice, m.errorText = "", ""
		return m, nil
	}
	if m.screen == screenSettings {
		return m.handleSettingsKey(stroke)
	}
	if stroke == "ctrl+g" && m.composerEdit == composerEditNone {
		m.resizeMode = !m.resizeMode
		m.notice, m.errorText = "", ""
		m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		return m, nil
	}
	if m.resizeMode {
		return m.handleResizeKey(key)
	}
	if m.busy {
		return m, nil
	}

	m.errorText = ""
	if stroke != "ctrl+x" {
		m.confirmStop = ""
		m.confirmDelete = ""
	}
	// An armed archive survives exactly one keystroke, and only its own. Every
	// other key disarms it, so a chord pressed by accident costs a notice line
	// rather than a workstream.
	if stroke != archiveChord {
		m.confirmArchive = ""
	}
	draft := m.inputValue()
	prompt := strings.TrimSpace(draft)
	// The destination is chosen before typing and stays visible in the composer
	// prefix, so these bindings only ever change or announce the destination.
	// Enter alone commits, and it commits to whatever the prefix shows. While an
	// organize edit owns the composer they are inert: the reply key defaults to
	// Space, which has to stay typeable inside a workstream name.
	if m.composerEdit == composerEditNone {
		switch bindingStroke {
		case m.settings.ReplyKey():
			// Only an untouched composer is a mode switch; afterwards the key is
			// ordinary text, which is what makes a leading space typeable at all.
			if m.replyTarget == "" && len(m.input) == 0 {
				return m.beginReply()
			}
		case m.settings.CycleRunnerKey():
			if m.replyTarget != "" {
				m.notice = replyTargetFixedNotice
				return m, nil
			}
			m.backend = m.backend.Next()
			m.notice = "new sessions use " + string(m.backend)
			return m, nil
		case m.settings.CycleRootKey():
			if m.replyTarget != "" {
				m.notice = replyTargetFixedNotice
				return m, nil
			}
			if m.cycleSelectedRoot(1) {
				m.notice = "launch root · " + format.CompactPath(m.launchRoot())
			}
			return m, nil
		}
	}
	if m.handleComposerShortcut(bindingStroke) {
		m.notice = ""
		return m, nil
	}

	switch stroke {
	case "ctrl+s", "f2":
		m.screen = screenSettings
		m.settingsOffset = 0
		m.notice, m.errorText = "", ""
		return m, nil

	case "f3":
		// One catch-all re-read. Snapshot and preview are already polled every
		// second; the artifact context is cached until the selection moves, so
		// this is the only way to see notes an agent rewrote under the cursor.
		m.notice = "refreshed"
		return m, tea.Batch(m.requestSnapshot(), m.requestSelectedPreview(), m.artifactContextCmd(true))

	case "ctrl+n":
		m.beginComposerEdit(composerEditCreateWorkstream, "", "")
		m.notice = "name the new workstream · root " + format.CompactPath(m.root)
		return m, nil

	case "ctrl+r":
		return m.renameSelection()

	// Ctrl-O rather than a mnemonic letter: the composer already owns every
	// readline chord a text field is expected to answer to, and taking Ctrl-R
	// or Ctrl-E back from rename or end-of-line would cost more than the
	// mnemonic is worth. The composer prefix is what makes it discoverable.
	case "ctrl+o":
		return m.editRootSelection()

	case "ctrl+t":
		return m.markOrMoveSelection()

	// Archiving reads the selected row for its noun like every other organize
	// chord, and answers to a workstream only. See archiveChord for why this
	// key and not Ctrl-A, which the composer owns as line start.
	case archiveChord:
		return m.archiveSelection()

	case "shift+up":
		return m.reorderSelection(-1)

	case "shift+down":
		return m.reorderSelection(1)

	case "esc":
		if m.composerEdit != composerEditNone {
			m.cancelComposerEdit()
			m.notice = "canceled · composing a new session"
			return m, nil
		}
		// A reply releases in one press and takes its draft with it. Clearing the
		// text first would leave a follow-up sitting in a composer now aimed at a
		// new session, where the next Enter spawns a real one — the asymmetric
		// mistake the pinned-destination model exists to prevent.
		if m.replyTarget != "" {
			m.replyTarget = ""
			m.clearInput()
			m.notice = "composing a new session"
			return m, nil
		}
		if len(m.input) > 0 {
			m.clearInput()
			m.notice = "composer cleared"
			return m, nil
		}
		// Esc resets the cursor rather than quitting. Exiting on a stray Esc over
		// an already-empty composer is too easy to do by accident, and Ctrl-C is
		// the deliberate exit that works from every screen.
		if m.markedSession != "" {
			m.markedSession = ""
			m.notice = "move canceled"
			return m, nil
		}
		m.selected = ungroupedKey
		m.restoreSelection()
		m.notice = "Ungrouped · Ctrl-C to quit heikou"
		return m, m.artifactContextCmd(false)

	case "up":
		if m.hasMultilineInput() {
			m.moveInputVertical(-1)
			return m, nil
		}
		if m.selectionLocked() {
			return m.reportSelectionLock()
		}
		return m.moveSelection(-1)

	case "down":
		if m.hasMultilineInput() {
			m.moveInputVertical(1)
			return m, nil
		}
		if m.selectionLocked() {
			return m.reportSelectionLock()
		}
		return m.moveSelection(1)

	// Option-↑ / Option-↓ move by structure where PgUp/PgDn move by distance. A
	// long list is mostly sessions, so stepping over every one of them to reach
	// the next group is the slow part of reading it. Unlike ↑ and ↓ these do not
	// defer to a multiline draft: they are then the only way to move the
	// selection by hand, which is the same bargain PgUp/PgDn already make.
	case "alt+up", "meta+up":
		if m.selectionLocked() {
			return m.reportSelectionLock()
		}
		return m.moveToAdjacentGroup(-1)

	case "alt+down", "meta+down":
		if m.selectionLocked() {
			return m.reportSelectionLock()
		}
		return m.moveToAdjacentGroup(1)

	case "pgup":
		if m.selectionLocked() {
			return m.reportSelectionLock()
		}
		return m.moveSelection(-max(1, m.listHeight()-1))

	case "pgdown":
		if m.selectionLocked() {
			return m.reportSelectionLock()
		}
		return m.moveSelection(max(1, m.listHeight()-1))

	case "left":
		if len(m.input) == 0 && m.toggleSelectedGroup(true) {
			return m, nil
		}
		if m.inputCursor > 0 {
			m.inputCursor--
			m.resetInputColumn()
		}
		return m, nil

	case "right":
		if len(m.input) == 0 && m.toggleSelectedGroup(false) {
			return m, nil
		}
		if m.inputCursor < len(m.input) {
			m.inputCursor++
			m.resetInputColumn()
		}
		return m, nil

	case "home", "ctrl+a":
		m.moveInputLineBoundary(-1)
		return m, nil

	case "end", "ctrl+e":
		m.moveInputLineBoundary(1)
		return m, nil

	case "backspace", "ctrl+h":
		m.deleteInputBackward()
		return m, nil

	case "delete":
		m.deleteInputForward()
		return m, nil

	case "ctrl+u":
		m.deleteInputToLineStart()
		return m, nil

	case "ctrl+w":
		m.deletePreviousWord()
		return m, nil

	case "enter":
		// An organize edit commits even when empty, because clearing a session
		// title is a real outcome rather than an unfinished draft.
		if m.composerEdit != composerEditNone {
			return m.commitComposerEdit(draft)
		}
		if prompt != "" {
			return m.commitComposer(draft)
		}
		// An empty composer in reply mode keeps the pinned target, so Enter must
		// not attach to whatever the cursor happens to sit on instead.
		if m.replyTarget != "" {
			return m, nil
		}
		row, ok := m.selectedRow()
		if ok && (row.kind == rowWorkstream || row.kind == rowOrphanHeader) {
			m.collapsed[row.key] = !m.collapsed[row.key]
			return m, nil
		}
		selected, ok := m.selectedSession()
		if !ok {
			m.errorText = "type a task to start a session"
			return m, nil
		}
		if !selected.Available() {
			m.errorText = "this runtime is unavailable"
			return m, nil
		}
		m.busy = true
		m.notice = "opening terminal…"
		return m, m.attachCommandCmd(selected.ID)

	case "ctrl+x":
		selected, ok := m.selectedSession()
		if !ok {
			m.errorText = "select a session to stop or delete"
			return m, nil
		}
		return m.handleSessionLifecycle(selected)
	}
	blockedModifiers := tea.ModCtrl | tea.ModAlt | tea.ModMeta | tea.ModHyper | tea.ModSuper
	if key.Text != "" && key.Mod&blockedModifiers == 0 {
		m.insertText(key.Text)
		m.notice = ""
	}
	return m, nil
}

// selectionLocked reports whether the composer currently owns a destination
// that the cursor must not wander away from. The pin already guarantees the
// message reaches the right session, but a list that scrolls underneath a reply
// invites reading the wrong row's preview as the conversation being answered.
func (m Model) selectionLocked() bool {
	return m.replyTarget != "" || m.composerEdit != composerEditNone
}

func (m Model) reportSelectionLock() (tea.Model, tea.Cmd) {
	if m.composerEdit != composerEditNone {
		m.notice = "finish with Enter or cancel with Esc to move again"
		return m, nil
	}
	m.notice = "replying to " + format.ShortID(m.replyTarget) + " · Esc to compose a new session"
	return m, nil
}

// beginReply pins the selected session as the composer's destination. The pin
// is taken here, while the composer is still empty, so the message can only
// ever reach the session named in the prefix the user typed under.
func (m Model) beginReply() (tea.Model, tea.Cmd) {
	selected, ok := m.selectedSession()
	if !ok || !selected.Alive() {
		m.errorText = "select a live session before replying"
		return m, nil
	}
	m.replyTarget = selected.ID
	m.notice = "replying to " + format.ShortID(selected.ID) + " · Esc to compose a new session"
	return m, nil
}

// commitComposer routes the draft to the destination the composer has been
// displaying, which is the whole point of choosing that destination up front.
func (m Model) commitComposer(draft string) (tea.Model, tea.Cmd) {
	if m.replyTarget == "" {
		m.busy = true
		m.notice = "starting " + string(m.backend) + "…"
		return m, m.startCmd(draft)
	}
	target, ok := m.session(m.replyTarget)
	if !ok || !target.Alive() {
		m.replyTarget = ""
		m.errorText = "reply target is no longer live · composing a new session"
		return m, nil
	}
	m.busy = true
	m.notice = "sending to " + format.ShortID(target.ID) + "…"
	return m, m.sendCmd(target.ID, draft)
}

func (m Model) handleSettingsKey(stroke string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	m.errorText = ""
	switch stroke {
	case "esc", "ctrl+s", "f2":
		m.screen = screenDashboard
		m.notice = "settings closed"
		return m, nil
	case "r":
		m.busy = true
		m.notice = "reloading settings…"
		return m, m.loadSettingsCmd()
	case "up":
		m.settingsOffset--
		m.clampSettingsOffset()
		return m, nil
	case "down":
		m.settingsOffset++
		m.clampSettingsOffset()
		return m, nil
	case "pgup":
		m.settingsOffset -= max(1, m.settingsViewportHeight()-1)
		m.clampSettingsOffset()
		return m, nil
	case "pgdown":
		m.settingsOffset += max(1, m.settingsViewportHeight()-1)
		m.clampSettingsOffset()
		return m, nil
	case "home":
		m.settingsOffset = 0
		return m, nil
	case "end":
		m.settingsOffset = m.settingsMaxOffset()
		return m, nil
	case "e":
		if err := m.store.Ensure(); err != nil {
			m.errorText = err.Error()
			return m, nil
		}
		command, err := editorCommand(m.store.Path)
		if err != nil {
			m.errorText = err.Error()
			return m, nil
		}
		m.busy = true
		m.notice = "editing settings…"
		return m, tea.ExecProcess(command, func(err error) tea.Msg { return settingsEditorMsg{err: err} })
	}
	return m, nil
}

func (m Model) handleSessionLifecycle(selected control.Session) (tea.Model, tea.Cmd) {
	if selected.Available() {
		m.confirmDelete = ""
		if m.confirmStop != selected.ID {
			m.confirmStop = selected.ID
			m.notice = "Ctrl-X again to stop runtime " + format.ShortID(selected.ID) + " (record stays)"
			return m, nil
		}
		m.busy = true
		m.notice = "stopping " + format.ShortID(selected.ID) + "…"
		return m, m.stopCmd(selected.ID)
	}
	if !selected.Durable || selected.Orphaned {
		m.errorText = "this orphan has no durable session record to delete"
		return m, nil
	}
	m.confirmStop = ""
	if m.confirmDelete != selected.ID {
		m.confirmDelete = selected.ID
		m.notice = "Ctrl-X again to permanently delete record " + format.ShortID(selected.ID)
		return m, nil
	}
	m.busy = true
	m.notice = "deleting record " + format.ShortID(selected.ID) + "…"
	return m, m.deleteSessionCmd(selected.ID)
}

// The organize chords below read the selected row rather than a mode. A single
// dashboard row is either a workstream or a session, so one chord can carry the
// verb and let the cursor supply the noun it applies to.

// renameSelection renames the selected workstream or retitles the selected
// session. The composer takes the draft, so a rename inherits paste, word
// motion, and the destination prefix instead of a second private text input.
// editRootSelection opens the selected workstream's roots in the composer.
//
// Roots get one chord rather than three because they are one list with a
// cursor the dashboard already has: Shift-Tab picks which root a new session
// launches into, and that same choice is what add, replace and remove act on.
// Pressing Ctrl-O again walks to the next root and then to an empty slot, so
// "add" is editing the slot past the end. What the composer is about to change
// is named in its prefix the whole time, which is the only way a single chord
// with three outcomes can be honest.
func (m Model) editRootSelection() (tea.Model, tea.Cmd) {
	if m.composerEdit == composerEditRoot {
		return m.advanceRootSlot()
	}
	item, ok := m.workstream(m.launchWorkstreamID())
	if !ok {
		m.errorText = "select a named workstream to manage its roots"
		return m, nil
	}
	m.openRootSlot(item, m.rootPosition(item.ID, len(item.Roots)))
	return m, nil
}

// advanceRootSlot steps to the next root, wrapping through the add slot. A
// draft is discarded on the way: the field always shows the slot it names.
func (m Model) advanceRootSlot() (tea.Model, tea.Cmd) {
	item, ok := m.workstream(m.composerEditTarget)
	if !ok {
		m.cancelComposerEdit()
		m.errorText = "that workstream is no longer available"
		return m, nil
	}
	m.openRootSlot(item, (m.composerRootSlot+1)%(len(item.Roots)+1))
	return m, nil
}

func (m *Model) openRootSlot(item workstream.Workstream, slot int) {
	value, original := m.root, ""
	if slot < len(item.Roots) {
		value, original = item.Roots[slot], item.Roots[slot]
	}
	m.beginComposerEdit(composerEditRoot, item.ID, value)
	m.composerRootSlot, m.composerRootOriginal = slot, original
	if original == "" {
		m.notice = "new root for " + item.Name + " · Enter adds · Ctrl-O next · Esc cancels"
		return
	}
	m.notice = "edit this root · empty Enter removes it · Ctrl-O next · Esc cancels"
}

// rootSlotLabel names the slot the composer is pointed at. Without it a single
// path in the input has no way to say whether Enter replaces a root or adds one.
func (m Model) rootSlotLabel() string {
	item, ok := m.workstream(m.composerEditTarget)
	if !ok {
		return "✎ root"
	}
	if m.composerRootSlot >= len(item.Roots) {
		return "✎ new root · " + item.Name
	}
	return fmt.Sprintf("✎ root %d/%d · %s", m.composerRootSlot+1, len(item.Roots), item.Name)
}

func (m Model) commitRootEdit(value string) (tea.Model, tea.Cmd) {
	// A pending removal survives exactly one Enter. Taking it now means any
	// other outcome disarms it, so a refused replace cannot leave the next
	// empty Enter primed to delete without asking again.
	armed := m.confirmRootRemoval
	m.confirmRootRemoval = ""

	item, ok := m.workstream(m.composerEditTarget)
	if !ok {
		m.cancelComposerEdit()
		m.errorText = "that workstream is no longer available"
		return m, nil
	}
	if m.composerRootOriginal == "" {
		if value == "" {
			m.cancelComposerEdit()
			m.notice = "no root added"
			return m, nil
		}
		m.busy = true
		m.notice = "adding root…"
		return m, m.addRootCmd(item.ID, value)
	}
	if !slices.Contains(item.Roots, m.composerRootOriginal) {
		// Another process changed the roots while this edit was open. Editing by
		// remembered path rather than by position means this is detectable at
		// all; committing anyway would rewrite whichever root moved into the slot.
		m.cancelComposerEdit()
		m.errorText = "that root is no longer registered"
		return m, nil
	}
	switch {
	case value == "":
		if len(item.Roots) <= 1 {
			m.errorText = "a workstream must keep one root; type a different path to replace this one"
			return m, nil
		}
		confirmation := item.ID + "\x00" + m.composerRootOriginal
		if armed != confirmation {
			m.confirmRootRemoval = confirmation
			m.notice = "remove root " + format.CompactPath(m.composerRootOriginal) + " · Enter again · files and sessions stay"
			return m, nil
		}
		m.busy = true
		m.notice = "removing root…"
		return m, m.removeRootCmd(item.ID, m.composerRootOriginal)
	case value == m.composerRootOriginal:
		m.cancelComposerEdit()
		m.notice = "root unchanged"
		return m, nil
	default:
		m.busy = true
		m.notice = "replacing root…"
		return m, m.replaceRootCmd(item.ID, m.composerRootOriginal, value)
	}
}

func (m Model) renameSelection() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	switch row.kind {
	case rowWorkstream:
		if row.workstreamID == "" {
			m.errorText = "Ungrouped is a synthetic inbox and cannot be renamed"
			return m, nil
		}
		item, found := m.workstream(row.workstreamID)
		if !found {
			return m, nil
		}
		m.beginComposerEdit(composerEditRenameWorkstream, row.workstreamID, item.Name)
		m.notice = "rename workstream · Enter saves · Esc cancels"
	case rowSession:
		session, found := m.session(row.sessionID)
		if !found {
			return m, nil
		}
		m.beginComposerEdit(composerEditSessionTitle, row.sessionID, session.Record.Title)
		m.notice = "title session " + format.ShortID(row.sessionID) + " · empty Enter clears it"
	case rowOrphan:
		m.errorText = "adopt this runtime with Ctrl-T before giving it a durable title"
	case rowOrphanHeader:
		m.errorText = "select a workstream or a session to rename"
	}
	return m, nil
}

// markOrMoveSelection is one chord for both halves of a move: it marks a
// session, then completes the move when a workstream is selected. Marking is
// deliberately single-session; see Model.markedSession.
func (m Model) markOrMoveSelection() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	switch row.kind {
	case rowSession, rowOrphan:
		if m.markedSession == row.sessionID {
			m.markedSession = ""
			m.notice = "move canceled"
			return m, nil
		}
		m.markedSession = row.sessionID
		m.notice = "marked " + format.ShortID(row.sessionID) + " · select a workstream and press Ctrl-T"
		return m, nil
	case rowWorkstream:
		if m.markedSession == "" {
			m.errorText = "select a session and press Ctrl-T to mark it first"
			return m, nil
		}
		return m.moveMarkedSession(row.workstreamID)
	}
	m.errorText = "orphaned runtimes are adopted into a workstream, not into this section"
	return m, nil
}

func (m Model) moveMarkedSession(workstreamID string) (tea.Model, tea.Cmd) {
	if m.markedSession == "" {
		return m, nil
	}
	session, ok := m.session(m.markedSession)
	if !ok {
		m.markedSession = ""
		m.errorText = "the marked session is no longer available"
		return m, nil
	}
	m.busy = true
	// An orphan has no durable record to move, so claiming it is a different
	// action with a different name; the chord is shared, the command is not.
	if session.Orphaned {
		if workstreamID == "" {
			m.busy = false
			m.errorText = "adopt this runtime into a named workstream, not Ungrouped"
			return m, nil
		}
		m.notice = "adopting " + format.ShortID(session.ID) + "…"
		return m, m.adoptSessionCmd(session.ID, workstreamID)
	}
	m.notice = "moving " + format.ShortID(session.ID) + "…"
	return m, m.moveSessionCmd(session.ID, workstreamID)
}

// archiveSelection archives the selected workstream behind a second press of
// the same chord.
//
// Archiving is durable organization rather than deletion or shutdown: the
// controller drops the workstream's memberships and stops nothing, so every
// session survives in Ungrouped and a live runtime keeps running. That is the
// part a chord can hide, so the arming press says it before the second press
// does it. The verb answers to a workstream row only, which is also what keeps
// it from firing on the row a reply or a session lifecycle chord is about.
func (m Model) archiveSelection() (tea.Model, tea.Cmd) {
	// A rename, a title or a root edit has the composer pointed at this very
	// workstream. Archiving out from under an open edit would leave the draft
	// naming something that no longer exists.
	if m.composerEdit != composerEditNone {
		m.errorText = "finish with Enter or cancel with Esc before archiving"
		return m, nil
	}
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	if row.kind == rowOrphanHeader {
		m.errorText = "orphaned runtimes have no workstream; adopt one with Ctrl-T first"
		return m, nil
	}
	if row.kind != rowWorkstream {
		m.errorText = "select a workstream to archive it; Ctrl-X is the session chord"
		return m, nil
	}
	if row.workstreamID == "" {
		m.errorText = "Ungrouped is a synthetic inbox and cannot be archived"
		return m, nil
	}
	item, found := m.workstream(row.workstreamID)
	if !found {
		m.errorText = "that workstream is no longer available"
		return m, nil
	}
	pressed := m.clock()
	if m.confirmArchive != item.ID {
		m.confirmArchive = item.ID
		m.archiveChordAt = pressed
		m.notice = archiveChordLabel + " again to archive " + item.Name + " · " + archiveConsequence(m.sessionsForRow(row))
		return m, nil
	}
	// A second press this soon after the first is the terminal repeating a held
	// key, not an answer to the question the first one asked. The arm survives
	// it — nothing was decided either way — but the interval is measured from
	// the latest press, so a key held down goes on refusing rather than
	// eventually satisfying itself.
	if pressed.Sub(m.archiveChordAt) < archiveChordMinInterval {
		m.archiveChordAt = pressed
		return m, nil
	}
	// The arm is consumed by the press that acts on it, so a refused archive
	// cannot leave the next single keystroke primed to try again unasked.
	m.confirmArchive = ""
	m.busy = true
	m.notice = "archiving " + item.Name + "…"
	return m, m.archiveWorkstreamCmd(item.ID, item.Name)
}

// archiveConsequence describes what archiving is about to do to the members of
// a workstream, so the arming press names an outcome rather than only a noun.
func archiveConsequence(sessions []control.Session) string {
	if len(sessions) == 0 {
		return "it holds no sessions"
	}
	live := 0
	for _, session := range sessions {
		if session.Alive() {
			live++
		}
	}
	moved := fmt.Sprintf("its %d sessions move to Ungrouped", len(sessions))
	if len(sessions) == 1 {
		moved = "its session moves to Ungrouped"
	}
	if live == 0 {
		return moved
	}
	return fmt.Sprintf("%s · %d live keep running", moved, live)
}

// reorderSelection moves a workstream in the durable display order, or moves a
// session to the adjacent workstream. Sessions have no durable order inside a
// workstream to change; see todos/session-ordering.md.
func (m Model) reorderSelection(delta int) (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	switch row.kind {
	case rowWorkstream:
		if row.workstreamID == "" {
			m.errorText = "Ungrouped always sorts after the named workstreams"
			return m, nil
		}
		m.busy = true
		m.notice = "moving workstream…"
		return m, m.reorderWorkstreamCmd(row.workstreamID, delta)
	case rowSession:
		destination, ok := m.adjacentWorkstream(row.workstreamID, delta)
		if !ok {
			edge := "first"
			if delta > 0 {
				edge = "last"
			}
			m.errorText = "already in the " + edge + " workstream"
			return m, nil
		}
		m.busy = true
		m.notice = "moving " + format.ShortID(row.sessionID) + "…"
		return m, m.moveSessionCmd(row.sessionID, destination)
	case rowOrphan:
		m.errorText = "adopt this runtime with Ctrl-T before moving it"
	}
	return m, nil
}

// adjacentWorkstream walks the durable workstream order with Ungrouped pinned
// at the end, so Shift-Up and Shift-Down step a session through exactly the
// destinations the list already displays, in the order it displays them.
func (m Model) adjacentWorkstream(current string, delta int) (string, bool) {
	order := make([]string, 0, len(m.overview.workstreams)+1)
	for _, item := range m.overview.workstreams {
		order = append(order, item.ID)
	}
	order = append(order, "")
	for index, id := range order {
		if id != current {
			continue
		}
		next := index + delta
		if next < 0 || next >= len(order) {
			return "", false
		}
		return order[next], true
	}
	return "", false
}

func (m *Model) beginComposerEdit(mode composerEditMode, target, value string) {
	m.composerEdit = mode
	m.composerEditTarget = target
	m.replyTarget = ""
	m.markedSession = ""
	m.input = splitGraphemes(inlineSafeText(value))
	m.inputCursor = len(m.input)
	m.resetInputColumn()
	m.notice, m.errorText = "", ""
}

func (m *Model) cancelComposerEdit() {
	m.composerEdit = composerEditNone
	m.composerEditTarget = ""
	m.composerRootSlot, m.composerRootOriginal, m.confirmRootRemoval = 0, "", ""
	m.clearInput()
}

// composerEditTargetExists reports whether the noun being renamed is still in
// the snapshot, so a target deleted by another process releases the composer
// instead of committing a name to something that is gone.
func (m Model) composerEditTargetExists() bool {
	switch m.composerEdit {
	case composerEditRenameWorkstream:
		_, ok := m.workstream(m.composerEditTarget)
		return ok
	case composerEditSessionTitle:
		_, ok := m.session(m.composerEditTarget)
		return ok
	case composerEditRoot:
		_, ok := m.workstream(m.composerEditTarget)
		return ok
	}
	return true
}

func (m Model) commitComposerEdit(draft string) (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(draft)
	switch m.composerEdit {
	case composerEditCreateWorkstream:
		if value == "" {
			m.errorText = "a workstream name is required"
			return m, nil
		}
		m.busy = true
		m.notice = "creating workstream…"
		return m, m.createWorkstreamCmd(value)
	case composerEditRenameWorkstream:
		if value == "" {
			m.errorText = "a workstream name is required"
			return m, nil
		}
		if !m.composerEditTargetExists() {
			m.cancelComposerEdit()
			m.errorText = "that workstream is no longer available"
			return m, nil
		}
		m.busy = true
		m.notice = "renaming workstream…"
		return m, m.renameWorkstreamCmd(m.composerEditTarget, value)
	case composerEditSessionTitle:
		if !m.composerEditTargetExists() {
			m.cancelComposerEdit()
			m.errorText = "that session is no longer available"
			return m, nil
		}
		m.busy = true
		m.notice = "saving title…"
		return m, m.setSessionTitleCmd(m.composerEditTarget, value)
	case composerEditRoot:
		return m.commitRootEdit(value)
	}
	return m, nil
}

func (m Model) composerEditLabel() string {
	switch m.composerEdit {
	case composerEditCreateWorkstream:
		return "✎ new workstream"
	case composerEditRenameWorkstream:
		name := m.workstreamName(m.composerEditTarget)
		return "✎ rename " + name
	case composerEditSessionTitle:
		return "✎ title " + format.ShortID(m.composerEditTarget)
	case composerEditRoot:
		return m.rootSlotLabel()
	}
	return ""
}

func (m *Model) requestSelectedPreview() tea.Cmd {
	selected, ok := m.selectedSession()
	if !ok || !selected.Available() {
		return nil
	}
	m.previewID = ""
	return m.requestPreview(selected.ID)
}

func (m Model) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		view := tea.NewView("")
		view.AltScreen = true
		return view
	}
	if m.overlay == overlayHelp {
		view := tea.NewView(textStyle.MaxWidth(m.width).Render(m.renderHelp()))
		view.AltScreen = true
		view.WindowTitle = "heikou · help"
		return view
	}
	if m.screen == screenSettings {
		view := tea.NewView(textStyle.MaxWidth(m.width).Render(m.renderSettings()))
		view.AltScreen = true
		view.WindowTitle = "heikou · settings"
		return view
	}
	sections := []string{m.renderHeader(), m.renderRule(), m.renderWorkstreams(), m.renderDetails(), m.renderComposer()}
	content := textStyle.MaxWidth(m.width).Render(strings.Join(sections, "\n"))
	view := tea.NewView(clipPane(content, m.width, m.height))
	view.AltScreen = true
	view.WindowTitle = "heikou · parallel agents"
	return view
}

func (m Model) renderHeader() string {
	live, finished, unavailable := 0, 0, 0
	for _, session := range m.overview.allSessions() {
		switch session.Status {
		case control.StatusLive:
			live++
		case control.StatusUnavailable:
			unavailable++
		default:
			finished++
		}
	}
	counts := fmt.Sprintf("%d workstreams · %d live", len(m.overview.workstreams), live)
	if unavailable > 0 {
		counts += fmt.Sprintf(" · %d unavailable", unavailable)
	}
	if finished > 0 {
		counts += fmt.Sprintf(" · %d finished", finished)
	}
	mode := "DASHBOARD"
	if m.resizeMode {
		mode += " · RESIZE"
	}
	return m.renderModeHeader(mode, counts)
}

func (m Model) renderModeHeader(mode, context string) string {
	mode = format.OneLine(mode)
	context = format.OneLine(context)
	brand := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render("heikou")
	left := brand + "  " + modeBadgeStyle.Render(mode)
	if context == "" {
		return truncateANSI(left, m.width)
	}
	right := mutedStyle.Render(context)
	space := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		// Mode is primary; counts and paths disappear before the mode label.
		return truncateANSI(left, m.width)
	}
	return left + strings.Repeat(" ", space) + right
}

func (m Model) renderRule() string {
	return faintStyle.Render(strings.Repeat("─", max(1, m.width)))
}

func (m Model) renderWorkstreams() string {
	height := m.listHeight()
	rows := m.rows()
	if len(rows) == 0 {
		line := mutedStyle.Render("  No workstreams yet. Press Ctrl-N to create one.")
		return line + strings.Repeat("\n", max(0, height-1))
	}
	start := 0
	if m.cursor >= height {
		start = m.cursor - height + 1
	}
	if start+height > len(rows) {
		start = max(0, len(rows)-height)
	}
	end := min(len(rows), start+height)
	lines := make([]string, 0, height)
	for index := start; index < end; index++ {
		row := rows[index]
		selected := row.key == m.selected
		switch row.kind {
		case rowWorkstream, rowOrphanHeader:
			lines = append(lines, m.renderWorkstreamRow(row, selected))
		case rowSession, rowOrphan:
			if session, ok := m.session(row.sessionID); ok {
				lines = append(lines, m.renderSessionRow(session, selected))
			}
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderWorkstreamRow(row listRow, selected bool) string {
	name := "Ungrouped"
	if row.kind == rowOrphanHeader {
		name = "Orphaned tmux"
	} else if row.workstreamID != "" {
		if item, ok := m.workstream(row.workstreamID); ok {
			name = item.Name
		}
	}
	sessions := m.sessionsForRow(row)
	live := 0
	latest := time.Time{}
	for _, session := range sessions {
		if session.Alive() {
			live++
		}
		if activity := session.LastActivity(); activity.After(latest) {
			latest = activity
		}
	}
	twist := "▾"
	if m.collapsed[row.key] {
		twist = "▸"
	}
	marker := " "
	if selected {
		marker = "›"
	}
	counts := fmt.Sprintf("%d/%d live", live, len(sessions))
	root := m.rootSummary(row)
	activity := ""
	if !latest.IsZero() {
		activity = format.RelativeTime(latest, time.Now())
	}
	plain := marker + " " + twist + " " + name + "  " + counts
	if root != "" {
		plain += "  " + root
	}
	if activity != "" {
		plain += "  " + activity
	}
	plain = padANSI(truncateANSI(plain, m.width), m.width)
	if selected {
		return selectedStyle.Render(ansi.Strip(plain))
	}
	return marker + " " + faintStyle.Render(twist) + " " + lipgloss.NewStyle().Bold(true).Render(name) +
		mutedStyle.Render(truncatePlain(strings.TrimPrefix(ansi.Strip(plain), marker+" "+twist+" "+name), max(0, m.width-lipgloss.Width(marker+" "+twist+" "+name))))
}

const (
	sessionRowRichMinWidth   = 64
	sessionRowRunnerMinWidth = 48
	// The root basename column is a fixed cost the summary pays for. Sixteen
	// columns bought little that a basename needs and took the difference out
	// of the one cell that carries content, so it is narrower here; the details
	// pane below still shows the full path.
	rowRootWidth = 12
)

func (m Model) renderSessionRow(session control.Session, selected bool) string {
	icon, status := statusLabel(session)
	iconStyle := mutedStyle
	if session.Alive() {
		iconStyle = liveStyle
	} else if code, ok := session.ExitCode(); (ok && code != 0) || session.Status == control.StatusStartFailed {
		iconStyle = failedStyle
	}
	const runnerWidth = 8
	marker := " "
	if selected {
		marker = "›"
	}
	// A marked session has to stay identifiable after the cursor leaves it,
	// because completing the move means selecting a different row.
	mark := " "
	markStyle := mutedStyle
	if session.ID == m.markedSession {
		mark, markStyle = "◆", noticeStyle
	}
	if m.width < sessionRowRichMinWidth {
		prefix := marker + " " + markStyle.Render(mark) + " " + iconStyle.Render(icon) + " "
		if m.width >= sessionRowRunnerMinWidth {
			prefix += padANSI(backendStyle(session.Backend).Render(string(session.Backend)), runnerWidth) + " "
		}
		prefix += padANSI(mutedStyle.Render(truncatePlain(status, 11)), 11) + " "
		taskWidth := max(1, m.width-ansi.StringWidth(prefix))
		row := prefix + padPlain(m.sessionRowSummary(session, taskWidth), taskWidth)
		row = padANSI(truncateANSI(row, m.width), m.width)
		if selected {
			return selectedStyle.Render(ansi.Strip(row))
		}
		return row
	}
	// 35 columns of prefix (marker, icon, runner, short id, state) plus the
	// nine-column runtime suffix. This used to read 42, two short of what the
	// row actually builds, so every row overflowed by two columns and the final
	// truncate clipped the tail of the runtime — a session alive ten hours or
	// more rendered "23h59" instead of "23h59m".
	fixedWidth := 44
	path := ""
	if m.width >= 96 {
		path = truncatePlain(format.OneLine(filepath.Base(session.Root)), rowRootWidth)
		fixedWidth += rowRootWidth + 2
	}
	taskWidth := max(1, m.width-fixedWidth)
	task := m.sessionRowSummary(session, taskWidth)
	row := marker + " " + markStyle.Render(mark) + " " + iconStyle.Render(icon) + " " +
		padANSI(backendStyle(session.Backend).Render(string(session.Backend)), runnerWidth) + " " +
		padPlain(format.ShortID(session.ID), 7) + " " +
		padANSI(mutedStyle.Render(truncatePlain(status, 11)), 11) + " " +
		padPlain(task, taskWidth)
	if path != "" {
		row += "  " + padANSI(mutedStyle.Render(path), rowRootWidth)
	}
	row += "  " + padANSI(mutedStyle.Render(format.Duration(session.RuntimeDuration(time.Now()))), 7)
	row = padANSI(truncateANSI(row, m.width), m.width)
	if selected {
		return selectedStyle.Render(ansi.Strip(row))
	}
	return row
}

func (m Model) renderDetails() string {
	height := m.detailHeight()
	if height <= 0 {
		return ""
	}
	row, ok := m.selectedRow()
	if !ok {
		return strings.Repeat("\n", height)
	}
	if row.kind == rowWorkstream || row.kind == rowOrphanHeader {
		return m.renderWorkstreamDetails(row, height)
	}
	selected, ok := m.session(row.sessionID)
	if !ok {
		return strings.Repeat("\n", height)
	}
	width := max(10, m.width-2)
	statusIcon, status := statusLabel(selected)
	header := " " + backendStyle(selected.Backend).Render(string(selected.Backend)) +
		"  " + selectedKeyHint.Render(format.ShortID(selected.ID)) + "  " + statusIcon + " " + status +
		"  " + mutedStyle.Render(format.Duration(selected.RuntimeDuration(time.Now())))
	lines := []string{truncateANSI(header, m.width)}
	if len(lines) < height {
		lines = append(lines, mutedStyle.Render(" title ")+truncatePlain(m.sessionDisplayTitle(selected), max(8, width-7)))
	}
	if detail := m.sessionSecondaryDetail(selected); detail != "" && len(lines) < height {
		lines = append(lines, mutedStyle.Render("       ")+truncatePlain(detail, max(8, width-7)))
	}
	if strings.TrimSpace(selected.Record.Title) != "" && strings.TrimSpace(selected.LastUserMessage) != "" && len(lines) < height {
		initial := "initial task · " + format.OneLine(selected.Prompt)
		lines = append(lines, mutedStyle.Render("       ")+truncatePlain(initial, max(8, width-7)))
	}
	if len(lines) < height {
		path := selected.Root
		if selected.Runtime != nil && selected.Runtime.CurrentPath != "" {
			path = selected.Runtime.CurrentPath
		}
		lines = append(lines, mutedStyle.Render(" cwd   ")+truncatePlain(format.OneLine(format.CompactPath(path)), max(8, width-7)))
	}
	if len(lines) < height {
		container := m.workstreamName(selected.WorkstreamID)
		state := container + " · durable launch identity"
		if selected.Orphaned {
			state = "orphaned tmux runtime · no durable membership"
		} else if selected.Runtime != nil && !selected.Runtime.LastActivityAt.IsZero() {
			state += " · terminal activity " + format.RelativeTime(selected.Runtime.LastActivityAt, time.Now())
		}
		lines = append(lines, mutedStyle.Render(" state ")+truncatePlain(state, max(8, width-7)))
	}
	if len(lines) < height {
		lines = append(lines, faintStyle.Render(" "+strings.Repeat("─", max(1, m.width-2))))
	}
	outputRoom := height - len(lines)
	if outputRoom > 0 {
		preview := m.preview
		if !selected.Available() {
			preview = unavailableMessage(selected)
		} else if m.previewID != selected.ID {
			preview = "loading terminal preview…"
		}
		previewLines := wrapLines(format.Sanitize(preview), width)
		if len(previewLines) == 0 {
			previewLines = []string{"terminal preview is empty"}
		}
		if len(previewLines) > outputRoom {
			previewLines = previewLines[len(previewLines)-outputRoom:]
		}
		for _, line := range previewLines {
			lines = append(lines, " "+truncatePlain(line, width))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func (m Model) renderWorkstreamDetails(row listRow, height int) string {
	name := "Ungrouped"
	description := "Durable sessions without a workstream."
	if row.kind == rowOrphanHeader {
		name = "Orphaned tmux"
		description = "Runtime panes with no durable SessionRecord; membership is intentionally ignored."
	} else if row.workstreamID != "" {
		if item, ok := m.workstream(row.workstreamID); ok {
			name, description = item.Name, item.Description
			if description == "" {
				description = "Durable organization only; no manager or autonomy semantics."
			}
		}
	}
	sessions := m.sessionsForRow(row)
	live := 0
	for _, session := range sessions {
		if session.Alive() {
			live++
		}
	}
	lines := []string{" " + lipgloss.NewStyle().Bold(true).Render(name) + mutedStyle.Render(fmt.Sprintf("  %d live · %d total", live, len(sessions)))}
	if height > 1 {
		lines = append(lines, mutedStyle.Render(" about ")+truncatePlain(description, max(1, m.width-8)))
	}
	if height > 2 {
		lines = append(lines, mutedStyle.Render(" root  ")+truncatePlain(format.CompactPath(m.launchRoot()), max(1, m.width-8)))
	}
	// The remaining rows are the workstream's own notes and files, which is the
	// context a workstream row has to offer in the slot a session row fills with
	// its terminal preview.
	if item, ok := m.selectedWorkstreamContext(); ok && height > len(lines) {
		lines = append(lines, m.renderWorkstreamArtifacts(item, height-len(lines))...)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:height], "\n")
}

func (m Model) renderComposer() string {
	prefix := m.composerPrefix()
	composer := strings.Join(m.renderComposerInput(prefix), "\n")
	// The chords are named as a family rather than spelled out. Five verbs plus
	// four launch bindings does not fit a terminal width anyone uses, and the
	// pointer to full help is the one part that must never be the bit truncated.
	help := fmt.Sprintf("Enter start · %s reply · %s runner · %s root · Ctrl-N/R/T/O/V organize · ? help",
		helpKeyLabel(m.settings.ReplyKey()), helpKeyLabel(m.settings.CycleRunnerKey()),
		helpKeyLabel(m.settings.CycleRootKey()))
	if m.replyTarget != "" {
		help = "Enter send · Esc compose a new session · Shift-Enter newline · Ctrl-G resize · ? help"
	}
	if m.markedSession != "" {
		help = "◆ " + format.ShortID(m.markedSession) + " marked · select a workstream and press Ctrl-T · Esc cancels"
	}
	if m.composerEdit != composerEditNone {
		help = "Enter saves · Esc cancels"
	}
	if m.resizeMode {
		help = "RESIZE · ↑ bigger snapshot · ↓ more sessions · PgUp/PgDn faster · r reset · Ctrl-G/Esc done"
	}
	message := help
	style := mutedStyle
	if m.errorText != "" {
		message, style = "error: "+m.errorText, failedStyle
	} else if m.notice != "" {
		message, style = m.notice, noticeStyle
	}
	return m.renderRule() + "\n" + composer + "\n" + style.Render(truncatePlain(format.OneLine(message), m.width))
}

// composerPrefix names the destination Enter will commit to. It is the only
// thing distinguishing a new session from a reply, so it has to read as a
// different bar rather than a subtle variation on the same one.
func (m Model) composerPrefix() string {
	// An organize edit is one more destination rather than a separate input, so
	// it names itself in the same place a reply does.
	if label := m.composerEditLabel(); label != "" {
		text := label + " › "
		if lipgloss.Width(text) > max(1, m.width-8) {
			text = truncatePlain(text, max(4, m.width-8))
		}
		return noticeStyle.Bold(true).Render(text)
	}
	if m.replyTarget != "" {
		target, found := m.session(m.replyTarget)
		label := format.ShortID(m.replyTarget)
		style := mutedStyle.Bold(true)
		if found {
			label += " · " + m.sessionDisplayTitle(target)
			style = backendStyle(target.Backend).Bold(true)
		}
		text := "↳ reply " + label + " › "
		if lipgloss.Width(text) > max(1, m.width-8) {
			text = truncatePlain(text, max(4, m.width-8))
		}
		return style.Render(text)
	}
	contextLabel := m.workstreamName(m.launchWorkstreamID()) + " · " + format.CompactPath(m.launchRoot())
	prefixText := string(m.backend) + " · " + contextLabel + " › "
	if lipgloss.Width(prefixText) > max(1, m.width-8) {
		contextLabel = truncatePlain(contextLabel, max(3, m.width-lipgloss.Width(string(m.backend))-8))
		prefixText = string(m.backend) + " · " + contextLabel + " › "
	}
	return backendStyle(m.backend).Bold(true).Render(prefixText)
}

func (m Model) renderSettings() string {
	return m.fitPane(m.settingsLines(), "e edit JSON · r reload · ↑↓/PgUp/PgDn scroll · ? help · Esc / Ctrl-S dashboard")
}

func (m Model) settingsLines() []string {
	title := m.renderModeHeader("SETTINGS", "")
	state := "not created yet"
	if m.store.Exists() {
		state = "JSON file"
	}
	lines := []string{
		title, m.renderRule(), "",
		mutedStyle.Render(" config   ") + truncatePlain(format.OneLine(format.CompactPath(m.store.Path)), max(1, m.width-10)),
		mutedStyle.Render(" state    ") + state,
		mutedStyle.Render(" app data ") + truncatePlain(format.OneLine(format.CompactPath(m.snapshot.StatePath)), max(1, m.width-10)),
		mutedStyle.Render(" startup default  ") + backendStyle(m.settings.DefaultRunner).Render(string(m.settings.DefaultRunner)),
		"", lipgloss.NewStyle().Bold(true).Render(" composer keys"),
		mutedStyle.Render(" commit ") + "Enter sends to the destination shown in the composer",
		mutedStyle.Render(" empty  ") + helpKeyLabel(m.settings.ReplyKey()) + " reply to selection",
		mutedStyle.Render(" any    ") + helpKeyLabel(m.settings.CycleRunnerKey()) + " cycle runner · " + helpKeyLabel(m.settings.CycleRootKey()) + " cycle root",
		"", lipgloss.NewStyle().Bold(true).Render(" launch commands"),
	}
	for _, backend := range []heikou.Backend{heikou.BackendCodex, heikou.BackendClaude} {
		raw := jsonCommand(m.settings.Command(backend))
		lines = append(lines, " "+padPlain(string(backend), 9)+truncatePlain(raw, max(1, m.width-11)))
		resolved, err := runner.ResolveCommand(backend, m.settings.Command(backend))
		resolution := jsonCommand(resolved)
		if err != nil {
			resolution = "missing · " + format.OneLine(err.Error())
		}
		lines = append(lines, mutedStyle.Render("   resolved ")+truncatePlain(resolution, max(1, m.width-12)))
	}
	lines = append(lines, " "+padPlain(string(heikou.BackendNoAgent), 9)+"tmux default shell")

	lines = append(lines, m.briefSettingsLines()...)

	lines = append(lines, "", mutedStyle.Render(" Commands are JSON argv arrays. Composer bindings apply after reload."))
	return lines
}

func (m Model) fitPane(lines []string, help string) string {
	message := ""
	style := noticeStyle
	if m.errorText != "" {
		message, style = "error: "+m.errorText, failedStyle
	} else if m.notice != "" {
		message = m.notice
	}
	footer := []string{m.renderRule(), style.Render(truncatePlain(format.OneLine(message), m.width)), mutedStyle.Render(truncatePlain(help, m.width))}
	available := max(0, m.height-len(footer))
	offset := min(max(0, m.settingsOffset), max(0, len(lines)-available))
	if len(lines) > available {
		lines = lines[offset:min(len(lines), offset+available)]
	}
	for len(lines) < available {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	if len(lines) > m.height {
		lines = lines[:max(0, m.height)]
	}
	for index := range lines {
		lines[index] = truncateANSI(lines[index], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) settingsViewportHeight() int { return max(0, m.height-3) }

func (m Model) settingsMaxOffset() int {
	return max(0, len(m.settingsLines())-m.settingsViewportHeight())
}

func (m *Model) clampSettingsOffset() {
	m.settingsOffset = min(max(0, m.settingsOffset), m.settingsMaxOffset())
}

func (m Model) rows() []listRow {
	return m.overview.rows(m.collapsed)
}

func (m *Model) restoreSelection() {
	rows := m.rows()
	if len(rows) == 0 {
		m.cursor, m.selected = 0, ""
		return
	}
	// A freshly created workstream becomes the selection as soon as it lands in
	// a snapshot, so the composer's launch target follows the create.
	if m.pendingWorkstream != "" {
		m.selected = workstreamRowKey(m.pendingWorkstream)
		m.pendingWorkstream = ""
	}
	if m.selected != "" {
		for index, row := range rows {
			if row.key == m.selected {
				m.cursor = index
				return
			}
		}
	}
	if m.selected == "" {
		for index, row := range rows {
			if row.key == ungroupedKey {
				m.cursor, m.selected = index, row.key
				return
			}
		}
	}
	m.cursor = min(max(0, m.cursor), len(rows)-1)
	m.selected = rows[m.cursor].key
}

func (m *Model) selectRowKey(key string) {
	if key == "" {
		return
	}
	m.selected = key
	m.restoreSelection()
}

func (m Model) selectedRow() (listRow, bool) {
	for _, row := range m.rows() {
		if row.key == m.selected {
			return row, true
		}
	}
	return listRow{}, false
}

func (m Model) selectedSession() (control.Session, bool) {
	row, ok := m.selectedRow()
	if !ok || (row.kind != rowSession && row.kind != rowOrphan) {
		return control.Session{}, false
	}
	return m.session(row.sessionID)
}

func (m Model) moveSelection(delta int) (tea.Model, tea.Cmd) {
	rows := m.rows()
	if len(rows) == 0 {
		return m, nil
	}
	m.cursor = min(len(rows)-1, max(0, m.cursor+delta))
	m.selected = rows[m.cursor].key
	m.previewID = ""
	m.alignRootToSelection()
	// Notes and files are read when the selection lands on a new workstream and
	// cached until it leaves, so scrubbing the list is the only thing that costs
	// a filesystem read. A stationary cursor costs nothing.
	context := m.artifactContextCmd(false)
	if selected, ok := m.selectedSession(); ok && selected.Available() {
		return m, tea.Batch(m.requestPreview(selected.ID), context)
	}
	m.previewFetch.queuedID = ""
	m.preview = ""
	return m, context
}

// moveToAdjacentGroup puts the cursor on the nearest workstream header above or
// below it, so a long list is crossed in group-sized steps instead of one
// session at a time. A collapsed group is a single row, so it needs no special
// handling here, and Orphaned counts as a group because the list draws it as
// one.
//
// From inside a group, up lands on that group's own header rather than skipping
// past it. That is the ordinary section-motion rule, and it makes the first
// press a dependable "top of what I am looking at" instead of a jump the cursor
// has to be walked back from. Nothing moves when no header remains in that
// direction: the end of the list is not a mistake worth a message.
func (m Model) moveToAdjacentGroup(delta int) (tea.Model, tea.Cmd) {
	rows := m.rows()
	for index := m.cursor + delta; index >= 0 && index < len(rows); index += delta {
		if rows[index].kind == rowWorkstream || rows[index].kind == rowOrphanHeader {
			return m.moveSelection(index - m.cursor)
		}
	}
	return m, nil
}

func (m *Model) toggleSelectedGroup(collapse bool) bool {
	row, ok := m.selectedRow()
	if !ok || (row.kind != rowWorkstream && row.kind != rowOrphanHeader) {
		return false
	}
	m.collapsed[row.key] = collapse
	return true
}

func (m Model) session(id string) (control.Session, bool) {
	return m.overview.session(id)
}

func (m Model) workstream(id string) (workstream.Workstream, bool) {
	return m.overview.workstream(id)
}

func (m Model) sessionsForRow(row listRow) []control.Session {
	return m.overview.sessionsFor(row)
}

func (m Model) launchWorkstreamID() string {
	row, ok := m.selectedRow()
	if !ok || row.kind == rowOrphan || row.kind == rowOrphanHeader {
		return ""
	}
	if row.kind == rowSession {
		if session, found := m.session(row.sessionID); found {
			return session.WorkstreamID
		}
	}
	return row.workstreamID
}

func (m Model) launchRoot() string {
	workstreamID := m.launchWorkstreamID()
	if workstreamID != "" {
		if item, ok := m.workstream(workstreamID); ok && len(item.Roots) > 0 {
			return item.Roots[m.rootPosition(workstreamID, len(item.Roots))]
		}
	}
	if session, ok := m.selectedSession(); ok && session.Root != "" {
		return session.Root
	}
	return m.root
}

func (m Model) workstreamName(id string) string {
	if id == "" {
		return "Ungrouped"
	}
	if item, ok := m.workstream(id); ok {
		return item.Name
	}
	return "Unavailable workstream"
}

func (m *Model) cycleSelectedRoot(delta int) bool {
	id := m.launchWorkstreamID()
	return m.cycleRoot(id, delta)
}

func (m *Model) cycleRoot(id string, delta int) bool {
	item, ok := m.workstream(id)
	if !ok || len(item.Roots) < 2 {
		return false
	}
	position := m.rootPosition(id, len(item.Roots))
	m.rootIndex[id] = (position + delta + len(item.Roots)) % len(item.Roots)
	return true
}

func (m Model) rootPosition(id string, count int) int {
	if count <= 0 {
		return 0
	}
	return ((m.rootIndex[id] % count) + count) % count
}

func (m *Model) alignRootToSelection() {
	session, ok := m.selectedSession()
	if !ok || session.WorkstreamID == "" {
		return
	}
	item, ok := m.workstream(session.WorkstreamID)
	if !ok {
		return
	}
	for index, root := range item.Roots {
		if filepath.Clean(root) == filepath.Clean(session.Root) {
			m.rootIndex[item.ID] = index
			return
		}
	}
}

func (m Model) rootSummary(row listRow) string {
	if row.kind == rowOrphanHeader {
		return "unmanaged"
	}
	if row.workstreamID == "" {
		return format.CompactPath(m.root)
	}
	item, ok := m.workstream(row.workstreamID)
	if !ok || len(item.Roots) == 0 {
		return ""
	}
	name := filepath.Base(item.Roots[m.rootPosition(item.ID, len(item.Roots))])
	if len(item.Roots) > 1 {
		name += fmt.Sprintf(" +%d", len(item.Roots)-1)
	}
	return name
}

// selectedWorkstreamContext resolves the workstream whose notes and files the
// detail pane should show. A session row resolves to its parent, so moving
// between a workstream and its members does not thrash the cache.
func (m Model) selectedWorkstreamContext() (workstream.Workstream, bool) {
	row, ok := m.selectedRow()
	if !ok || row.workstreamID == "" || row.kind == rowOrphan || row.kind == rowOrphanHeader {
		return workstream.Workstream{}, false
	}
	return m.workstream(row.workstreamID)
}

func (m *Model) insertText(value string) {
	inserted := splitGraphemes(composerSafeText(value))
	if len(inserted) == 0 {
		return
	}
	tail := append([]string(nil), m.input[m.inputCursor:]...)
	m.input = append(m.input[:m.inputCursor], inserted...)
	m.input = append(m.input, tail...)
	m.inputCursor += len(inserted)
	m.resetInputColumn()
}

func (m *Model) deletePreviousWord() {
	start := previousWordBoundary(m.input, m.inputCursor)
	if start == m.inputCursor {
		return
	}
	m.input = append(m.input[:start], m.input[m.inputCursor:]...)
	m.inputCursor = start
	m.resetInputColumn()
}

func (m *Model) clearInput() {
	m.input = nil
	m.inputCursor = 0
	m.resetInputColumn()
}

func (m Model) inputValue() string { return strings.Join(m.input, "") }

func (m Model) renderTextInput(clusters []string, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	cursorCellWidth := 1
	if cursor < len(clusters) {
		cursorCellWidth = max(1, lipgloss.Width(clusters[cursor]))
	}
	start, used := cursor, min(width, cursorCellWidth)
	for start > 0 {
		clusterWidth := lipgloss.Width(clusters[start-1])
		if used+clusterWidth > width {
			break
		}
		start--
		used += clusterWidth
	}
	end := cursor
	if cursor < len(clusters) {
		end++
	}
	for end < len(clusters) {
		clusterWidth := lipgloss.Width(clusters[end])
		if used+clusterWidth > width {
			break
		}
		end++
		used += clusterWidth
	}
	visible := clusters[start:end]
	visibleCursor := cursor - start
	var result strings.Builder
	for index := 0; index <= len(visible); index++ {
		if index == visibleCursor {
			character := " "
			if index < len(visible) {
				character = visible[index]
			}
			result.WriteString(lipgloss.NewStyle().Reverse(true).Render(character))
			if index < len(visible) {
				continue
			}
		}
		if index < len(visible) {
			result.WriteString(visible[index])
		}
	}
	return truncateANSI(result.String(), width)
}

func (m *Model) requestSnapshot() tea.Cmd {
	if m.snapshotFetch.activeGeneration != 0 {
		m.snapshotFetch.queued = true
		return nil
	}
	m.snapshotFetch.generation++
	m.snapshotFetch.activeGeneration = m.snapshotFetch.generation
	return m.fetchSnapshotCmd(m.snapshotFetch.activeGeneration)
}

func (m *Model) finishSnapshot(generation uint64) (bool, tea.Cmd) {
	if generation == 0 || generation != m.snapshotFetch.activeGeneration {
		return false, nil
	}
	m.snapshotFetch.activeGeneration = 0
	queued := m.snapshotFetch.queued
	m.snapshotFetch.queued = false
	if queued {
		return true, m.requestSnapshot()
	}
	return true, nil
}

func (m Model) fetchSnapshotCmd(generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		snapshot, err := m.controller.Snapshot(ctx)
		return snapshotMsg{generation: generation, snapshot: snapshot, err: err}
	}
}

func (m *Model) requestPreview(id string) tea.Cmd {
	if id == "" {
		m.previewFetch.queuedID = ""
		return nil
	}
	if m.previewFetch.activeGeneration != 0 {
		m.previewFetch.queuedID = id
		return nil
	}
	m.previewFetch.generation++
	m.previewFetch.activeGeneration = m.previewFetch.generation
	m.previewFetch.activeID = id
	m.previewFetch.queuedID = ""
	return m.capturePreviewCmd(m.previewFetch.activeGeneration, id)
}

func (m *Model) finishPreview(generation uint64, id string) (bool, string) {
	if generation == 0 || generation != m.previewFetch.activeGeneration || id != m.previewFetch.activeID {
		return false, ""
	}
	m.previewFetch.activeGeneration = 0
	m.previewFetch.activeID = ""
	queuedID := m.previewFetch.queuedID
	m.previewFetch.queuedID = ""
	return true, queuedID
}

func (m Model) capturePreviewCmd(generation uint64, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		preview, err := m.controller.Capture(ctx, id, 120)
		return previewMsg{generation: generation, id: id, text: preview, err: err}
	}
}

func (m Model) startCmd(prompt string) tea.Cmd {
	root, workstreamID := m.launchRoot(), m.launchWorkstreamID()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		session, err := m.controller.Start(ctx, control.StartRequest{
			Backend: m.backend, Prompt: prompt, Root: root, WorkstreamID: workstreamID,
		})
		return startMsg{session: session, err: err}
	}
}

func (m Model) sendCmd(id, message string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		return sendMsg{id: id, err: m.controller.Send(ctx, id, message)}
	}
}

func (m Model) stopCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		return stopMsg{id: id, err: m.controller.Stop(ctx, id)}
	}
}

func (m Model) deleteSessionCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		return deleteMsg{id: id, err: m.controller.DeleteSession(ctx, id)}
	}
}

func (m Model) attachCommandCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		command, err := m.controller.AttachCommand(ctx, id)
		return attachReadyMsg{command: command, err: err}
	}
}

func (m Model) loadSettingsCmd() tea.Cmd {
	return func() tea.Msg {
		settings, err := m.store.Load()
		return settingsMsg{settings: settings, err: err}
	}
}

func (m Model) createWorkstreamCmd(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		item, err := m.controller.CreateWorkstream(ctx, name, "", []string{m.root})
		return workstreamMsg{action: "create", item: item, err: err}
	}
}

func (m Model) addRootCmd(workstreamID, root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.AddRoot(ctx, workstreamID, root)
		return workstreamMsg{action: "root_add", workstreamID: workstreamID, root: root, err: err}
	}
}

func (m Model) replaceRootCmd(workstreamID, current, replacement string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.ReplaceRoot(ctx, workstreamID, current, replacement)
		return workstreamMsg{action: "root_replace", workstreamID: workstreamID, root: replacement, err: err}
	}
}

func (m Model) removeRootCmd(workstreamID, root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.RemoveRoot(ctx, workstreamID, root)
		return workstreamMsg{action: "root_remove", workstreamID: workstreamID, root: root, err: err}
	}
}

func (m Model) renameWorkstreamCmd(id, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.RenameWorkstream(ctx, id, name)
		return workstreamMsg{action: "rename", workstreamID: id, err: err}
	}
}

func (m Model) setSessionTitleCmd(id, title string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.SetSessionTitle(ctx, id, title)
		return sessionTitleMsg{id: id, title: title, err: err}
	}
}

// archiveWorkstreamCmd routes the archive through control.Service like every
// other human mutation the dashboard makes; the UI never reaches the store.
func (m Model) archiveWorkstreamCmd(id, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.ArchiveWorkstream(ctx, id)
		return workstreamMsg{action: "archive", workstreamID: id, name: name, err: err}
	}
}

func (m Model) reorderWorkstreamCmd(id string, delta int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		moved, err := m.controller.ReorderWorkstream(ctx, id, delta)
		return workstreamMsg{action: "reorder", workstreamID: id, delta: delta, moved: moved, err: err}
	}
}

func (m Model) moveSessionCmd(sessionID, workstreamID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.MoveSession(ctx, sessionID, workstreamID)
		return workstreamMsg{action: "move", sessionID: sessionID, workstreamID: workstreamID, err: err}
	}
}

func (m Model) adoptSessionCmd(sessionID, workstreamID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		_, err := m.controller.AdoptSession(ctx, sessionID, workstreamID)
		return workstreamMsg{action: "adopt", sessionID: sessionID, workstreamID: workstreamID, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func backendStyle(backend heikou.Backend) lipgloss.Style {
	switch backend {
	case heikou.BackendClaude:
		return lipgloss.NewStyle().Foreground(colorClaude)
	case heikou.BackendNoAgent:
		return lipgloss.NewStyle().Foreground(colorNoAgent)
	default:
		return lipgloss.NewStyle().Foreground(colorCodex)
	}
}

func statusLabel(session control.Session) (string, string) {
	switch session.Status {
	case control.StatusLive:
		if session.Runtime != nil && session.Runtime.AttachedClients > 0 {
			return "●", "attached"
		}
		return "●", "live"
	case control.StatusStartFailed:
		return "×", "start failed"
	case control.StatusUnavailable:
		return "?", "unavailable"
	case control.StatusStopped:
		return "■", "stopped"
	case control.StatusExited:
		if code, ok := session.ExitCode(); ok {
			if code != 0 {
				return "×", "failed " + strconv.Itoa(code)
			}
		} else {
			return "○", "exited ?"
		}
		return "○", "exited"
	default:
		return "?", string(session.Status)
	}
}

func unavailableMessage(session control.Session) string {
	if session.Status == control.StatusStartFailed && session.Record.Outcome != nil {
		return "start failed: " + session.Record.Outcome.Error
	}
	switch session.Status {
	case control.StatusStopped:
		return "runtime was explicitly stopped; the durable session record remains"
	case control.StatusExited:
		return "runtime exited; no retained tmux pane is currently available"
	default:
		return "runtime unavailable: no pane and no terminal outcome was inferred"
	}
}

func workstreamRowKey(id string) string {
	if id == "" {
		return ungroupedKey
	}
	return "workstream:" + id
}

func sessionRowKey(session control.Session) string {
	if session.Orphaned {
		return "orphan:" + session.ID
	}
	return "session:" + session.ID
}

func inlineSafeText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
}

func composerSafeText(value string) string {
	value = ansi.Strip(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
}

func normalizeComposerPaste(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func normalizeInlinePaste(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Join(strings.Fields(value), " ")
}

func wrapLines(value string, width int) []string {
	if width <= 0 || value == "" {
		return nil
	}
	var result []string
	for _, sourceLine := range strings.Split(value, "\n") {
		line := strings.TrimRight(sourceLine, " \t")
		if line == "" {
			result = append(result, "")
			continue
		}
		for lipgloss.Width(line) > width {
			candidate := ansi.Cut(line, 0, width)
			cut := len(candidate)
			if index := strings.LastIndexAny(candidate, " \t"); index > width/3 {
				cut = index
			}
			result = append(result, strings.TrimRight(line[:cut], " \t"))
			line = strings.TrimLeft(line[cut:], " \t")
		}
		result = append(result, line)
	}
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}
	return result
}

func truncatePlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, "…")
}

func truncateANSI(value string, width int) string { return ansi.Truncate(value, max(0, width), "") }

func clipPane(value string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		lines[index] = truncateANSI(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

func padPlain(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}
func padANSI(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func adaptive(light, dark string) compat.AdaptiveColor {
	return compat.AdaptiveColor{Light: lipgloss.Color(light), Dark: lipgloss.Color(dark)}
}

func splitGraphemes(value string) []string {
	iterator := graphemes.FromString(value)
	var result []string
	for iterator.Next() {
		result = append(result, iterator.Value())
	}
	return result
}

func clusterIsSpace(value string) bool {
	for _, r := range value {
		return unicode.IsSpace(r)
	}
	return false
}

func jsonCommand(command []string) string {
	data, err := json.Marshal(command)
	if err != nil {
		return "[]"
	}
	return format.OneLine(string(data))
}

func editorCommand(path string) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create editor directory: %w", err)
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return nil, errors.New("VISUAL or EDITOR must name an executable")
	}
	editorPath, err := exec.LookPath(parts[0])
	if err != nil {
		return nil, fmt.Errorf("find editor %q: %w", parts[0], err)
	}
	arguments := append(append([]string(nil), parts[1:]...), path)
	command := exec.Command(editorPath, arguments...)
	command.Env, command.Stdin, command.Stdout, command.Stderr = os.Environ(), os.Stdin, os.Stdout, os.Stderr
	return command, nil
}
