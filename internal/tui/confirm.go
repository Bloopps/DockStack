package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/user/dockerstack/internal/compose"
)

// Libellés d'affichage des actions canoniques (ids utilisés par les raccourcis
// clavier et les menus : "up", "down", "restart", "recreate", ...).
var actionTitle = map[string]string{
	"up":       "Up",
	"down":     "Down",
	"restart":  "Restart",
	"recreate": "Recreate",
	"pull":     "Pull",
	"remove":   "Remove",
}

// askConfirm bascule sur la vue de confirmation ; exec n'est lancé que sur y/↩.
// warning (optionnel) est affiché en rouge sous le titre.
func (m Model) askConfirm(label, warning string, names []string, exec func(Model) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	m.confirmLabel = label
	m.confirmWarning = warning
	m.confirmNames = names
	m.confirmExec = exec
	// Annuler ramène d'où l'on vient quand ça a du sens (vue backup),
	// sinon à la liste.
	m.confirmReturn = viewList
	if m.view == viewBackup {
		m.confirmReturn = viewBackup
	}
	m.view = viewConfirm
	return m, nil
}

// confirmGroupAction demande confirmation avant une action groupée
// perturbatrice. La sélection est conservée si l'utilisateur annule.
func (m Model) confirmGroupAction(action string, stacks []compose.Stack) (tea.Model, tea.Cmd) {
	return m.confirmGroupActionWarn(action, "", stacks)
}

func (m Model) confirmGroupActionWarn(action, warning string, stacks []compose.Stack) (tea.Model, tea.Cmd) {
	names := make([]string, len(stacks))
	for i, s := range stacks {
		names[i] = s.Name
	}
	label := fmt.Sprintf("%s — %d stacks", actionTitle[action], len(stacks))
	return m.askConfirm(label, warning, names, func(m Model) (tea.Model, tea.Cmd) {
		return m.startGroupAction(action, stacks)
	})
}

// upWarning explique pourquoi un Up sur du déjà-en-marche se confirme :
// RecreateDiverged peut recréer (couper) les conteneurs dont la config a changé.
const upWarning = "Déjà en marche : les conteneurs dont la config/image a changé seront recréés"

// anyActive dit si au moins une stack tourne (running ou partial).
func anyActive(stacks []compose.Stack) bool {
	for _, s := range stacks {
		if st := s.State(); st == compose.StateRunning || st == compose.StatePartial {
			return true
		}
	}
	return false
}

// confirmStackAction demande confirmation avant une action perturbatrice
// (down/restart/recreate, ou up sur une stack qui tourne) sur une stack unique.
func (m Model) confirmStackAction(action string, stack compose.Stack) (tea.Model, tea.Cmd) {
	label := actionTitle[action] + " — " + stack.Name
	warning := ""
	if action == "up" {
		warning = upWarning
	}
	return m.askConfirm(label, warning, []string{stack.Name},
		func(m Model) (tea.Model, tea.Cmd) { return m.startStackAction(action, stack) })
}

// startOrConfirmUp lance un Up unitaire directement si la stack est arrêtée
// (cas nominal, sans risque) et le confirme si elle tourne déjà.
func (m Model) startOrConfirmUp(stack compose.Stack) (tea.Model, tea.Cmd) {
	if anyActive([]compose.Stack{stack}) {
		return m.confirmStackAction("up", stack)
	}
	return m.startStackAction("up", stack)
}

// startOrConfirmGroupUp : même règle pour un Up groupé.
func (m Model) startOrConfirmGroupUp(stacks []compose.Stack) (tea.Model, tea.Cmd) {
	if anyActive(stacks) {
		return m.confirmGroupActionWarn("up", upWarning, stacks)
	}
	return m.startGroupAction("up", stacks)
}

func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		exec := m.confirmExec
		m.confirmExec = nil
		m.view = viewList
		if exec != nil {
			return exec(m)
		}
	case "n", "esc", "q":
		m.confirmExec = nil
		m.view = m.confirmReturn
	}
	return m, nil
}

const confirmMaxNames = 8

func renderConfirmView(m Model, height int) string {
	inner := panelWidth + 4
	divider := styleDim.Render(strings.Repeat("─", inner))

	rows := []string{
		styleYellow.Bold(true).Render("⚠ Confirmer : ") + stylePanelTitle.Render(m.confirmLabel),
	}
	if m.confirmWarning != "" {
		rows = append(rows, styleRed.Render(m.confirmWarning))
	}
	rows = append(rows, divider)

	names := m.confirmNames
	extra := 0
	if len(names) > confirmMaxNames {
		extra = len(names) - confirmMaxNames
		names = names[:confirmMaxNames]
	}
	for _, n := range names {
		rows = append(rows, "  "+styleDim.Render(n))
	}
	if extra > 0 {
		rows = append(rows, "  "+styleDim.Render(fmt.Sprintf("… et %d autres", extra)))
	}
	rows = append(rows, "", styleGreen.Render("y / ↩  confirmer")+"   "+styleRed.Render("n / esc  annuler"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("3")).
		Padding(0, 2).
		MarginLeft(2).
		MarginTop(1)

	return box.Render(strings.Join(rows, "\n"))
}
