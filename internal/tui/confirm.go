package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Libellés d'affichage des actions canoniques (ids utilisés par les raccourcis
// clavier et les menus : "up", "down", "restart", "recreate", ...).
var actionTitle = map[string]string{
	"up":       "Up",
	"down":     "Down",
	"stop":     "Stop",
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
	if m.view == viewBackup || m.view == viewRestorePick || m.view == viewCleanup {
		m.confirmReturn = m.view
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

// anyActive dit si au moins une stack tourne (running, partial ou unhealthy).
func anyActive(stacks []compose.Stack) bool {
	for _, s := range stacks {
		if st := s.State(); st == compose.StateRunning || st == compose.StatePartial || st == compose.StateUnhealthy {
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
// (cas nominal, sans risque). Si elle tourne déjà, on prévoit d'abord ce que
// up recréerait : rien de perturbé → direct, sinon confirmation détaillée.
func (m Model) startOrConfirmUp(stack compose.Stack) (tea.Model, tea.Cmd) {
	if anyActive([]compose.Stack{stack}) {
		return m.confirmUpWithPlan("Up — "+stack.Name, []string{stack.Name}, []compose.Stack{stack},
			func(m Model) (tea.Model, tea.Cmd) { return m.startStackAction("up", stack) })
	}
	return m.startStackAction("up", stack)
}

// startOrConfirmGroupUp : même règle pour un Up groupé.
func (m Model) startOrConfirmGroupUp(stacks []compose.Stack) (tea.Model, tea.Cmd) {
	if anyActive(stacks) {
		names := make([]string, len(stacks))
		for i, s := range stacks {
			names[i] = s.Name
		}
		return m.confirmUpWithPlan(fmt.Sprintf("Up — %d stacks", len(stacks)), names, stacks,
			func(m Model) (tea.Model, tea.Cmd) { return m.startGroupAction("up", stacks) })
	}
	return m.startGroupAction("up", stacks)
}

// planStatus : statut affiché pendant la prévision d'un up.
const planStatus = "Analyse des changements…"

// planUpMsg : résultat de la prévision des recréations d'un up.
type planUpMsg struct {
	label string
	names []string
	recs  []string // « service · raison », préfixé par la stack si plusieurs
	err   error
	run   func(Model) (tea.Model, tea.Cmd)
	from  view // vue d'où la prévision a été demandée
	seq   int  // planSeq au moment de la demande
}

// confirmUpWithPlan prévoit (hors boucle UI) quels services en marche un up
// recréerait, puis décide dans Update : rien à recréer → l'action part sans
// confirmation ; sinon la confirmation liste précisément les services et la
// raison. Si la prévision échoue, on retombe sur la confirmation générique.
func (m Model) confirmUpWithPlan(label string, names []string, stacks []compose.Stack, run func(Model) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	client, from := m.client, m.view
	m.planSeq++
	seq := m.planSeq
	m.setStatus(planStatus, false)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var recs []string
		for _, s := range stacks {
			plan, err := client.PlanUp(ctx, s)
			if err != nil {
				return planUpMsg{label: label, names: names, err: err, run: run, from: from, seq: seq}
			}
			for _, r := range plan {
				line := r.Service + " · " + r.Reason
				if len(stacks) > 1 {
					line = s.Name + " / " + line
				}
				recs = append(recs, line)
			}
		}
		return planUpMsg{label: label, names: names, recs: recs, run: run, from: from, seq: seq}
	}
}

// handlePlanUp applique la décision après la prévision (voir confirmUpWithPlan).
func (m Model) handlePlanUp(msg planUpMsg) (tea.Model, tea.Cmd) {
	// Demande remplacée par une plus récente (qui porte son propre statut),
	// ou abandonnée par un changement de vue (statut déjà effacé).
	if msg.seq != m.planSeq {
		return m, nil
	}
	m.status = ""
	// Vue changée (même revenue depuis), ou opération lancée entre-temps :
	// la demande est périmée.
	if m.view != msg.from || m.opInProgress() {
		return m, nil
	}
	switch {
	case msg.err != nil:
		return m.askConfirm(msg.label, upWarning, msg.names, msg.run)
	case len(msg.recs) == 0:
		return msg.run(m) // aucun service en marche n'est perturbé
	}
	return m.askConfirm(msg.label, "Ces services en marche seront recréés (brève coupure) :", msg.recs, msg.run)
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
	case "n", "esc":
		m.confirmExec = nil
		m.view = m.confirmReturn
	}
	return m, nil
}

const confirmMaxNames = 8

func renderConfirmView(m Model, height int) string {
	inner := panelWidth + 4
	divider := styleDim.Render(strings.Repeat("─", inner))
	// Largeur utile dans le cadre (bordure, marges intérieures et marge
	// gauche : 8 colonnes) : un nom de stack ou de service très long
	// débordait de l'écran, et le terminal coupait le cadre.
	room := max(m.width-8, 20)

	rows := []string{
		styleYellow.Bold(true).Render("⚠ Confirmer : ") + stylePanelTitle.Render(truncate(m.confirmLabel, max(room-14, 6))),
	}
	if m.confirmWarning != "" {
		for _, l := range wordWrap(m.confirmWarning, room) {
			rows = append(rows, styleRed.Render(l))
		}
	}
	rows = append(rows, divider)

	// Plafond adapté à la hauteur : un alignement ou une restauration liste
	// facilement des dizaines de lignes qu'il faut pouvoir relire avant de valider.
	maxNames := max(confirmMaxNames, height-9)
	names := m.confirmNames
	extra := 0
	if len(names) > maxNames {
		extra = len(names) - maxNames
		names = names[:maxNames]
	}
	for _, n := range names {
		rows = append(rows, "  "+styleDim.Render(truncate(n, room-2)))
	}
	if extra > 0 {
		rows = append(rows, "  "+styleDim.Render(fmt.Sprintf("… et %d autres", extra)))
	}
	rows = append(rows, "", styleGreen.Render("y / ↩  confirmer")+"   "+styleRed.Render("n / esc  annuler"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(activeScheme.Warn).
		Padding(0, 2).
		MarginLeft(2).
		MarginTop(1)

	return box.Render(strings.Join(rows, "\n"))
}
