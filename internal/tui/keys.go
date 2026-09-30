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
	color string // index de couleur ANSI
	key   string // raccourci, le même que dans la liste ("" = aucun : Remove)
}

// Remove n'a volontairement pas de raccourci : détruire les volumes doit
// passer par le panneau, jamais par une lettre tapée par erreur.
var stackActions = []menuAction{
	{"up", "▶", "Up", "2", "u"},
	{"stop", "■", "Stop", "1", "s"},
	{"down", "▪", "Down", "1", "d"},
	{"restart", "↺", "Restart", "3", "r"},
	{"recreate", "↻", "Recreate", "3", "c"},
	{"pull", "↓", "Pull", "6", "p"},
	{"logs", "≡", "Logs", "6", "l"},
	{"compose", "▤", "Compose", "6", "v"},
	{"remove", "✕", "Remove", "1", ""},
	{"back", "←", "Retour", "8", "esc"},
}

var groupActions = []menuAction{
	{"up", "▶", "Up", "2", "u"},
	{"stop", "■", "Stop", "1", "s"},
	{"down", "▪", "Down", "1", "d"},
	{"restart", "↺", "Restart", "3", "r"},
	{"recreate", "↻", "Recreate", "3", "c"},
	{"pull", "↓", "Pull", "6", "p"},
	{"back", "←", "Retour", "8", "esc"},
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
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.invalidateRows()
			m.cursor = 0
			return m, cmd
		}
	}

	if m.svcFocus {
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
	case "?":
		m.view, m.helpScroll = viewHelp, 0
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
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
		if g, ok := m.cursorGroup(); ok {
			delete(m.foldedGroups, g)
			m.invalidateRows()
		}
	case " ", "space":
		// bubbletea v2 reports the space key as "space" (v1 used " ").
		// Sur un en-tête de groupe : (dé)sélectionne tout le groupe.
		if row, ok := m.cursorRow(); ok && row.header {
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
			m.view = viewAction
			m.actionCursor = 0
			return m, nil
		}
		if row, ok := m.cursorRow(); ok && row.header {
			if m.foldedGroups[row.group] {
				delete(m.foldedGroups, row.group)
			} else {
				m.foldedGroups[row.group] = true
			}
			m.invalidateRows()
			return m, nil
		}
		if _, ok := m.cursorStack(); !ok {
			return m, nil
		}
		m.view = viewAction
		m.actionCursor = 0
	case "u", "s", "d", "r", "c":
		// Raccourcis directs calqués sur les noms compose (up/stop/down/
		// restart/recreate), les mêmes dans la liste, le mode services et le
		// panneau d'actions : agissent sur la sélection si elle existe, sinon
		// sur le groupe (si le curseur est sur un en-tête), sinon sur la stack
		// sous le curseur. Ce qui perturbe un service passe par la confirmation.
		action := map[string]string{"u": "up", "s": "stop", "d": "down", "r": "restart", "c": "recreate"}[msg.String()]
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
		m.setStatus("Compose : viser une stack, pas un dossier", false)
		return m, clearStatusIn(3 * time.Second)
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
		return m, loadBackups(m.cfg.ConfigDir())
	case "o":
		start := m.cfg.StackDir
		if start == "" {
			start = "/"
		}
		m.view = viewDirPicker
		return m, loadDirCmd(start)
	}
	return m, nil
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
		return m, clearStatusIn(6 * time.Second)
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
			return m, clearStatusIn(4 * time.Second)
		})
}

// ---- action menu (stack courante ou sélection) ----

// actionLabels renvoie les entrées du panneau d'actions selon la cible :
// la sélection (actions groupées) ou la stack sous le curseur.
func (m Model) actionLabels() []menuAction {
	if len(m.selectedStacks()) > 0 {
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
		if len(m.selectedStacks()) > 0 {
			return m.execGroupAction()
		}
		return m.execStackAction()
	default:
		// Les lettres de la liste marchent aussi dans le panneau (affichées à
		// côté de chaque entrée) : y apprendre d puis devoir descendre avec
		// les flèches jusqu'à Down n'avait pas de sens.
		for i, a := range m.actionLabels() {
			if a.key != "" && a.key != "esc" && a.key == msg.String() {
				m.actionCursor = i
				if len(m.selectedStacks()) > 0 {
					return m.execGroupAction()
				}
				return m.execStackAction()
			}
		}
	}
	return m, nil
}

func (m Model) execStackAction() (tea.Model, tea.Cmd) {
	stack, ok := m.cursorStack()
	if !ok {
		m.view = viewList
		return m, nil
	}

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
		// les volumes (down --volumes --remove-orphans).
		return m.askConfirm("Remove — "+stack.Name,
			"Supprime conteneurs, réseaux ET volumes (données incluses)",
			[]string{stack.Name},
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
		m.logStack = stackLabel(stack)
		m.logLines = nil
		m.logScroll = 0
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

func (m Model) execGroupAction() (tea.Model, tea.Cmd) {
	stacks := m.selectedStacks()
	if len(stacks) == 0 {
		m.view = viewList
		return m, nil
	}

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

// startGroupAction lance une action live sur plusieurs stacks. La sélection
// n'est vidée qu'ici, donc annuler la confirmation la conserve.
func (m Model) startGroupAction(action string, stacks []compose.Stack) (tea.Model, tea.Cmd) {
	// Verrou d'opération (cf. startStackAction), avant de vider la sélection :
	// un refus la conserve, comme une annulation de confirmation.
	if m.opInProgress() {
		return m.refuseOp()
	}
	client := m.client
	par := m.cfg.MaxParallel
	m.selected = make(map[string]bool)
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
	switch msg.String() {
	case "esc":
		if m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
		}
		m.logSeq++ // périme la session : stoppe la pompe de lecture
		m.logCh = nil
		m.view = viewList
	case "up", "k":
		if m.logScroll > 0 {
			m.logScroll--
		}
	case "down", "j":
		// Borné : au-delà, renderLogsView découperait hors limites.
		if m.logScroll < max(0, len(m.logLines)-m.logsHeight()) {
			m.logScroll++
		}
	case "g", "home":
		m.logScroll = 0
	case "G", "end":
		m.logScroll = max(0, len(m.logLines)-m.logsHeight())
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
	case "g":
		m.composeScroll = 0
	case "G":
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
	case "esc":
		m.view = viewList
	case "c":
		// État inconnu (liste pas encore chargée, Docker injoignable) : la
		// capture serait vide et écraserait active_stacks, lu par les scripts.
		if m.lastRefresh.IsZero() || m.err != nil {
			m.setStatus("État des stacks inconnu (liste non chargée ou Docker injoignable) : capture impossible", true)
			return m, clearStatusIn(5 * time.Second)
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
		return m, clearStatusIn(5 * time.Second)
	}
	plan := planAlign(snap.Items(), m.stacks)
	if plan.empty() {
		m.setStatus("Déjà aligné sur cette capture", false)
		return m, clearStatusIn(3 * time.Second)
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
		warning = fmt.Sprintf("%d arrêt(s) : ce qui tourne mais n'est pas dans la capture sera arrêté (conteneurs conservés).", nStop)
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
		return m, clearStatusIn(5 * time.Second)
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
			m.setStatus("Aucune stack sélectionnée", true)
			return m, clearStatusIn(3 * time.Second)
		}
		if m.opInProgress() {
			return m.refuseOp()
		}
		// Comme les autres actions, la restauration passe par la vue de
		// progression : flux live, annulable (esc), au lieu d'un Up bloquant
		// sur ctx.Background().
		stacks, missing := resolveRestoreStacks(items, m.cfg.StackDir)
		if len(stacks) == 0 {
			m.setStatus(fmt.Sprintf("Aucune stack relançable (%d introuvable(s) sur le disque)", len(missing)), true)
			return m, clearStatusIn(5 * time.Second)
		}
		title := fmt.Sprintf("Relance — %d stack(s)", len(stacks))
		if len(missing) > 0 {
			// Signaler les introuvables au lieu de les ignorer en silence.
			title += fmt.Sprintf(" (%d introuvable(s) ignorée(s))", len(missing))
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
		case "[*] Choisir ce dossier":
			m.cfg.StackDir = m.dirPath
			m.view = viewList
			m.loading = true
			// On peut arriver ici avec un client nil (init Docker échoué, le
			// footer d'erreur propose « o ») : retenter la connexion au lieu
			// de charger — loadStacks sur un client nil paniquerait. Une fois
			// le client prêt, clientReadyMsg chargera la liste.
			var cmds []tea.Cmd
			if m.client == nil {
				m.err = nil
				cmds = []tea.Cmd{initClient()}
			} else {
				cmds = []tea.Cmd{m.loadStacksCmd()}
			}
			// Le choix vaut pour la session même si l'écriture échoue ;
			// prévenir qu'il ne survivra pas au prochain lancement.
			if err := m.cfg.Save(); err != nil {
				m.setStatus("Config non sauvegardée : "+err.Error(), true)
				cmds = append(cmds, clearStatusIn(6*time.Second))
			}
			return m, tea.Batch(cmds...)
		case "../":
			return m, loadDirCmd(filepath.Dir(m.dirPath))
		default:
			next := filepath.Join(m.dirPath, strings.TrimSuffix(entry, "/"))
			return m, loadDirCmd(next)
		}
	}
	return m, nil
}

// ---- helpers ----

func loadDirCmd(path string) tea.Cmd {
	return func() tea.Msg {
		entries := []string{"[*] Choisir ce dossier"}
		if path != "/" {
			entries = append(entries, "../")
		}
		// L'erreur est portée par le message : sans elle, un répertoire
		// illisible (permissions) s'affiche comme simplement vide.
		dirs, err := listSubDirs(path)
		entries = append(entries, dirs...)
		return dirLoadedMsg{path: path, entries: entries, err: err}
	}
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
