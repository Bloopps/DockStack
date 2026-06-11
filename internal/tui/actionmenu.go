package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

const panelWidth = 34

var (
	stylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("6")).
			Padding(0, 1)

	stylePanelTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
)

// Per-action styling: icon color, label color when selected
type actionStyle struct {
	icon string
	// color is an ANSI color index (e.g. "2"); v2's lipgloss.Color is a function,
	// so we keep the raw string and convert at use sites.
	color string
}

var stackActionStyles = []actionStyle{
	{"▶", "2"}, // Up       — vert
	{"▪", "1"}, // Down     — rouge
	{"↺", "3"}, // Restart  — jaune
	{"↻", "3"}, // Recreate — jaune
	{"↓", "6"}, // Pull     — cyan
	{"≡", "6"}, // Logs     — cyan
	{"✕", "1"}, // Remove   — rouge
	{"←", "8"}, // Retour   — gris
}

var groupActionStyles = []actionStyle{
	{"▶", "2"},
	{"▪", "1"},
	{"↺", "3"},
	{"↻", "3"},
	{"↓", "6"},
	{"←", "8"},
}

func renderActionMenu(m Model, height int) string {
	sel := m.selectedStacks()
	stack, okStack := m.cursorStack()
	if len(sel) == 0 && !okStack {
		return ""
	}

	// Panneau seul, centré : même panneau pour les deux cibles (sélection si
	// elle existe, sinon la stack sous le curseur). La cible est rappelée
	// dans le titre, afficher la liste assombrie derrière n'était que du
	// bruit visuel.
	var panel string
	if len(sel) > 0 {
		panel = buildGroupPanel(sel, m.actionCursor)
	} else {
		panel = buildActionPanel(stack, m.actionCursor)
	}
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, panel)
}

func buildActionPanel(stack compose.Stack, cursor int) string {
	dot, dotStyle := stateStyle(stack.State())

	title := stylePanelTitle.Render(stack.Name)
	state := dotStyle.Render(dot) + " " + styleDim.Render(stateLabel(stack))

	inner := panelWidth - 4
	divider := styleDim.Render(strings.Repeat("─", inner))

	rows := []string{title, state}
	if len(stack.Services) > 1 {
		rows = append(rows, divider)
		for _, svc := range stack.Services {
			rows = append(rows, renderServiceLine(svc, inner))
		}
	}
	rows = append(rows, divider, "")

	labels := []string{"Up", "Down", "Restart", "Recreate", "Pull", "Logs", "Remove", "Retour"}

	for i, lbl := range labels {
		st := stackActionStyles[i]
		col := lipgloss.NewStyle().Foreground(lipgloss.Color(st.color))
		icon := col.Render(st.icon)

		var line string
		if i == cursor {
			accent := col.Bold(true).Render("▌")
			line = accent + " " + col.Bold(true).Render(fmt.Sprintf("%s  %s", st.icon, lbl))
			pad := inner - lipgloss.Width("  "+st.icon+"  "+lbl)
			if pad > 0 {
				line += strings.Repeat(" ", pad)
			}
		} else {
			line = "  " + icon + "  " + styleDim.Render(lbl)
		}
		rows = append(rows, line)
	}

	return stylePanel.Width(panelWidth).Render(strings.Join(rows, "\n"))
}

// renderServiceLine affiche une ligne « ● nom  x/y » pour un service de la
// stack, le nom étant tronqué pour tenir dans la largeur du panneau.
func renderServiceLine(svc compose.ServiceStatus, width int) string {
	dot, dotStyle := stateStyle(svc.State())
	count := fmt.Sprintf("%d/%d", svc.Running, svc.Total)

	const prefixW = 4 // "  " + dot + " "
	maxName := width - prefixW - lipgloss.Width(count) - 1
	if maxName < 1 {
		maxName = 1
	}
	name := truncate(svc.Name, maxName)

	pad := width - prefixW - lipgloss.Width(name) - lipgloss.Width(count)
	if pad < 1 {
		pad = 1
	}
	return "  " + dotStyle.Render(dot) + " " + styleDim.Render(name) + strings.Repeat(" ", pad) + styleDim.Render(count)
}

func buildGroupPanel(selected []compose.Stack, cursor int) string {
	inner := panelWidth - 4
	divider := styleDim.Render(strings.Repeat("─", inner))

	title := stylePanelTitle.Render(fmt.Sprintf("%d stacks sélectionnées", len(selected)))
	rows := []string{title, divider, ""}

	labels := []string{"Up", "Down", "Restart", "Recreate", "Pull", "Retour"}

	for i, lbl := range labels {
		st := groupActionStyles[i]
		col := lipgloss.NewStyle().Foreground(lipgloss.Color(st.color))
		icon := col.Render(st.icon)

		var line string
		if i == cursor {
			accent := col.Bold(true).Render("▌")
			line = accent + " " + col.Bold(true).Render(fmt.Sprintf("%s  %s", st.icon, lbl))
		} else {
			line = "  " + icon + "  " + styleDim.Render(lbl)
		}
		rows = append(rows, line)
	}

	rows = append(rows, "", divider)
	for _, s := range selected {
		dot, dotStyle := stateStyle(s.State())
		rows = append(rows, "  "+dotStyle.Render(dot)+" "+styleDim.Render(s.Name))
	}

	return stylePanel.Width(panelWidth).Render(strings.Join(rows, "\n"))
}
