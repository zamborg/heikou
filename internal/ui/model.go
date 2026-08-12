package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/uax29/v2/graphemes"
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
	screenOrganizer
	screenSettings
)

type overlayKind uint8

const (
	overlayNone overlayKind = iota
	overlayHelp
)

type organizerEditMode uint8

const (
	organizerEditNone organizerEditMode = iota
	organizerEditCreate
	organizerEditWorkstreamName
	organizerEditSessionTitle
	organizerEditAddRoot
	organizerEditReplaceRoot
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
	snapshotFetch snapshotFetchState
	previewFetch  previewFetchState

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

	organizerCursor        int
	organizerSelected      string
	organizerCollapsed     map[string]bool
	organizerSource        string
	organizerEdit          organizerEditMode
	organizerRootTarget    string
	organizerInput         []string
	organizerInputCursor   int
	confirmArchive         string
	confirmRootRemoval     string
	pendingWorkstream      string
	organizerContext       organizerContextState
	organizerContextAdjust int
}

func New(controller control.Service, root string, backend heikou.Backend, store config.Store, settings config.Config) Model {
	return Model{
		controller: controller, root: root, backend: backend, store: store, settings: settings,
		overview: newOverviewModel(control.Snapshot{}), collapsed: make(map[string]bool), rootIndex: make(map[string]int), organizerCollapsed: make(map[string]bool),
	}
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
	sessionID    string
	delta        int
	moved        bool
	err          error
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
		return m, tea.Batch(m.requestSnapshot(), tickCmd())

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
		if m.organizerSource != "" {
			if _, found := m.session(m.organizerSource); !found {
				m.organizerSource = ""
			}
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
		m.restoreOrganizerSelection()
		if m.screen == screenOrganizer {
			return m, tea.Batch(queuedSnapshot, m.organizerContextCmd(false))
		}
		if selected, ok := m.selectedSession(); ok && selected.Available() {
			return m, tea.Batch(queuedSnapshot, m.requestPreview(selected.ID))
		}
		m.previewFetch.queuedID = ""
		m.preview, m.previewID = "", ""
		return m, queuedSnapshot

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
			m.confirmStop, m.confirmDelete = "", ""
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
		m.confirmStop, m.confirmDelete = "", ""
		return m, tea.Batch(m.requestSnapshot(), m.requestPreview(message.id))

	case stopMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.notice = "stopped runtime · " + format.ShortID(message.id)
		m.confirmStop = ""
		if source, ok := m.session(message.id); ok && source.Orphaned && m.organizerSource == message.id {
			m.organizerSource = ""
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
		if m.organizerSource == message.id {
			m.organizerSource = ""
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
		m.errorText = ""
		m.notice = "settings reloaded · commands apply to new sessions"
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
		m.organizerEdit = organizerEditNone
		m.organizerRootTarget = ""
		m.organizerInput, m.organizerInputCursor = nil, 0
		m.confirmArchive = ""
		m.confirmRootRemoval = ""
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
			m.notice = "archived workstream"
		case "move":
			m.organizerSource = ""
			m.organizerSelected = workstreamRowKey(message.workstreamID)
			m.notice = "moved session · " + format.ShortID(message.sessionID)
		case "adopt":
			m.organizerSource = ""
			m.organizerSelected = workstreamRowKey(message.workstreamID)
			m.notice = "adopted legacy runtime · " + format.ShortID(message.sessionID)
		case "root":
			m.notice = "added workstream root"
		case "root_replace":
			m.notice = "updated workstream root"
		case "root_remove":
			m.notice = "removed workstream root"
		}
		return m, m.requestSnapshot()

	case sessionTitleMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.organizerEdit = organizerEditNone
		m.organizerInput, m.organizerInputCursor = nil, 0
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
		if m.screen == screenOrganizer {
			return m, tea.Batch(m.requestSnapshot(), m.organizerContextCmd(true))
		}
		return m, m.requestSnapshot()

	case artifactContextMsg:
		m.acceptArtifactContext(message)
		return m, nil

	case tea.PasteMsg:
		if m.busy || m.screen == screenSettings || m.overlay == overlayHelp {
			return m, nil
		}
		m.resizeMode = false
		if m.screen == screenOrganizer {
			if m.organizerEdit != organizerEditNone {
				m.insertOrganizerText(normalizeInlinePaste(message.Content))
			}
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
	questionOpensHelp := stroke == "?" && (m.screen == screenSettings ||
		(m.screen == screenOrganizer && m.organizerEdit == organizerEditNone) ||
		(m.screen == screenDashboard && m.inputValue() == ""))
	if stroke == "f1" || questionOpensHelp {
		m.overlay = overlayHelp
		m.resizeMode = false
		m.helpOffset = 0
		m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		m.confirmRootRemoval = ""
		m.notice, m.errorText = "", ""
		return m, nil
	}
	if m.screen == screenSettings {
		return m.handleSettingsKey(stroke)
	}
	if stroke == "ctrl+g" && m.organizerEdit == organizerEditNone {
		m.resizeMode = !m.resizeMode
		m.notice, m.errorText = "", ""
		m.confirmStop, m.confirmDelete, m.confirmArchive = "", "", ""
		m.confirmRootRemoval = ""
		return m, nil
	}
	if m.resizeMode {
		return m.handleResizeKey(key)
	}
	if m.screen == screenOrganizer {
		return m.handleOrganizerKey(key)
	}
	if m.busy {
		return m, nil
	}

	m.errorText = ""
	if stroke != "ctrl+x" {
		m.confirmStop = ""
		m.confirmDelete = ""
	}
	draft := m.inputValue()
	prompt := strings.TrimSpace(draft)
	// The destination is chosen before typing and stays visible in the composer
	// prefix, so these bindings only ever change or announce the destination.
	// Enter alone commits, and it commits to whatever the prefix shows.
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
		m.openOrganizer()
		return m, m.organizerContextCmd(true)

	case "esc":
		if len(m.input) > 0 {
			m.clearInput()
			m.notice = "composer cleared"
			return m, nil
		}
		if m.replyTarget != "" {
			m.replyTarget = ""
			m.notice = "composing a new session"
			return m, nil
		}
		return m, tea.Quit

	case "up":
		if m.hasMultilineInput() {
			m.moveInputVertical(-1)
			return m, nil
		}
		return m.moveSelection(-1)

	case "down":
		if m.hasMultilineInput() {
			m.moveInputVertical(1)
			return m, nil
		}
		return m.moveSelection(1)

	case "pgup":
		return m.moveSelection(-max(1, m.listHeight()-1))

	case "pgdown":
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

	case "ctrl+r":
		m.backend = m.backend.Next()
		m.notice = "new sessions use " + string(m.backend)
		return m, nil

	case "enter":
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

func (m Model) handleOrganizerKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	if m.busy {
		return m, nil
	}
	m.errorText = ""
	if m.organizerEdit != organizerEditNone {
		switch stroke {
		case "esc":
			m.organizerEdit = organizerEditNone
			m.organizerRootTarget = ""
			m.organizerInput, m.organizerInputCursor = nil, 0
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.organizerValue())
			if value == "" && m.organizerEdit != organizerEditSessionTitle {
				m.errorText = "a value is required"
				return m, nil
			}
			row, ok := m.selectedOrganizerRow()
			m.busy = true
			switch m.organizerEdit {
			case organizerEditCreate:
				return m, m.createWorkstreamCmd(value)
			case organizerEditWorkstreamName:
				if !ok || row.kind != rowWorkstream || row.workstreamID == "" {
					m.busy = false
					return m, nil
				}
				return m, m.renameWorkstreamCmd(row.workstreamID, value)
			case organizerEditSessionTitle:
				if !ok || row.kind != rowSession {
					m.busy = false
					return m, nil
				}
				return m, m.setSessionTitleCmd(row.sessionID, value)
			case organizerEditAddRoot:
				if !ok || row.kind != rowWorkstream || row.workstreamID == "" {
					m.busy = false
					return m, nil
				}
				return m, m.addRootCmd(row.workstreamID, value)
			case organizerEditReplaceRoot:
				if !ok || row.kind != rowWorkstream || row.workstreamID == "" || m.organizerRootTarget == "" {
					m.busy = false
					return m, nil
				}
				return m, m.replaceRootCmd(row.workstreamID, m.organizerRootTarget, value)
			}
		case "left":
			if m.organizerInputCursor > 0 {
				m.organizerInputCursor--
			}
			return m, nil
		case "right":
			if m.organizerInputCursor < len(m.organizerInput) {
				m.organizerInputCursor++
			}
			return m, nil
		case "home", "ctrl+a":
			m.organizerInputCursor = 0
			return m, nil
		case "end", "ctrl+e":
			m.organizerInputCursor = len(m.organizerInput)
			return m, nil
		case "backspace", "ctrl+h":
			if m.organizerInputCursor > 0 {
				m.organizerInput = append(m.organizerInput[:m.organizerInputCursor-1], m.organizerInput[m.organizerInputCursor:]...)
				m.organizerInputCursor--
			}
			return m, nil
		case "delete":
			if m.organizerInputCursor < len(m.organizerInput) {
				m.organizerInput = append(m.organizerInput[:m.organizerInputCursor], m.organizerInput[m.organizerInputCursor+1:]...)
			}
			return m, nil
		}
		blockedModifiers := tea.ModCtrl | tea.ModAlt | tea.ModMeta | tea.ModHyper | tea.ModSuper
		if key.Text != "" && key.Mod&blockedModifiers == 0 {
			m.insertOrganizerText(key.Text)
		}
		return m, nil
	}

	if stroke != "a" {
		m.confirmArchive = ""
	}
	if stroke != "d" {
		m.confirmRootRemoval = ""
	}
	if stroke != "ctrl+x" {
		m.confirmStop, m.confirmDelete = "", ""
	}
	rows := m.organizerRows()
	switch stroke {
	case "esc", "f3":
		m.screen = screenDashboard
		m.organizerSource = ""
		m.confirmRootRemoval = ""
		m.notice = "workstreams closed"
		return m, nil
	case "shift+up", "shift+down":
		row, ok := m.selectedOrganizerRow()
		if !ok || row.kind != rowWorkstream || row.workstreamID == "" {
			m.errorText = "select a named workstream to reorder"
			return m, nil
		}
		delta := -1
		if stroke == "shift+down" {
			delta = 1
		}
		m.busy = true
		m.notice = "moving workstream…"
		return m, m.reorderWorkstreamCmd(row.workstreamID, delta)
	case "up":
		m.organizerCursor = max(0, m.organizerCursor-1)
		m.syncOrganizerSelection(rows)
		return m, m.organizerContextCmd(false)
	case "down":
		m.organizerCursor = min(max(0, len(rows)-1), m.organizerCursor+1)
		m.syncOrganizerSelection(rows)
		return m, m.organizerContextCmd(false)
	case "pgup":
		m.organizerCursor = max(0, m.organizerCursor-max(1, m.organizerViewportHeight()-1))
		m.syncOrganizerSelection(rows)
		return m, m.organizerContextCmd(false)
	case "pgdown":
		m.organizerCursor = min(max(0, len(rows)-1), m.organizerCursor+max(1, m.organizerViewportHeight()-1))
		m.syncOrganizerSelection(rows)
		return m, m.organizerContextCmd(false)
	case "left":
		row, ok := m.selectedOrganizerRow()
		if !ok {
			return m, nil
		}
		if organizerGroup(row) {
			m.organizerCollapsed[row.key] = true
			m.restoreOrganizerSelection()
			return m, nil
		}
		m.selectOrganizerKey(organizerParentKey(row))
		return m, m.organizerContextCmd(false)
	case "right":
		row, ok := m.selectedOrganizerRow()
		if ok && organizerGroup(row) {
			m.organizerCollapsed[row.key] = false
			m.restoreOrganizerSelection()
		}
		return m, nil
	case "enter":
		row, ok := m.selectedOrganizerRow()
		if !ok {
			return m, nil
		}
		if organizerDestination(row) && m.organizerSource != "" {
			return m.moveOrganizerSource(row.workstreamID)
		}
		if organizerGroup(row) {
			m.organizerCollapsed[row.key] = !m.organizerCollapsed[row.key]
			m.restoreOrganizerSelection()
			return m, nil
		}
		m.organizerSource = row.sessionID
		m.notice = "move source · " + format.ShortID(row.sessionID) + " · choose a workstream and press Enter"
		return m, nil
	case "u", " ", "space":
		row, ok := m.selectedOrganizerRow()
		if !ok {
			return m, nil
		}
		if row.kind == rowSession || row.kind == rowOrphan {
			m.collapsed[organizerParentKey(row)] = false
		}
		m.selected = row.key
		m.screen = screenDashboard
		m.organizerSource = ""
		m.restoreSelection()
		m.alignRootToSelection()
		m.notice = "dashboard selection · " + m.organizerRowName(row)
		return m, nil
	case "n":
		m.beginOrganizerEdit(organizerEditCreate, "")
		return m, nil
	case "R":
		if _, ok := m.selectedOrganizerWorkstream(); !ok {
			m.errorText = "select a named workstream or one of its sessions to refresh context"
			return m, nil
		}
		m.notice = "refreshing workstream context…"
		return m, m.organizerContextCmd(true)
	case "r":
		row, ok := m.selectedOrganizerRow()
		if !ok {
			return m, nil
		}
		switch row.kind {
		case rowWorkstream:
			if row.workstreamID != "" {
				m.beginOrganizerEdit(organizerEditWorkstreamName, m.organizerRowName(row))
			}
		case rowSession:
			if session, found := m.session(row.sessionID); found {
				m.beginOrganizerEdit(organizerEditSessionTitle, session.Record.Title)
			}
		case rowOrphan:
			m.errorText = "adopt this runtime before giving its session a durable title"
		}
		return m, nil
	case "p":
		row, ok := m.selectedOrganizerRow()
		if ok && row.kind == rowWorkstream && row.workstreamID != "" {
			m.beginOrganizerEdit(organizerEditAddRoot, m.root)
		}
		return m, nil
	case "P":
		row, ok := m.selectedOrganizerRow()
		root, found := m.selectedOrganizerRoot(row)
		if ok && found {
			m.organizerRootTarget = root
			m.beginOrganizerEdit(organizerEditReplaceRoot, root)
		}
		return m, nil
	case "d":
		row, ok := m.selectedOrganizerRow()
		root, found := m.selectedOrganizerRoot(row)
		if !ok || !found {
			m.errorText = "select a named workstream to remove its current root"
			return m, nil
		}
		container, _ := m.workstream(row.workstreamID)
		if len(container.Roots) <= 1 {
			m.errorText = "a workstream must keep one root; edit it with Shift-P"
			return m, nil
		}
		confirmation := row.workstreamID + "\x00" + root
		if m.confirmRootRemoval != confirmation {
			m.confirmRootRemoval = confirmation
			m.notice = "remove root " + filepath.Base(root) + " · press d again (files and sessions stay)"
			return m, nil
		}
		m.busy = true
		m.notice = "removing root · " + format.CompactPath(root)
		return m, m.removeRootCmd(row.workstreamID, root)
	case "tab":
		row, ok := m.selectedOrganizerRow()
		if ok && row.kind == rowWorkstream && row.workstreamID != "" {
			if m.cycleRoot(row.workstreamID, 1) {
				if root, found := m.selectedOrganizerRoot(row); found {
					m.notice = "launch root · " + format.CompactPath(root)
				}
			}
		}
		return m, nil
	case "m":
		row, ok := m.selectedOrganizerRow()
		if !ok {
			return m, nil
		}
		if row.kind == rowSession || row.kind == rowOrphan {
			if m.organizerSource == row.sessionID {
				m.organizerSource = ""
				m.notice = "move canceled"
			} else {
				m.organizerSource = row.sessionID
				m.notice = "move source · " + format.ShortID(row.sessionID) + " · choose a workstream and press Enter"
			}
			return m, nil
		}
		if !organizerDestination(row) || m.organizerSource == "" {
			m.errorText = "select a session with m, then select its destination"
			return m, nil
		}
		return m.moveOrganizerSource(row.workstreamID)
	case "ctrl+x":
		row, ok := m.selectedOrganizerRow()
		if !ok || (row.kind != rowSession && row.kind != rowOrphan) {
			m.errorText = "select a session to stop or delete"
			return m, nil
		}
		selected, ok := m.session(row.sessionID)
		if !ok {
			m.errorText = "session is no longer available"
			return m, nil
		}
		return m.handleSessionLifecycle(selected)
	case "a":
		row, ok := m.selectedOrganizerRow()
		if !ok || row.kind != rowWorkstream || row.workstreamID == "" {
			return m, nil
		}
		if m.confirmArchive != row.workstreamID {
			m.confirmArchive = row.workstreamID
			m.notice = "press a again to archive " + m.organizerRowName(row) + " (sessions become ungrouped)"
			return m, nil
		}
		m.busy = true
		return m, m.archiveWorkstreamCmd(row.workstreamID)
	case "e", "o":
		row, ok := m.selectedOrganizerRow()
		if !ok || row.kind != rowWorkstream || row.workstreamID == "" {
			return m, nil
		}
		container, ok := m.workstream(row.workstreamID)
		if !ok {
			return m, nil
		}
		var command *exec.Cmd
		var err error
		label := "artifact directory"
		if stroke == "e" {
			label = "notes editor"
			command, err = editorCommand(filepath.Join(container.ArtifactDir, "notes.md"))
		} else {
			command, err = openDirectoryCommand(container.ArtifactDir)
		}
		if err != nil {
			m.errorText = err.Error()
			return m, nil
		}
		m.busy = true
		return m, tea.ExecProcess(command, func(err error) tea.Msg { return externalMsg{label: label, err: err} })
	}
	return m, nil
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
	if m.screen == screenOrganizer {
		view := tea.NewView(textStyle.MaxWidth(m.width).Render(m.renderOrganizer()))
		view.AltScreen = true
		view.WindowTitle = "heikou · workstreams"
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
		line := mutedStyle.Render("  No workstreams yet. Press F3 to create one.")
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
	if m.width < sessionRowRichMinWidth {
		prefix := marker + "   " + iconStyle.Render(icon) + " "
		if m.width >= sessionRowRunnerMinWidth {
			prefix += padANSI(backendStyle(session.Backend).Render(string(session.Backend)), runnerWidth) + " "
		}
		prefix += padANSI(mutedStyle.Render(truncatePlain(status, 11)), 11) + " "
		taskWidth := max(1, m.width-ansi.StringWidth(prefix))
		row := prefix + padPlain(sessionRowSummary(session, taskWidth), taskWidth)
		row = padANSI(truncateANSI(row, m.width), m.width)
		if selected {
			return selectedStyle.Render(ansi.Strip(row))
		}
		return row
	}
	fixedWidth := 42
	path := ""
	if m.width >= 96 {
		path = truncatePlain(format.OneLine(filepath.Base(session.Root)), 16)
		fixedWidth += 18
	}
	taskWidth := max(1, m.width-fixedWidth)
	task := sessionRowSummary(session, taskWidth)
	row := marker + "   " + iconStyle.Render(icon) + " " +
		padANSI(backendStyle(session.Backend).Render(string(session.Backend)), runnerWidth) + " " +
		padPlain(format.ShortID(session.ID), 7) + " " +
		padANSI(mutedStyle.Render(truncatePlain(status, 11)), 11) + " " +
		padPlain(task, taskWidth)
	if path != "" {
		row += "  " + padANSI(mutedStyle.Render(path), 16)
	}
	row += "  " + padANSI(mutedStyle.Render(format.Duration(session.RuntimeDuration(time.Now()))), 7)
	row = padANSI(truncateANSI(row, m.width), m.width)
	if selected {
		return selectedStyle.Render(ansi.Strip(row))
	}
	return row
}

func sessionDisplayTitle(session control.Session) string {
	if title := strings.TrimSpace(session.Record.Title); title != "" {
		return format.OneLine(title)
	}
	if prompt := strings.TrimSpace(session.Prompt); prompt != "" {
		return format.OneLine(prompt)
	}
	return string(session.Backend) + " session"
}

func sessionSecondaryDetail(session control.Session) string {
	if latest := strings.TrimSpace(session.LastUserMessage); latest != "" {
		return "latest via Heikou · " + format.OneLine(latest)
	}
	if strings.TrimSpace(session.Record.Title) != "" {
		if prompt := strings.TrimSpace(session.Prompt); prompt != "" {
			return "initial task · " + format.OneLine(prompt)
		}
	}
	return ""
}

func sessionRowSummary(session control.Session, width int) string {
	if width <= 0 {
		return ""
	}
	title := truncatePlain(sessionDisplayTitle(session), width)
	detail := sessionSecondaryDetail(session)
	if detail == "" || lipgloss.Width(title)+3 >= width {
		return title
	}
	return truncatePlain(title+" · "+detail, width)
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
		lines = append(lines, mutedStyle.Render(" title ")+truncatePlain(sessionDisplayTitle(selected), max(8, width-7)))
	}
	if detail := sessionSecondaryDetail(selected); detail != "" && len(lines) < height {
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
	artifact := ""
	if row.kind == rowOrphanHeader {
		name = "Orphaned tmux"
		description = "Runtime panes with no durable SessionRecord; membership is intentionally ignored."
	} else if row.workstreamID != "" {
		if item, ok := m.workstream(row.workstreamID); ok {
			name, description, artifact = item.Name, item.Description, item.ArtifactDir
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
	if height > 3 && artifact != "" {
		lines = append(lines, mutedStyle.Render(" files ")+truncatePlain(format.CompactPath(artifact), max(1, m.width-8)))
	}
	if height > 4 {
		lines = append(lines, faintStyle.Render(" F3 organize · Shift-Tab cycle roots · Enter collapse/expand"))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:height], "\n")
}

func (m Model) renderComposer() string {
	prefix := m.composerPrefix()
	composer := strings.Join(m.renderComposerInput(prefix), "\n")
	help := fmt.Sprintf("Enter start · %s reply · %s runner · %s root · Shift-Enter newline · Ctrl-G resize · ? help",
		helpKeyLabel(m.settings.ReplyKey()), helpKeyLabel(m.settings.CycleRunnerKey()),
		helpKeyLabel(m.settings.CycleRootKey()))
	if m.replyTarget != "" {
		help = "Enter send · Esc compose a new session · Shift-Enter newline · Ctrl-G resize · ? help"
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
	if m.replyTarget != "" {
		target, found := m.session(m.replyTarget)
		label := format.ShortID(m.replyTarget)
		style := mutedStyle.Bold(true)
		if found {
			label += " · " + sessionDisplayTitle(target)
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
	lines = append(lines, " "+padPlain(string(heikou.BackendNoAgent), 9)+"tmux default shell", "", mutedStyle.Render(" Commands are JSON argv arrays. Composer bindings apply after reload."))
	return lines
}

func (m Model) renderOrganizer() string {
	mode := "WORKSTREAM ORGANIZER"
	if m.resizeMode {
		mode += " · RESIZE"
	}
	title := m.renderModeHeader(mode, m.organizerContextLabel())
	lines := []string{title, m.renderRule()}
	rows := m.organizerRows()
	height := m.organizerViewportHeight()
	start := 0
	if m.organizerCursor >= height {
		start = m.organizerCursor - height + 1
	}
	if start+height > len(rows) {
		start = max(0, len(rows)-height)
	}
	end := min(len(rows), start+height)
	for index := start; index < end; index++ {
		row := rows[index]
		if organizerGroup(row) {
			lines = append(lines, m.renderOrganizerGroupRow(row, index == m.organizerCursor))
		} else if session, ok := m.session(row.sessionID); ok {
			lines = append(lines, m.renderOrganizerSessionRow(session, index == m.organizerCursor, session.ID == m.organizerSource))
		}
	}
	for len(lines) < 2+height {
		lines = append(lines, "")
	}
	lines = append(lines, m.renderOrganizerContext(m.organizerContextHeight())...)
	if m.organizerEdit != organizerEditNone {
		label := map[organizerEditMode]string{
			organizerEditCreate:         "new name",
			organizerEditWorkstreamName: "rename",
			organizerEditSessionTitle:   "session title",
			organizerEditAddRoot:        "add root",
			organizerEditReplaceRoot:    "edit root",
		}[m.organizerEdit]
		prefix := noticeStyle.Render(label + " › ")
		lines = append(lines, prefix+m.renderTextInput(m.organizerInput, m.organizerInputCursor, max(1, m.width-lipgloss.Width(prefix))))
	}
	message := m.notice
	style := noticeStyle
	if m.errorText != "" {
		message, style = "error: "+m.errorText, failedStyle
	} else if message == "" && m.organizerSource != "" {
		message = "move source · " + format.ShortID(m.organizerSource) + " · choose a workstream and press Enter"
	}
	help := m.organizerHelp()
	lines = append(lines, m.renderRule(), style.Render(truncatePlain(format.OneLine(message), m.width)), mutedStyle.Render(truncatePlain(help, m.width)))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = truncateANSI(lines[index], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) organizerHelp() string {
	if m.resizeMode {
		return "↑ bigger notes/files · ↓ more sessions · PgUp/PgDn faster · r reset · Ctrl-G/Esc done"
	}
	if m.organizerEdit != organizerEditNone {
		return "Enter save · Esc cancel"
	}
	row, ok := m.selectedOrganizerRow()
	if !ok {
		return "↑↓ navigate · n new workstream · ? help · Esc dashboard"
	}
	switch row.kind {
	case rowSession:
		return "Enter mark for move · r title · R refresh files · u/Space select on dashboard · Ctrl-X stop/delete · ? help"
	case rowOrphan:
		return "Enter mark for move · R refresh files · u/Space select on dashboard · Ctrl-X stop/delete · ? help"
	case rowWorkstream:
		if m.organizerSource != "" {
			return "Enter move here · u/Space use on dashboard · m move here · ? help · Esc close"
		}
		if row.workstreamID != "" {
			return "Shift-↑↓ reorder · Ctrl-G resize · Enter expand/collapse · r rename · R refresh · u/Space use · p/P/d roots · ? help"
		}
		return "Enter expand/collapse · u/Space use Ungrouped · ? help · Esc dashboard"
	case rowOrphanHeader:
		return "Enter expand/collapse · u/Space select on dashboard · ? help · Esc dashboard"
	default:
		return "↑↓ navigate · ? help · Esc dashboard"
	}
}

func (m Model) renderOrganizerGroupRow(row listRow, selected bool) string {
	name := m.organizerRowName(row)
	sessions := m.sessionsForRow(row)
	live := 0
	for _, session := range sessions {
		if session.Alive() {
			live++
		}
	}
	twist := "▾"
	if m.organizerCollapsed[row.key] {
		twist = "▸"
	}
	root := m.rootSummary(row)
	plain := fmt.Sprintf("  %s %s  %d/%d live", twist, name, live, len(sessions))
	if root != "" {
		plain += "  " + root
	}
	if selected {
		return renderSelectedOrganizerRow(plain, m.width)
	}
	plain = padPlain(truncatePlain(plain, m.width), m.width)
	return "  " + faintStyle.Render(twist) + " " + lipgloss.NewStyle().Bold(true).Render(name) +
		mutedStyle.Render(truncatePlain(strings.TrimPrefix(plain, "  "+twist+" "+name), max(0, m.width-lipgloss.Width("  "+twist+" "+name))))
}

func (m Model) renderOrganizerSessionRow(session control.Session, selected, source bool) string {
	icon, status := statusLabel(session)
	iconStyle := mutedStyle
	if session.Alive() {
		iconStyle = liveStyle
	} else if code, ok := session.ExitCode(); (ok && code != 0) || session.Status == control.StatusStartFailed {
		iconStyle = failedStyle
	}
	sourceMark := " "
	if source {
		sourceMark = "◆"
	}
	if m.width < sessionRowRichMinWidth {
		plainPrefix := "    " + sourceMark + " " + icon + " "
		styledPrefix := "    " + noticeStyle.Render(sourceMark) + " " + iconStyle.Render(icon) + " "
		if m.width >= sessionRowRunnerMinWidth {
			plainPrefix += padPlain(string(session.Backend), 8) + " "
			styledPrefix += backendStyle(session.Backend).Render(padPlain(string(session.Backend), 8)) + " "
		}
		plainPrefix += padPlain(status, 11) + " "
		styledPrefix += mutedStyle.Render(padPlain(status, 11)) + " "
		taskWidth := max(1, m.width-lipgloss.Width(plainPrefix))
		task := sessionRowSummary(session, taskWidth)
		if selected {
			return renderSelectedOrganizerRow(plainPrefix+task, m.width)
		}
		return padANSI(truncateANSI(styledPrefix+task, m.width), m.width)
	}
	fixed := 35
	task := sessionRowSummary(session, max(1, m.width-fixed))
	plain := "    " + sourceMark + " " + icon + " " + padPlain(string(session.Backend), 8) + " " +
		padPlain(format.ShortID(session.ID), 7) + " " + padPlain(status, 11) + " " + task
	if selected {
		return renderSelectedOrganizerRow(plain, m.width)
	}
	// Bounded the same way as the narrow branch above. The styled row carries
	// ANSI, so it has to go through the ANSI-aware pair; truncating the plain
	// string instead measured the right thing and then threw it away, which let
	// a wide row overflow the pane by the couple of columns `fixed` under-counts.
	styled := "    " + noticeStyle.Render(sourceMark) + " " + iconStyle.Render(icon) + " " +
		backendStyle(session.Backend).Render(padPlain(string(session.Backend), 8)) + " " +
		padPlain(format.ShortID(session.ID), 7) + " " + mutedStyle.Render(padPlain(status, 11)) + " " + task
	return padANSI(truncateANSI(styled, m.width), m.width)
}

func renderSelectedOrganizerRow(plain string, width int) string {
	if width <= 0 {
		return ""
	}
	body := strings.TrimPrefix(plain, " ")
	line := "›" + truncatePlain(body, width-1)
	return selectedStyle.Render(padPlain(line, width))
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
	if selected, ok := m.selectedSession(); ok && selected.Available() {
		return m, m.requestPreview(selected.ID)
	}
	m.previewFetch.queuedID = ""
	m.preview = ""
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

func (m Model) selectedOrganizerRoot(row listRow) (string, bool) {
	if row.kind != rowWorkstream || row.workstreamID == "" {
		return "", false
	}
	item, ok := m.workstream(row.workstreamID)
	if !ok || len(item.Roots) == 0 {
		return "", false
	}
	return item.Roots[m.rootPosition(item.ID, len(item.Roots))], true
}

func (m *Model) openOrganizer() {
	m.screen = screenOrganizer
	m.organizerEdit = organizerEditNone
	m.organizerRootTarget = ""
	m.organizerInput, m.organizerInputCursor = nil, 0
	m.notice, m.errorText = "", ""
	m.organizerSource = ""
	m.confirmRootRemoval = ""
	m.organizerSelected = m.selected
	if session, ok := m.selectedSession(); ok {
		m.organizerSource = session.ID
		m.organizerCollapsed[organizerSessionParentKey(session)] = false
	}
	m.restoreOrganizerSelection()
}

func (m Model) organizerRows() []listRow {
	return m.overview.rows(m.organizerCollapsed)
}

func (m Model) selectedOrganizerRow() (listRow, bool) {
	rows := m.organizerRows()
	if m.organizerCursor < 0 || m.organizerCursor >= len(rows) {
		return listRow{}, false
	}
	return rows[m.organizerCursor], true
}

func (m *Model) restoreOrganizerSelection() {
	rows := m.organizerRows()
	if len(rows) == 0 {
		m.organizerCursor, m.organizerSelected = 0, ""
		return
	}
	if m.pendingWorkstream != "" {
		m.organizerSelected = workstreamRowKey(m.pendingWorkstream)
		m.pendingWorkstream = ""
	}
	if m.organizerSelected != "" {
		for index, row := range rows {
			if row.key == m.organizerSelected {
				m.organizerCursor = index
				return
			}
		}
	}
	m.organizerCursor = min(max(0, m.organizerCursor), len(rows)-1)
	m.organizerSelected = rows[m.organizerCursor].key
}

func (m *Model) syncOrganizerSelection(rows []listRow) {
	if len(rows) == 0 {
		m.organizerCursor, m.organizerSelected = 0, ""
		return
	}
	m.organizerCursor = min(max(0, m.organizerCursor), len(rows)-1)
	m.organizerSelected = rows[m.organizerCursor].key
}

func (m *Model) selectOrganizerKey(key string) {
	if key == "" {
		return
	}
	m.organizerSelected = key
	m.restoreOrganizerSelection()
}

func (m Model) organizerRowName(row listRow) string {
	if row.kind == rowOrphanHeader {
		return "Orphaned tmux"
	}
	if row.kind == rowSession || row.kind == rowOrphan {
		if session, ok := m.session(row.sessionID); ok {
			return sessionDisplayTitle(session)
		}
	}
	if row.workstreamID == "" {
		return "Ungrouped"
	}
	if item, ok := m.workstream(row.workstreamID); ok {
		return item.Name
	}
	return "Unavailable workstream"
}

func (m Model) organizerContextLabel() string {
	item, ok := m.selectedOrganizerWorkstream()
	if !ok {
		if row, found := m.selectedOrganizerRow(); found {
			return m.organizerRowName(row)
		}
		return ""
	}
	return item.Name + " · " + format.CompactPath(item.ArtifactDir)
}

func (m Model) selectedOrganizerWorkstream() (workstream.Workstream, bool) {
	row, ok := m.selectedOrganizerRow()
	if !ok || row.workstreamID == "" || row.kind == rowOrphan || row.kind == rowOrphanHeader {
		return workstream.Workstream{}, false
	}
	return m.workstream(row.workstreamID)
}

func (m Model) moveOrganizerSource(workstreamID string) (tea.Model, tea.Cmd) {
	if m.organizerSource == "" {
		m.errorText = "select a session to move"
		return m, nil
	}
	source, found := m.session(m.organizerSource)
	if !found {
		m.errorText = "move source is no longer available"
		m.organizerSource = ""
		return m, nil
	}
	if !source.Orphaned && source.WorkstreamID == workstreamID {
		m.errorText = "session is already in " + m.workstreamName(workstreamID)
		return m, nil
	}
	m.busy = true
	m.organizerSelected = workstreamRowKey(workstreamID)
	m.notice = "moving " + format.ShortID(source.ID) + "…"
	if source.Orphaned {
		return m, m.adoptSessionCmd(source.ID, workstreamID)
	}
	return m, m.moveSessionCmd(source.ID, workstreamID)
}

func organizerGroup(row listRow) bool {
	return row.kind == rowWorkstream || row.kind == rowOrphanHeader
}

func organizerDestination(row listRow) bool { return row.kind == rowWorkstream }

func organizerParentKey(row listRow) string {
	if row.kind == rowOrphan {
		return orphanedKey
	}
	if row.kind == rowSession {
		return workstreamRowKey(row.workstreamID)
	}
	return ""
}

func organizerSessionParentKey(session control.Session) string {
	if session.Orphaned {
		return orphanedKey
	}
	return workstreamRowKey(session.WorkstreamID)
}

func (m *Model) beginOrganizerEdit(mode organizerEditMode, value string) {
	m.organizerEdit = mode
	m.organizerInput = splitGraphemes(value)
	m.organizerInputCursor = len(m.organizerInput)
	m.notice, m.errorText = "", ""
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

func (m *Model) insertOrganizerText(value string) {
	inserted := splitGraphemes(inlineSafeText(value))
	if len(inserted) == 0 {
		return
	}
	tail := append([]string(nil), m.organizerInput[m.organizerInputCursor:]...)
	m.organizerInput = append(m.organizerInput[:m.organizerInputCursor], inserted...)
	m.organizerInput = append(m.organizerInput, tail...)
	m.organizerInputCursor += len(inserted)
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

func (m Model) inputValue() string     { return strings.Join(m.input, "") }
func (m Model) organizerValue() string { return strings.Join(m.organizerInput, "") }

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

func (m Model) reorderWorkstreamCmd(id string, delta int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		moved, err := m.controller.ReorderWorkstream(ctx, id, delta)
		return workstreamMsg{action: "reorder", workstreamID: id, delta: delta, moved: moved, err: err}
	}
}

func (m Model) archiveWorkstreamCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.ArchiveWorkstream(ctx, id)
		return workstreamMsg{action: "archive", workstreamID: id, err: err}
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

func (m Model) addRootCmd(workstreamID, root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.AddRoot(ctx, workstreamID, root)
		return workstreamMsg{action: "root", workstreamID: workstreamID, err: err}
	}
}

func (m Model) replaceRootCmd(workstreamID, current, replacement string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.ReplaceRoot(ctx, workstreamID, current, replacement)
		return workstreamMsg{action: "root_replace", workstreamID: workstreamID, err: err}
	}
}

func (m Model) removeRootCmd(workstreamID, root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		err := m.controller.RemoveRoot(ctx, workstreamID, root)
		return workstreamMsg{action: "root_remove", workstreamID: workstreamID, err: err}
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

func openDirectoryCommand(path string) (*exec.Cmd, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	binary, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", name, err)
	}
	command := exec.Command(binary, path)
	command.Env, command.Stdin, command.Stdout, command.Stderr = os.Environ(), os.Stdin, os.Stdout, os.Stderr
	return command, nil
}
