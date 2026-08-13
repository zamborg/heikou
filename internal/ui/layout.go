package ui

import tea "charm.land/bubbletea/v2"

// handleResizeKey is a small, explicit layout mode rather than a collection of
// printable-key overrides. Ctrl-G enters it on the dashboard; arrows move the
// horizontal divider until Ctrl-G, Enter, or Esc closes the mode.
func (m Model) handleResizeKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	stroke := key.String()
	switch stroke {
	case "ctrl+g", "enter", "esc":
		m.resizeMode = false
		m.notice = "resize mode closed"
		return m, nil
	case "up":
		m.resizeLowerPane(1)
		return m, nil
	case "down":
		m.resizeLowerPane(-1)
		return m, nil
	case "pgup":
		m.resizeLowerPane(3)
		return m, nil
	case "pgdown":
		m.resizeLowerPane(-3)
		return m, nil
	case "r":
		m.detailAdjust = 0
		return m, nil
	}

	// A non-layout key closes the mode and then keeps its normal meaning, so a
	// stray prefix never traps composer input or an organize chord.
	m.resizeMode = false
	return m.handleKey(key)
}

func (m *Model) resizeLowerPane(delta int) {
	target := min(m.dashboardMaximumDetailHeight(), max(0, m.detailHeight()+delta))
	m.detailAdjust = target - m.defaultDetailHeight()
}

func (m Model) defaultListHeight() int {
	effectiveHeight := m.height - (m.composerInputHeight() - 1)
	if effectiveHeight < 18 {
		return max(2, effectiveHeight-9)
	}
	return max(5, min(14, (effectiveHeight-8)/2))
}

func (m Model) dashboardAvailableHeight() int {
	return max(0, m.height-m.composerInputHeight()-5)
}

func (m Model) dashboardMaximumDetailHeight() int {
	return max(0, m.dashboardAvailableHeight()-2)
}

func (m Model) listHeight() int {
	return max(2, m.dashboardAvailableHeight()-m.detailHeight())
}

func (m Model) defaultDetailHeight() int {
	available := m.dashboardAvailableHeight()
	base := max(0, available-m.defaultListHeight())
	return min(m.dashboardMaximumDetailHeight(), base)
}

func (m Model) detailHeight() int {
	return min(m.dashboardMaximumDetailHeight(), max(0, m.defaultDetailHeight()+m.detailAdjust))
}
