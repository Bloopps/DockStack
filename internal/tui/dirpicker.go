package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

type dirLoadedMsg struct {
	path    string
	entries []string
}

func renderDirPicker(m Model) string {
	title := styleBold.Foreground(lipgloss.Color("6")).Render("📂 Choisir le répertoire des stacks")
	sub := styleDim.Render(fmt.Sprintf("  Répertoire actuel : %s", m.dirPath))

	var lines []string
	for i, e := range m.dirEntries {
		line := "  " + e
		if i == m.dirCursor {
			pad := m.width - lipgloss.Width(line)
			if pad < 0 {
				pad = 0
			}
			line = styleSelected.Render(line + strings.Repeat(" ", pad))
		}
		lines = append(lines, line)
	}

	body := strings.Join(lines, "\n")
	hint := styleDim.Render("  ↑↓ naviguer  ↩ ouvrir/choisir  Échap annuler")

	return lipgloss.JoinVertical(lipgloss.Left, title, sub, "", body, "", hint)
}
