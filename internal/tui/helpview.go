package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

// renderHelpView affiche l'aide sur height lignes. Si elle dépasse (terminal
// bas ou étroit, une seule colonne), elle défile (↑↓, pgup/pgdn, g/G) et la
// dernière ligne indique ce qui reste caché : couper la fin masquait les
// captures et la règle de sortie.
func renderHelpView(m Model, height int) string {
	lines := strings.Split(helpBox(m), "\n")
	if len(lines) <= height || height < 2 {
		return strings.Join(lines, "\n")
	}
	win := height - 1
	start := min(m.helpScroll, len(lines)-win)
	out := append([]string(nil), lines[start:start+win]...)
	var hint []string
	if start > 0 {
		hint = append(hint, fmt.Sprintf("↑ %d ligne%s au-dessus", start, plural(start)))
	}
	if below := len(lines) - start - win; below > 0 {
		hint = append(hint, fmt.Sprintf("↓ %d ligne%s en dessous", below, plural(below)))
	}
	return strings.Join(append(out, styleDim.Render("    "+strings.Join(hint, " · "))), "\n")
}

// helpMaxScroll : défilement maximal de l'aide (0 si elle tient à l'écran).
func (m Model) helpMaxScroll() int {
	n := strings.Count(helpBox(m), "\n") + 1
	h := m.bodyHeight()
	if n <= h || h < 2 {
		return 0
	}
	return n - (h - 1)
}

// helpBox : aide-mémoire des raccourcis, ouvert avec « ? ». Deux colonnes dès
// que la largeur le permet, sinon une seule (qui défile si besoin).
func helpBox(m Model) string {
	k := func(s string) string { return styleCyan.Bold(true).Render(s) }
	d := func(s string) string { return styleDim.Render(s) }
	title := func(s, note string) string { return styleYellow.Render(s) + d(note) }

	actions := []string{
		title("Actions", "  (stacks cochées visibles, sinon stack/dossier courant)"),
		"  " + k("u") + "  Up      " + k("s") + "  Stop      " + k("d") + "  Down",
		"  " + k("r") + "  Restart " + k("c") + "  Recreate  " + k("p") + "  Pull",
		"  " + k("l") + "  Logs" + d("  (") + k("G") + d(" suivre · ") + k("/") + d(" filtrer · ") + k("w") + d(" replier les lignes)"),
		"  " + k("↩") + "  panneau d'actions" + d("  (mêmes lettres, plus Remove)"),
	}
	services := []string{
		title("Services", "  (agir sur certains services, sans toucher aux autres)"),
		"  " + k("→") + "  entrer dans les services de la stack",
		"  " + k("␣") + "  cocher   " + k("ctrl+a") + "  tout   " + k("e") + "  en défaut   " + k("/") + "  filtrer",
		"  " + k("u") + " up  " + k("s") + " stop  " + k("d") + " down  " + k("r") + " restart  " + k("c") + " recreate  " + k("p") + " pull  " + k("l") + " logs",
		"  " + k("+") + "  inclure les dépendances" + d("  (up, recreate, pull)"),
		"  " + k("←") + "  retour" + d("  (esc : efface le filtre, décoche, puis sort)"),
	}
	selection := []string{
		title("Sélection", ""),
		"  " + k("␣") + "  sélectionner   " + k("ctrl+a") + "  tout (visible)",
		"  " + k("esc") + "  efface le filtre, puis la sélection",
		"  " + d("cochée puis masquée (filtre, a) : jamais visée"),
	}
	// Légende de la liste : une pastille par état (la couleur seule
	// disparaît sous le curseur), la coche, et ⟳ qui n'était expliqué
	// nulle part.
	sym := func(st compose.State, label string) string { return stateDot(st) + " " + label }
	symbols := []string{
		title("Symboles", ""),
		"  " + sym(compose.StateRunning, "en marche") + "   " + sym(compose.StatePartial, "partielle") + "   " + sym(compose.StateUnhealthy, "unhealthy"),
		"  " + sym(compose.StateStopped, "arrêtée") + "     " + sym(compose.StateUnknown, "non déployée"),
		"  " + styleYellow.Render("┃") + " cochée      ⟳ recréé hors compose" + d("  (rattaché par nom)"),
	}
	groups := []string{
		title("Dossiers", "  (sur l'en-tête : ␣ et u/s/d/r/c/p = tout le dossier)"),
		"  " + k("←") + "  replier   " + k("→") + "  déplier   " + k("↩") + "  replier/déplier",
	}
	list := []string{
		title("Liste", ""),
		"  " + k("↑↓") + " / " + k("j k") + "  naviguer   " + k("g G") + "  haut / bas",
		"  " + k("/") + "  filtrer" + d("  (↑↓ choisir, ↩ fige le filtre, esc l'efface)"),
		"  " + k("v") + "  fichier compose en plein écran" + d("  (") + k("tab") + d(" : dans le volet)"),
		"  " + k("a") + "  afficher/masquer les stacks arrêtées" + d("  (et non déployées)"),
		"  " + k("x") + "  exclure du scan" + d("  (réversible dans ~/.dockstack/config.json)"),
		"  " + k("R") + "  rafraîchir" + d("  (auto)") + "   " + k("o") + "  répertoire   " + k("t") + "  thème   " + k("n") + "  nettoyage",
	}
	captures := []string{
		title("Captures d'état", ""),
		"  " + k("b") + "  ouvrir" + d("  (c capturer, ↩ relancer, i à l'identique)"),
		"  " + d("n nommer · * épingler (jamais supprimée) · del supprimer"),
	}
	cleanup := []string{
		title("Nettoyage", "  (n)"),
		"  " + k("␣") + "  cocher   " + k("→ ←") + "  déplier / replier   " + k("R") + "  réanalyser",
		"  " + k("↩") + "  nettoyer la sélection" + d("  (sur une commande prune : la lancer)"),
	}
	general := []string{
		title("Général", ""),
		"  " + k("q") + "  quitter" + d("  (2 frappes, partout)") + "  " + k("esc") + "  retour  " + k("?") + "  aide",
		"  " + k("pgup pgdn") + "  page   " + k("g G") + "  haut / bas" + d("  (listes, logs, compose…)"),
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
	// Symboles à gauche, la colonne la plus courte : à droite, l'aide ne
	// tenait plus entière sur un écran large de 30 lignes.
	left := join(actions, services, selection, symbols)
	right := join(groups, list, captures, cleanup, general)

	var body string
	if lipgloss.Width(left)+lipgloss.Width(right)+14 <= m.width {
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, "      ", right)
	} else {
		body = join(actions, services, selection, groups, list, captures, cleanup, general, symbols)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(activeScheme.Border).
		Padding(0, 2).
		MarginLeft(2)

	return box.Render(styleBold.Render("Raccourcis") + "\n\n" + body)
}
