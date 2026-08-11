package ui

import tea "charm.land/bubbletea/v2"

// handleResizeKey is a small, explicit layout mode rather than a collection of
// printable-key overrides. Ctrl-G enters it on either primary surface; arrows
// move the horizontal divider until Ctrl-G, Enter, or Esc closes the mode.
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
		if m.organizerOpen {
			m.organizerContextAdjust = 0
		} else {
			m.detailAdjust = 0
		}
		return m, nil
	}

	// A non-layout key closes the mode and then keeps its normal meaning, so a
	// stray prefix never traps composer input or organizer actions.
	m.resizeMode = false
	return m.handleKey(key)
}

func (m *Model) resizeLowerPane(delta int) {
	if m.organizerOpen {
		target := min(m.organizerMaximumContextHeight(), max(0, m.organizerContextHeight()+delta))
		m.organizerContextAdjust = target - m.defaultOrganizerContextHeight()
		return
	}
	target := min(m.dashboardMaximumDetailHeight(), max(0, m.detailHeight()+delta))
	m.detailAdjust = target - m.defaultDetailHeight()
}

func (m Model) organizerViewportHeight() int {
	return max(0, m.organizerAvailableHeight()-m.organizerContextHeight())
}

func (m Model) organizerAvailableHeight() int {
	reserved := 5 // title, rule, status rule, message, key help
	if m.organizerEdit != "" {
		reserved++
	}
	return max(0, m.height-reserved)
}

func (m Model) organizerMaximumContextHeight() int {
	return max(0, m.organizerAvailableHeight()-3)
}

func (m Model) defaultOrganizerContextHeight() int {
	available := m.organizerAvailableHeight()
	if available < 4 {
		return 0
	}
	// Give notes and artifacts a useful default while retaining at least three
	// rows for the workstream/session tree. Ctrl-G resize mode can move the
	// divider all the way down when a larger session overview is preferred.
	maximum := m.organizerMaximumContextHeight()
	base := max(2, available*55/100)
	if m.height >= 20 {
		base = max(7, base)
	}
	return min(maximum, base)
}

func (m Model) organizerContextHeight() int {
	return min(m.organizerMaximumContextHeight(), max(0, m.defaultOrganizerContextHeight()+m.organizerContextAdjust))
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
