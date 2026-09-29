package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderRestorePick affiche la sélection des stacks d'une capture à relancer.
// Seules celles « à relancer » sont cochées par défaut ; les déjà en marche
// peuvent l'être à la main (un up est alors prévu avant confirmation) et les
// introuvables ne sont pas sélectionnables.
func renderRestorePick(m Model, height int) string {
	var nRelaunch, nRunning, nMissing, selected int
	for _, e := range m.restoreEntries {
		switch e.kind {
		case entryRelaunch:
			nRelaunch++
		case entryRunning:
			nRunning++
		case entryMissing:
			nMissing++
		}
		if m.restoreSel[e.name] {
			selected++
		}
	}
	var parts []string
	if nRelaunch > 0 {
		parts = append(parts, styleYellow.Render(fmt.Sprintf("%d à relancer", nRelaunch)))
	}
	if nRunning > 0 {
		parts = append(parts, styleGreen.Render(fmt.Sprintf("%d déjà en marche", nRunning)))
	}
	if nMissing > 0 {
		parts = append(parts, styleRed.Render(fmt.Sprintf("%d introuvable(s)", nMissing)))
	}

	header := []string{
		styleBold.Render("  🔄 Relancer — " + m.restoreLabel),
		styleDim.Render("  ␣ coche ou décoche, puis ↩ pour relancer."),
		ansi.Truncate("  "+styleCyan.Render(fmt.Sprintf("%d/%d sélectionnées", selected, len(m.restoreEntries)))+
			styleDim.Render("  ·  ")+strings.Join(parts, styleDim.Render(" · ")), max(m.width, 1), ""),
		"",
	}

	// Fenêtrage centré sur le curseur : une capture peut contenir des
	// centaines de stacks.
	n := len(m.restoreEntries)
	listH := max(height-len(header)-2, 3) // 2 = lignes « ··· au-dessus/en-dessous »
	start, end := 0, n
	if n > listH {
		start = min(max(m.restoreCursor-listH/2, 0), n-listH)
		end = start + listH
	}

	lines := header
	if start > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("    ··· %d au-dessus", start)))
	}
	nameW := 0
	for _, e := range m.restoreEntries {
		nameW = max(nameW, lipgloss.Width(e.name))
	}
	nameW = min(nameW, max(m.width-40, 12))
	for i := start; i < end; i++ {
		e := m.restoreEntries[i]
		var check, status string
		switch e.kind {
		case entryMissing:
			check, status = styleDim.Render("[-]"), styleRed.Render("introuvable")
		case entryRunning:
			status = styleGreen.Render("déjà en marche")
		default:
			status = styleYellow.Render("à relancer")
			if len(e.down) > 0 {
				status += styleDim.Render(" · " + truncate(strings.Join(e.down, ", "), max(m.width-nameW-30, 8)))
			}
		}
		if e.kind != entryMissing {
			if m.restoreSel[e.name] {
				check = styleGreen.Render("[x]")
			} else {
				check = styleDim.Render("[ ]")
			}
		}
		name := truncate(e.name, nameW)
		name += strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))
		if i == m.restoreCursor {
			// Sous le curseur : texte brut dans un seul style, statut ajouté hors zone.
			plain := "    [" + map[bool]string{true: "x", false: " "}[m.restoreSel[e.name]] + "] " + name
			if e.kind == entryMissing {
				plain = "    [-] " + name
			}
			lines = append(lines, styleSelected.Render(plain)+"  "+status)
			continue
		}
		lines = append(lines, "    "+check+" "+name+"  "+status)
	}
	if end < n {
		lines = append(lines, styleDim.Render(fmt.Sprintf("    ··· %d en-dessous", n-end)))
	}

	return strings.Join(lines, "\n")
}
