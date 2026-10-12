package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

const panelWidth = 34

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
		panel = buildGroupPanel(sel, m.actionCursor, height)
	} else {
		panel = buildActionPanel(stack, m.actionCursor, height)
	}
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, panel)
}

// menuLines rend les entrées d'un panneau d'actions, curseur mis en valeur,
// avec leur raccourci aligné à droite (les mêmes lettres que dans la liste).
func menuLines(actions []menuAction, cursor, inner int) []string {
	lines := make([]string, len(actions))
	for i, st := range actions {
		col := slotStyle(st.color)
		var line string
		if i == cursor {
			line = col.Bold(true).Render("▌") + " " + col.Bold(true).Render(fmt.Sprintf("%s  %s", st.icon, st.label))
		} else {
			line = "  " + col.Render(st.icon) + "  " + styleDim.Render(st.label)
		}
		if st.key != "" {
			pad := max(inner-lipgloss.Width(line)-lipgloss.Width(st.key), 1)
			line += strings.Repeat(" ", pad) + styleCursorMark.Render(st.key)
		}
		lines[i] = line
	}
	return lines
}

// windowLines garde au plus h lignes autour du curseur (terminal très bas).
func windowLines(lines []string, cursor, h int) []string {
	h = max(h, 1)
	if len(lines) <= h {
		return lines
	}
	start := min(max(cursor-h/2, 0), len(lines)-h)
	return lines[start : start+h]
}

// fitPanel assemble un panneau qui tient dans height lignes (bordure
// comprise) : les actions passent avant tout. On retire d'abord les lignes
// décoratives (sep, de la fin vers le début), puis le détail de l'en-tête
// (head sauf son titre), et en dernier recours on fenêtre les actions. extra
// (services, stacks sélectionnées) ne reçoit que la place restante, avec
// « … +N » s'il est coupé ; il est omis s'il ne reste pas 2 lignes.
func fitPanel(head, sep, actions []string, cursor int, extraSep string, extra []string, more func(n int) string, extraFirst bool, height int) []string {
	room := height - 2 // bordure haute et basse
	for len(head)+len(sep)+len(actions) > room && len(sep) > 0 {
		sep = sep[:len(sep)-1]
	}
	if len(head)+len(sep)+len(actions) > room && len(head) > 1 {
		head = head[:1]
	}
	actions = windowLines(actions, cursor, room-len(head)-len(sep))
	rows := append(append(append([]string(nil), head...), sep...), actions...)
	budget := room - len(rows) - 1 // 1 = séparateur avant extra
	if len(extra) == 0 || budget < 2 {
		return rows
	}
	if len(extra) > budget {
		n := len(extra) - (budget - 1)
		extra = append(append([]string(nil), extra[:budget-1]...), more(n))
	}
	if extraFirst {
		// Juste sous l'en-tête : head, séparateur, extra, puis sep et actions.
		nh := len(head)
		out := append([]string(nil), rows[:nh]...)
		out = append(append(out, extraSep), extra...)
		return append(out, rows[nh:]...)
	}
	return append(append(rows, extraSep), extra...)
}

func buildActionPanel(stack compose.Stack, cursor, height int) string {
	dot, dotStyle := stateStyle(stack.State())

	inner := panelWidth - 4
	// Tronqué : un nom plus large que le cadre y était replié sur plusieurs
	// lignes, que fitPanel ne comptait pas (cadre plus haut que l'écran).
	title := stylePanelTitle.Render(truncate(stack.Name, inner))
	state := dotStyle.Render(dot) + " " + styleDim.Render(stateLabel(stack))

	divider := styleDim.Render(strings.Repeat("─", inner))

	// Services plafonnés à la place restante : sans plafond, une stack de
	// quelques services poussait Compose/Remove/Retour hors de l'écran.
	var svcs []string
	if len(stack.Services) > 1 {
		for _, svc := range stack.Services {
			svcs = append(svcs, renderServiceLine(svc, inner))
		}
	}
	rows := fitPanel([]string{title, state}, []string{divider, ""}, menuLines(stackActions, cursor, inner), cursor,
		divider, svcs, func(n int) string { return styleDim.Render(fmt.Sprintf("  … +%d services", n)) }, true, height)
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

func buildGroupPanel(selected []compose.Stack, cursor, height int) string {
	inner := panelWidth - 4
	divider := styleDim.Render(strings.Repeat("─", inner))

	title := stylePanelTitle.Render(fmt.Sprintf("%d stacks sélectionnées", len(selected)))
	names := make([]string, len(selected))
	for i, s := range selected {
		dot, dotStyle := stateStyle(s.State())
		names[i] = "  " + dotStyle.Render(dot) + " " + styleDim.Render(truncate(s.Name, inner-4))
	}
	rows := fitPanel([]string{title}, []string{divider, ""}, menuLines(groupActions, cursor, inner), cursor,
		divider, names, func(n int) string { return styleDim.Render(fmt.Sprintf("  … et %d autres", n)) }, false, height)
	return stylePanel.Width(panelWidth).Render(strings.Join(rows, "\n"))
}
