package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	// Touche en bleu clair gras sur fond gris ardoise : lisible sur tout thème
	// (l'ancien noir sur cyan de palette ne l'était pas), et moins criard que
	// des pavés pleins répétés sur toute la barre.
	styleFooterKey = lipgloss.NewStyle().
			Foreground(colorAccent).
			Background(colorKeyBg).
			Bold(true).
			Padding(0, 1)

	styleFooterDesc = lipgloss.NewStyle().
			Foreground(colorKeyTxt)

	styleFooterSep = lipgloss.NewStyle().
			Foreground(lipgloss.Color("238")).
			SetString(" · ")
)

type footerKey = struct{ key, desc string }

// renderBar rend un pied de page en deux blocs, identique dans toutes les vues :
// à gauche les touches propres à l'écran, à droite (après « │ ») la sortie et
// l'aide, toujours au même endroit. Sur un terminal étroit, ce sont les
// dernières touches de gauche qui cèdent la place, jamais celles de droite.
// info (position dans la liste, heure du rafraîchissement…) s'aligne tout à
// droite s'il tient.
func renderBar(left, right []footerKey, width int, info string) string {
	chip := func(k footerKey) string {
		return styleFooterKey.Render(k.key) + " " + styleFooterDesc.Render(k.desc)
	}
	join := func(ks []footerKey) string {
		parts := make([]string, len(ks))
		for i, k := range ks {
			parts[i] = chip(k)
		}
		return strings.Join(parts, styleFooterSep.String())
	}
	build := func(l []footerKey) string {
		line := "  " + join(l)
		if len(right) > 0 {
			if len(l) > 0 {
				line += styleFooterBlockSep
			}
			line += join(right)
		}
		return line
	}
	line := build(left)
	for width > 0 && lipgloss.Width(line) > width && len(left) > 0 {
		left = left[:len(left)-1]
		line = build(left)
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

// styleFooterBlockSep sépare les touches de l'écran de la sortie et de l'aide.
var styleFooterBlockSep = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("   │   ")

// listFooter : pied de page de la liste. Il sert à naviguer : les actions sur
// la stack sont dans le volet de droite (selon son état), pas répétées ici —
// sauf si le volet est masqué faute de largeur. Il ne change pas d'une ligne à
// l'autre, seulement avec le mode (liste, sélection, services).
func (m Model) listFooter() (left, right []footerKey) {
	help := footerKey{"?", "aide"}
	if m.svcFocus {
		return []footerKey{{"␣", "cocher"}, {"e", "en défaut"}, {"/", "filtre"}, {"+", "dépendances"}},
			[]footerKey{{"←", "retour"}, help}
	}
	if n := len(m.selectedStacks()); n > 0 {
		return []footerKey{
				{fmt.Sprintf("%d", n), "cochée" + plural(n)},
				{"u", "up"}, {"s", "stop"}, {"d", "down"}, {"r", "restart"},
				{"↩", "toutes les actions"},
			},
			[]footerKey{{"esc", "décocher"}, help}
	}
	right = []footerKey{help, {"q", "quitter"}}
	common := []footerKey{{"/", "filtre"}, {"b", "captures"}, {"o", "dossier"}}
	row, ok := m.cursorRow()
	switch {
	case ok && row.header:
		left = []footerKey{{"↩", "replier"}, {"␣", "cocher le dossier"}}
		if !m.previewVisible() {
			left = append([]footerKey{{"u", "up"}, {"d", "down"}, {"r", "restart"}}, left...)
		}
	case ok && row.isStack():
		left = []footerKey{{"↩", "actions"}, {"␣", "cocher"}}
		if !m.previewVisible() {
			left = append([]footerKey{{"u", "up"}, {"d", "down"}, {"r", "restart"}, {"l", "logs"}}, left...)
		}
	}
	if m.previewVisible() && ok && row.isStack() {
		label := "compose"
		if m.previewCompose {
			label = "détail"
		}
		common = append(common, footerKey{"tab", label})
	}
	return append(left, common...), right
}
