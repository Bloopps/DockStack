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

	"github.com/Bloopps/dockstack/internal/compose"
)

var stackActions = []string{
	"▶  Up",
	"▪  Down",
	"↺  Restart",
	"↻  Recreate",
	"↓  Pull",
	"≡  Logs",
	"✕  Remove",
	"←  Retour",
}

var groupActions = []string{
	"▶  Up (sélection)",
	"▪  Down (sélection)",
	"↺  Restart (sélection)",
	"↻  Recreate (sélection)",
	"↓  Pull (sélection)",
	"←  Retour",
}

// La restauration ne passe pas par une entrée de menu : la liste des
// captures est navigable directement (↩ sur une capture = restaurer).
var backupMenuActions = []string{
	"💾 Capturer l'état actuel",
	"←  Retour",
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
		m.view = viewHelp
	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "left":
		// Replie le groupe courant et pose le curseur sur son en-tête.
		if g, ok := m.cursorGroup(); ok && m.filter.Value() == "" {
			m.foldedGroups[g] = true
			m.invalidateRows()
			m.cursor = m.headerRowIndex(g)
		}
	case "right":
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
				if !rows[c].sep && !rows[c].header {
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
	case "u", "d", "r", "c":
		// Raccourcis directs calqués sur les noms compose (up/down/restart/
		// recreate) : agissent sur la sélection si elle existe, sinon sur le
		// groupe (si le curseur est sur un en-tête), sinon sur la stack sous
		// le curseur. Les actions groupées destructives (Down/Restart/
		// Recreate) passent par la confirmation.
		action := map[string]string{"u": "up", "d": "down", "r": "restart", "c": "recreate"}[msg.String()]
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
	case "b":
		m.view = viewBackup
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

// ---- action menu (stack courante ou sélection) ----

// actionLabels renvoie les entrées du panneau d'actions selon la cible :
// la sélection (actions groupées) ou la stack sous le curseur.
func (m Model) actionLabels() []string {
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
	case "esc", "q":
		m.view = viewList
	case "enter":
		if len(m.selectedStacks()) > 0 {
			return m.execGroupAction()
		}
		return m.execStackAction()
	}
	return m, nil
}

func (m Model) execStackAction() (tea.Model, tea.Cmd) {
	stack, ok := m.cursorStack()
	if !ok {
		m.view = viewList
		return m, nil
	}

	switch stackActions[m.actionCursor] {
	case "▶  Up":
		return m.startOrConfirmUp(stack)
	case "▪  Down":
		return m.confirmStackAction("down", stack)
	case "↺  Restart":
		return m.confirmStackAction("restart", stack)
	case "↻  Recreate":
		return m.confirmStackAction("recreate", stack)
	case "↓  Pull":
		return m.startStackAction("pull", stack)
	case "≡  Logs":
		return m.startStackAction("logs", stack)
	case "✕  Remove":
		// Destructif : confirmation obligatoire, et Remove supprime aussi
		// les volumes (down --volumes --remove-orphans).
		return m.askConfirm("Remove — "+stack.Name,
			"Supprime conteneurs, réseaux ET volumes (données incluses)",
			[]string{stack.Name},
			func(m Model) (tea.Model, tea.Cmd) { return m.startStackAction("remove", stack) })
	}
	// "←  Retour" and any other case: back to the list.
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
		return m.startProgressOp("Up: "+stack.Name, cancel, client.UpManyLive(ctx, one, 1))
	case "down":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Down: "+stack.Name, cancel, client.DownManyLive(ctx, one, 1))
	case "restart":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Restart: "+stack.Name, cancel, client.RestartManyLive(ctx, one, 1))
	case "recreate":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Recreate: "+stack.Name, cancel, client.RecreateManyLive(ctx, one, 1))
	case "pull":
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp("Pull: "+stack.Name, cancel, client.PullManyLive(ctx, one, 1))
	case "logs":
		m.view = viewLogs
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
		return m.startProgressOp("Remove: "+stack.Name, cancel, client.RemoveManyLive(ctx, one, 1))
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

	switch groupActions[m.actionCursor] {
	case "▶  Up (sélection)":
		return m.startOrConfirmGroupUp(stacks)
	case "▪  Down (sélection)":
		return m.confirmGroupAction("down", stacks)
	case "↺  Restart (sélection)":
		return m.confirmGroupAction("restart", stacks)
	case "↻  Recreate (sélection)":
		return m.confirmGroupAction("recreate", stacks)
	case "↓  Pull (sélection)":
		return m.startGroupAction("pull", stacks)
	}
	// "←  Retour" and any other case: back to the list.
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
	case "q", "esc":
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
	case "G":
		m.logScroll = max(0, len(m.logLines)-m.logsHeight())
	}
	return m, nil
}

// ---- backup ----

func (m Model) handleBackupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Le curseur parcourt les entrées de menu puis la liste des sauvegardes.
	total := len(backupMenuActions) + len(m.backups)
	switch msg.String() {
	case "up", "k":
		if m.actionCursor > 0 {
			m.actionCursor--
		}
	case "down", "j":
		if m.actionCursor < total-1 {
			m.actionCursor++
		}
	case "esc", "q":
		m.view = viewList
	case "enter":
		return m.execBackupAction()
	}
	return m, nil
}

func (m Model) execBackupAction() (tea.Model, tea.Cmd) {
	if m.actionCursor < len(backupMenuActions) {
		m.view = viewList
		if backupMenuActions[m.actionCursor] == "💾 Capturer l'état actuel" {
			// startOp arme aussi spinner.Tick, sans quoi le spinner reste figé.
			return m.startOp("Capture de l'état...", saveBackup(m.cfg.ConfigDir(), m.cfg.StackDir, m.stacks))
		}
		// "←  Retour"
		return m, nil
	}

	idx := m.actionCursor - len(backupMenuActions)
	if idx >= len(m.backups) {
		m.view = viewList
		return m, nil
	}
	// Ouvre le sélecteur de restauration : tout est coché par défaut,
	// l'utilisateur peut décocher avant de relancer (↩).
	snap := m.backups[idx]
	m.view = viewRestorePick
	m.restoreLabel = fmt.Sprintf("Capture du %s", snap.Date.Format("02/01/2006 15:04"))
	m.restoreNames = snap.Stacks
	m.restoreSel = make(map[string]bool, len(snap.Stacks))
	for _, n := range snap.Stacks {
		m.restoreSel[n] = true
	}
	m.restoreCursor = 0
	return m, nil
}

// ---- restore picker ----

func (m Model) handleRestorePickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.restoreCursor > 0 {
			m.restoreCursor--
		}
	case "down", "j":
		if m.restoreCursor < len(m.restoreNames)-1 {
			m.restoreCursor++
		}
	case " ", "space":
		if m.restoreCursor < len(m.restoreNames) {
			name := m.restoreNames[m.restoreCursor]
			m.restoreSel[name] = !m.restoreSel[name]
			if m.restoreCursor < len(m.restoreNames)-1 {
				m.restoreCursor++
			}
		}
	case "ctrl+a":
		all := true
		for _, n := range m.restoreNames {
			if !m.restoreSel[n] {
				all = false
				break
			}
		}
		for _, n := range m.restoreNames {
			m.restoreSel[n] = !all
		}
	case "esc", "q":
		m.view = viewBackup
	case "enter":
		var names []string
		for _, n := range m.restoreNames {
			if m.restoreSel[n] {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			m.setStatus("Aucune stack sélectionnée", true)
			return m, clearStatusIn(3 * time.Second)
		}
		if m.opInProgress() {
			return m.refuseOp()
		}
		// Comme les autres actions, la restauration passe par la vue de
		// progression : flux live, annulable (esc), au lieu d'un Up bloquant
		// sur ctx.Background().
		stacks, missing := resolveRestoreStacks(names, m.cfg.StackDir)
		if len(stacks) == 0 {
			m.setStatus(fmt.Sprintf("Aucune stack restaurable (%d introuvable(s) sur le disque)", len(missing)), true)
			return m, clearStatusIn(5 * time.Second)
		}
		title := fmt.Sprintf("Restauration — %d stack(s)", len(stacks))
		if len(missing) > 0 {
			// Signaler les introuvables au lieu de les ignorer en silence.
			title += fmt.Sprintf(" (%d introuvable(s) ignorée(s))", len(missing))
		}
		ctx, cancel := context.WithCancel(context.Background())
		return m.startProgressOp(title, cancel, m.client.UpManyLive(ctx, stacks, m.cfg.MaxParallel))
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
	case "esc", "q":
		if m.cfg.StackDir != "" {
			m.view = viewList
		} else if msg.String() == "q" {
			// Premier lancement : aucune liste où revenir — q quitte (deux
			// frappes), sinon le picker rendrait l'application inquittable.
			return m.armOrQuit()
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
				cmds = []tea.Cmd{loadStacks(m.client, m.cfg.StackDir)}
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

// resolveRestoreStacks transforme les noms d'une capture en stacks restaurables.
// Chaque nom doit désigner un répertoire local (refus de toute traversée « ../ »
// ou chemin absolu, le nom venant d'un fichier de capture) contenant encore un
// fichier compose. Les noms non résolus sont renvoyés à part (missing) pour être
// signalés à l'utilisateur, et non ignorés en silence.
func resolveRestoreStacks(names []string, stackDir string) (stacks []compose.Stack, missing []string) {
	for _, name := range names {
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
		})
	}
	return stacks, missing
}
