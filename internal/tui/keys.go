package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// menuAction : une entrée de panneau d'actions. Une SEULE liste sert au rendu
// et à l'exécution : deux listes parallèles avaient divergé (« Compose » ajouté
// à l'une seulement), décalant tout le menu — « Retour » lançait Remove.
type menuAction struct {
	id    string // action canonique ("up", "compose", "back"…)
	icon  string
	label string
	color string // emplacement de couleur du thème (cf. slotStyle)
	key   string // raccourci, le même que dans la liste ("" = aucun : Remove)
}

// Remove n'a volontairement pas de raccourci : détruire les volumes doit
// passer par le panneau, jamais par une lettre tapée par erreur.
var stackActions = []menuAction{
	{"up", "▶", "Up", "green", "u"},
	{"stop", "■", "Stop", "red", "s"},
	{"down", "▪", "Down", "red", "d"},
	{"restart", "↺", "Restart", "yellow", "r"},
	{"recreate", "↻", "Recreate", "yellow", "c"},
	{"pull", "↓", "Pull", "cyan", "p"},
	{"logs", "≡", "Logs", "cyan", "l"},
	{"compose", "▤", "Compose", "cyan", "v"},
	{"remove", "✕", "Remove", "red", ""},
	{"back", "←", "Retour", "muted", "esc"},
}

var groupActions = []menuAction{
	{"up", "▶", "Up", "green", "u"},
	{"stop", "■", "Stop", "red", "s"},
	{"down", "▪", "Down", "red", "d"},
	{"restart", "↺", "Restart", "yellow", "r"},
	{"recreate", "↻", "Recreate", "yellow", "c"},
	{"pull", "↓", "Pull", "cyan", "p"},
	{"back", "←", "Retour", "muted", "esc"},
}

// ---- list ----

func (m Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// En mode filtre, la plupart des touches vont au textinput
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filter.Reset()
			m.filter.Blur()
			m.invalidateRows()
			m.cursor = 0
			return m, nil
		case "enter":
			// Fige le filtre : on sort de la saisie mais la liste reste filtrée
			// (esc depuis la liste l'efface).
			m.filtering = false
			m.filter.Blur()
			return m, nil
		case "up":
			// ↑↓ choisissent parmi les résultats sans quitter la saisie (le
			// champ, d'une ligne, n'en fait rien : ils ramenaient le curseur
			// en tête de liste).
			m.moveCursor(-1)
			return m, nil
		case "down":
			m.moveCursor(1)
			return m, nil
		case "pgup", "pgdown":
			m.pageCursor(msg.String())
			return m, nil
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.invalidateRows()
			m.cursor = 0
			return m, cmd
		}
	}

	// Écran d'erreur (cf. viewContent) : la liste gardée en mémoire n'y est
	// pas affichée. Seules les touches qui ne visent aucune stack agissent
	// (q, o, b, n ; R, t et ? sont globales) : u lançait Up sur la stack
	// cachée sous le curseur, sans confirmation si elle était arrêtée, et ↩
	// ouvrait son panneau. Le mode services y est inerte pour la même raison.
	if m.err != nil && !m.loading {
		switch msg.String() {
		case "q", "ctrl+c", "o", "b", "n":
		default:
			return m, nil
		}
	} else if m.svcFocus {
		return m.handleServiceKey(msg)
	}

	// Le désarmement de la confirmation de sortie par les autres touches est
	// global (handleKey), comme l'intercept ctrl+c.
	switch msg.String() {
	case "q", "ctrl+c":
		// Quitter en deux frappes (style Claude Code) : la première arme,
		// la seconde dans les 2 s quitte.
		return m.armOrQuit()
	case "esc":
		// Efface d'abord le filtre appliqué, puis la sélection.
		if m.filter.Value() != "" {
			m.filter.Reset()
			m.invalidateRows()
			m.cursor = 0
		} else if len(m.selected) > 0 {
			m.selected = make(map[string]bool)
		}
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup", "pgdown":
		m.pageCursor(msg.String())
	case "g", "home":
		m.cursorToEdge(false)
	case "G", "end":
		m.cursorToEdge(true)
	case "left":
		// Replie le groupe courant et pose le curseur sur son en-tête.
		if g, ok := m.cursorGroup(); ok && m.filter.Value() == "" {
			m.foldedGroups[g] = true
			m.invalidateRows()
			m.cursor = m.headerRowIndex(g)
		}
	case "right":
		// Sur une stack : entre dans ses services (le volet est à droite).
		if _, ok := m.cursorStack(); ok {
			return m.enterServices()
		}
		// Sur un en-tête : le déplie, comme ← le replie, sans filtre
		// seulement (la liste ignore alors le repli : le changer ne se
		// verrait qu'une fois le filtre effacé).
		if g, ok := m.cursorGroup(); ok && m.filter.Value() == "" {
			delete(m.foldedGroups, g)
			m.invalidateRows()
		}
	case " ", "space":
		// bubbletea v2 reports the space key as "space" (v1 used " ").
		// Sur un en-tête de groupe : (dé)sélectionne tout le groupe, déplié
		// pour que ses coches se voient (repliées, elles ne seraient pas
		// visées, cf. selectedStacks).
		if row, ok := m.cursorRow(); ok && row.header {
			if row.folded {
				delete(m.foldedGroups, row.group)
				m.invalidateRows()
			}
			m.toggleSelectAll(m.groupStacks(row.group))
			return m, nil
		}
		if stack, ok := m.cursorStack(); ok {
			if m.selected[stack.Name] {
				delete(m.selected, stack.Name)
			} else {
				m.selected[stack.Name] = true
			}
			// Avance jusqu'à la stack suivante (sans s'arrêter sur un
			// en-tête, où espace sélectionnerait tout le groupe).
			rows := m.listRows()
			for c := m.cursor + 1; c < len(rows); c++ {
				if rows[c].isStack() {
					m.cursor = c
					break
				}
			}
		}
	case "ctrl+a":
		// Tout (dé)sélectionner parmi les stacks affichées (filtre et repli
		// respectés).
		m.toggleSelectAll(m.visibleStacks())
	case "enter":
		// Une sélection active prime : ↩ ouvre le panneau d'actions sur la
		// sélection, même depuis un en-tête de groupe (←/→ restent là pour
		// replier). Sans sélection : ↩ sur un en-tête replie/déplie, sur une
		// stack ouvre le panneau.
		if len(m.selectedStacks()) > 0 {
			return m.openActionPanel()
		}
		if row, ok := m.cursorRow(); ok && row.header {
			// Sous un filtre, la liste ignore le repli : ↩ ne changeait rien
			// à l'écran mais repliait le dossier en silence, et une stack
			// cochée pendant le filtre disparaissait en l'effaçant.
			if m.filter.Value() != "" {
				return m, nil
			}
			if m.foldedGroups[row.group] {
				delete(m.foldedGroups, row.group)
			} else {
				m.foldedGroups[row.group] = true
			}
			m.invalidateRows()
			return m, nil
		}
		return m.openActionPanel()
	case "u", "s", "d", "r", "c":
		// Raccourcis directs calqués sur les noms compose (up/stop/down/
		// restart/recreate), les mêmes dans la liste, le mode services et le
		// panneau d'actions : agissent sur la sélection si elle existe, sinon
		// sur le groupe (si le curseur est sur un en-tête), sinon sur la stack
		// sous le curseur. Ce qui perturbe un service passe par la confirmation.
		action := map[string]string{"u": "up", "s": "stop", "d": "down", "r": "restart", "c": "recreate"}[msg.String()]
		if sel := m.selection(); sel.shown == 0 && sel.hidden > 0 {
			return m.refuseHiddenSelection(sel)
		}
		if sel := m.selectedStacks(); len(sel) > 0 {
			if action == "up" {
				return m.startOrConfirmGroupUp(sel)
			}
			return m.confirmGroupAction(action, sel)
		}
		if row, ok := m.cursorRow(); ok && row.header {
			members := m.groupStacks(row.group)
			if action == "up" {
				return m.startOrConfirmGroupUp(members)
			}
			return m.confirmGroupAction(action, members)
		}
		if stack, ok := m.cursorStack(); ok {
			// Up direct seulement sur une stack arrêtée ; tout ce qui peut
			// perturber un service en marche se confirme.
			if action == "up" {
				return m.startOrConfirmUp(stack)
			}
			return m.confirmStackAction(action, stack)
		}
	case "p":
		if sel := m.selection(); sel.shown == 0 && sel.hidden > 0 {
			return m.refuseHiddenSelection(sel)
		}
		if sel := m.selectedStacks(); len(sel) > 0 {
			return m.startGroupAction("pull", sel)
		}
		if row, ok := m.cursorRow(); ok && row.header {
			return m.startGroupAction("pull", m.groupStacks(row.group))
		}
		if stack, ok := m.cursorStack(); ok {
			return m.startStackAction("pull", stack)
		}
	case "l":
		if stack, ok := m.cursorStack(); ok {
			return m.startStackAction("logs", stack)
		}
	case "v":
		// Fichier compose en plein écran (le volet de droite, lui, est
		// toujours affiché quand la largeur le permet).
		if s, ok := m.cursorStack(); ok {
			return m.openComposeView(s)
		}
		m.setInfo("Compose : viser une stack, pas un dossier")
		return m, m.clearStatusIn(3 * time.Second)
	case "tab":
		// Volet ouvert : bascule détail de la stack ↔ fichier compose.
		if m.previewVisible() {
			m.previewCompose = !m.previewCompose
		}
	case "a":
		return m.toggleInactive()
	case "x":
		return m.confirmExclude()
	case "b":
		m.view = viewBackup
		m.captureSeen = true // le bandeau « stacks à relancer » a fait son office
		m.actionCursor = 0
		// La liste est relue à l'ouverture : c enregistre m.stacks, qui
		// pouvait dater d'avant une coupure du flux d'événements.
		reload := m.refreshOrMark()
		return m, tea.Batch(loadBackups(m.cfg.ConfigDir()), reload)
	case "n":
		return m.openCleanup()
	case "o":
		start := m.cfg.StackDir
		if start == "" {
			start = pickerStart()
		}
		m.view = viewDirPicker
		return m, loadDirCmd(start)
	}
	return m, nil
}

// pageCursor déplace le curseur de la liste d'une page (pgup, pgdown), pas
// à pas : moveCursor saute les lignes que ↑↓ sautent, et s'arrête aux bords.
// Une ligne de moins pour les titres des colonnes.
func (m *Model) pageCursor(key string) {
	d := 1
	if key == "pgup" {
		d = -1
	}
	for range page(m.bodyHeight() - 1) {
		m.moveCursor(d)
	}
}

// toggleInactive affiche/masque les stacks qui ne tournent pas (arrêtées ou
// jamais déployées ; choix mémorisé), touche a. Si le curseur était sur la
// ligne « N masquées » (liste sans aucune stack en marche), il se pose sur la
// première d'entre elles : c'est pour en démarrer une qu'on les affiche.
func (m Model) toggleInactive() (tea.Model, tea.Cmd) {
	anchor := m.cursorAnchor()
	fromMore := false
	if row, ok := m.cursorRow(); ok && row.more {
		fromMore = true
	}
	m.cfg.ShowAll = !m.cfg.ShowAll
	m.invalidateRows()
	m.seekCursor(anchor)
	if fromMore {
		for i, r := range m.listRows() {
			if r.isStack() && dormant(r.stack) {
				m.cursor = i
				break
			}
		}
	}
	if err := m.cfg.Save(); err != nil {
		m.setStatus("Config non sauvegardée : "+err.Error(), true)
		return m, m.clearStatusIn(6 * time.Second)
	}
	return m, nil
}

// confirmExclude propose d'exclure du scan la stack ou le dossier sous le
// curseur (dépôts de dev, exemples…). Réversible via config.json.
func (m Model) confirmExclude() (tea.Model, tea.Cmd) {
	row, ok := m.cursorRow()
	if !ok || row.sep || row.more {
		return m, nil
	}
	target := row.stack.Name
	if row.header {
		target = row.group
	}
	// Lister TOUT ce que l'exclusion retire : elle vaut pour le chemin et tout
	// ce qui est en dessous (sous-stacks, stacks masquées), pas
	// seulement la ligne visée.
	probe := config.Config{Exclude: []string{target}}
	var names []string
	for _, s := range m.stacks {
		if probe.IsExcluded(s.Name) {
			names = append(names, s.Name)
		}
	}
	return m.askConfirm("Exclure du scan — "+target,
		"Réversible : retirer « "+target+" » de exclude dans ~/.dockstack/config.json",
		names,
		func(m Model) (tea.Model, tea.Cmd) {
			m.cfg.Exclude = append(m.cfg.Exclude, target)
			kept := m.stacks[:0:0]
			for _, s := range m.stacks {
				if m.cfg.IsExcluded(s.Name) {
					delete(m.selected, s.Name)
					continue
				}
				kept = append(kept, s)
			}
			m.stacks = kept
			m.invalidateRows()
			m.clampCursor()
			if err := m.cfg.Save(); err != nil {
				m.setStatus("Exclu pour cette session seulement : "+err.Error(), true)
			} else {
				m.setStatus("Exclu du scan : "+target, false)
			}
			return m, m.clearStatusIn(4 * time.Second)
		})
}

// ---- action menu (stack courante ou sélection) ----

// openActionPanel ouvre le panneau d'actions sur la sélection visible, ou à
// défaut sur la stack sous le curseur, et fige cette cible par noms (cf.
// actionTarget). Rien ne s'ouvre sur un en-tête sans sélection.
func (m Model) openActionPanel() (tea.Model, tea.Cmd) {
	if sel := m.selectedStacks(); len(sel) > 0 {
		m.actionGroup, m.actionTarget = true, make([]string, len(sel))
		for i, s := range sel {
			m.actionTarget[i] = s.Name
		}
	} else if s, ok := m.cursorStack(); ok {
		m.actionGroup, m.actionTarget = false, []string{s.Name}
	} else {
		return m, nil
	}
	m.view = viewAction
	m.actionCursor = 0
	return m, nil
}

// actionStacks : stacks visées par le panneau, relues par nom dans la liste
// courante (leur état est à jour), dans l'ordre de l'ouverture. Celles qui
// ont disparu depuis (dossier supprimé) sont omises.
func (m Model) actionStacks() []compose.Stack {
	by := make(map[string]compose.Stack, len(m.stacks))
	for _, s := range m.stacks {
		by[s.Name] = s
	}
	var out []compose.Stack
	for _, n := range m.actionTarget {
		if s, ok := by[n]; ok {
			out = append(out, s)
		}
	}
	return out
}

// actionLabels renvoie les entrées du panneau d'actions selon sa cible : la
// sélection (actions groupées) ou une seule stack.
func (m Model) actionLabels() []menuAction {
	if m.actionGroup {
		return groupActions
	}
	return stackActions
}

func (m Model) handleActionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.actionCursor > 0 {
			m.actionCursor--
		}
	case "down", "j":
		if m.actionCursor < len(m.actionLabels())-1 {
			m.actionCursor++
		}
	case "esc":
		m.view = viewList
	case "g", "home":
		m.actionCursor = 0
	case "G", "end":
		m.actionCursor = len(m.actionLabels()) - 1
	case "enter":
		return m.execAction()
	default:
		// Les lettres de la liste marchent aussi dans le panneau (affichées à
		// côté de chaque entrée) : y apprendre d puis devoir descendre avec
		// les flèches jusqu'à Down n'avait pas de sens.
		for i, a := range m.actionLabels() {
			if a.key != "" && a.key != "esc" && a.key == msg.String() {
				m.actionCursor = i
				return m.execAction()
			}
		}
	}
	return m, nil
}

// execAction lance l'entrée choisie du panneau sur sa cible figée. Ni le
// curseur ni la sélection ne sont relus : une relecture de la liste arrivée
// panneau ouvert peut les avoir changés (stack devenue masquée), et la
// lettre partait alors sur une autre stack, sans confirmation pour p.
func (m Model) execAction() (tea.Model, tea.Cmd) {
	targets := m.actionStacks()
	if len(targets) == 0 {
		m.view = viewList
		return m, nil
	}
	if m.actionGroup {
		return m.execGroupAction(targets)
	}
	return m.execStackAction(targets[0])
}

func (m Model) execStackAction(stack compose.Stack) (tea.Model, tea.Cmd) {
	switch stackActions[m.actionCursor].id {
	case "up":
		return m.startOrConfirmUp(stack)
	case "stop", "down", "restart", "recreate":
		return m.confirmStackAction(stackActions[m.actionCursor].id, stack)
	case "pull", "logs":
		return m.startStackAction(stackActions[m.actionCursor].id, stack)
	case "compose":
		return m.openComposeView(stack)
	case "remove":
		// Destructif : confirmation obligatoire, et Remove supprime aussi
		// les volumes (down --volumes --remove-orphans) : l'effet chiffre ce
		// qui sera effacé.
		return m.openConfirm("Remove — "+stack.Name,
			"Supprime conteneurs, réseaux ET volumes (données incluses)",
			m.removeEffect(stack), true, nil,
			func(m Model) (tea.Model, tea.Cmd) { return m.startStackAction("remove", stack) })
	}
	// "back" (Retour) : retour à la liste.
	m.view = viewList
	return m, nil
}

// toggleSelectAll sélectionne toutes les stacks données, ou les désélectionne
// si elles l'étaient déjà toutes.
func (m *Model) toggleSelectAll(stacks []compose.Stack) {
	all := len(stacks) > 0
	for _, s := range stacks {
		if !m.selected[s.Name] {
			all = false
			break
		}
	}
	for _, s := range stacks {
		if all {
			delete(m.selected, s.Name)
		} else {
			m.selected[s.Name] = true
		}
	}
}

// startStackAction lance une action canonique ("up", "down", "restart",
// "recreate", "pull", "logs", "remove") sur une seule stack. Les quatre
// premières passent par la vue de progression live, Pull/Remove par le
// spinner du footer.
func (m Model) startStackAction(action string, stack compose.Stack) (tea.Model, tea.Cmd) {
	// Verrou d'opération : la liste reste interactive pendant une capture/
	// restauration (spinner), rien n'empêcherait de lancer une op concurrente
	// sur les mêmes stacks. Logs inclus : la session de logs partage la vue
	// et son cycle de vie avec le reste.
	if m.opInProgress() {
		return m.refuseOp()
	}
	client := m.client
	one := []compose.Stack{stack}

	switch action {
	case "up":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Up: "+stackLabel(stack), cancel, client.UpManyLive(ctx, one, 1))
	case "down":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Down: "+stackLabel(stack), cancel, client.DownManyLive(ctx, one, 1))
	case "stop":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Stop: "+stackLabel(stack), cancel, client.StopManyLive(ctx, one, 1))
	case "restart":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Restart: "+stackLabel(stack), cancel, client.RestartManyLive(ctx, one, 1))
	case "recreate":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Recreate: "+stackLabel(stack), cancel, client.RecreateManyLive(ctx, one, 1))
	case "pull":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Pull: "+stackLabel(stack), cancel, client.PullManyLive(ctx, one, 1))
	case "logs":
		m.view = viewLogs
		m.resetLogSession(stack)
		m.logSeq++ // nouvelle session : invalide les messages en vol de la précédente
		// Contexte annulable : fermer la vue arrête le suivi des logs (sans
		// lui, le flux Follow continuerait en arrière-plan indéfiniment).
		ctx, cancel := context.WithCancel(context.Background())
		m.logCancel = cancel
		return m, startLogs(ctx, client, stack, m.logSeq)
	case "remove":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Remove: "+stackLabel(stack), cancel, client.RemoveManyLive(ctx, one, 1))
	}
	m.view = viewList
	return m, nil
}

// ---- group action ----

func (m Model) execGroupAction(stacks []compose.Stack) (tea.Model, tea.Cmd) {
	switch id := groupActions[m.actionCursor].id; id {
	case "up":
		return m.startOrConfirmGroupUp(stacks)
	case "stop", "down", "restart", "recreate":
		return m.confirmGroupAction(id, stacks)
	case "pull":
		return m.startGroupAction("pull", stacks)
	}
	// "back" (Retour) : retour à la liste.
	m.view = viewList
	return m, nil
}

// startGroupAction lance une action live sur plusieurs stacks. Les stacks
// visées ne sont décochées qu'ici, donc annuler la confirmation conserve la
// sélection.
func (m Model) startGroupAction(action string, stacks []compose.Stack) (tea.Model, tea.Cmd) {
	// Verrou d'opération (cf. startStackAction), avant de vider la sélection :
	// un refus la conserve, comme une annulation de confirmation.
	if m.opInProgress() {
		return m.refuseOp()
	}
	client := m.client
	par := m.cfg.MaxParallel
	// Seules les stacks visées sont décochées : une stack cochée puis
	// masquée (filtre, a, repli) n'a pas été touchée, elle reste cochée.
	for _, s := range stacks {
		delete(m.selected, s.Name)
	}
	title := fmt.Sprintf("%s — %d stacks", actionTitle[action], len(stacks))
	ctx, cancel := context.WithCancel(context.Background())

	switch action {
	case "up":
		return m.startProgressOp(title, cancel, client.UpManyLive(ctx, stacks, par))
	case "down":
		return m.startProgressOp(title, cancel, client.DownManyLive(ctx, stacks, par))
	case "stop":
		return m.startProgressOp(title, cancel, client.StopManyLive(ctx, stacks, par))
	case "restart":
		return m.startProgressOp(title, cancel, client.RestartManyLive(ctx, stacks, par))
	case "recreate":
		return m.startProgressOp(title, cancel, client.RecreateManyLive(ctx, stacks, par))
	case "pull":
		return m.startProgressOp(title, cancel, client.PullManyLive(ctx, stacks, par))
	}
	cancel()
	m.view = viewList
	return m, nil
}

// ---- logs ----

func (m Model) handleLogsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// Saisie du filtre, sur le modèle de celui des services : chaque lettre
	// refiltre aussitôt, ↩ fige le filtre, esc l'efface. ↑↓ font défiler,
	// pour parcourir les lignes retenues sans quitter la saisie.
	if m.logFiltering {
		switch key {
		case "esc":
			m.logFiltering = false
			m.setLogFilter("")
		case "enter":
			m.logFiltering = false
		case "backspace":
			if r := []rune(m.logFilter); len(r) > 0 {
				m.setLogFilter(string(r[:len(r)-1]))
			}
		case "up", "down", "pgup", "pgdown":
			m.scrollLogs(key)
		default:
			if t := msg.Key().Text; t != "" {
				m.setLogFilter(m.logFilter + t)
			}
		}
		return m, nil
	}
	switch key {
	case "esc":
		// Le filtre s'efface d'abord, comme dans la liste et les services.
		if m.logFilter != "" {
			m.setLogFilter("")
			return m, nil
		}
		if m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
		}
		m.logSeq++ // périme la session : stoppe la pompe de lecture
		m.logCh = nil
		m.view = viewList
	case "/":
		m.logFiltering = true
	case "w":
		// Replié ou tronqué, la ligne du haut reste la même : logScroll compte
		// des lignes de log, pas des lignes d'écran.
		m.logWrap = !m.logWrap
		m.syncLogScroll()
	default:
		m.scrollLogs(key)
	}
	return m, nil
}

// ---- compose (vue plein écran) ----

func (m Model) handleComposeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewList
	case "up", "k":
		if m.composeScroll > 0 {
			m.composeScroll--
		}
	case "down", "j":
		if m.composeScroll < m.composeMaxScroll() {
			m.composeScroll++
		}
	case "pgup":
		m.composeScroll = max(m.composeScroll-page(m.composeHeight()), 0)
	case "pgdown":
		m.composeScroll = min(m.composeScroll+page(m.composeHeight()), m.composeMaxScroll())
	case "g", "home":
		m.composeScroll = 0
	case "G", "end":
		m.composeScroll = m.composeMaxScroll()
	case "tab":
		// Bascule brut ↔ résolu et recharge ; le curseur repart en haut car le
		// contenu (donc le nombre de lignes) change.
		m.composeResolved = !m.composeResolved
		m.composeScroll = 0
		m.loadComposeContent()
	case "m":
		// Relâche/reprend la capture souris : relâchée, la sélection/copie
		// native du terminal fonctionne ; captée, la molette défile.
		m.composeMouseOff = !m.composeMouseOff
	}
	return m, nil
}

// ---- backup ----

// Dans la vue des captures, actionCursor indexe directement m.backups.
func (m Model) handleBackupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.captureLabeling {
		return m.handleCaptureLabelKey(msg)
	}
	var snap backup.Snapshot
	hasSnap := m.actionCursor >= 0 && m.actionCursor < len(m.backups)
	if hasSnap {
		snap = m.backups[m.actionCursor]
	}
	dir := m.cfg.ConfigDir()

	switch msg.String() {
	case "up", "k":
		if m.actionCursor > 0 {
			m.actionCursor--
		}
	case "down", "j":
		if m.actionCursor < len(m.backups)-1 {
			m.actionCursor++
		}
	case "pgup":
		// Titre, ligne vide et bordures du cadre autour de la liste (cf.
		// renderBackupView).
		m.actionCursor = max(m.actionCursor-page(m.bodyHeight()-4), 0)
	case "pgdown":
		m.actionCursor = max(min(m.actionCursor+page(m.bodyHeight()-4), len(m.backups)-1), 0)
	case "esc":
		m.view = viewList
	case "c":
		// État inconnu (liste pas encore chargée, Docker injoignable) : la
		// capture serait vide et écraserait active_stacks, lu par les scripts.
		if m.lastRefresh.IsZero() || m.err != nil {
			m.setStatus("État des stacks inconnu (liste non chargée ou Docker injoignable) : capture impossible", true)
			return m, m.clearStatusIn(5 * time.Second)
		}
		// Liste en cours de relecture (ouverture de la vue, événement Docker
		// reçu) : la capture enregistrerait l'état d'avant.
		if m.listDirty || m.refreshing || m.refreshPending {
			m.setInfo("Liste en cours d'actualisation : c dans un instant")
			return m, m.clearStatusIn(3 * time.Second)
		}
		// On reste sur la vue : la nouvelle capture doit y apparaître,
		// sinon rien ne prouve à l'utilisateur qu'elle a été faite.
		// startOp arme aussi spinner.Tick, sans quoi le spinner reste figé.
		return m.startOp("Capture de l'état", saveBackup(dir, m.cfg.StackDir, m.stacks))
	case "n":
		if hasSnap {
			m.captureLabeling, m.captureLabel = true, snap.Label
		}
	case "g", "home":
		m.actionCursor = 0
	case "G", "end":
		m.actionCursor = max(len(m.backups)-1, 0)
	case "*":
		// Épingler : * (p reste Pull partout ailleurs).
		if hasSnap {
			done := "Capture épinglée : elle ne sera jamais supprimée par la rotation"
			if snap.Pinned {
				done = "Capture désépinglée"
			}
			return m, setBackupMeta(dir, snap, snap.Label, !snap.Pinned, done)
		}
	case "i":
		// À l'identique : toujours confirmé (liste de ce qui démarre et
		// s'arrête).
		if hasSnap {
			return m.confirmAlign(snap)
		}
	case "delete", "backspace":
		// backspace : la touche « delete » des claviers Mac envoie un retour
		// arrière, sans elle la suppression y était impossible.
		if !hasSnap {
			return m, nil
		}
		warning := ""
		if snap.Pinned {
			warning = "Cette capture est épinglée."
		}
		return m.askConfirm("Supprimer la capture du "+snap.Date.Format("02/01/2006 15:04"),
			warning, snap.Stacks,
			func(m Model) (tea.Model, tea.Cmd) {
				m.view = viewBackup
				return m, deleteBackup(dir, snap)
			})
	case "enter":
		return m.openRestorePick()
	}
	return m, nil
}

// handleCaptureLabelKey : saisie du libellé d'une capture.
func (m Model) handleCaptureLabelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc":
		m.captureLabeling = false
	case "enter":
		m.captureLabeling = false
		if m.actionCursor < 0 || m.actionCursor >= len(m.backups) {
			return m, nil
		}
		snap := m.backups[m.actionCursor]
		done := "Capture nommée"
		if strings.TrimSpace(m.captureLabel) == "" {
			done = "Nom de la capture retiré"
		}
		return m, setBackupMeta(m.cfg.ConfigDir(), snap, m.captureLabel, snap.Pinned, done)
	case "backspace":
		if r := []rune(m.captureLabel); len(r) > 0 {
			m.captureLabel = string(r[:len(r)-1])
		}
	case "ctrl+c":
		return m.armOrQuit()
	default:
		if t := msg.Key().Text; t != "" && len([]rune(m.captureLabel)) < 60 {
			m.captureLabel += t
		}
	}
	return m, nil
}

// confirmAlign prépare l'alignement de l'état actuel sur une capture : démarrer
// ce qui manque et ARRÊTER (stop, conteneurs conservés) ce qui tourne sans
// figurer dans la capture. Tout est listé dans la confirmation.
func (m Model) confirmAlign(snap backup.Snapshot) (tea.Model, tea.Cmd) {
	if m.opInProgress() {
		return m.refuseOp()
	}
	if snap.StackDir != "" && snap.StackDir != m.cfg.StackDir {
		m.setStatus("Capture prise depuis "+snap.StackDir+" : alignement impossible depuis ce répertoire", true)
		return m, m.clearStatusIn(5 * time.Second)
	}
	plan := planAlign(snap.Items(), m.stacks)
	if plan.empty() {
		m.setStatus("Déjà aligné sur cette capture", false)
		return m, m.clearStatusIn(3 * time.Second)
	}

	var lines []string
	for _, e := range plan.relaunch {
		line := "▶ démarrer " + e.name
		if len(e.down) > 0 {
			line += " (" + strings.Join(e.down, ", ") + ")"
		}
		lines = append(lines, line)
	}
	for _, s := range plan.stopStacks {
		lines = append(lines, "■ arrêter "+s.Name)
	}
	for _, s := range plan.stopServices {
		lines = append(lines, "■ arrêter "+strings.Join(s.Only, ", ")+" de "+s.Name)
	}
	warning := ""
	if nStop := len(plan.stopStacks) + len(plan.stopServices); nStop > 0 {
		warning = fmt.Sprintf("%d arrêt%s : ce qui tourne mais n'est pas dans la capture sera arrêté (conteneurs conservés).", nStop, plural(nStop))
	}

	ups, _ := resolveRestoreStacks(entriesOf(plan.relaunch, snap), m.cfg.StackDir)
	stopStacks, stopServices, parallel := plan.stopStacks, plan.stopServices, m.cfg.MaxParallel
	title := fmt.Sprintf("Alignement — %d à démarrer, %d à arrêter", len(ups), len(stopStacks)+len(stopServices))
	run := func(m Model) (tea.Model, tea.Cmd) {
		ctx, cancel := context.WithCancel(context.Background())
		client := m.client
		var phases []livePhase
		if len(stopStacks) > 0 {
			phases = append(phases, livePhase{" — arrêt", func() <-chan compose.ProgressEvent { return client.StopManyLive(ctx, stopStacks, parallel) }})
		}
		if len(stopServices) > 0 {
			phases = append(phases, livePhase{" — arrêt de services", func() <-chan compose.ProgressEvent { return client.StopManyLive(ctx, stopServices, parallel) }})
		}
		if len(ups) > 0 {
			phases = append(phases, livePhase{"", func() <-chan compose.ProgressEvent { return client.UpManyLive(ctx, ups, parallel) }})
		}
		return m.startProgressOp(title, cancel, chainLive(ctx, phases))
	}
	return m.askConfirm("Mettre à l'identique de la capture du "+snap.Date.Format("02/01/2006 15:04"), warning, lines, run)
}

// entriesOf retrouve, dans la capture, les entrées (avec leurs services) des
// stacks à relancer.
func entriesOf(rel []captureEntry, snap backup.Snapshot) []backup.Entry {
	byName := make(map[string]backup.Entry, len(snap.Stacks))
	for _, it := range snap.Items() {
		byName[it.Name] = it
	}
	out := make([]backup.Entry, 0, len(rel))
	for _, e := range rel {
		out = append(out, byName[e.name])
	}
	return out
}

func (m Model) openRestorePick() (tea.Model, tea.Cmd) {
	if m.actionCursor >= len(m.backups) {
		return m, nil
	}
	// Ouvre le sélecteur de restauration. Seules les stacks à relancer sont
	// cochées : celles déjà en marche (un up peut les recréer) et les
	// introuvables ne le sont pas.
	snap := m.backups[m.actionCursor]
	// Comme l'alignement : les noms d'une capture prise depuis un autre
	// répertoire désigneraient d'autres stacks (ou aucune) ici.
	if snap.StackDir != "" && snap.StackDir != m.cfg.StackDir {
		m.setStatus("Capture prise depuis "+snap.StackDir+" : relance impossible depuis ce répertoire", true)
		return m, m.clearStatusIn(5 * time.Second)
	}
	m.view = viewRestorePick
	m.restoreLabel = fmt.Sprintf("Capture du %s", snap.Date.Format("02/01/2006 15:04"))
	if snap.Label != "" {
		m.restoreLabel += " · " + snap.Label
	}
	m.restoreEntries = diffCapture(snap.Items(), m.stacks)
	m.restoreSel = make(map[string]bool, len(m.restoreEntries))
	for _, e := range m.restoreEntries {
		if e.kind == entryRelaunch {
			m.restoreSel[e.name] = true
		}
	}
	m.restoreCursor = 0
	return m, nil
}

// ---- restore picker ----

func (m Model) handleRestorePickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.restoreEntries)
	switch msg.String() {
	case "up", "k":
		if m.restoreCursor > 0 {
			m.restoreCursor--
		}
	case "down", "j":
		if m.restoreCursor < n-1 {
			m.restoreCursor++
		}
	case "pgup":
		// En-tête de 4 lignes et repères au-dessus et en dessous (cf.
		// renderRestorePick).
		m.restoreCursor = max(m.restoreCursor-page(m.bodyHeight()-6), 0)
	case "pgdown":
		m.restoreCursor = max(min(m.restoreCursor+page(m.bodyHeight()-6), n-1), 0)
	case "g", "home":
		m.restoreCursor = 0
	case "G", "end":
		m.restoreCursor = max(n-1, 0)
	case " ", "space":
		if m.restoreCursor < n {
			e := m.restoreEntries[m.restoreCursor]
			if e.kind != entryMissing { // rien à relancer si la stack n'existe plus
				m.restoreSel[e.name] = !m.restoreSel[e.name]
			}
			if m.restoreCursor < n-1 {
				m.restoreCursor++
			}
		}
	case "ctrl+a":
		all := true
		for _, e := range m.restoreEntries {
			if e.kind != entryMissing && !m.restoreSel[e.name] {
				all = false
				break
			}
		}
		for _, e := range m.restoreEntries {
			if e.kind != entryMissing {
				m.restoreSel[e.name] = !all
			}
		}
	case "esc":
		m.view = viewBackup
	case "enter":
		var items []backup.Entry
		anyRunning := false
		for _, e := range m.restoreEntries {
			if m.restoreSel[e.name] {
				items = append(items, backup.Entry{Name: e.name, Services: e.services})
				// Une stack « à relancer » peut être partielle : ses conteneurs
				// en marche peuvent être recréés par le up, on prévoit aussi.
				anyRunning = anyRunning || e.kind == entryRunning || e.stack.Running > 0
			}
		}
		if len(items) == 0 {
			m.setInfo("Aucune stack cochée : ␣ pour cocher")
			return m, m.clearStatusIn(3 * time.Second)
		}
		if m.opInProgress() {
			return m.refuseOp()
		}
		// Comme les autres actions, la restauration passe par la vue de
		// progression : flux live, annulable (esc), au lieu d'un Up bloquant
		// sur ctx.Background().
		stacks, missing := resolveRestoreStacks(items, m.cfg.StackDir)
		if len(stacks) == 0 {
			m.setStatus(fmt.Sprintf("Aucune stack relançable (%d introuvable%s sur le disque)", len(missing), plural(len(missing))), true)
			return m, m.clearStatusIn(5 * time.Second)
		}
		title := fmt.Sprintf("Relance — %d stack%s", len(stacks), plural(len(stacks)))
		if len(missing) > 0 {
			// Signaler les introuvables au lieu de les ignorer en silence.
			title += fmt.Sprintf(" (%d introuvable%s ignorée%s)", len(missing), plural(len(missing)), plural(len(missing)))
		}
		parallel := m.cfg.MaxParallel
		run := func(m Model) (tea.Model, tea.Cmd) {
			ctx, cancel := context.WithCancel(context.Background())
			return m.startProgressOp(title, cancel, m.client.UpManyLive(ctx, stacks, parallel))
		}
		if anyRunning {
			// Une stack déjà en marche est cochée : on prévoit ce qu'un up
			// recréerait avant de lancer.
			names := make([]string, len(stacks))
			for i, s := range stacks {
				names[i] = s.Name
			}
			return m.confirmUpWithPlan(title, names, stacks, run)
		}
		return run(m)
	}
	return m, nil
}

// ---- dir picker ----

func (m Model) handleDirKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.dirCursor > 0 {
			m.dirCursor--
		}
	case "down", "j":
		if m.dirCursor < len(m.dirEntries)-1 {
			m.dirCursor++
		}
	case "pgup":
		m.dirCursor = max(m.dirCursor-page(m.dirRowsH()), 0)
	case "pgdown":
		m.dirCursor = max(min(m.dirCursor+page(m.dirRowsH()), len(m.dirEntries)-1), 0)
	case "g", "home":
		m.dirCursor = 0
	case "G", "end":
		m.dirCursor = max(len(m.dirEntries)-1, 0)
	case "left", "backspace":
		// Dossier parent, curseur posé sur celui d'où l'on sort.
		if m.dirPath != "" && m.dirPath != "/" {
			return m, loadDirFocus(filepath.Dir(m.dirPath), filepath.Base(m.dirPath)+"/")
		}
	case "right":
		// Ouvre le sous-dossier sous le curseur (jamais de choix : ↩ seul
		// choisit, sur « Choisir ce répertoire »).
		if m.dirCursor < len(m.dirEntries) {
			switch entry := m.dirEntries[m.dirCursor]; entry {
			case dirChooseEntry:
			case "../":
				return m, loadDirFocus(filepath.Dir(m.dirPath), filepath.Base(m.dirPath)+"/")
			default:
				return m, loadDirCmd(filepath.Join(m.dirPath, strings.TrimSuffix(entry, "/")))
			}
		}
	case "esc":
		// Premier lancement : aucune liste où revenir ; q (global) quitte.
		if m.cfg.StackDir != "" {
			m.view = viewList
		}
	case "enter":
		if len(m.dirEntries) == 0 {
			return m, nil
		}
		entry := m.dirEntries[m.dirCursor]
		switch entry {
		case dirChooseEntry:
			if m.dirPath != m.cfg.StackDir {
				// Les stacks affichées et la sélection (par nom relatif)
				// désignaient l'ancien dossier : un nom identique dans le
				// nouveau aurait hérité de la coche.
				m.stacks = nil
				m.selected = make(map[string]bool)
				m.lastFail = nil // même raison : un échec par nom relatif
				m.svcFocus = false
				m.cursor = 0
				m.invalidateRows()
			}
			m.cfg.StackDir = m.dirPath
			m.view = viewList
			m.loading = true
			m.loadingSince = time.Now()
			// On peut arriver ici avec un client nil (init Docker échoué, le
			// footer d'erreur propose « o ») : retenter la connexion au lieu
			// de charger — loadStacks sur un client nil paniquerait. Une fois
			// le client prêt, clientReadyMsg chargera la liste.
			cmds := []tea.Cmd{m.spinner.Tick} // anime l'écran de chargement
			if m.client == nil {
				m.err = nil
				cmds = append(cmds, initClient())
			} else {
				cmds = append(cmds, m.loadStacksCmd())
			}
			// Le choix vaut pour la session même si l'écriture échoue ;
			// prévenir qu'il ne survivra pas au prochain lancement.
			if err := m.cfg.Save(); err != nil {
				m.setStatus("Config non sauvegardée : "+err.Error(), true)
				cmds = append(cmds, m.clearStatusIn(6*time.Second))
			}
			return m, tea.Batch(cmds...)
		case "../":
			return m, loadDirFocus(filepath.Dir(m.dirPath), filepath.Base(m.dirPath)+"/")
		default:
			next := filepath.Join(m.dirPath, strings.TrimSuffix(entry, "/"))
			return m, loadDirCmd(next)
		}
	}
	return m, nil
}

// ---- helpers ----

// dirChooseEntry : première ligne du sélecteur, qui choisit le répertoire
// affiché. « répertoire » comme le titre du sélecteur : « dossier » désigne
// les groupes 📁 de la liste.
const dirChooseEntry = "[*] Choisir ce répertoire"

func loadDirCmd(path string) tea.Cmd { return loadDirFocus(path, "") }

// loadDirFocus liste path et pose le curseur sur l'entrée focus si elle
// existe (le dossier d'où l'on remonte).
func loadDirFocus(path, focus string) tea.Cmd {
	return func() tea.Msg {
		entries := []string{dirChooseEntry}
		if path != "/" {
			entries = append(entries, "../")
		}
		// L'erreur est portée par le message : sans elle, un répertoire
		// illisible (permissions) s'affiche comme simplement vide.
		dirs, err := listSubDirs(path)
		entries = append(entries, dirs...)
		return dirLoadedMsg{path: path, entries: entries, err: err, focus: focus}
	}
}

// pickerStart : point de départ du sélecteur sans dossier configuré. Le
// dossier courant (souvent celui des stacks, ou le home), à défaut le home,
// et « / » en dernier recours : depuis « / », un ↩ réflexe choisissait la
// racine, que le scan (3 niveaux) ne peut pas exploiter. Un dossier courant
// égal à « / » (lancement par un service) compte comme un défaut.
func pickerStart() string {
	if wd, err := os.Getwd(); err == nil && wd != "/" {
		return wd
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

func listSubDirs(path string) ([]string, error) {
	infos, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range infos {
		if e.IsDir() && e.Name() != "." && e.Name() != ".." {
			dirs = append(dirs, e.Name()+"/")
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// resolveRestoreStacks transforme les entrées d'une capture en stacks
// restaurables. Chaque nom doit désigner un répertoire local (refus de toute
// traversée « ../ » ou chemin absolu, le nom venant d'un fichier de capture)
// contenant encore un fichier compose. Les noms non résolus sont renvoyés à
// part (missing) pour être signalés, et non ignorés en silence. Une entrée avec
// services ne relance QUE ceux-là (les services disparus du compose sont
// ignorés et signalés dans la vue de progression).
func resolveRestoreStacks(items []backup.Entry, stackDir string) (stacks []compose.Stack, missing []string) {
	for _, it := range items {
		name := it.Name
		if !filepath.IsLocal(name) {
			missing = append(missing, name)
			continue
		}
		dir := filepath.Join(stackDir, name)
		composeFile := compose.FindComposeFile(dir)
		if composeFile == "" {
			missing = append(missing, name)
			continue
		}
		stacks = append(stacks, compose.Stack{
			Name:        name,
			Dir:         dir,
			ComposeFile: composeFile,
			Only:        it.Services,
			Lenient:     len(it.Services) > 0,
		})
	}
	return stacks, missing
}
