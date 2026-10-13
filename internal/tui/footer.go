package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type footerKey = struct{ key, desc string }

// renderBar rend un pied de page en deux blocs, identique dans toutes les vues :
// à gauche les touches propres à l'écran, à droite (après « │ ») la sortie et
// l'aide, toujours au même endroit. Sur un terminal étroit, ce sont les
// dernières touches de gauche qui cèdent la place, jamais celles de droite.
// info (position dans la liste, heure du rafraîchissement…) s'aligne tout à
// droite s'il tient.
func renderBar(left, right []footerKey, width int, info string) string {
	line := buildBar(left, right)
	for width > 0 && lipgloss.Width(line) > width && len(left) > 0 {
		left = left[:len(left)-1]
		line = buildBar(left, right)
	}
	if info != "" {
		ind := styleDim.Render(info)
		if pad := width - lipgloss.Width(line) - lipgloss.Width(ind) - 2; pad > 0 {
			line += strings.Repeat(" ", pad) + ind
		}
	}
	if width > 0 {
		line = ansi.Truncate(line, width, "")
	}
	return line
}

// buildBar assemble la ligne de renderBar, sans la couper. Une touche sans
// description (« ↑↓ pgup/pgdn ») se passe d'elle et de son espace.
func buildBar(left, right []footerKey) string {
	chip := func(k footerKey) string {
		if k.desc == "" {
			return styleFooterKey.Render(k.key)
		}
		return styleFooterKey.Render(k.key) + " " + styleFooterDesc.Render(k.desc)
	}
	join := func(ks []footerKey) string {
		parts := make([]string, len(ks))
		for i, k := range ks {
			parts[i] = chip(k)
		}
		return strings.Join(parts, styleFooterSep.String())
	}
	line := "  " + join(left)
	if len(right) > 0 {
		if len(left) > 0 {
			line += styleFooterBlockSep
		}
		line += join(right)
	}
	return line
}

// renderBarFit : renderBar avec plusieurs jeux de touches de gauche, du
// plus complet au plus court. Le premier qui tient en entier est retenu,
// sinon le dernier, coupé par la fin comme d'habitude : un pied étroit
// garde ainsi des touches choisies plutôt que les premières de la liste.
func renderBarFit(lefts [][]footerKey, right []footerKey, width int, info string) string {
	for _, l := range lefts[:len(lefts)-1] {
		if lipgloss.Width(buildBar(l, right)) <= width {
			return renderBar(l, right, width, info)
		}
	}
	return renderBar(lefts[len(lefts)-1], right, width, info)
}

// Touches de défilement des vues à lire (logs, compose) ; scrollKeyShort,
// sans description, sert aux pieds étroits (cf. renderBarFit).
var (
	scrollKey      = footerKey{"↑↓ pgup/pgdn", "défiler"}
	scrollKeyShort = footerKey{"↑↓ pgup/pgdn", ""}
	edgeKey        = footerKey{"g/G", "haut/bas"}
)

// listFooter : pied de page de la liste. Il sert à naviguer : les actions sur
// la stack sont dans le volet de droite (selon son état), pas répétées ici —
// sauf si le volet est masqué faute de largeur. Il ne change pas d'une ligne à
// l'autre, seulement avec le mode (liste, saisie du filtre, sélection,
// services).
func (m Model) listFooter() (left, right []footerKey) {
	help := footerKey{"?", "aide"}
	// Saisie du filtre : les lettres, ␣ et ↩ n'agissent pas sur la liste, on
	// n'annonce que ce qui marche pendant la saisie (? y est une lettre).
	if m.filtering {
		return []footerKey{{"↩", "valider"}, {"↑↓", "choisir"}}, []footerKey{{"esc", "effacer"}}
	}
	if m.svcFocus {
		return []footerKey{{"␣", "cocher"}, {"e", "en défaut"}, {"/", "filtre"}, {"+", "dépendances"}},
			[]footerKey{{"←", "retour"}, help}
	}
	if sel := m.selection(); sel.any() {
		// Les cochées masquées (filtre, a, repli) sont comptées à part :
		// les actions ne visent que les visibles, et aucune lettre n'agit si
		// toutes sont masquées (cf. refuseHiddenSelection).
		esc := footerKey{"esc", "décocher"}
		if sel.byFilter {
			esc = footerKey{"esc", "effacer le filtre"} // esc efface d'abord le filtre
		}
		if sel.shown == 0 {
			left = []footerKey{{fmt.Sprintf("%d", sel.hidden), "cochée" + plural(sel.hidden) + " masquée" + plural(sel.hidden)}}
			if !sel.byFilter {
				if sel.dormant > 0 {
					left = append(left, footerKey{"a", "tout afficher"})
				}
				if sel.folded > 0 {
					left = append(left, footerKey{"→", "déplier le dossier"})
				}
			}
			return left, []footerKey{esc, help}
		}
		left = []footerKey{{fmt.Sprintf("%d", sel.shown), "cochée" + plural(sel.shown)}}
		if sel.hidden > 0 {
			left = append(left, footerKey{fmt.Sprintf("%d", sel.hidden), "masquée" + plural(sel.hidden)})
		}
		return append(left, footerKey{"u", "up"}, footerKey{"s", "stop"}, footerKey{"d", "down"},
				footerKey{"r", "restart"}, footerKey{"↩", "toutes les actions"}),
			[]footerKey{esc, help}
	}
	right = []footerKey{help, {"q", "quitter"}}
	// Pas de o : l'en-tête rappelle « o changer » à côté du dossier, à
	// toutes les largeurs. n y gagne sa place : la page Nettoyage n'était
	// annoncée nulle part.
	common := []footerKey{{"/", "filtre"}, {"b", "captures"}, {"n", "nettoyage"}}
	row, ok := m.cursorRow()
	switch {
	case ok && row.header:
		// Sous un filtre, la liste ignore le repli : ↩ n'y replie rien.
		// Replié, ↩ le déplie.
		left = []footerKey{{"␣", "cocher le dossier"}}
		if m.filter.Value() == "" {
			fold := "replier"
			if row.folded {
				fold = "déplier"
			}
			left = append([]footerKey{{"↩", fold}}, left...)
		}
		if !m.previewVisible() {
			left = append([]footerKey{{"u", "up"}, {"d", "down"}, {"r", "restart"}}, left...)
		}
	case ok && row.isStack():
		left = []footerKey{{"↩", "actions"}}
		if !m.previewVisible() {
			// Volet masqué : les actions courantes, mais APRÈS ↩, seule porte
			// vers Stop, Recreate, Pull, Compose et Remove. renderBar coupe
			// par la fin : devant, elles le poussaient hors de l'écran en
			// 80 colonnes, là même où le volet qui les rappelle manque.
			left = append(left, footerKey{"u", "up"}, footerKey{"d", "down"}, footerKey{"r", "restart"}, footerKey{"l", "logs"})
		}
		left = append(left, footerKey{"␣", "cocher"})
	}
	if m.previewVisible() && ok && row.isStack() {
		label := "compose"
		if m.previewCompose {
			label = "détail"
		}
		common = append(common, footerKey{"tab", label})
	}
	common = append(common, footerKey{"t", "thème"})
	return append(left, common...), right
}
