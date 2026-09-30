package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

type dirLoadedMsg struct {
	path    string
	entries []string
	err     error // lecture du répertoire impossible (permissions…)
}

func renderDirPicker(m Model) string {
	title := styleTitle.Render("📂 Choisir le répertoire des stacks")
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
	// Répertoire illisible : le dire plutôt que de l'afficher comme vide.
	if m.dirErr != nil {
		body += "\n\n  " + styleRed.Render("⚠ "+truncate(m.dirErr.Error(), max(m.width-6, 10)))
	}
	exit := footerKey{"esc", "annuler"}
	if m.cfg.StackDir == "" {
		// Premier lancement : esc n'a nulle part où revenir, q quitte.
		exit = footerKey{"q", "quitter"}
	}
	hint := renderBar([]footerKey{{"↑↓", "naviguer"}, {"↩", "ouvrir / choisir"}}, []footerKey{exit}, m.width, "")
	// Cette vue a son propre pied de page : la confirmation de sortie doit y
	// être visible aussi (le footer commun ne s'affiche pas ici).
	if m.quitArmed {
		hint = quitWarning()
	}

	return lipgloss.JoinVertical(lipgloss.Left, title, sub, "", body, "", hint)
}
