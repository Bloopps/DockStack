package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

func renderBackupView(m Model, height int) string {
	// La feature capture la LISTE des stacks en marche, pas leurs données —
	// l'expliquer d'emblée évite le contresens « sauvegarde de volumes ».
	lines := []string{
		styleBold.Render("  💾 Captures d'état"),
		styleDim.Render("  Mémorise quelles stacks tournent — pas leurs données."),
		styleDim.Render("  Restaurer une capture relance (Up) les stacks listées."),
		"",
	}

	running := 0
	for _, s := range m.stacks {
		if st := s.State(); st == compose.StateRunning || st == compose.StatePartial {
			running++
		}
	}

	for i, a := range backupMenuActions {
		line := "    " + a
		if i == 0 {
			line += styleDim.Render(fmt.Sprintf("  (%d stacks en marche)", running))
		}
		if i == m.actionCursor {
			pad := m.width - lipgloss.Width(line)
			if pad < 0 {
				pad = 0
			}
			line = styleSelected.Render(line + strings.Repeat(" ", pad))
		}
		lines = append(lines, line)
	}

	if len(m.backups) > 0 {
		lines = append(lines, "", styleDim.Render("  Captures — ↩ pour restaurer :"))
		for i, s := range m.backups {
			date := s.Date.Format("02/01/2006 15:04")
			line := fmt.Sprintf("    🔄 %s — %d stacks", date, len(s.Stacks))
			cursorHere := len(backupMenuActions)+i == m.actionCursor
			if cursorHere {
				pad := m.width - lipgloss.Width(line)
				if pad < 0 {
					pad = 0
				}
				line = styleSelected.Render(line + strings.Repeat(" ", pad))
			}
			lines = append(lines, line)
			// Aperçu du contenu de la capture sélectionnée.
			if cursorHere {
				lines = append(lines, "       "+styleDim.Render(previewNames(s.Stacks, 4)))
			}
		}
	} else {
		lines = append(lines, "",
			styleDim.Render("  Aucune capture pour l'instant — ↩ sur « Capturer » pour créer la première."))
	}

	return strings.Join(lines, "\n")
}

func previewNames(names []string, max int) string {
	if len(names) == 0 {
		return "(capture vide)"
	}
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(" … +%d autres", len(names)-max)
}
