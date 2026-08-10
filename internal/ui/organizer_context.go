package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/zamborg/heikou/internal/workstream"
)

const artifactContextTimeout = 2 * time.Second

type artifactContextMsg struct {
	generation uint64
	key        string
	snapshot   artifactContextSnapshot
}

type organizerContextState struct {
	generation uint64
	loadingKey string
	snapshot   artifactContextSnapshot
}

func artifactContextKey(item workstream.Workstream) string {
	return item.ID + "\x00" + item.ArtifactDir
}

// organizerContextCmd refreshes the UI-owned artifact cache. The controller
// and durable workstream model deliberately remain unaware of this preview.
func (m *Model) organizerContextCmd(force bool) tea.Cmd {
	item, ok := m.selectedOrganizerWorkstream()
	if !ok {
		m.organizerContext.generation++
		m.organizerContext.loadingKey = ""
		m.organizerContext.snapshot = artifactContextSnapshot{}
		return nil
	}
	key := artifactContextKey(item)
	loadedKey := m.organizerContext.snapshot.WorkstreamID + "\x00" + m.organizerContext.snapshot.ArtifactDir
	if !force {
		if loadedKey == key {
			// Returning to a cached selection abandons any read for the selection
			// we just left. Otherwise that stale result can be rejected below while
			// leaving loadingKey behind, permanently suppressing a future retry.
			if m.organizerContext.loadingKey != "" && m.organizerContext.loadingKey != key {
				m.organizerContext.generation++
				m.organizerContext.loadingKey = ""
			}
			return nil
		}
		if m.organizerContext.loadingKey == key {
			return nil
		}
	}
	m.organizerContext.generation++
	generation := m.organizerContext.generation
	m.organizerContext.loadingKey = key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), artifactContextTimeout)
		defer cancel()
		return artifactContextMsg{
			generation: generation,
			key:        key,
			snapshot:   readArtifactContext(ctx, item, defaultArtifactContextLimits()),
		}
	}
}

func (m *Model) acceptArtifactContext(message artifactContextMsg) {
	if message.generation != m.organizerContext.generation {
		return
	}
	item, ok := m.selectedOrganizerWorkstream()
	if !ok || message.key != artifactContextKey(item) {
		return
	}
	m.organizerContext.loadingKey = ""
	m.organizerContext.snapshot = message.snapshot
}

func (m Model) renderOrganizerContext(height int) []string {
	if height <= 0 {
		return nil
	}
	item, ok := m.selectedOrganizerWorkstream()
	if !ok {
		label := "CONTEXT"
		if row, found := m.selectedOrganizerRow(); found {
			label += " · " + m.organizerRowName(row)
		}
		if height == 1 {
			label += " · no notes or artifacts"
		}
		lines := []string{m.renderContextDivider(label)}
		if height > 1 {
			lines = append(lines, mutedStyle.Render(" No workstream notes or artifacts."))
		}
		return fitContextLines(lines, height, m.width)
	}

	key := artifactContextKey(item)
	loadedKey := m.organizerContext.snapshot.WorkstreamID + "\x00" + m.organizerContext.snapshot.ArtifactDir
	if loadedKey != key {
		label := "CONTEXT · " + item.Name
		if height == 1 {
			label += " · loading"
		}
		lines := []string{m.renderContextDivider(label)}
		if height > 1 {
			lines = append(lines, mutedStyle.Render(" Loading notes and artifacts…"))
		}
		return fitContextLines(lines, height, m.width)
	}
	if height == 1 {
		return []string{m.renderContextDivider("CONTEXT · " + item.Name + " · " + artifactContextSummary(m.organizerContext.snapshot))}
	}
	lines := []string{m.renderContextDivider("CONTEXT · " + item.Name)}

	contentHeight := height - 1
	if contentHeight == 1 {
		summary := artifactContextSummary(m.organizerContext.snapshot)
		return fitContextLines(append(lines, mutedStyle.Render(" "+summary)), height, m.width)
	}

	notesHeight := 1
	if contentHeight >= 6 {
		notesHeight = min(3, max(1, contentHeight/3))
	}
	notes := renderArtifactNotes(m.organizerContext.snapshot, m.width, notesHeight)
	lines = append(lines, notes...)

	filesHeight := max(1, contentHeight-len(notes))
	filesLabel := " FILES " + oneLine(sanitize(compactPath(item.ArtifactDir)))
	lines = append(lines, mutedStyle.Render(truncatePlain(filesLabel, m.width)))
	filesHeight--
	if filesHeight > 0 {
		lines = append(lines, renderArtifactTree(m.organizerContext.snapshot, m.width, filesHeight)...)
	}
	return fitContextLines(lines, height, m.width)
}

func (m Model) renderContextDivider(label string) string {
	label = " " + oneLine(sanitize(label)) + " "
	prefix := "─"
	available := max(0, m.width-lipgloss.Width(prefix)-lipgloss.Width(label))
	return faintStyle.Render(prefix) + lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(label) +
		faintStyle.Render(strings.Repeat("─", available))
}

func renderArtifactNotes(snapshot artifactContextSnapshot, width, height int) []string {
	if height <= 0 {
		return nil
	}
	var body string
	switch snapshot.NotesStatus {
	case artifactNotesReady:
		body = snapshot.Notes
		if strings.TrimSpace(body) == "" {
			body = "No notes yet · e to edit"
		}
	case artifactNotesMissing:
		body = "No notes yet · e to edit"
	default:
		body = "Unavailable"
		if snapshot.NotesError != "" {
			body += " · " + snapshot.NotesError
		}
	}
	body = strings.ToValidUTF8(sanitize(body), "�")
	wrapped := wrapLines(body, max(1, width-8))
	if len(wrapped) == 0 {
		wrapped = []string{"No notes yet · e to edit"}
	}
	truncated := snapshot.NotesTruncated || len(wrapped) > height
	wrapped = wrapped[:min(len(wrapped), height)]
	lines := make([]string, 0, len(wrapped))
	for index, line := range wrapped {
		prefix := "       "
		if index == 0 {
			prefix = " NOTES "
		}
		if truncated && index == len(wrapped)-1 {
			line = truncatePlain(strings.TrimSpace(line), max(1, width-lipgloss.Width(prefix)-1)) + "…"
		}
		lines = append(lines, mutedStyle.Render(prefix)+truncatePlain(line, max(1, width-lipgloss.Width(prefix))))
	}
	return lines
}

func renderArtifactTree(snapshot artifactContextSnapshot, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if len(snapshot.Tree) == 0 {
		message := "No artifacts yet"
		if snapshot.TreeError != "" {
			message = "Unavailable · " + snapshot.TreeError
		}
		return []string{mutedStyle.Render(" └─ ") + truncatePlain(oneLine(sanitize(message)), max(1, width-4))}
	}

	limit := min(len(snapshot.Tree), height)
	showMore := snapshot.TreeTruncated || len(snapshot.Tree) > height
	showStatus := showMore || snapshot.TreeError != ""
	if showStatus && height > 1 {
		limit = min(limit, height-1)
	}
	lines := make([]string, 0, height)
	for _, entry := range snapshot.Tree[:limit] {
		indent := strings.Repeat("│  ", max(0, entry.Depth-1))
		name := oneLine(sanitize(strings.ToValidUTF8(entry.Name, "�")))
		switch {
		case entry.Symlink:
			name += "@"
		case entry.Directory:
			name += "/"
		case !entry.Regular:
			name += " [special]"
		}
		if entry.Error != "" {
			name += " [unreadable]"
		}
		prefix := " " + indent + "├─ "
		lines = append(lines, faintStyle.Render(prefix)+truncatePlain(name, max(1, width-lipgloss.Width(prefix))))
	}
	if showStatus && len(lines) < height {
		status := ""
		if showMore {
			status = "… more entries"
		}
		if snapshot.TreeError != "" {
			if status != "" {
				status += " · "
			}
			status += snapshot.TreeError
		}
		lines = append(lines, faintStyle.Render(" └─ ")+mutedStyle.Render(truncatePlain(oneLine(sanitize(status)), max(1, width-4))))
	}
	return lines
}

func artifactContextSummary(snapshot artifactContextSnapshot) string {
	notes := "no notes"
	if snapshot.NotesStatus == artifactNotesReady && strings.TrimSpace(snapshot.Notes) != "" {
		notes = "notes"
	} else if snapshot.NotesStatus == artifactNotesUnavailable {
		notes = "notes unavailable"
	}
	files := "no artifacts"
	if len(snapshot.Tree) > 0 {
		label := "artifacts"
		if len(snapshot.Tree) == 1 {
			label = "artifact"
		}
		count := fmt.Sprintf("%d", len(snapshot.Tree))
		if snapshot.TreeTruncated {
			count += "+"
		}
		files = fmt.Sprintf("%s · %s %s", filepath.Base(snapshot.ArtifactDir), count, label)
	} else if snapshot.TreeError != "" {
		files = "artifacts unavailable"
	}
	return notes + " · " + files
}

func fitContextLines(lines []string, height, width int) []string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = truncateANSI(lines[index], width)
	}
	return lines
}
