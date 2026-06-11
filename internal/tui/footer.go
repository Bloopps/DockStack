package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
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
			SetString("  ·  ")
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

// renderFooterKeysRight affiche les chips de raccourcis et, aligné à droite,
// un indicateur libre (position dans la liste, refresh en cours…).
func renderFooterKeysRight(keys []struct{ key, desc string }, width int, right string) string {
	var parts []string
	for _, k := range keys {
		part := styleFooterKey.Render(k.key) + " " + styleFooterDesc.Render(k.desc)
		parts = append(parts, part)
	}

	sep := styleFooterSep.String()
	line := "  " + strings.Join(parts, sep)

	if right != "" {
		indicator := styleDim.Render(right)
		pad := width - lipgloss.Width(line) - lipgloss.Width(indicator) - 2
		if pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		line += indicator
	}

	return lipgloss.NewStyle().PaddingBottom(0).Render(line)
}
