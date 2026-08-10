package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// handleHelpKey owns navigation while the full-screen help panel is open.
// Keeping it separate prevents help navigation from mutating the composer or
// the dashboard selection hidden underneath the panel.
func (m Model) handleHelpKey(stroke string) (tea.Model, tea.Cmd) {
	switch stroke {
	case "f1", "?", "esc":
		m.helpOpen = false
		m.helpOffset = 0
		return m, nil
	case "up":
		m.helpOffset--
	case "down":
		m.helpOffset++
	case "pgup":
		m.helpOffset -= max(1, m.helpViewportHeight()-1)
	case "pgdown":
		m.helpOffset += max(1, m.helpViewportHeight()-1)
	case "home":
		m.helpOffset = 0
	case "end":
		m.helpOffset = m.helpMaxOffset()
	}
	m.clampHelpOffset()
	return m, nil
}

// renderHelp renders a height-safe viewport over the help document. Every
// line is wrapped before styling and truncated once more at the boundary so
// even very narrow terminals cannot overflow.
func (m Model) renderHelp() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	title := m.renderModeHeader("HELP", "")
	if m.height == 1 {
		return truncateANSI(title, m.width)
	}

	header := []string{title}
	if m.height >= 4 {
		header = append(header, m.renderRule())
	}

	content := m.helpContentLines()
	viewportHeight := m.helpViewportHeight()
	maxOffset := max(0, len(content)-viewportHeight)
	offset := min(max(0, m.helpOffset), maxOffset)
	end := min(len(content), offset+viewportHeight)
	body := append([]string(nil), content[offset:end]...)
	for len(body) < viewportHeight {
		body = append(body, "")
	}

	position := "empty"
	if len(content) > 0 {
		first := min(len(content), offset+1)
		last := min(len(content), offset+viewportHeight)
		position = fmt.Sprintf("%d–%d/%d", first, last, len(content))
	}
	legend := "F1 / ? / Esc close · ↑↓ PgUp/PgDn Home/End scroll · " + position
	footer := []string{mutedStyle.Render(legend)}
	if m.height >= 6 {
		footer = append([]string{m.renderRule()}, footer...)
	}

	lines := append(header, body...)
	lines = append(lines, footer...)
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

func (m Model) helpContentLines() []string {
	var lines []string
	lines = appendHelpParagraph(lines, m.width,
		"Heikou—‘parallel’ in Japanese—is a local command center for parallel native coding agents. Workstreams provide durable organization while tmux owns terminals and current process observation. Closing the dashboard never stops a runtime.")

	lines = appendHelpSection(lines, m.width, "Nouns")
	for _, item := range []struct {
		term        string
		description string
	}{
		{"Workstream", "A durable project grouping with a name, registered roots, notes and artifacts, and zero or more sessions. It does not imply a manager or autonomy."},
		{"Session", "A durable launch identity with its initial task, root, runner, and recorded outcome. It remains after its process stops."},
		{"Runtime", "The tmux pane currently associated with a session. It supplies live process observations and may be live, retained after exit, or unavailable."},
		{"Root", "An explicitly registered directory used as the working directory for a new launch. A workstream may have several."},
		{"Runner", "The native program Heikou launches: Codex, Claude, or a no-agent interactive shell."},
		{"Composer", "The input bar at the bottom of the dashboard. Its configured keys act differently when the bar is empty or contains text."},
		{"Ungrouped", "Durable sessions that currently have no workstream membership."},
		{"Orphaned", "tmux panes carrying a Heikou ID that is unknown to durable state. They remain outside workstreams until explicitly adopted."},
	} {
		lines = appendHelpDefinition(lines, m.width, item.term, item.description)
	}

	lines = appendHelpSection(lines, m.width, "Dashboard and composer")
	composerBindings := []struct {
		key         string
		description string
	}{
		{helpKeyLabel(m.settings.NewSessionKey()) + " + text", "Start a new session in the selected workstream and displayed root."},
		{helpKeyLabel(m.settings.SendMessageKey()) + " + text", "Send a follow-up to the selected live session; its row then shows this newest Heikou-routed message."},
		{helpKeyLabel(m.settings.CycleRunnerKey()) + " · empty", "Cycle Codex, Claude, and no-agent for the next launch."},
		{helpKeyLabel(m.settings.CycleRootKey()) + " · empty", "Cycle the registered roots of the selected workstream."},
		{"↑ / ↓", "Select a workstream or session; in a multiline composer, move between its logical lines instead."},
		{"PgUp / PgDn", "Move through the dashboard list one viewport at a time."},
		{"← / → · empty", "Collapse or expand the selected workstream."},
		{"← / → · text", "Move the composer cursor."},
		{"Enter · empty", "Collapse a workstream, or attach to an available session runtime."},
		{"Shift-Enter / Ctrl-J", "Insert a composer newline unless that chord is explicitly rebound to another composer action."},
		{"Option-← / Option-→", "Move one word; Option-Delete removes the previous word."},
		{"Command-← / Command-→", "Move to the start or end of the logical line; Command-↑ / Command-↓ jumps across the whole draft."},
		{"Home / Ctrl-A", "Move to the start of the logical line; End / Ctrl-E moves to its end."},
		{"Backspace / Ctrl-H", "Delete the previous character; Delete removes the next one."},
		{"Ctrl-W / Ctrl-U", "Delete the previous word, or clear text back to the current line start."},
		{"Ctrl-R", "Cycle the next-session runner regardless of composer text."},
		{"Ctrl-X twice", "Stop an available runtime while keeping its durable session record. Once no pane remains, the same chord twice permanently deletes that record and membership."},
		{"F3", "Open the workstream organizer."},
		{"Ctrl-S / F2", "Open settings."},
		{"F1 / ?", "Open this help panel."},
		{"Esc", "Clear composer text; when already empty, quit the dashboard."},
		{"Ctrl-C", "Quit the dashboard immediately without stopping runtimes."},
	}
	for _, binding := range composerBindings {
		lines = appendHelpBinding(lines, m.width, binding.key, binding.description)
	}
	lines = appendHelpParagraph(lines, m.width,
		"The macOS terminal decides which modifier chords reach Heikou. Enhanced Option/Command events and common Alt, Home/End, and Ctrl-key fallbacks are supported; use Ctrl-J when Shift-Enter is reported as ordinary Enter.")

	lines = appendHelpSection(lines, m.width, "Workstream organizer · F3")
	for _, binding := range []struct {
		key         string
		description string
	}{
		{"↑ / ↓", "Navigate workstreams, Ungrouped, Orphaned, and their expanded session rows."},
		{"PgUp / PgDn", "Move through the organizer tree one viewport at a time."},
		{"Enter · workstream", "Collapse or expand it; when a move source is active, move that session here instead."},
		{"Enter · session", "Mark the session as a move source; it does not attach. Use u or Space to return with it selected on the dashboard."},
		{"← / →", "Collapse or expand a workstream, or move from a session row to its parent."},
		{"m", "Mark or unmark a session as the move source. Choose a workstream and press Enter or m to move it; an orphan is explicitly adopted."},
		{"u / Space", "Use the highlighted workstream as the launch target, or return to the dashboard with a session selected for attach/send."},
		{"Lower pane", "Preview the selected workstream's notes.md and shallow artifact-directory tree; a session shows its parent workstream context."},
		{"n / r", "Create or rename a workstream."},
		{"p / Shift-P", "Add a root, or edit the currently selected root."},
		{"d twice", "Remove the selected root without deleting files or historical sessions; every workstream keeps at least one root."},
		{"Tab", "Cycle the highlighted workstream's selected launch root."},
		{"Ctrl-X twice", "Stop or delete the highlighted session using the same safe lifecycle as the dashboard."},
		{"e / o", "Edit notes or open the workstream artifact directory."},
		{"a twice", "Archive the workstream; its durable sessions become Ungrouped."},
		{"Esc / F3", "Return to the dashboard."},
	} {
		lines = appendHelpBinding(lines, m.width, binding.key, binding.description)
	}

	lines = appendHelpSection(lines, m.width, "Settings · Ctrl-S or F2")
	lines = appendHelpBinding(lines, m.width, "e", "Edit the JSON settings file, including runner commands and composer bindings.")
	lines = appendHelpBinding(lines, m.width, "r", "Reload settings from disk.")
	lines = appendHelpBinding(lines, m.width, "↑ / ↓ · PgUp / PgDn", "Scroll settings; Home and End jump to the first and last rows.")
	lines = appendHelpBinding(lines, m.width, "Esc", "Return to the dashboard. Changes affect new sessions and future key presses.")

	lines = appendHelpSection(lines, m.width, "Attached terminal")
	lines = appendHelpParagraph(lines, m.width,
		"Attachment enters the native Codex, Claude, or shell terminal. Use Ctrl-\\ or Ctrl-b d to detach back to the same Heikou dashboard. Detaching and quitting Heikou leave the agent running.")

	lines = appendHelpSection(lines, m.width, "CLI commands")
	for _, binding := range []struct {
		key         string
		description string
	}{
		{"h", "Open the dashboard."},
		{"h spawn [-r RUNNER] [-C DIR] [-w WORKSTREAM] LABEL", "Start a session without opening the dashboard."},
		{"h list", "List durable sessions and orphaned runtimes."},
		{"h send ID MESSAGE", "Send a follow-up through tmux."},
		{"h attach ID", "Enter a session's native terminal."},
		{"h stop ID", "Stop its runtime and keep the durable record."},
		{"h doctor", "Check tmux, runners, settings, and local state paths."},
		{"h version", "Print the installed Heikou version."},
		{"h help", "Print command-line help."},
	} {
		lines = appendHelpBinding(lines, m.width, binding.key, binding.description)
	}
	return lines
}

func (m Model) helpViewportHeight() int {
	if m.height <= 1 {
		return 0
	}
	headerHeight := 1
	if m.height >= 4 {
		headerHeight = 2
	}
	footerHeight := 1
	if m.height >= 6 {
		footerHeight = 2
	}
	return max(0, m.height-headerHeight-footerHeight)
}

func (m Model) helpMaxOffset() int {
	return max(0, len(m.helpContentLines())-m.helpViewportHeight())
}

func (m *Model) clampHelpOffset() {
	m.helpOffset = min(max(0, m.helpOffset), m.helpMaxOffset())
}

func appendHelpSection(lines []string, width int, title string) []string {
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	style := lipgloss.NewStyle().Bold(true).Foreground(colorText)
	return append(lines, style.Render(truncatePlain(title, max(0, width))))
}

func appendHelpParagraph(lines []string, width int, text string) []string {
	return append(lines, helpWrappedLines(width, 0, text)...)
}

func appendHelpDefinition(lines []string, width int, term, description string) []string {
	return appendHelpLabeled(lines, width, term, description, 13)
}

func appendHelpBinding(lines []string, width int, key, description string) []string {
	return appendHelpLabeled(lines, width, key, description, 24)
}

func appendHelpLabeled(lines []string, width int, label, description string, column int) []string {
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(colorText)
	if width >= column+20 && lipgloss.Width(label) < column-2 {
		prefix := " " + padPlain(label, column-1)
		wrapped := wrapLines(description, max(1, width-column))
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for index, line := range wrapped {
			if index == 0 {
				lines = append(lines, labelStyle.Render(prefix)+truncatePlain(line, max(1, width-column)))
			} else {
				lines = append(lines, strings.Repeat(" ", min(column, width))+truncatePlain(line, max(1, width-column)))
			}
		}
		return lines
	}
	lines = append(lines, labelStyle.Render(truncatePlain(" "+label, max(0, width))))
	return append(lines, helpWrappedLines(width, 3, description)...)
}

func helpWrappedLines(width, indent int, text string) []string {
	if width <= 0 {
		return nil
	}
	indent = min(max(0, indent), max(0, width-1))
	prefix := strings.Repeat(" ", indent)
	wrapped := wrapLines(text, max(1, width-indent))
	if len(wrapped) == 0 {
		return []string{prefix}
	}
	for index := range wrapped {
		wrapped[index] = truncatePlain(prefix+wrapped[index], width)
	}
	return wrapped
}

func helpKeyLabel(value string) string {
	parts := strings.Split(strings.TrimSpace(strings.ToLower(value)), "+")
	for index, part := range parts {
		switch part {
		case "ctrl":
			parts[index] = "Ctrl"
		case "alt":
			parts[index] = "Alt"
		case "shift":
			parts[index] = "Shift"
		case "meta":
			parts[index] = "Meta"
		case "super":
			parts[index] = "Super"
		case "enter":
			parts[index] = "Enter"
		case "tab":
			parts[index] = "Tab"
		case "space":
			parts[index] = "Space"
		case "backspace":
			parts[index] = "Backspace"
		case "delete":
			parts[index] = "Delete"
		case "esc":
			parts[index] = "Esc"
		case "up":
			parts[index] = "↑"
		case "down":
			parts[index] = "↓"
		case "left":
			parts[index] = "←"
		case "right":
			parts[index] = "→"
		default:
			if len([]rune(part)) == 1 {
				parts[index] = strings.ToUpper(part)
			} else if part != "" {
				parts[index] = strings.ToUpper(part[:1]) + part[1:]
			}
		}
	}
	return strings.Join(parts, "+")
}
