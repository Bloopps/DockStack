package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Aide-mémoire des raccourcis de la liste, ouvert avec « ? » et fermé par
// n'importe quelle touche. Deux colonnes dès que la largeur le permet : sur
// une seule, l'aide dépassait la hauteur d'un terminal courant et la fin
// (captures, quitter) était coupée.
func renderHelpView(m Model, height int) string {
	k := func(s string) string { return styleCyan.Bold(true).Render(s) }
	d := func(s string) string { return styleDim.Render(s) }
	title := func(s, note string) string { return styleYellow.Render(s) + d(note) }

	actions := []string{
		title("Actions", "  (sélection si active, sinon stack/dossier courant)"),
		"  " + k("u") + "  Up           " + k("d") + "  Down",
		"  " + k("r") + "  Restart      " + k("c") + "  Recreate",
		"  " + k("p") + "  Pull         " + k("l") + "  Logs" + d("  (stack courante)"),
		"  " + k("↩") + "  panneau d'actions" + d("  (dont Compose, Remove)"),
	}
	selection := []string{
		title("Sélection", ""),
		"  " + k("␣") + "  sélectionner   " + k("ctrl+a") + "  tout (visible)",
		"  " + k("esc") + "  efface le filtre, puis la sélection",
	}
	groups := []string{
		title("Dossiers", "  (sur l'en-tête : ␣ et u/d/r/c/p = tout le dossier)"),
		"  " + k("←") + "  replier        " + k("→") + "  déplier",
		"  " + k("↩") + "  sur l'en-tête : replier/déplier",
	}
	list := []string{
		title("Liste", ""),
		"  " + k("↑↓") + " / " + k("j k") + "  naviguer",
		"  " + k("/") + "  filtrer" + d("  (↩ fige le filtre, esc l'efface)"),
		"  " + k("v") + "  aperçu compose" + d("  (") + k("vv") + d(" = plein écran)"),
		"  " + k("a") + "  afficher/masquer les non déployées" + d("  (ou ↩ sur leur ligne)"),
		"  " + k("x") + "  exclure du scan" + d("  (réversible dans ~/.dockstack/config.json)"),
		"  " + k("R") + "  rafraîchir" + d("  (auto : événements Docker + 60 s)"),
		"  " + k("o") + "  changer de répertoire",
	}
	captures := []string{
		title("Captures d'état", ""),
		"  " + k("b") + "  ouvrir" + d("  (puis c capturer, ↩ restaurer, x supprimer)"),
	}
	general := []string{
		title("Général", ""),
		"  " + k("q") + " / " + k("Q") + " / " + k("ctrl+c") + "  quitter" + d("  (deux frappes)"),
		"  " + k("?") + "  cette aide" + d("  (une touche pour fermer)"),
	}

	join := func(blocks ...[]string) string {
		var out []string
		for i, b := range blocks {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, b...)
		}
		return strings.Join(out, "\n")
	}
	left := join(actions, selection, groups)
	right := join(list, captures, general)

	var body string
	if lipgloss.Width(left)+lipgloss.Width(right)+14 <= m.width {
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, "      ", right)
	} else {
		body = join(actions, selection, groups, list, captures, general)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("6")).
		Padding(0, 2).
		MarginLeft(2)

	return box.Render(styleBold.Render("Raccourcis") + "\n\n" + body)
}
