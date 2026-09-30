package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func renderLogsView(m Model) string {
	title := styleTitle.Render(truncate("📋 Logs — "+m.logStack, max(m.width, 1)))
	hint := renderBar([]footerKey{{"↑↓", "défiler"}, {"g/G", "haut/bas"}},
		[]footerKey{{"esc", "retour"}, {"q", "quitter"}}, m.width, "")
	if m.quitArmed {
		hint = quitWarning()
	}
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
		// Tronqué à la largeur du terminal : une ligne qui wrappe ajouterait
		// des lignes physiques non comptées par bodyH et décalerait toute la
		// mise en page (titre/footer compris).
		lines = append(lines, truncate("  "+l, m.width))
	}

	body := strings.Join(lines, "\n")
	return lipgloss.JoinVertical(lipgloss.Left, title, sep, body, hint)
}
