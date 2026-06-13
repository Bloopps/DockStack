package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
)

type view int

const (
	maxLogLines  = 10000 // lignes de logs conservées en mémoire
	logTrimChunk = 512   // marge avant taille, pour amortir les recopies
)

const (
	viewList view = iota
	viewAction
	viewLogs
	viewBackup
	viewDirPicker
	viewProgress
	viewConfirm
	viewHelp
	viewRestorePick
)

// ---- message types ----

type tickMsg time.Time
type metricsMsg monitor.Metrics
type clientReadyMsg struct{ client dockerService }
type stacksLoadedMsg []compose.Stack
type backupsLoadedMsg []backup.Snapshot
type opDoneMsg struct {
	err error
}

type clearStatusMsg struct{}
type quitDisarmMsg int
type stackTickMsg struct{}
type resubscribeEventsMsg struct{}

// Les messages de logs portent le numéro de session : ceux d'une session
// fermée (vue quittée puis rouverte) sont ignorés, sinon les lignes de
// l'ancien flux se mélangeraient au nouveau.
// logLinesMsg porte un lot de lignes : readLogCmd vide le canal sans bloquer
// pour livrer plusieurs lignes par message, au lieu d'un cycle Update par
// ligne (le suivi de nombreux conteneurs peut être très verbeux).
type logLinesMsg struct {
	entries []compose.LogEntry
	seq     int
}
type logDoneMsg struct{ seq int }

// fatalErrMsg : erreur qui empêche d'utiliser l'appli (init Docker échoué,
// listing des stacks impossible). Affichée en plein écran avec le rappel
// R/o/q, et effacée par un chargement réussi ou un R.
type fatalErrMsg error

// opErrMsg : erreur d'une opération ponctuelle lancée depuis une vue dédiée
// (ouverture des logs, chargement des captures). Récupérable : le flux n'a
// jamais démarré, donc on revient à la liste et on affiche l'erreur en barre
// de statut. (L'ancien errMsg fourre-tout posait m.err, invisible partout
// sauf dans la vue liste : une erreur d'ouverture des logs laissait un écran
// vide.)
type opErrMsg struct{ err error }

// ---- model ----

type Model struct {
	cfg    *config.Config
	client dockerService

	view view

	// Stack list. La sélection est indexée par nom de stack (pas par position) :
	// la liste est re-triée à chaque refresh et peut être filtrée, les indices
	// ne sont donc jamais stables.
	// Le curseur indexe les lignes de listRows() (en-têtes de groupe inclus).
	stacks       []compose.Stack
	cursor       int
	selected     map[string]bool
	foldedGroups map[string]bool
	rowsCache    *rowsCacheBox
	refreshing   bool
	lastRefresh  time.Time // dernier chargement des stacks (affiché au footer)
	manualR      bool      // refresh déclenché par R : confirmer la fin par un statut

	// Flux d'événements Docker (start/stop/die/health_status…), qui pilote le
	// refresh de la liste à la place du seul tick périodique. refreshGen est
	// incrémenté à chaque événement pertinent ; debounceRefreshMsg ne déclenche
	// le rechargement que si aucun nouvel événement n'est arrivé depuis (gen
	// inchangé), ce qui coalesce les rafales (ex: up/down d'une stack entière).
	eventsCh       <-chan compose.DockerEvent
	refreshPending bool
	refreshGen     int

	// Action menu cursor (shared between viewAction / viewBackup)
	actionCursor int

	// Logs (canal et annulation portés par le Model, pas par des globals).
	// logSeq numérote la session courante pour invalider les messages en vol
	// d'une session précédente.
	logLines  []string
	logScroll int
	logCh     <-chan compose.LogEntry
	logCancel context.CancelFunc
	logSeq    int

	// Dir picker
	dirPath    string
	dirEntries []string
	dirCursor  int
	dirErr     error // lecture du répertoire courant impossible

	// Backup
	backups []backup.Snapshot

	// Restauration : sélection des stacks de la capture à relancer
	restoreLabel  string
	restoreNames  []string
	restoreSel    map[string]bool
	restoreCursor int

	// Filter
	filter    textinput.Model
	filtering bool

	// Operation in progress
	spinner  spinner.Model
	spinning bool
	opLabel  string

	// Status bar (ephemeral)
	status    string
	statusErr bool

	// Quitter en deux frappes (q/ctrl+c) ; quitSeq invalide les timers de
	// désarmement périmés.
	quitArmed bool
	quitSeq   int

	// Progress (live streaming view)
	progressTitle         string
	progress              *progressState
	progressScroll        int  // lines above the bottom to offset; 0 = auto-scroll to bottom
	progressManualScroll  bool // true once user has scrolled up manually
	progressDone          bool
	progressOk            int
	progressTotal         int
	progressCancel        context.CancelFunc
	progressCh            <-chan compose.ProgressEvent
	progressSeq           int  // numérote l'op courante : invalide les messages en vol d'une précédente
	progressConfirmCancel bool // armed by a first esc/q while running; the next confirms
	progressCancelling    bool // cancel() fired, waiting for the stream to drain

	// Confirmation (destructive actions)
	confirmLabel   string
	confirmWarning string
	confirmNames   []string
	confirmExec    func(Model) (tea.Model, tea.Cmd)
	confirmReturn  view // vue de retour si l'utilisateur annule

	// Metrics
	metrics monitor.Metrics
	history metricsHistory

	// Terminal
	width  int
	height int

	loading bool
	err     error
}

func New(cfg *config.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filtrer..."
	ti.CharLimit = 64
	// v2: per-state styling via the Styles struct instead of PromptStyle/TextStyle/…
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = styleCyan
	tiStyles.Focused.Text = lipgloss.NewStyle()
	tiStyles.Focused.Placeholder = styleDim
	tiStyles.Blurred.Prompt = styleCyan
	tiStyles.Blurred.Placeholder = styleDim
	ti.SetStyles(tiStyles)

	return Model{
		cfg:          cfg,
		spinner:      sp,
		filter:       ti,
		selected:     make(map[string]bool),
		foldedGroups: make(map[string]bool),
		rowsCache:    &rowsCacheBox{},
		loading:      true,
	}
}

func (m Model) Init() tea.Cmd {
	// collectMetricsNow paints the header immediately; the metricsMsg handler then
	// schedules the recurring tickMetrics. prewarmStacks overlaps the filesystem
	// walk with the Docker client init in initClient.
	return tea.Batch(
		initClient(),
		prewarmStacks(m.cfg.StackDir),
		collectMetricsNow(),
		tickStacks(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case metricsMsg:
		m.metrics = monitor.Metrics(msg)
		m.history.push(m.metrics)
		return m, tickMetrics()

	case clientReadyMsg:
		m.client = msg.client
		m.err = nil
		if m.cfg.StackDir == "" {
			m.loading = false
			m.view = viewDirPicker
			return m, tea.Batch(loadDirCmd("/"), subscribeDockerEvents(m.client))
		}
		// La liste est l'écran d'accueil ; les stacks se chargent en arrière-plan
		m.view = viewList
		return m, tea.Batch(loadStacks(m.client, m.cfg.StackDir), subscribeDockerEvents(m.client))

	case stacksLoadedMsg:
		// Restaure la position du curseur par identité de ligne (stack ou
		// en-tête de groupe) : les indices bougent à chaque re-tri/refresh.
		var curStack, curGroup string
		if row, ok := m.cursorRow(); ok {
			if row.header {
				curGroup = row.group
			} else if !row.sep {
				curStack = row.stack.Name
			}
		}
		// Ne pas toucher à m.spinning : il appartient aux opérations backup
		// (startOp/opDoneMsg) ; un refresh auto qui aboutit pendant une
		// capture/restauration effacerait son spinner avant la fin.
		m.stacks = sortByState([]compose.Stack(msg))
		m.invalidateRows()
		// Un chargement réussi efface l'erreur précédente : sans ça, un raté
		// transitoire (ex: daemon redémarré) laisserait l'écran d'erreur
		// affiché à vie alors que les refresh suivants aboutissent.
		m.err = nil
		m.loading = false
		m.refreshing = false
		m.lastRefresh = time.Now()
		m.cursor = 0
		for i, r := range m.listRows() {
			if (curStack != "" && !r.header && !r.sep && r.stack.Name == curStack) ||
				(curGroup != "" && r.header && r.group == curGroup) {
				m.cursor = i
				break
			}
		}
		// Un refresh manuel (R) confirme sa fin ; l'auto-refresh reste discret.
		if m.manualR {
			m.manualR = false
			m.setStatus("Liste actualisée", false)
			return m, clearStatusIn(2 * time.Second)
		}

	case backupsLoadedMsg:
		m.backups = []backup.Snapshot(msg)
		m.spinning = false

	case opDoneMsg:
		// Backup save/restore result (stack actions use the live progress view).
		// Pas de retour forcé à la liste : capture et restauration y sont déjà
		// (leurs handlers posent viewList avant startOp) ; écraser la vue ici
		// éjecterait l'utilisateur de là où il a navigué pendant l'opération
		// (vue de progression d'une autre op, aide, dossier…).
		m.spinning = false
		if msg.err != nil {
			s := truncate(m.opLabel+": "+msg.err.Error(), 80)
			m.setStatus(s, true)
			return m, tea.Batch(clearStatusIn(6*time.Second), loadStacks(m.client, m.cfg.StackDir))
		}
		m.setStatus("✓ "+m.opLabel, false)
		return m, tea.Batch(clearStatusIn(3*time.Second), loadStacks(m.client, m.cfg.StackDir))

	case progressEventMsg:
		// Message d'une op périmée : ne pas l'appliquer ni ré-armer sa pompe
		// (le verrou d'op rend le cas improbable, le seq le rend inoffensif).
		if msg.seq != m.progressSeq {
			return m, nil
		}
		if m.progress != nil {
			m.progress.apply(msg.ev)
		}
		if msg.ev.StackDone {
			m.progressTotal++
			if msg.ev.Err == nil {
				m.progressOk++
			}
		}
		return m, readProgressCmd(m.progressCh, msg.seq)

	case progressAllDoneMsg:
		if msg.seq != m.progressSeq {
			return m, nil
		}
		m.progressDone = true
		m.progressCh = nil
		if m.progressCancel != nil {
			m.progressCancel()
			m.progressCancel = nil
		}
		return m, loadStacks(m.client, m.cfg.StackDir)

	case logLinesMsg:
		if msg.seq != m.logSeq {
			return m, nil // session de logs périmée : ne pas réarmer la lecture
		}
		for _, entry := range msg.entries {
			line := fmt.Sprintf("[%s] %s", sanitizeLogLine(entry.Name), sanitizeLogLine(entry.Line))
			m.logLines = append(m.logLines, line)
		}
		// Plafond glissant : le suivi est continu (Follow), sans limite la
		// mémoire grandit indéfiniment. On taille par paquets pour ne pas
		// recopier à chaque ligne, et on réalloue pour libérer l'ancien tampon.
		if len(m.logLines) > maxLogLines+logTrimChunk {
			drop := len(m.logLines) - maxLogLines
			m.logLines = append([]string(nil), m.logLines[drop:]...)
			if m.logScroll > drop {
				m.logScroll -= drop
			} else {
				m.logScroll = 0
			}
		}
		if m.logScroll >= len(m.logLines)-m.logsHeight()-2 {
			m.logScroll = max(0, len(m.logLines)-m.logsHeight())
		}
		return m, readLogCmd(m.logCh, m.logSeq)

	case logsStartedMsg:
		if msg.seq != m.logSeq {
			return m, nil // la vue a été fermée avant l'ouverture du flux
		}
		m.logCh = msg.ch
		return m, readLogCmd(msg.ch, msg.seq)

	case clearStatusMsg:
		m.status = ""
		m.statusErr = false

	case quitDisarmMsg:
		if int(msg) == m.quitSeq {
			m.quitArmed = false
		}

	case stackTickMsg:
		// Auto-refresh seulement depuis la liste : recharger pendant qu'un
		// menu d'action ou une confirmation est ouvert re-trie les stacks et
		// peut déplacer le curseur, donc faire viser une autre stack au
		// moment d'exécuter l'action.
		if m.client != nil && m.cfg.StackDir != "" && !m.spinning && m.view == viewList {
			m.refreshing = true
			return m, tea.Batch(loadStacks(m.client, m.cfg.StackDir), tickStacks())
		}
		return m, tickStacks()

	case dockerEventsStartedMsg:
		m.eventsCh = msg.ch
		return m, readDockerEventCmd(msg.ch)

	case dockerEventMsg:
		cmds := []tea.Cmd{readDockerEventCmd(m.eventsCh)}
		if compose.IsRelevantDockerEvent(msg.Action) {
			m.refreshGen++
			if !m.refreshPending {
				m.refreshPending = true
				cmds = append(cmds, debounceRefresh(m.refreshGen))
			}
		}
		return m, tea.Batch(cmds...)

	case dockerEventsClosedMsg:
		// Flux interrompu (erreur/EOF, ex: redémarrage du daemon) : on se
		// réabonne après un court délai. Le tick périodique continue de
		// rafraîchir la liste entre-temps.
		m.eventsCh = nil
		if m.client == nil {
			return m, nil
		}
		return m, tea.Tick(reconnectDelay, func(time.Time) tea.Msg {
			return resubscribeEventsMsg{}
		})

	case resubscribeEventsMsg:
		if m.client == nil {
			return m, nil
		}
		return m, subscribeDockerEvents(m.client)

	case debounceRefreshMsg:
		if msg.gen != m.refreshGen {
			// Un événement plus récent est arrivé pendant l'attente : reprogrammer.
			return m, debounceRefresh(m.refreshGen)
		}
		m.refreshPending = false
		if m.client != nil && m.cfg.StackDir != "" && !m.spinning && m.view == viewList {
			m.refreshing = true
			return m, loadStacks(m.client, m.cfg.StackDir)
		}
		return m, nil

	case logDoneMsg:
		if msg.seq != m.logSeq {
			return m, nil
		}
		m.logCh = nil
		// Flux clos après fermeture de la vue (annulation) : rien à signaler.
		if m.view != viewLogs {
			return m, nil
		}
		m.setStatus("Logs terminés", false)
		return m, clearStatusIn(3 * time.Second)

	case dirLoadedMsg:
		m.dirPath = msg.path
		m.dirEntries = msg.entries
		m.dirErr = msg.err
		m.dirCursor = 0

	case fatalErrMsg:
		m.err = msg
		m.loading = false
		m.spinning = false

	case opErrMsg:
		m.loading = false
		m.spinning = false
		// L'op a échoué avant de démarrer son flux : libérer le contexte de
		// logs s'il a été ouvert (sinon le cancel resterait pendant), revenir
		// à la liste et signaler l'erreur en barre de statut plutôt que de
		// laisser la vue dédiée afficher un écran vide.
		if m.logCancel != nil {
			m.logCancel()
			m.logCancel = nil
			m.logSeq++ // périme la session de logs qui n'a pas pu démarrer
			m.logCh = nil
		}
		m.view = viewList
		m.setStatus(truncate(msg.err.Error(), 80), true)
		return m, clearStatusIn(6 * time.Second)

	case spinner.TickMsg:
		// Keep the spinner animating both for footer ops (m.spinning) and for the
		// live progress view while at least one task is still running.
		if m.spinning || (m.view == viewProgress && !m.progressDone) {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}

	case tea.MouseWheelMsg:
		if m.view == viewProgress {
			switch msg.Button {
			case tea.MouseWheelUp:
				if m.progressScroll < m.progressMaxScroll() {
					m.progressScroll++
					m.progressManualScroll = true
				}
			case tea.MouseWheelDown:
				if m.progressScroll > 0 {
					m.progressScroll--
				}
				if m.progressScroll == 0 {
					m.progressManualScroll = false
				}
			}
			return m, nil
		}

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.view == viewProgress {
		switch msg.String() {
		case "up", "k":
			if m.progressScroll < m.progressMaxScroll() {
				m.progressScroll++
				m.progressManualScroll = true
			}
		case "down", "j":
			if m.progressScroll > 0 {
				m.progressScroll--
			}
			if m.progressScroll == 0 {
				m.progressManualScroll = false
			}
		case "g", "G":
			m.progressScroll = 0
			m.progressManualScroll = false
		case "q", "esc", "ctrl+c":
			if m.progressDone {
				m.leaveProgressView()
				break
			}
			// Opération en cours : premier esc arme la confirmation, le second annule.
			if !m.progressConfirmCancel {
				m.progressConfirmCancel = true
				break
			}
			m.progressConfirmCancel = false
			if m.progressCancel != nil {
				m.progressCancel()
				m.progressCancel = nil
				m.progressCancelling = true
			}
		case "enter":
			if m.progressDone {
				m.leaveProgressView()
			}
		default:
			// Toute autre touche désarme la confirmation d'annulation.
			m.progressConfirmCancel = false
		}
		return m, nil
	}
	// L'aide se ferme sur n'importe quelle touche (avant l'intercept global de r).
	if m.view == viewHelp {
		m.view = viewList
		return m, nil
	}
	// ctrl+c quitte depuis n'importe quelle vue (deux frappes) : les vues qui
	// ne le traitent pas l'avaleraient — au premier lancement, le dirpicker
	// (sans esc/q possibles) rendait l'application inquittable.
	if msg.String() == "ctrl+c" {
		return m.armOrQuit()
	}
	// Toute touche autre que q désarme la confirmation de sortie (le ctrl+c
	// est traité juste au-dessus).
	if msg.String() != "q" {
		m.quitArmed = false
	}
	// R = reload global (r est réservé à Restart dans la liste). Si l'init
	// Docker a échoué, R retente la connexion au lieu de ne rien faire.
	if msg.String() == "R" && !m.filtering {
		if m.client == nil {
			m.loading = true
			m.err = nil
			return m, initClient()
		}
		if m.cfg.StackDir != "" {
			compose.InvalidateComposeCache()
			m.refreshing = true
			m.manualR = true
			return m, loadStacks(m.client, m.cfg.StackDir)
		}
	}
	switch m.view {
	case viewList:
		return m.handleListKey(msg)
	case viewAction:
		return m.handleActionKey(msg)
	case viewLogs:
		return m.handleLogsKey(msg)
	case viewBackup:
		return m.handleBackupKey(msg)
	case viewDirPicker:
		return m.handleDirKey(msg)
	case viewConfirm:
		return m.handleConfirmKey(msg)
	case viewRestorePick:
		return m.handleRestorePickKey(msg)
	}
	return m, nil
}

// View wraps the rendered content in a tea.View. In bubbletea v2 the alt-screen
// and mouse mode are carried by the View itself (not program options).
func (m Model) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	// Capture the mouse only in the progress view while the operation runs
	// (for wheel scrolling). Once it's done — and everywhere else — leave the
	// mouse free so the terminal's native text selection / copy works, e.g.
	// to copy an error message from the result. Keyboard scroll (↑↓/j/k)
	// still works after completion.
	if m.view == viewProgress && !m.progressDone {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m Model) viewContent() string {
	if m.width == 0 {
		return ""
	}

	switch m.view {
	case viewLogs:
		return renderLogsView(m)
	case viewDirPicker:
		return renderDirPicker(m)
	case viewProgress:
		return renderProgressView(m)
	}

	header := renderHeader(m.metrics, m.history, m.stackCounts(), m.width)

	headerH := lipgloss.Height(header)
	footerH := 1
	bodyH := m.height - headerH - footerH
	if bodyH < 0 {
		bodyH = 0
	}

	var body string
	switch m.view {
	case viewHelp:
		body = renderHelpView(m, bodyH)
	case viewConfirm:
		body = renderConfirmView(m, bodyH)
	case viewRestorePick:
		body = renderRestorePick(m, bodyH)
	case viewAction:
		body = renderActionMenu(m, bodyH)
	case viewBackup:
		body = renderBackupView(m, bodyH)
	default:
		if m.loading {
			body = styleDim.Render("\n  Chargement...")
		} else if m.err != nil {
			body = renderError(m.err)
		} else {
			body = renderStackList(m, bodyH)
		}
	}

	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func renderError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "docker.sock") || strings.Contains(msg, "permission denied") {
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("1")).
			Padding(0, 2).
			MarginLeft(2)

		content := strings.Join([]string{
			styleRed.Bold(true).Render("Accès Docker refusé"),
			"",
			styleDim.Render("Relancer en sudo :") + "  " + styleYellow.Render("sudo dockstack"),
			styleDim.Render("Ou rejoindre le groupe :") + "  " + styleYellow.Render("sudo usermod -aG docker $USER  && newgrp docker"),
			styleDim.Render("Ou démarrer le daemon :") + "  " + styleYellow.Render("sudo systemctl start docker"),
		}, "\n")

		return "\n" + box.Render(content)
	}
	return styleRed.Render(fmt.Sprintf("\n  Erreur: %v", err))
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
}

func (m Model) logsHeight() int {
	return m.height - 3
}

func (m Model) stackCounts() StackCounts {
	if m.rowsCache.countsOK {
		return m.rowsCache.counts
	}
	sc := StackCounts{Dir: m.cfg.StackDir, Total: len(m.stacks)}
	for _, s := range m.stacks {
		switch s.State() {
		case compose.StateUnhealthy:
			sc.Unhealthy++
		case compose.StateRunning:
			sc.Running++
		case compose.StatePartial:
			sc.Partial++
		case compose.StateStopped:
			sc.Stopped++
		default:
			sc.NotDep++
		}
	}
	m.rowsCache.counts = sc
	m.rowsCache.countsOK = true
	return sc
}

// filteredStacks applique le filtre dès qu'il a une valeur, que le champ de
// saisie soit focus ou non : valider avec ↩ fige le filtre au lieu de l'effacer.
func (m Model) filteredStacks() []compose.Stack {
	if m.rowsCache.filteredOK {
		return m.rowsCache.filtered
	}
	out := m.stacks
	if m.filter.Value() != "" {
		query := strings.ToLower(m.filter.Value())
		out = nil
		for _, s := range m.stacks {
			lc := s.NameLC
			if lc == "" { // stacks construites hors ListStacks
				lc = strings.ToLower(s.Name)
			}
			if strings.Contains(lc, query) {
				out = append(out, s)
			}
		}
	}
	m.rowsCache.filtered = out
	m.rowsCache.filteredOK = true
	return out
}

func (m Model) selectedStacks() []compose.Stack {
	var out []compose.Stack
	for _, s := range m.stacks {
		if m.selected[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

func (m Model) renderFooter() string {
	if m.quitArmed {
		return styleYellow.Bold(true).PaddingLeft(2).Render("⚠ Appuyer à nouveau pour quitter (q / ctrl+c)") +
			styleDim.Render("  ·  autre touche = annuler")
	}
	if m.status != "" {
		msg := m.status
		maxW := m.width - 4
		if maxW > 0 && lipgloss.Width(msg) > maxW {
			msg = truncate(msg, maxW)
		}
		if m.statusErr {
			return styleRed.PaddingLeft(2).Render("✕ " + msg)
		}
		return styleGreen.PaddingLeft(2).Render("✓ " + msg)
	}
	if m.spinning {
		bar := lipgloss.NewStyle().
			Foreground(lipgloss.Color("6")).
			Bold(true).
			PaddingLeft(2).
			Render("▶ " + m.opLabel + "  " + m.spinner.View())
		return bar
	}
	if m.err != nil {
		return renderFooterKeys([]struct{ key, desc string }{
			{"R", "réessayer"},
			{"o", "répertoire"},
			{"q", "quitter"},
		}, m.width)
	}
	if m.view == viewConfirm {
		return renderFooterKeys([]struct{ key, desc string }{
			{"y / ↩", "confirmer"},
			{"n / esc", "annuler"},
		}, m.width)
	}
	if m.view == viewAction || m.view == viewBackup {
		return renderFooterKeys([]struct{ key, desc string }{
			{"↑↓", "naviguer"},
			{"↩", "valider"},
			{"esc", "retour"},
		}, m.width)
	}
	if m.view == viewRestorePick {
		n := 0
		for _, v := range m.restoreSel {
			if v {
				n++
			}
		}
		return renderFooterKeys([]struct{ key, desc string }{
			{"␣", "(dé)sélect"},
			{"ctrl+a", "tout"},
			{"↩", fmt.Sprintf("relancer %d stack(s)", n)},
			{"esc", "retour"},
		}, m.width)
	}
	if m.view == viewHelp {
		return styleDim.PaddingLeft(2).Render("appuyer sur une touche pour fermer")
	}
	if m.view == viewList {
		// À droite : état du rafraîchissement (en cours / heure du dernier)
		// puis position dans la liste.
		right := m.listPosition()
		switch {
		case m.refreshing:
			right = "↻ actualisation…   " + right
		case !m.lastRefresh.IsZero():
			right = "maj " + m.lastRefresh.Format("15:04:05") + "   " + right
		}
		if n := len(m.selectedStacks()); n > 0 {
			return renderFooterKeysRight([]struct{ key, desc string }{
				{fmt.Sprintf("%d", n), "sélectionnées"},
				{"↩", "actions"},
				{"esc", "désélect"},
				{"?", "aide"},
				{"q", "quitter"},
			}, m.width, right)
		}
		return renderFooterKeysRight([]struct{ key, desc string }{
			{"↩", "actions"},
			{"␣", "sélect"},
			{"/", "filtre"},
			{"b", "backup"},
			{"o", "dossier"},
			{"R", "rafraîchir"},
			{"?", "aide"},
			{"q", "quitter"},
		}, m.width, right)
	}
	return renderFooterRefresh(m.width, m.refreshing)
}

// truncate coupe s à max caractères (suffixe « … ») en comptant des runes,
// pas des octets : couper au milieu d'un caractère multi-octets produirait
// un caractère invalide à l'affichage.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// ansiCSI repère les séquences ANSI/CSI (couleurs, déplacement du curseur,
// effacement de ligne/écran…) qu'émettent les programmes pensés pour un
// terminal interactif. On les retire en bloc : ne garder que le filtrage
// caractère par caractère ci-dessous laisserait leur résidu textuel
// (« [2K », « [1;1H »…) visible dans les logs.
var ansiCSI = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")

// sanitizeLogLine retire les séquences ANSI/escape et les caractères de
// contrôle qu'un conteneur peut écrire dans ses logs : non filtrés, ils
// corrompraient le rendu de la TUI, voire le terminal lui-même (titre,
// presse-papiers…).
//
// Un programme pensé pour un terminal interactif redessine une ligne de
// progression avec '\r' plutôt que '\n' : dans un vrai terminal, le curseur
// revient en colonne 0 et le texte suivant écrase l'ancien. Sans '\n' entre
// les deux, tout finit dans la même entrée de log ici ; on ne garde donc que
// ce qui suit le dernier '\r', soit ce qu'un terminal afficherait au final
// sur cette ligne.
func sanitizeLogLine(s string) string {
	if i := strings.LastIndexByte(s, '\r'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	s = ansiCSI.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// listPosition renvoie « i/N » pour la stack sous le curseur (indices dans la
// liste filtrée complète, repli ignoré pour que N reste stable).
func (m Model) listPosition() string {
	all := m.filteredStacks()
	if len(all) == 0 {
		return ""
	}
	if st, ok := m.cursorStack(); ok {
		for i, s := range all {
			if s.Name == st.Name {
				return fmt.Sprintf("%d/%d", i+1, len(all))
			}
		}
	}
	return fmt.Sprintf("%d stacks", len(all))
}

func clearStatusIn(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return clearStatusMsg{} })
}

func tickStacks() tea.Cmd {
	return tea.Tick(60*time.Second, func(time.Time) tea.Msg { return stackTickMsg{} })
}

func tickMetrics() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return metricsMsg(monitor.Collect())
	})
}

// armOrQuit applique la sortie en deux frappes : la première arme (désarmée
// après 2 s ou par une autre touche), la seconde quitte. Partagé par q dans la
// liste, ctrl+c globalement et q dans le dirpicker du premier lancement.
func (m Model) armOrQuit() (tea.Model, tea.Cmd) {
	if m.quitArmed {
		return m, tea.Quit
	}
	m.quitArmed = true
	m.quitSeq++
	seq := m.quitSeq
	return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return quitDisarmMsg(seq) })
}

// opInProgress dit si une opération court déjà : spinner du footer
// (capture/restauration) ou vue de progression pas encore terminée.
func (m Model) opInProgress() bool {
	return m.spinning || (m.view == viewProgress && !m.progressDone)
}

// refuseOp refuse de démarrer une opération quand une autre court déjà :
// deux ops simultanées pourraient viser les mêmes stacks (ex. une
// restauration et un Up), et la vue de progression n'affiche qu'un flux.
func (m Model) refuseOp() (tea.Model, tea.Cmd) {
	m.setStatus("Opération déjà en cours — attendre la fin", true)
	return m, clearStatusIn(3 * time.Second)
}

func (m Model) startOp(label string, op tea.Cmd) (tea.Model, tea.Cmd) {
	if m.opInProgress() {
		return m.refuseOp()
	}
	m.spinning = true
	m.opLabel = label
	m.status = ""
	return m, tea.Batch(op, m.spinner.Tick)
}

// leaveProgressView revient à la liste et libère l'état de progression : un
// gros pull peut accumuler des dizaines de milliers de ressources dans
// progressState, inutile de les retenir une fois de retour sur la liste.
func (m *Model) leaveProgressView() {
	m.view = viewList
	m.progress = nil
}

func (m Model) startProgressOp(title string, cancel context.CancelFunc, ch <-chan compose.ProgressEvent) (tea.Model, tea.Cmd) {
	m.progressTitle = title
	m.progress = newProgressState()
	m.progressScroll = 0
	m.progressManualScroll = false
	m.progressDone = false
	m.progressOk = 0
	m.progressTotal = 0
	m.progressCancel = cancel
	m.progressConfirmCancel = false
	m.progressCancelling = false
	m.spinning = false
	m.status = ""
	m.view = viewProgress
	m.progressCh = ch
	m.progressSeq++ // nouvelle op : périme les messages en vol de la précédente
	return m, tea.Batch(readProgressCmd(ch, m.progressSeq), m.spinner.Tick)
}

func (m Model) filterBar() string {
	// Visible pendant la saisie, et tant qu'un filtre validé reste appliqué.
	if !m.filtering && m.filter.Value() == "" {
		return ""
	}
	return m.filter.View()
}
