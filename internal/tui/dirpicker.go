package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

type dirLoadedMsg struct {
	path    string
	entries []string
	err     error  // lecture du répertoire impossible (permissions…)
	focus   string // entrée sous le curseur à l'arrivée (« » : la première)
}

func renderDirPicker(m Model) string {
	title := styleTitle.Render("📂 Choisir le répertoire des stacks")
	// Noms de dossiers assainis à l'affichage : n'importe qui peut créer dans
	// /tmp un dossier dont le nom contient une séquence d'échappement.
	sub := styleDim.Render(truncate(fmt.Sprintf("  Répertoire actuel : %s", sanitizeText(m.dirPath)), max(m.width, 1)))

	// Fenêtre qui suit le curseur : un dossier aux centaines de
	// sous-dossiers (/usr/lib, un home chargé) poussait le curseur et le pied
	// de page hors de l'écran.
	rowsH := m.dirRowsH()
	off := 0
	if m.dirCursor >= rowsH {
		off = m.dirCursor - rowsH + 1
	}
	var lines []string
	for i := off; i < len(m.dirEntries) && i < off+rowsH; i++ {
		line := "  " + truncate(sanitizeText(m.dirEntries[i]), max(m.width-2, 1))
		if i == m.dirCursor {
			pad := m.width - lipgloss.Width(line)
			if pad < 0 {
				pad = 0
			}
			line = styleSelected.Render(line + strings.Repeat(" ", pad))
		}
		lines = append(lines, line)
	}
	for len(lines) < rowsH {
		lines = append(lines, "")
	}

	body := strings.Join(lines, "\n")
	// Répertoire illisible : le dire plutôt que de l'afficher comme vide.
	if m.dirErr != nil {
		body += "\n\n  " + styleRed.Render("⚠ "+truncate(sanitizeText(m.dirErr.Error()), max(m.width-6, 10)))
	}
	exit := footerKey{"esc", "annuler"}
	if m.cfg.StackDir == "" {
		// Premier lancement : esc n'a nulle part où revenir, q quitte.
		exit = footerKey{"q", "quitter"}
	}
	// ↩ dit ce qu'il fera sur la ligne visée : choisir le dossier affiché,
	// ouvrir le sous-dossier (comme →) ou remonter (comme ←). ↩ vient avant
	// → et ← : sur un terminal étroit, renderBar retire d'abord les
	// dernières touches, et ↩ est la seule qui choisit.
	keys := []footerKey{{"↑↓", "naviguer"}, {"↩", "choisir"}, {"→", "ouvrir"}, {"←", "parent"}}
	if m.dirCursor < len(m.dirEntries) {
		switch m.dirEntries[m.dirCursor] {
		case dirChooseEntry:
		case "../":
			keys = []footerKey{{"↑↓", "naviguer"}, {"↩ / ←", "parent"}, {"→", "ouvrir"}}
		default:
			keys = []footerKey{{"↑↓", "naviguer"}, {"↩ / →", "ouvrir"}, {"←", "parent"}}
		}
	}
	hint := renderBar(keys, []footerKey{exit}, m.width, "")
	// Cette vue a son propre pied de page : la confirmation de sortie doit y
	// être visible aussi (le footer commun ne s'affiche pas ici).
	if m.quitArmed {
		hint = quitWarning()
	}

	return lipgloss.JoinVertical(lipgloss.Left, title, sub, "", body, "", hint)
}

// dirRowsH : lignes de la liste des dossiers. 5 lignes fixes (titre,
// chemin, 2 blancs, pied), 2 de plus pour l'erreur éventuelle.
func (m Model) dirRowsH() int {
	rowsH := m.height - 5
	if m.dirErr != nil {
		rowsH -= 2
	}
	return max(rowsH, 1)
}
