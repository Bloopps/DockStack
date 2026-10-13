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
// warning (optionnel) est affiché sous le titre, en jaune (en rouge pour une
// action qui efface des données).
func (m Model) askConfirm(label, warning string, names []string, exec func(Model) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	return m.openConfirm(label, warning, "", false, names, exec)
}

// openConfirm : askConfirm complet. effect dit ce que fait réellement
// l'action (ligne grise sous le séparateur, cf. actionEffect) ; danger
// réserve la confirmation à y (cf. askConfirmDanger).
func (m Model) openConfirm(label, warning, effect string, danger bool, names []string, exec func(Model) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	m.confirmLabel = label
	m.confirmWarning = warning
	m.confirmEffect = effect
	m.confirmNames = names
	m.confirmScroll = 0
	m.confirmExec = exec
	m.confirmDanger = danger
	// Annuler ramène d'où l'on vient quand ça a du sens (vue backup),
	// sinon à la liste.
	m.confirmReturn = viewList
	if m.view == viewBackup || m.view == viewRestorePick || m.view == viewCleanup {
		m.confirmReturn = m.view
	}
	m.view = viewConfirm
	return m, nil
}

// askConfirmDanger : askConfirm pour une action qui efface des données
// (volumes, conteneurs arrêtés). Seul y confirme : ↩ est la touche qui ouvre
// le panneau d'actions et lance l'entrée choisie, un double appui sur ↩
// suffisait à supprimer les volumes d'une stack.
func (m Model) askConfirmDanger(label, warning string, names []string, exec func(Model) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	return m.openConfirm(label, warning, "", true, names, exec)
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
	return m.openConfirm(label, warning, actionEffect(action, false), false, names, func(m Model) (tea.Model, tea.Cmd) {
		return m.startGroupAction(action, stacks)
	})
}

// actionEffect : ce que fait réellement l'action, en une phrase grise dans sa
// confirmation. Le verbe compose ne le dit pas : down supprime les conteneurs
// mais garde les volumes, et restart ne relit pas le compose (piège
// classique : c'est recreate qui applique une modification). scoped : seuls
// certains services sont visés (stack.Only), down garde alors le réseau,
// que les autres services utilisent encore. "" pour up et pull : la
// prévision ou l'absence de risque en tient lieu.
func actionEffect(action string, scoped bool) string {
	switch action {
	case "stop":
		return "Arrête les conteneurs sans les supprimer (u relance)"
	case "down":
		if scoped {
			return "Arrête et supprime ces conteneurs ; réseau, volumes et images conservés"
		}
		return "Arrête et supprime conteneurs et réseau ; volumes et images conservés"
	case "restart":
		return "Redémarre tel quel, sans relire le compose (c recreate l'applique)"
	case "recreate":
		return "Recrée les conteneurs : applique le compose et l'image locale ; volumes conservés"
	}
	return ""
}

// removeEffect : ligne d'effet de Remove, chiffrée avec la mesure des volumes
// de la stack (stackDisk) quand elle est connue. Les dossiers montés (bind
// mounts) ne sont ni mesurés ni effacés.
func (m Model) removeEffect(stack compose.Stack) string {
	size := "taille inconnue"
	if v, _, known := m.stackDisk(stack); known {
		size = fmtDisk(v)
	}
	return "Volumes effacés : " + size + " (dossiers montés conservés)"
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
// (stop/down/restart/recreate, ou up sur une stack qui tourne) sur une stack
// unique. Le titre la nomme déjà : la ligne d'effet remplace la répétition
// de son nom sous le séparateur.
func (m Model) confirmStackAction(action string, stack compose.Stack) (tea.Model, tea.Cmd) {
	label := actionTitle[action] + " — " + stack.Name
	warning := ""
	if action == "up" {
		warning = upWarning
	}
	return m.openConfirm(label, warning, actionEffect(action, len(stack.Only) > 0), false, nil,
		func(m Model) (tea.Model, tea.Cmd) { return m.startStackAction(action, stack) })
}

// startOrConfirmUp lance un Up unitaire directement si la stack est arrêtée
// (cas nominal, sans risque). Si elle tourne déjà, on prévoit d'abord ce que
// up recréerait : rien de perturbé → direct, sinon confirmation détaillée.
func (m Model) startOrConfirmUp(stack compose.Stack) (tea.Model, tea.Cmd) {
	if anyActive([]compose.Stack{stack}) {
		// Pas de liste de noms : le titre nomme déjà la stack.
		return m.confirmUpWithPlan("Up — "+stack.Name, nil, []compose.Stack{stack},
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

// planStatus : statut affiché (animé, cf. setBusy) pendant la prévision
// d'un up, qui peut durer jusqu'à 20 s : précédé du nom de l'action.
const planStatus = "analyse des changements…"

// planUpMsg : résultat de la prévision des recréations d'un up.
type planUpMsg struct {
	label string
	names []string
	recs  []string // « service · raison », préfixé par la stack si plusieurs
	err   error
	run   func(Model) (tea.Model, tea.Cmd)
	from  view // vue d'où la prévision a été demandée
	seq   int  // planSeq au moment de la demande
	keys  int  // keySeq au moment de la demande
}

// confirmUpWithPlan prévoit (hors boucle UI) quels services en marche un up
// recréerait, puis décide dans Update : rien à recréer → l'action part sans
// confirmation ; sinon la confirmation liste précisément les services et la
// raison. Si la prévision échoue, on retombe sur la confirmation générique.
func (m Model) confirmUpWithPlan(label string, names []string, stacks []compose.Stack, run func(Model) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	client, from, keys := m.client, m.view, m.keySeq
	m.planSeq++
	seq := m.planSeq
	m.setBusy(label + " : " + planStatus)
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var recs []string
		for _, s := range stacks {
			plan, err := client.PlanUp(ctx, s)
			if err != nil {
				return planUpMsg{label: label, names: names, err: err, run: run, from: from, seq: seq, keys: keys}
			}
			for _, r := range plan {
				line := r.Service + " · " + r.Reason
				if len(stacks) > 1 {
					line = s.Name + " / " + line
				}
				recs = append(recs, line)
			}
		}
		return planUpMsg{label: label, names: names, recs: recs, run: run, from: from, seq: seq, keys: keys}
	})
}

// handlePlanUp applique la décision après la prévision (voir confirmUpWithPlan).
func (m Model) handlePlanUp(msg planUpMsg) (tea.Model, tea.Cmd) {
	// Demande remplacée par une plus récente (qui porte son propre statut),
	// ou abandonnée par un changement de vue (statut déjà effacé).
	if msg.seq != m.planSeq {
		return m, nil
	}
	// Seulement l'attente : un statut posé depuis (R, refus…) reste affiché.
	if m.statusKind == statusBusy {
		m.setStatus("", false)
	}
	// Vue changée (même revenue depuis), ou opération lancée entre-temps :
	// la demande est périmée.
	if m.view != msg.from || m.opInProgress() {
		return m, nil
	}
	if msg.err == nil && len(msg.recs) == 0 {
		return msg.run(m) // aucun service en marche n'est perturbé
	}
	// La confirmation arriverait sous la frappe : si l'utilisateur a tapé
	// depuis la demande (filtre, autre stack), son prochain ↩ ou y la
	// validerait sans l'avoir lue, et l'up recréerait des services en
	// marche. On le dit au lieu de l'ouvrir ; u relance la prévision.
	if m.keySeq != msg.keys {
		what := "prévision impossible"
		if msg.err == nil {
			n := len(msg.recs)
			what = fmt.Sprintf("%d service%s en marche à recréer", n, plural(n))
		}
		m.setInfo(msg.label + " non lancé (touche pressée pendant l'analyse) : " + what + " · u pour revoir")
		return m, m.clearStatusIn(6 * time.Second)
	}
	if msg.err != nil {
		return m.askConfirm(msg.label, upWarning, msg.names, msg.run)
	}
	return m.askConfirm(msg.label, "Ces services en marche seront recréés (brève coupure) :", msg.recs, msg.run)
}

func (m Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		if m.confirmDanger && msg.String() == "enter" {
			m.setStatus("Action destructrice : y pour confirmer, esc pour annuler", true)
			return m, m.clearStatusIn(4 * time.Second)
		}
		exec := m.confirmExec
		m.confirmExec = nil
		m.confirmDanger = false
		m.view = viewList
		if exec != nil {
			return exec(m)
		}
	case "n", "esc":
		m.confirmExec = nil
		m.confirmDanger = false
		m.view = m.confirmReturn
	// Défilement de la liste : un alignement, une restauration ou un
	// nettoyage listent des dizaines de lignes qu'il faut pouvoir relire
	// avant de valider, la fin comprise (volumes effacés, stacks arrêtées).
	case "up", "k":
		m.scrollConfirm(-1)
	case "down", "j":
		m.scrollConfirm(1)
	case "pgup":
		m.scrollConfirm(-max(m.confirmArea(m.bodyHeight())-1, 1))
	case "pgdown":
		m.scrollConfirm(max(m.confirmArea(m.bodyHeight())-1, 1))
	case "g", "home":
		m.confirmScroll = 0
	case "G", "end":
		m.confirmScroll = m.confirmMaxScroll()
	}
	return m, nil
}

// scrollConfirm fait défiler la liste de la confirmation, bornée à sa fin.
func (m *Model) scrollConfirm(delta int) {
	m.confirmScroll = min(max(m.confirmScroll+delta, 0), m.confirmMaxScroll())
}

// confirmMaxScroll : défilement maximal de la liste (0 si elle tient).
func (m Model) confirmMaxScroll() int {
	return max(len(m.confirmNames)-m.confirmArea(m.bodyHeight()), 0)
}

// confirmKeys : touches qui confirment (↩ exclue pour une action destructrice).
func (m Model) confirmKeys() string {
	if m.confirmDanger {
		return "y"
	}
	return "y / ↩"
}

// confirmRoom : largeur utile dans le cadre (bordure et marges intérieures :
// 6 colonnes, plus une de marge de chaque côté). Un nom de stack ou de
// service très long débordait de l'écran, et le terminal coupait le cadre.
func (m Model) confirmRoom() int {
	return max(m.width-8, 20)
}

// confirmHead : lignes du cadre au-dessus de la liste (titre, avertissement,
// séparateur, effet). rule est l'index du séparateur (-1 sans séparateur) :
// laissé vide ici, il prend au rendu la largeur de la plus longue ligne.
func (m Model) confirmHead(room int) (rows []string, rule int) {
	title := styleYellow.Bold(true).Render("⚠ Confirmer : ")
	warn := styleYellow
	if m.confirmDanger {
		// Effacement de données : titre, avertissement et cadre en rouge.
		// Down et Remove avaient le même cadre jaune, la gravité ne se
		// lisait pas ; le rouge est réservé à ce cas.
		title = styleRed.Bold(true).Render("✕ Suppression définitive : ")
		warn = styleRed
	}
	rows = []string{title + stylePanelTitle.Render(truncate(m.confirmLabel, max(room-lipgloss.Width(title), 6)))}
	if m.confirmWarning != "" {
		for _, l := range wordWrap(m.confirmWarning, room) {
			rows = append(rows, warn.Render(l))
		}
	}
	// Sans effet ni liste, le séparateur ne séparerait rien.
	if m.confirmEffect == "" && len(m.confirmNames) == 0 {
		return rows, -1
	}
	rule = len(rows)
	rows = append(rows, "")
	if m.confirmEffect != "" {
		for _, l := range wordWrap(m.confirmEffect, room) {
			rows = append(rows, styleDim.Render(l))
		}
	}
	return rows, rule
}

// confirmArea : lignes laissées à la liste dans une vue de height lignes. Le
// cadre doit tenir à l'écran (bordures, blanc et touches : 4 lignes) : sur un
// terminal bas, un minimum fixe de 8 noms poussait la ligne des touches hors
// de l'écran.
func (m Model) confirmArea(height int) int {
	head, _ := m.confirmHead(m.confirmRoom())
	return max(height-4-len(head), 1)
}

// confirmMarks : repères de la liste quand elle déborde, les mêmes que ceux
// des autres listes ; celui du bas rappelle comment lire la suite.
func confirmMarks(above, below int) (string, string) {
	return styleDim.Render(fmt.Sprintf("  ··· %d au-dessus", above)),
		styleDim.Render(fmt.Sprintf("  ··· %d en dessous (↓ pour lire)", below))
}

func renderConfirmView(m Model, height int) string {
	room := m.confirmRoom()
	head, rule := m.confirmHead(room)
	keys := styleGreen.Render(m.confirmKeys()+"  confirmer") + "   " + styleRed.Render("n / esc  annuler")

	names := make([]string, len(m.confirmNames))
	for i, n := range m.confirmNames {
		names[i] = "  " + styleDim.Render(truncate(n, room-2))
	}
	area := m.confirmArea(height)
	from, to, above, below := markedWindow(len(names), m.confirmScroll, area)

	// Séparateur à la largeur de la plus longue ligne du cadre, liste entière
	// et repères compris : une largeur fixe s'arrêtait au milieu des cadres
	// larges, et le cadre ne change pas de largeur en défilant.
	width := lipgloss.Width(keys)
	for _, l := range append(append([]string(nil), head...), names...) {
		width = max(width, lipgloss.Width(l))
	}
	if len(names) > area {
		_, mark := confirmMarks(len(names), len(names))
		width = max(width, lipgloss.Width(mark))
	}

	if rule >= 0 {
		head[rule] = styleDim.Render(strings.Repeat("─", width))
	}
	rows := append(make([]string, 0, len(head)+area+2), head...)
	up, down := confirmMarks(above, below)
	if above > 0 {
		rows = append(rows, up)
	}
	rows = append(rows, names[from:to]...)
	if below > 0 {
		rows = append(rows, down)
	}
	rows = append(rows, "", keys)

	border := activeScheme.Warn
	if m.confirmDanger {
		border = activeScheme.Danger
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 2)

	// Centrée, comme le panneau d'actions qu'elle remplace souvent à l'écran.
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box.Render(strings.Join(rows, "\n")))
}
