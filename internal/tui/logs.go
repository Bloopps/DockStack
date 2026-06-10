package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func renderLogsView(m Model) string {
	title := styleBold.Foreground(lipgloss.Color("6")).Render("📋 Logs")
	hint := styleDim.Render("  ↑↓ défiler  G fin  q quitter")
	sep := styleDim.Render(strings.Repeat("─", m.width))

	bodyH := m.height - 3
	if bodyH < 0 {
		bodyH = 0
	}

	start := m.logScroll
	if start < 0 {
		start = 0
	}
	if start > len(m.logLines) {
		start = len(m.logLines)
	}
	end := start + bodyH
	if end > len(m.logLines) {
		end = len(m.logLines)
	}

	var lines []string
	for _, l := range m.logLines[start:end] {
		lines = append(lines, "  "+l)
	}

	body := strings.Join(lines, "\n")
	return lipgloss.JoinVertical(lipgloss.Left, title, sep, body, hint)
}
