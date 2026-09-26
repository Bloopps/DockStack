package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	styleFooterKey = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("6")).
			Bold(true).
			Padding(0, 1)

	styleFooterDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	styleFooterSep = lipgloss.NewStyle().
			Foreground(lipgloss.Color("238")).
			SetString(" · ")
)

func renderFooterRefresh(width int, refreshing bool) string {
	return renderFooterKeysRefresh([]struct{ key, desc string }{
		{"↑↓", "naviguer"},
		{"↩", "action"},
		{"␣", "sélect"},
		{"b", "backup"},
		{"R", "rafraîchir"},
		{"q", "quitter"},
	}, width, refreshing)
}

func renderFooterKeys(keys []struct{ key, desc string }, width int) string {
	return renderFooterKeysRefresh(keys, width, false)
}

func renderFooterKeysRefresh(keys []struct{ key, desc string }, width int, refreshing bool) string {
	right := ""
	if refreshing {
		right = "↻"
	}
	return renderFooterKeysRight(keys, width, right)
}

// footerPinned : les derniers chips (aide/quitter, retour…) restent toujours
// affichés ; ce sont ceux du milieu qui cèdent la place sur un terminal étroit.
const footerPinned = 2

// renderFooterKeysRight affiche les chips de raccourcis et, aligné à droite,
// un indicateur libre (position dans la liste, refresh en cours…). Le rendu
// coupe ce qui dépasse la largeur : sans cet ajustement, les derniers chips
// (? aide, q quitter) disparaissaient en silence sous ~140 colonnes.
func renderFooterKeysRight(keys []struct{ key, desc string }, width int, right string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = styleFooterKey.Render(k.key) + " " + styleFooterDesc.Render(k.desc)
	}
	sep := styleFooterSep.String()
	build := func(ps []string) string { return "  " + strings.Join(ps, sep) }

	// Retire les chips non épinglés en partant de la fin, jusqu'à tenir.
	line := build(parts)
	for width > 0 && lipgloss.Width(line) > width && len(parts) > footerPinned {
		drop := len(parts) - footerPinned - 1
		parts = append(parts[:drop:drop], parts[drop+1:]...)
		line = build(parts)
	}

	// L'indicateur de droite n'est affiché que s'il tient à côté.
	if right != "" {
		indicator := styleDim.Render(right)
		if pad := width - lipgloss.Width(line) - lipgloss.Width(indicator) - 2; pad > 0 {
			line += strings.Repeat(" ", pad) + indicator
		}
	}
	if width > 0 {
		line = ansi.Truncate(line, width, "")
	}
	return line
}

type footerKey = struct{ key, desc string }

// listFooterKeys : raccourcis de la liste selon ce qui est sous le curseur. Les
// actions directes (u/d/r/l) y figurent : ce sont les touches les plus
// utilisées, et sans elles il fallait ouvrir l'aide pour les découvrir. Sur
// terminal étroit, les chips précédant « ? aide » tombent en premier : l'ordre
// va donc du plus utile au moins utile.
func (m Model) listFooterKeys() []footerKey {
	tail := []footerKey{{"?", "aide"}, {"q", "quitter"}}
	if n := len(m.selectedStacks()); n > 0 {
		return append([]footerKey{
			{fmt.Sprintf("%d", n), "sélectionnées"},
			{"u", "up"}, {"d", "down"}, {"r", "restart"},
			{"b", "captures"},
			{"↩", "plus"},
			{"esc", "désélect"},
		}, tail...)
	}
	var keys []footerKey
	row, ok := m.cursorRow()
	switch {
	case ok && row.header:
		keys = []footerKey{
			{"u", "up dossier"}, {"d", "down"}, {"r", "restart"},
			{"b", "captures"}, {"↩", "replier"}, {"␣", "sélect. dossier"},
		}
	case ok && row.more:
		keys = []footerKey{{"↩", "afficher les non déployées"}}
	case ok && row.isStack():
		keys = []footerKey{
			{"u", "up"}, {"d", "down"}, {"r", "restart"}, {"l", "logs"},
			{"b", "captures"}, {"↩", "plus"}, {"␣", "sélect"},
		}
	}
	inactive := "afficher inactives"
	if m.cfg.ShowInactive {
		inactive = "masquer inactives"
	}
	if !ok || !row.isStack() && !row.header {
		keys = append(keys, footerKey{"b", "captures"})
	}
	keys = append(keys,
		footerKey{"/", "filtre"},
		footerKey{"a", inactive},
		footerKey{"v", "aperçu"},
		footerKey{"x", "exclure"},
		footerKey{"o", "dossier"},
		footerKey{"R", "rafraîchir"},
	)
	return append(keys, tail...)
}
