package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const maxComposerInputRows = 5

type composerLine struct {
	start int
	end   int
}

// handleComposerShortcut owns editing chords that never act on dashboard
// selection. Configured launch/send bindings run first, so an explicitly
// rebound Shift-Enter still wins; otherwise it is the native multiline key.
func (m *Model) handleComposerShortcut(stroke string) bool {
	switch stroke {
	case "shift+enter", "ctrl+j", "alt+enter", "meta+enter":
		m.insertText("\n")
	case "alt+left", "meta+left", "alt+b", "meta+b", "ctrl+left":
		m.moveInputWord(-1)
	case "alt+right", "meta+right", "alt+f", "meta+f", "ctrl+right":
		m.moveInputWord(1)
	case "super+left", "alt+up", "meta+up":
		m.moveInputLineBoundary(-1)
	case "super+right", "alt+down", "meta+down":
		m.moveInputLineBoundary(1)
	case "super+up", "ctrl+home":
		m.inputCursor = 0
		m.resetInputColumn()
	case "super+down", "ctrl+end":
		m.inputCursor = len(m.input)
		m.resetInputColumn()
	case "alt+backspace", "meta+backspace":
		m.deletePreviousWord()
	case "alt+delete", "meta+delete":
		m.deleteNextWord()
	case "super+backspace":
		m.deleteInputToLineStart()
	case "super+delete", "ctrl+k":
		m.deleteInputToLineEnd()
	case "ctrl+b":
		m.moveInputCharacter(-1)
	case "ctrl+f":
		m.moveInputCharacter(1)
	case "ctrl+d":
		m.deleteInputForward()
	case "ctrl+p":
		if !m.hasMultilineInput() {
			return false
		}
		m.moveInputVertical(-1)
	case "ctrl+n":
		if !m.hasMultilineInput() {
			return false
		}
		m.moveInputVertical(1)
	default:
		return false
	}
	return true
}

func (m *Model) moveInputCharacter(delta int) {
	m.inputCursor = min(max(0, m.inputCursor+delta), len(m.input))
	m.resetInputColumn()
}

func (m *Model) moveInputWord(delta int) {
	if delta < 0 {
		m.inputCursor = previousWordBoundary(m.input, m.inputCursor)
	} else if delta > 0 {
		m.inputCursor = nextWordBoundary(m.input, m.inputCursor)
	}
	m.resetInputColumn()
}

func previousWordBoundary(clusters []string, cursor int) int {
	cursor = min(max(0, cursor), len(clusters))
	for cursor > 0 && clusterIsSpace(clusters[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !clusterIsSpace(clusters[cursor-1]) {
		cursor--
	}
	return cursor
}

func nextWordBoundary(clusters []string, cursor int) int {
	cursor = min(max(0, cursor), len(clusters))
	for cursor < len(clusters) && clusterIsSpace(clusters[cursor]) {
		cursor++
	}
	for cursor < len(clusters) && !clusterIsSpace(clusters[cursor]) {
		cursor++
	}
	return cursor
}

func (m *Model) moveInputLineBoundary(direction int) {
	start, end := inputLineBounds(m.input, m.inputCursor)
	if direction < 0 {
		m.inputCursor = start
	} else if direction > 0 {
		m.inputCursor = end
	}
	m.resetInputColumn()
}

func inputLineBounds(clusters []string, cursor int) (int, int) {
	cursor = min(max(0, cursor), len(clusters))
	start := cursor
	for start > 0 && clusters[start-1] != "\n" {
		start--
	}
	end := cursor
	for end < len(clusters) && clusters[end] != "\n" {
		end++
	}
	return start, end
}

func (m *Model) moveInputVertical(delta int) {
	lines := composerLines(m.input)
	lineIndex := composerCursorLine(lines, m.inputCursor)
	target := lineIndex + delta
	if target < 0 || target >= len(lines) {
		return
	}
	if !m.inputColumnSet {
		line := lines[lineIndex]
		m.inputColumn = inputCellWidth(m.input[line.start:m.inputCursor])
		m.inputColumnSet = true
	}
	targetLine := lines[target]
	m.inputCursor = inputCursorAtCell(m.input, targetLine, m.inputColumn)
}

func inputCursorAtCell(clusters []string, line composerLine, target int) int {
	used := 0
	for index := line.start; index < line.end; index++ {
		width := composerClusterWidth(clusters[index])
		if used+width > target {
			if target-used >= used+width-target {
				return index + 1
			}
			return index
		}
		used += width
	}
	return line.end
}

func inputCellWidth(clusters []string) int {
	width := 0
	for _, cluster := range clusters {
		width += composerClusterWidth(cluster)
	}
	return width
}

func (m *Model) deleteInputBackward() {
	if m.inputCursor == 0 {
		return
	}
	m.input = append(m.input[:m.inputCursor-1], m.input[m.inputCursor:]...)
	m.inputCursor--
	m.resetInputColumn()
}

func (m *Model) deleteInputForward() {
	if m.inputCursor >= len(m.input) {
		return
	}
	m.input = append(m.input[:m.inputCursor], m.input[m.inputCursor+1:]...)
	m.resetInputColumn()
}

func (m *Model) deleteNextWord() {
	end := nextWordBoundary(m.input, m.inputCursor)
	if end == m.inputCursor {
		return
	}
	m.input = append(m.input[:m.inputCursor], m.input[end:]...)
	m.resetInputColumn()
}

func (m *Model) deleteInputToLineStart() {
	start, _ := inputLineBounds(m.input, m.inputCursor)
	if start == m.inputCursor {
		return
	}
	m.input = append(m.input[:start], m.input[m.inputCursor:]...)
	m.inputCursor = start
	m.resetInputColumn()
}

func (m *Model) deleteInputToLineEnd() {
	_, end := inputLineBounds(m.input, m.inputCursor)
	if end == m.inputCursor && end < len(m.input) && m.input[end] == "\n" {
		end++
	}
	if end == m.inputCursor {
		return
	}
	m.input = append(m.input[:m.inputCursor], m.input[end:]...)
	m.resetInputColumn()
}

func composerClusterWidth(cluster string) int {
	if cluster == "\t" {
		return 4
	}
	return lipgloss.Width(cluster)
}

func composerDisplayClusters(clusters []string) []string {
	display := append([]string(nil), clusters...)
	for index, cluster := range display {
		if cluster == "\t" {
			display[index] = "    "
		}
	}
	return display
}

func (m *Model) resetInputColumn() {
	m.inputColumn = 0
	m.inputColumnSet = false
}

func (m Model) hasMultilineInput() bool {
	for _, cluster := range m.input {
		if cluster == "\n" {
			return true
		}
	}
	return false
}

func composerLines(clusters []string) []composerLine {
	lines := []composerLine{{start: 0}}
	for index, cluster := range clusters {
		if cluster != "\n" {
			continue
		}
		lines[len(lines)-1].end = index
		lines = append(lines, composerLine{start: index + 1})
	}
	lines[len(lines)-1].end = len(clusters)
	return lines
}

func composerCursorLine(lines []composerLine, cursor int) int {
	for index, line := range lines {
		if cursor <= line.end {
			return index
		}
	}
	return max(0, len(lines)-1)
}

func (m Model) composerInputHeight() int {
	height := min(maxComposerInputRows, len(composerLines(m.input)))
	return min(height, max(1, m.height-7))
}

func (m Model) renderComposerInput(prefix string) []string {
	lines := composerLines(m.input)
	cursorLine := composerCursorLine(lines, m.inputCursor)
	height := m.composerInputHeight()
	start := max(0, cursorLine-height+1)
	if start+height > len(lines) {
		start = max(0, len(lines)-height)
	}
	visible := make([]string, 0, height)
	for lineIndex := start; lineIndex < min(len(lines), start+height); lineIndex++ {
		line := lines[lineIndex]
		label := prefix
		if lineIndex > 0 {
			label = mutedStyle.Render(fmt.Sprintf("%3d │ ", lineIndex+1))
		}
		if lipgloss.Width(label) >= m.width {
			label = truncateANSI(label, max(0, m.width-1))
		}
		inputWidth := max(1, m.width-lipgloss.Width(label))
		display := composerDisplayClusters(m.input[line.start:line.end])
		body := truncatePlain(strings.Join(display, ""), inputWidth)
		if lineIndex == cursorLine {
			body = m.renderTextInput(display, m.inputCursor-line.start, inputWidth)
		}
		visible = append(visible, padANSI(truncateANSI(label+body, m.width), m.width))
	}
	return visible
}
