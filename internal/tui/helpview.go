package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Aide-mémoire des raccourcis de la liste, ouvert avec « ? » et fermé par
// n'importe quelle touche.
func renderHelpView(m Model, height int) string {
	k := func(s string) string { return styleCyan.Bold(true).Render(s) }
	d := func(s string) string { return styleDim.Render(s) }

	rows := []string{
		styleBold.Render("Raccourcis"),
		"",
		styleYellow.Render("Actions") + d("  (sur la sélection si active, sinon la stack courante)"),
		"  " + k("u") + "  Up           " + k("d") + "  Down",
		"  " + k("r") + "  Restart      " + k("c") + "  Recreate",
		"  " + k("p") + "  Pull         " + k("l") + "  Logs" + d("  (stack courante)"),
		"  " + k("↩") + "  panneau d'actions" + d("  (dont Remove)"),
		"",
		styleYellow.Render("Sélection"),
		"  " + k("␣") + "  sélectionner   " + k("ctrl+a") + "  tout (visible)",
		"  " + k("esc") + "  désélectionner",
		"",
		styleYellow.Render("Groupes (dossiers)") + d("  (en-tête navigable : ␣ et u/d/r/c/p = tout le groupe)"),
		"  " + k("←") + "  replier        " + k("→") + "  déplier",
		"  " + k("↩") + "  sur l'en-tête : replier/déplier",
		"",
		styleYellow.Render("Liste"),
		"  " + k("/") + "  filtrer" + d("  (↩ fige le filtre, esc l'efface)"),
		"  " + k("R") + "  rafraîchir" + d("  (auto toutes les 15 s)") + "   " + k("b") + "  sauvegardes",
		"  " + k("o") + "  changer de répertoire",
		"",
		"  " + k("q") + "  quitter",
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("6")).
		Padding(0, 2).
		MarginLeft(2).
		MarginTop(1)

	return box.Render(strings.Join(rows, "\n"))
}
