package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderRestorePick affiche la sélection des stacks d'une capture à relancer :
// tout est coché par défaut, ␣ décoche, ↩ restaure ce qui reste coché.
func renderRestorePick(m Model, height int) string {
	selected := 0
	for _, v := range m.restoreSel {
		if v {
			selected++
		}
	}

	header := []string{
		styleBold.Render("  🔄 Restaurer — " + m.restoreLabel),
		styleDim.Render("  Décocher ce qu'il ne faut pas relancer, puis ↩."),
		"  " + styleCyan.Render(fmt.Sprintf("%d/%d sélectionnées", selected, len(m.restoreNames))),
		"",
	}

	// Fenêtrage centré sur le curseur : une capture peut contenir des
	// centaines de stacks.
	listH := height - len(header) - 2 // 2 = lignes « ··· au-dessus/en-dessous »
	if listH < 3 {
		listH = 3
	}
	start, end := 0, len(m.restoreNames)
	if len(m.restoreNames) > listH {
		start = m.restoreCursor - listH/2
		if start < 0 {
			start = 0
		}
		if start+listH > len(m.restoreNames) {
			start = len(m.restoreNames) - listH
		}
		end = start + listH
	}

	lines := header
	if start > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("    ··· %d au-dessus", start)))
	}
	for i := start; i < end; i++ {
		name := m.restoreNames[i]
		var check string
		if m.restoreSel[name] {
			check = styleGreen.Render("[x]")
		} else {
			check = styleDim.Render("[ ]")
		}
		line := "    " + check + " " + name
		if i == m.restoreCursor {
			pad := m.width - lipgloss.Width(line)
			if pad < 0 {
				pad = 0
			}
			line = styleSelected.Render(line + strings.Repeat(" ", pad))
		}
		lines = append(lines, line)
	}
	if end < len(m.restoreNames) {
		lines = append(lines, styleDim.Render(fmt.Sprintf("    ··· %d en-dessous", len(m.restoreNames)-end)))
	}

	return strings.Join(lines, "\n")
}
