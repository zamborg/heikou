package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/zamborg/heikou/internal/format"
	"github.com/zamborg/heikou/internal/workstream"
)

const artifactContextTimeout = 2 * time.Second

type artifactContextMsg struct {
	generation uint64
	key        string
	snapshot   artifactContextSnapshot
}

// artifactContextState is a single slot on purpose. Caching per workstream
// would grow with the number of workstreams; one slot keeps the cost of this
// pane constant no matter how large the installation gets.
type artifactContextState struct {
	generation uint64
	loadingKey string
	snapshot   artifactContextSnapshot
}

func artifactContextKey(item workstream.Workstream) string {
	return item.ID + "\x00" + item.ArtifactDir
}

// artifactContextCmd refreshes the UI-owned artifact cache. The controller and
// durable workstream model deliberately remain unaware of this preview. The
// cache is keyed on the selected workstream, so it reads when the selection
// lands somewhere new and costs nothing while the cursor sits still.
func (m *Model) artifactContextCmd(force bool) tea.Cmd {
	item, ok := m.selectedWorkstreamContext()
	if !ok {
		m.artifactContext.generation++
		m.artifactContext.loadingKey = ""
		m.artifactContext.snapshot = artifactContextSnapshot{}
		return nil
	}
	key := artifactContextKey(item)
	loadedKey := m.artifactContext.snapshot.WorkstreamID + "\x00" + m.artifactContext.snapshot.ArtifactDir
	if !force {
		if loadedKey == key {
			// Returning to a cached selection abandons any read for the selection
			// we just left. Otherwise that stale result can be rejected below while
			// leaving loadingKey behind, permanently suppressing a future retry.
			if m.artifactContext.loadingKey != "" && m.artifactContext.loadingKey != key {
				m.artifactContext.generation++
				m.artifactContext.loadingKey = ""
			}
			return nil
		}
		if m.artifactContext.loadingKey == key {
			return nil
		}
	}
	m.artifactContext.generation++
	generation := m.artifactContext.generation
	m.artifactContext.loadingKey = key
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
	if message.generation != m.artifactContext.generation {
		return
	}
	item, ok := m.selectedWorkstreamContext()
	if !ok || message.key != artifactContextKey(item) {
		return
	}
	m.artifactContext.loadingKey = ""
	m.artifactContext.snapshot = message.snapshot
}

// renderWorkstreamArtifacts fills the dashboard's detail pane for a selected
// workstream. It is the same slot a session row uses for its terminal preview:
// one pane showing context for whichever noun the cursor is on.
func (m Model) renderWorkstreamArtifacts(item workstream.Workstream, height int) []string {
	if height <= 0 {
		return nil
	}
	key := artifactContextKey(item)
	loadedKey := m.artifactContext.snapshot.WorkstreamID + "\x00" + m.artifactContext.snapshot.ArtifactDir
	if loadedKey != key {
		return fitContextLines([]string{mutedStyle.Render(" Loading notes and files…")}, height, m.width)
	}
	if height == 1 {
		summary := artifactContextSummary(m.artifactContext.snapshot)
		return []string{mutedStyle.Render(" " + truncatePlain(summary, max(1, m.width-1)))}
	}

	notesHeight := 1
	if height >= 5 {
		// Share larger panes more evenly. Keep at least a label and one tree row
		// for files, while allowing enough notes to be genuinely useful.
		notesHeight = min(height-3, max(2, (height-2)*2/5))
	}
	lines := renderArtifactNotes(m.artifactContext.snapshot, m.width, notesHeight)

	filesHeight := max(1, height-len(lines))
	filesLabel := " FILES " + format.OneLine(format.CompactPath(item.ArtifactDir))
	lines = append(lines, mutedStyle.Render(truncatePlain(filesLabel, m.width)))
	filesHeight--
	if filesHeight > 0 {
		lines = append(lines, renderArtifactTree(m.artifactContext.snapshot, m.width, filesHeight)...)
	}
	return fitContextLines(lines, height, m.width)
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
			body = "No notes yet"
		}
	case artifactNotesMissing:
		body = "No notes yet"
	default:
		body = "Unavailable"
		if snapshot.NotesError != "" {
			body += " · " + snapshot.NotesError
		}
	}
	body = strings.ToValidUTF8(format.Sanitize(body), "�")
	wrapped := wrapLines(body, max(1, width-8))
	if len(wrapped) == 0 {
		wrapped = []string{"No notes yet"}
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
		return []string{mutedStyle.Render(" └─ ") + truncatePlain(format.OneLine(message), max(1, width-4))}
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
		name := format.OneLine(strings.ToValidUTF8(entry.Name, "�"))
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
		lines = append(lines, faintStyle.Render(" └─ ")+mutedStyle.Render(truncatePlain(format.OneLine(status), max(1, width-4))))
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
