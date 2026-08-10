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
	"github.com/zamborg/heikou/internal/heikou"
	"github.com/zamborg/heikou/internal/runner"
	"github.com/zamborg/heikou/internal/workstream"
)

const (
	refreshInterval = time.Second
	commandTimeout  = 8 * time.Second
	ungroupedKey    = "workstream:ungrouped"
	orphanedKey     = "workstream:orphaned"
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

type organizerItem struct {
	id   string
	name string
}

type Model struct {
	controller control.Service
	root       string
	backend    heikou.Backend
	store      config.Store
	settings   config.Config

	snapshot    control.Snapshot
	cursor      int
	selected    string
	collapsed   map[string]bool
	rootIndex   map[string]int
	preview     string
	previewID   string
	width       int
	height      int
	busy        bool
	notice      string
	errorText   string
	confirmStop string

	input       []string
	inputCursor int

	settingsOpen bool

	organizerOpen        bool
	organizerCursor      int
	organizerSource      string
	organizerEdit        string
	organizerInput       []string
	organizerInputCursor int
	confirmArchive       string
	pendingWorkstream    string
}

func New(controller control.Service, root string, backend heikou.Backend, store config.Store, settings config.Config) Model {
	return Model{
		controller: controller, root: root, backend: backend, store: store, settings: settings,
		collapsed: make(map[string]bool), rootIndex: make(map[string]int),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tickCmd())
}

type snapshotMsg struct {
	snapshot control.Snapshot
	err      error
}

type previewMsg struct {
	id   string
	text string
	err  error
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
	err          error
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

	case tickMsg:
		return m, tea.Batch(m.refreshCmd(), tickCmd())

	case snapshotMsg:
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.snapshot = message.snapshot
		m.restoreSelection()
		m.restoreOrganizerSelection()
		if selected, ok := m.selectedSession(); ok && selected.Available() {
			return m, m.previewCmd(selected.ID)
		}
		m.preview, m.previewID = "", ""
		return m, nil

	case previewMsg:
		selected, ok := m.selectedSession()
		if !ok || message.id != selected.ID {
			return m, nil
		}
		if message.err == nil {
			m.preview, m.previewID = message.text, message.id
		}
		return m, nil

	case startMsg:
		m.busy = false
		if message.session.ID != "" {
			m.clearInput()
			m.selected = sessionRowKey(message.session)
			m.confirmStop = ""
		}
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, m.refreshCmd()
		}
		m.notice = fmt.Sprintf("started %s · %s", message.session.Backend, shortID(message.session.ID))
		return m, m.refreshCmd()

	case sendMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.clearInput()
		m.notice = "message sent · " + shortID(message.id)
		m.confirmStop = ""
		return m, tea.Batch(m.refreshCmd(), m.previewCmd(message.id))

	case stopMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.err.Error()
			return m, nil
		}
		m.notice = "stopped runtime · " + shortID(message.id)
		m.confirmStop = ""
		return m, m.refreshCmd()

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
		return m, m.refreshCmd()

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
		m.organizerEdit = ""
		m.organizerInput, m.organizerInputCursor = nil, 0
		m.confirmArchive = ""
		switch message.action {
		case "create":
			m.pendingWorkstream = message.item.ID
			m.notice = "created workstream · " + message.item.Name
		case "rename":
			m.notice = "renamed workstream"
		case "archive":
			m.notice = "archived workstream"
		case "move":
			m.notice = "moved session · " + shortID(message.sessionID)
		case "adopt":
			m.selected = "session:" + message.sessionID
			m.notice = "adopted legacy runtime · " + shortID(message.sessionID)
		case "root":
			m.notice = "added workstream root"
		}
		return m, m.refreshCmd()

	case externalMsg:
		m.busy = false
		if message.err != nil {
			m.errorText = message.label + ": " + message.err.Error()
		} else {
			m.notice = message.label + " closed"
		}
		return m, m.refreshCmd()

	case tea.PasteMsg:
		if m.busy || m.settingsOpen {
			return m, nil
		}
		if m.organizerOpen {
			if m.organizerEdit != "" {
				m.insertOrganizerText(normalizePaste(message.Content))
			}
			return m, nil
		}
		m.insertText(normalizePaste(message.Content))
		m.notice = ""
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}

func (m Model) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	if stroke == "ctrl+c" {
		return m, tea.Quit
	}
	if m.settingsOpen {
		return m.handleSettingsKey(stroke)
	}
	if m.organizerOpen {
		return m.handleOrganizerKey(key)
	}
	if m.busy {
		return m, nil
	}

	m.errorText = ""
	if stroke != "ctrl+x" {
		m.confirmStop = ""
	}

	switch stroke {
	case "ctrl+s", "f2":
		m.settingsOpen = true
		m.notice, m.errorText = "", ""
		return m, nil

	case "f3":
		m.openOrganizer()
		return m, nil

	case "esc":
		if len(m.input) > 0 {
			m.clearInput()
			m.notice = "composer cleared"
			return m, nil
		}
		return m, tea.Quit

	case "up":
		return m.moveSelection(-1)

	case "down":
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
		}
		return m, nil

	case "right":
		if len(m.input) == 0 && m.toggleSelectedGroup(false) {
			return m, nil
		}
		if m.inputCursor < len(m.input) {
			m.inputCursor++
		}
		return m, nil

	case "home", "ctrl+a":
		m.inputCursor = 0
		return m, nil

	case "end", "ctrl+e":
		m.inputCursor = len(m.input)
		return m, nil

	case "backspace", "ctrl+h":
		if m.inputCursor > 0 {
			m.input = append(m.input[:m.inputCursor-1], m.input[m.inputCursor:]...)
			m.inputCursor--
		}
		return m, nil

	case "delete":
		if m.inputCursor < len(m.input) {
			m.input = append(m.input[:m.inputCursor], m.input[m.inputCursor+1:]...)
		}
		return m, nil

	case "ctrl+u":
		m.input = append([]string(nil), m.input[m.inputCursor:]...)
		m.inputCursor = 0
		return m, nil

	case "ctrl+w":
		m.deletePreviousWord()
		return m, nil

	case "ctrl+r":
		m.backend = m.backend.Next()
		m.notice = "new sessions use " + string(m.backend)
		return m, nil

	case "shift+tab":
		if strings.TrimSpace(m.inputValue()) == "" {
			if m.cycleSelectedRoot(1) {
				m.notice = "launch root · " + compactPath(m.launchRoot())
			}
			return m, nil
		}

	case "tab":
		if strings.TrimSpace(m.inputValue()) == "" {
			m.backend = m.backend.Next()
			m.notice = "new sessions use " + string(m.backend)
			return m, nil
		}
		selected, ok := m.selectedSession()
		if !ok || !selected.Alive() {
			m.errorText = "select a live session before sending"
			return m, nil
		}
		m.busy = true
		m.notice = "sending to " + shortID(selected.ID) + "…"
		return m, m.sendCmd(selected.ID, m.inputValue())

	case "enter":
		prompt := strings.TrimSpace(m.inputValue())
		if prompt != "" {
			m.busy = true
			m.notice = "starting " + string(m.backend) + "…"
			return m, m.startCmd(prompt)
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
		if !ok || !selected.Available() {
			m.errorText = "select an available runtime to stop"
			return m, nil
		}
		if m.confirmStop != selected.ID {
			m.confirmStop = selected.ID
			m.notice = "Ctrl-X again to stop " + shortID(selected.ID)
			return m, nil
		}
		m.busy = true
		m.notice = "stopping " + shortID(selected.ID) + "…"
		return m, m.stopCmd(selected.ID)
	}
	blockedModifiers := tea.ModCtrl | tea.ModAlt | tea.ModMeta | tea.ModHyper | tea.ModSuper
	if key.Text != "" && key.Mod&blockedModifiers == 0 {
		m.insertText(key.Text)
		m.notice = ""
	}
	return m, nil
}

func (m Model) handleSettingsKey(stroke string) (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	m.errorText = ""
	switch stroke {
	case "esc", "ctrl+s", "f2":
		m.settingsOpen = false
		m.notice = "settings closed"
		return m, nil
	case "r":
		m.busy = true
		m.notice = "reloading settings…"
		return m, m.loadSettingsCmd()
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

func (m Model) handleOrganizerKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	if m.busy {
		return m, nil
	}
	m.errorText = ""
	if m.organizerEdit != "" {
		switch stroke {
		case "esc":
			m.organizerEdit = ""
			m.organizerInput, m.organizerInputCursor = nil, 0
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.organizerValue())
			if value == "" {
				m.errorText = "a value is required"
				return m, nil
			}
			item, ok := m.selectedOrganizerItem()
			m.busy = true
			switch m.organizerEdit {
			case "create":
				return m, m.createWorkstreamCmd(value)
			case "rename":
				if !ok || item.id == "" {
					m.busy = false
					return m, nil
				}
				return m, m.renameWorkstreamCmd(item.id, value)
			case "root":
				if !ok || item.id == "" {
					m.busy = false
					return m, nil
				}
				return m, m.addRootCmd(item.id, value)
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
	items := m.organizerItems()
	switch stroke {
	case "esc", "f3":
		m.organizerOpen = false
		m.notice = "workstreams closed"
		return m, nil
	case "up":
		m.organizerCursor = max(0, m.organizerCursor-1)
		return m, nil
	case "down":
		m.organizerCursor = min(max(0, len(items)-1), m.organizerCursor+1)
		return m, nil
	case "enter":
		item, ok := m.selectedOrganizerItem()
		if !ok {
			return m, nil
		}
		m.selected = workstreamRowKey(item.id)
		m.organizerOpen = false
		m.restoreSelection()
		m.notice = "launch target · " + item.name
		return m, nil
	case "n":
		m.beginOrganizerEdit("create", "")
		return m, nil
	case "r":
		item, ok := m.selectedOrganizerItem()
		if ok && item.id != "" {
			m.beginOrganizerEdit("rename", item.name)
		}
		return m, nil
	case "p":
		item, ok := m.selectedOrganizerItem()
		if ok && item.id != "" {
			m.beginOrganizerEdit("root", m.root)
		}
		return m, nil
	case "tab":
		item, ok := m.selectedOrganizerItem()
		if ok && item.id != "" {
			m.cycleRoot(item.id, 1)
		}
		return m, nil
	case "m":
		item, ok := m.selectedOrganizerItem()
		if !ok || m.organizerSource == "" {
			m.errorText = "open Workstreams while a durable session is selected to move it"
			return m, nil
		}
		m.busy = true
		if source, found := m.session(m.organizerSource); found && source.Orphaned {
			return m, m.adoptSessionCmd(m.organizerSource, item.id)
		}
		return m, m.moveSessionCmd(m.organizerSource, item.id)
	case "a":
		item, ok := m.selectedOrganizerItem()
		if !ok || item.id == "" {
			return m, nil
		}
		if m.confirmArchive != item.id {
			m.confirmArchive = item.id
			m.notice = "press a again to archive " + item.name + " (sessions become ungrouped)"
			return m, nil
		}
		m.busy = true
		return m, m.archiveWorkstreamCmd(item.id)
	case "e", "o":
		item, ok := m.selectedOrganizerItem()
		if !ok || item.id == "" {
			return m, nil
		}
		container, ok := m.workstream(item.id)
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
	if m.settingsOpen {
		view := tea.NewView(textStyle.MaxWidth(m.width).Render(m.renderSettings()))
		view.AltScreen = true
		view.WindowTitle = "heikou · settings"
		return view
	}
	if m.organizerOpen {
		view := tea.NewView(textStyle.MaxWidth(m.width).Render(m.renderOrganizer()))
		view.AltScreen = true
		view.WindowTitle = "heikou · workstreams"
		return view
	}
	sections := []string{m.renderHeader(), m.renderRule(), m.renderWorkstreams(), m.renderDetails(), m.renderComposer()}
	view := tea.NewView(textStyle.MaxWidth(m.width).Render(strings.Join(sections, "\n")))
	view.AltScreen = true
	view.WindowTitle = "heikou · parallel agents"
	return view
}

func (m Model) renderHeader() string {
	live, finished, unavailable := 0, 0, 0
	for _, session := range append(append([]control.Session(nil), m.snapshot.Sessions...), m.snapshot.Orphans...) {
		switch session.Status {
		case control.StatusLive:
			live++
		case control.StatusUnavailable:
			unavailable++
		default:
			finished++
		}
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render("heikou")
	subtitle := mutedStyle.Render(" workstreams")
	counts := fmt.Sprintf("%d workstreams · %d live", len(m.snapshot.Workstreams), live)
	if unavailable > 0 {
		counts += fmt.Sprintf(" · %d unavailable", unavailable)
	}
	if finished > 0 {
		counts += fmt.Sprintf(" · %d finished", finished)
	}
	left := title + subtitle
	right := mutedStyle.Render(counts)
	space := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		return truncateANSI(left+"  "+right, m.width)
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
		activity = relativeTime(latest, time.Now())
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

func (m Model) renderSessionRow(session control.Session, selected bool) string {
	icon, status := statusLabel(session)
	iconStyle := mutedStyle
	if session.Alive() {
		iconStyle = liveStyle
	} else if code, ok := session.ExitCode(); (ok && code != 0) || session.Status == control.StatusStartFailed {
		iconStyle = failedStyle
	}
	const runnerWidth = 8
	fixedWidth := 42
	path := ""
	if m.width >= 96 {
		path = truncatePlain(oneLine(filepath.Base(session.Root)), 16)
		fixedWidth += 18
	}
	taskWidth := max(1, m.width-fixedWidth)
	task := truncatePlain(oneLine(session.Prompt), taskWidth)
	marker := " "
	if selected {
		marker = "›"
	}
	row := marker + "   " + iconStyle.Render(icon) + " " +
		padANSI(backendStyle(session.Backend).Render(string(session.Backend)), runnerWidth) + " " +
		padPlain(shortID(session.ID), 7) + " " +
		padANSI(mutedStyle.Render(truncatePlain(status, 11)), 11) + " " +
		padPlain(task, taskWidth)
	if path != "" {
		row += "  " + padANSI(mutedStyle.Render(path), 16)
	}
	row += "  " + padANSI(mutedStyle.Render(formatDuration(session.RuntimeDuration(time.Now()))), 7)
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
		"  " + selectedKeyHint.Render(shortID(selected.ID)) + "  " + statusIcon + " " + status +
		"  " + mutedStyle.Render(formatDuration(selected.RuntimeDuration(time.Now())))
	lines := []string{truncateANSI(header, m.width)}
	if height > 1 {
		lines = append(lines, mutedStyle.Render(" task  ")+truncatePlain(oneLine(selected.Prompt), max(8, width-7)))
	}
	if height > 2 {
		path := selected.Root
		if selected.Runtime != nil && selected.Runtime.CurrentPath != "" {
			path = selected.Runtime.CurrentPath
		}
		lines = append(lines, mutedStyle.Render(" cwd   ")+truncatePlain(oneLine(compactPath(path)), max(8, width-7)))
	}
	if height > 3 {
		container := m.workstreamName(selected.WorkstreamID)
		state := container + " · durable launch identity"
		if selected.Orphaned {
			state = "orphaned tmux runtime · no durable membership"
		} else if selected.Runtime != nil && !selected.Runtime.LastActivityAt.IsZero() {
			state += " · terminal activity " + relativeTime(selected.Runtime.LastActivityAt, time.Now())
		}
		lines = append(lines, mutedStyle.Render(" state ")+truncatePlain(state, max(8, width-7)))
	}
	if height > 4 {
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
		previewLines := wrapLines(sanitize(preview), width)
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
		lines = append(lines, mutedStyle.Render(" root  ")+truncatePlain(compactPath(m.launchRoot()), max(1, m.width-8)))
	}
	if height > 3 && artifact != "" {
		lines = append(lines, mutedStyle.Render(" files ")+truncatePlain(compactPath(artifact), max(1, m.width-8)))
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
	contextLabel := m.workstreamName(m.launchWorkstreamID()) + " · " + compactPath(m.launchRoot())
	prefixText := string(m.backend) + " · " + contextLabel + " › "
	if lipgloss.Width(prefixText) > max(1, m.width-8) {
		contextLabel = truncatePlain(contextLabel, max(3, m.width-lipgloss.Width(string(m.backend))-8))
		prefixText = string(m.backend) + " · " + contextLabel + " › "
	}
	prefix := backendStyle(m.backend).Bold(true).Render(prefixText)
	inputWidth := max(1, m.width-lipgloss.Width(prefix))
	composer := padANSI(truncateANSI(prefix+m.renderInput(inputWidth), m.width), m.width)
	help := "↑↓ select · Enter new/attach/collapse · Tab send/runner · ⇧Tab root · F3 workstreams · F2 settings · Ctrl-X stop"
	message := help
	style := mutedStyle
	if m.errorText != "" {
		message, style = "error: "+m.errorText, failedStyle
	} else if m.notice != "" {
		message, style = m.notice, noticeStyle
	}
	return m.renderRule() + "\n" + composer + "\n" + style.Render(truncatePlain(oneLine(message), m.width))
}

func (m Model) renderSettings() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render("heikou")
	state := "not created yet"
	if m.store.Exists() {
		state = "JSON file"
	}
	lines := []string{
		title + mutedStyle.Render(" settings"), m.renderRule(), "",
		mutedStyle.Render(" config   ") + truncatePlain(oneLine(compactPath(m.store.Path)), max(1, m.width-10)),
		mutedStyle.Render(" state    ") + state,
		mutedStyle.Render(" app data ") + truncatePlain(oneLine(compactPath(m.snapshot.StatePath)), max(1, m.width-10)),
		mutedStyle.Render(" startup default  ") + backendStyle(m.settings.DefaultRunner).Render(string(m.settings.DefaultRunner)),
		"", lipgloss.NewStyle().Bold(true).Render(" launch commands"),
	}
	for _, backend := range []heikou.Backend{heikou.BackendCodex, heikou.BackendClaude} {
		raw := jsonCommand(m.settings.Command(backend))
		lines = append(lines, " "+padPlain(string(backend), 9)+truncatePlain(raw, max(1, m.width-11)))
		resolved, err := runner.ResolveCommand(backend, m.settings.Command(backend))
		resolution := jsonCommand(resolved)
		if err != nil {
			resolution = "missing · " + oneLine(err.Error())
		}
		lines = append(lines, mutedStyle.Render("   resolved ")+truncatePlain(resolution, max(1, m.width-12)))
	}
	lines = append(lines, " "+padPlain(string(heikou.BackendNoAgent), 9)+"tmux default shell", "", mutedStyle.Render(" Commands are JSON argv arrays; changes affect new sessions only."))
	return m.fitPane(lines, "e edit JSON · r reload · Esc / Ctrl-S dashboard")
}

func (m Model) renderOrganizer() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render("heikou") + mutedStyle.Render(" workstreams")
	lines := []string{title, m.renderRule()}
	items := m.organizerItems()
	for index, item := range items {
		container, hasContainer := m.workstream(item.id)
		root := compactPath(m.root)
		if hasContainer && len(container.Roots) > 0 {
			root = compactPath(container.Roots[m.rootPosition(item.id, len(container.Roots))])
		}
		count, live := 0, 0
		for _, session := range m.snapshot.Sessions {
			if session.WorkstreamID == item.id {
				count++
				if session.Alive() {
					live++
				}
			}
		}
		row := fmt.Sprintf("  %s  %d/%d live  %s", item.name, live, count, root)
		row = padANSI(truncateANSI(row, m.width), m.width)
		if index == m.organizerCursor {
			row = selectedStyle.Render("›" + ansi.Strip(row[1:]))
		}
		lines = append(lines, row)
	}
	if len(items) == 0 {
		lines = append(lines, mutedStyle.Render("  No active workstreams."))
	}
	if m.organizerSource != "" {
		lines = append(lines, "", mutedStyle.Render(" move source  ")+shortID(m.organizerSource))
	}
	if m.organizerEdit != "" {
		label := map[string]string{"create": "new name", "rename": "rename", "root": "add root"}[m.organizerEdit]
		prefix := noticeStyle.Render(label + " › ")
		lines = append(lines, "", prefix+m.renderTextInput(m.organizerInput, m.organizerInputCursor, max(1, m.width-lipgloss.Width(prefix))))
	}
	help := "↑↓ select · Enter use · n new · r rename · p add root · Tab root · m move/adopt · e notes · o files · a archive · Esc"
	if m.organizerEdit != "" {
		help = "Enter save · Esc cancel"
	}
	return m.fitPane(lines, help)
}

func (m Model) fitPane(lines []string, help string) string {
	message := ""
	style := noticeStyle
	if m.errorText != "" {
		message, style = "error: "+m.errorText, failedStyle
	} else if m.notice != "" {
		message = m.notice
	}
	footer := []string{m.renderRule(), style.Render(truncatePlain(oneLine(message), m.width)), mutedStyle.Render(truncatePlain(help, m.width))}
	available := max(0, m.height-len(footer))
	if len(lines) > available {
		lines = lines[:available]
	}
	for len(lines) < available {
		lines = append(lines, "")
	}
	lines = append(lines, footer...)
	for index := range lines {
		lines[index] = truncateANSI(lines[index], m.width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) rows() []listRow {
	var rows []listRow
	for _, item := range m.snapshot.Workstreams {
		key := workstreamRowKey(item.ID)
		rows = append(rows, listRow{key: key, kind: rowWorkstream, workstreamID: item.ID})
		if !m.collapsed[key] {
			for _, session := range m.snapshot.Sessions {
				if session.WorkstreamID == item.ID {
					rows = append(rows, listRow{key: sessionRowKey(session), kind: rowSession, workstreamID: item.ID, sessionID: session.ID})
				}
			}
		}
	}
	rows = append(rows, listRow{key: ungroupedKey, kind: rowWorkstream})
	if !m.collapsed[ungroupedKey] {
		for _, session := range m.snapshot.Sessions {
			if session.WorkstreamID == "" {
				rows = append(rows, listRow{key: sessionRowKey(session), kind: rowSession, sessionID: session.ID})
			}
		}
	}
	if len(m.snapshot.Orphans) > 0 {
		rows = append(rows, listRow{key: orphanedKey, kind: rowOrphanHeader})
		if !m.collapsed[orphanedKey] {
			for _, session := range m.snapshot.Orphans {
				rows = append(rows, listRow{key: sessionRowKey(session), kind: rowOrphan, sessionID: session.ID})
			}
		}
	}
	return rows
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
		return m, m.previewCmd(selected.ID)
	}
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
	for _, session := range m.snapshot.Sessions {
		if session.ID == id {
			return session, true
		}
	}
	for _, session := range m.snapshot.Orphans {
		if session.ID == id {
			return session, true
		}
	}
	return control.Session{}, false
}

func (m Model) workstream(id string) (workstream.Workstream, bool) {
	for _, item := range m.snapshot.Workstreams {
		if item.ID == id {
			return item, true
		}
	}
	return workstream.Workstream{}, false
}

func (m Model) sessionsForRow(row listRow) []control.Session {
	if row.kind == rowOrphanHeader {
		return m.snapshot.Orphans
	}
	var result []control.Session
	for _, session := range m.snapshot.Sessions {
		if session.WorkstreamID == row.workstreamID {
			result = append(result, session)
		}
	}
	return result
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
		return compactPath(m.root)
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

func (m *Model) openOrganizer() {
	m.organizerOpen = true
	m.organizerEdit = ""
	m.organizerInput, m.organizerInputCursor = nil, 0
	m.notice, m.errorText = "", ""
	m.organizerSource = ""
	if session, ok := m.selectedSession(); ok {
		m.organizerSource = session.ID
	}
	id := m.launchWorkstreamID()
	for index, item := range m.organizerItems() {
		if item.id == id {
			m.organizerCursor = index
			break
		}
	}
}

func (m Model) organizerItems() []organizerItem {
	items := []organizerItem{{name: "Ungrouped"}}
	for _, item := range m.snapshot.Workstreams {
		items = append(items, organizerItem{id: item.ID, name: item.Name})
	}
	return items
}

func (m Model) selectedOrganizerItem() (organizerItem, bool) {
	items := m.organizerItems()
	if m.organizerCursor < 0 || m.organizerCursor >= len(items) {
		return organizerItem{}, false
	}
	return items[m.organizerCursor], true
}

func (m *Model) restoreOrganizerSelection() {
	items := m.organizerItems()
	if m.pendingWorkstream != "" {
		for index, item := range items {
			if item.id == m.pendingWorkstream {
				m.organizerCursor = index
				m.pendingWorkstream = ""
				return
			}
		}
	}
	m.organizerCursor = min(max(0, m.organizerCursor), max(0, len(items)-1))
}

func (m *Model) beginOrganizerEdit(mode, value string) {
	m.organizerEdit = mode
	m.organizerInput = splitGraphemes(value)
	m.organizerInputCursor = len(m.organizerInput)
	m.notice, m.errorText = "", ""
}

func (m *Model) insertText(value string) {
	inserted := splitGraphemes(inlineSafeText(value))
	if len(inserted) == 0 {
		return
	}
	tail := append([]string(nil), m.input[m.inputCursor:]...)
	m.input = append(m.input[:m.inputCursor], inserted...)
	m.input = append(m.input, tail...)
	m.inputCursor += len(inserted)
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
	if m.inputCursor == 0 {
		return
	}
	start := m.inputCursor
	for start > 0 && clusterIsSpace(m.input[start-1]) {
		start--
	}
	for start > 0 && !clusterIsSpace(m.input[start-1]) {
		start--
	}
	m.input = append(m.input[:start], m.input[m.inputCursor:]...)
	m.inputCursor = start
}

func (m *Model) clearInput() {
	m.input = nil
	m.inputCursor = 0
}

func (m Model) inputValue() string     { return strings.Join(m.input, "") }
func (m Model) organizerValue() string { return strings.Join(m.organizerInput, "") }

func (m Model) renderInput(width int) string {
	return m.renderTextInput(m.input, m.inputCursor, width)
}

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

func (m Model) listHeight() int {
	if m.height < 18 {
		return max(2, m.height-9)
	}
	return max(5, min(14, (m.height-8)/2))
}

func (m Model) detailHeight() int { return max(0, m.height-m.listHeight()-6) }

func (m Model) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		snapshot, err := m.controller.Snapshot(ctx)
		return snapshotMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) previewCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		preview, err := m.controller.Capture(ctx, id, 120)
		return previewMsg{id: id, text: preview, err: err}
	}
}

func (m Model) startCmd(prompt string) tea.Cmd {
	root, workstreamID := m.launchRoot(), m.launchWorkstreamID()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
		defer cancel()
		session, err := m.controller.Start(ctx, control.StartRequest{
			Backend: m.backend, Prompt: prompt, Root: root, Command: m.settings.Command(m.backend), WorkstreamID: workstreamID,
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
		if code, ok := session.ExitCode(); ok && code != 0 {
			return "×", "failed " + strconv.Itoa(code)
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

func formatDuration(value time.Duration) string {
	if value < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(value.Seconds())))
	}
	if value < time.Hour {
		return fmt.Sprintf("%dm", int(value.Minutes()))
	}
	if value < 24*time.Hour {
		return fmt.Sprintf("%dh%02dm", int(value.Hours()), int(value.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(value.Hours()/24))
}

func relativeTime(value, now time.Time) string {
	delta := now.Sub(value)
	if delta < 0 {
		delta = 0
	}
	if delta < 2*time.Second {
		return "now"
	}
	return formatDuration(delta) + " ago"
}

func shortID(id string) string {
	clean := strings.ReplaceAll(id, "-", "")
	if len(clean) <= 6 {
		return clean
	}
	return clean[:6]
}

func compactPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func oneLine(value string) string { return strings.Join(strings.Fields(sanitize(value)), " ") }

func sanitize(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, value)
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

func normalizePaste(value string) string {
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
	return oneLine(string(data))
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
