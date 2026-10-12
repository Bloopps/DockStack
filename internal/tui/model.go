package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
	"github.com/Bloopps/dockstack/internal/theme"
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
	viewCompose
	viewTheme
	viewCleanup
)

// ---- message types ----

type metricsMsg monitor.Metrics
type clientReadyMsg struct{ client dockerService }

// stacksLoadedMsg : résultat d'un chargement de la liste. dir et at (départ
// du chargement) permettent d'écarter un résultat périmé : chargement lancé
// avant un changement de dossier, ou doublé par un chargement plus récent.
type stacksLoadedMsg struct {
	stacks []compose.Stack
	err    error
	dir    string
	at     time.Time
}
type backupsLoadedMsg []backup.Snapshot
type opDoneMsg struct {
	err    error
	n      int // nombre de stacks capturées
	pruned int // anciennes captures supprimées par la rotation
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

	// Volet de détail à droite de la liste, qui suit le curseur : toujours
	// affiché quand la largeur le permet. previewCache évite de relire le
	// fichier compose à chaque frame.
	// previewCompose : le volet montre le fichier compose (tab) au lieu du
	// détail de la stack (services, état, actions), vue par défaut.
	previewCompose bool
	// Mode services : le focus est dans le volet, on agit sur des services
	// précis d'une stack (svcStack, par nom) sans toucher aux autres.
	svcFocus  bool
	svcStack  string
	svcCursor int
	svcSel    map[string]bool
	svcCache  *svcCacheBox
	// Filtre par nom (/) et inclusion des dépendances (+) du mode services.
	svcFilter    string
	svcFiltering bool
	svcDeps      bool
	previewCache *previewCacheBox

	// Vue compose plein écran (ouverte depuis le menu d'actions). Bascule entre
	// le fichier brut et la config résolue (docker compose config).
	composeStack    compose.Stack
	composeLines    []string
	composeScroll   int
	composeResolved bool
	composeErr      error
	// composeMouseOff relâche la capture souris dans la vue compose : la molette
	// ne défile plus, mais la sélection/copie native du terminal refonctionne
	// (tous les terminaux n'offrent pas le contournement Maj). Bascule : « m ».
	composeMouseOff bool

	lastRefresh time.Time // dernier chargement des stacks (affiché au footer)
	stacksAt    time.Time // départ du chargement affiché (cf. stacksLoadedMsg)
	manualR     bool      // refresh déclenché par R : confirmer la fin par un statut

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
	logStack  string
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
	// Saisie du libellé d'une capture (n) et bandeau « stacks à relancer » :
	// captureSeen le masque une fois la vue des captures ouverte.
	captureLabeling bool
	captureLabel    string
	captureSeen     bool

	// Restauration : sélection des stacks de la capture à relancer
	restoreLabel   string
	restoreEntries []captureEntry
	restoreSel     map[string]bool
	restoreCursor  int

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

	// Défilement de l'aide (?) quand elle dépasse la hauteur de l'écran.
	helpScroll int

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

	// Page « Nettoyage » (cleanupview.go). cleanupSeq écarte le résultat
	// d'une analyse abandonnée (page quittée, R).
	cleanupRep     *compose.CleanupReport
	cleanupErr     error
	cleanupLoading bool
	cleanupSeq     int
	cleanupCancel  context.CancelFunc
	cleanupCursor  int
	cleanupSel     map[string]bool
	cleanupOpen    map[compose.CleanupKind]bool
	// Nettoyage lancé : rapport d'avant et espace prévu, pour annoncer
	// l'espace réellement libéré à la réanalyse qui suit.
	cleanupBefore  *compose.CleanupReport
	cleanupPlanned int64
	cleanupApprox  bool // l'espace prévu est une estimation
	cleanupResult  string

	// progressReturn : vue rouverte en quittant la progression (la liste,
	// ou la page de nettoyage qui se réanalyse).
	progressReturn view

	// planSeq numérote la dernière prévision d'up demandée. Il avance à
	// chaque nouvelle demande et à chaque changement de vue au clavier : une
	// réponse arrivée après un aller-retour dans l'aide, ou doublée par un
	// second u, est écartée au lieu de lancer l'up sans nouvelle frappe.
	planSeq int

	// Confirmation (destructive actions)
	confirmLabel   string
	confirmWarning string
	confirmNames   []string
	confirmExec    func(Model) (tea.Model, tea.Cmd)
	confirmReturn  view // vue de retour si l'utilisateur annule
	// confirmDanger : l'action efface des données, seul y la confirme.
	confirmDanger bool

	// Metrics
	metrics monitor.Metrics
	history metricsHistory

	// Occupation Docker de l'en-tête (nil tant qu'inconnue). usageBusy évite
	// d'empiler des requêtes si le démon est lent ; volumesAt date la
	// dernière mesure des volumes (coûteuse, espacée). imagesStale : un
	// événement a pu changer les images depuis leur dernière mesure.
	usage       *compose.DockerUsage
	usageBusy   bool
	volumesAt   time.Time
	imagesStale bool

	// Consommation CPU/RAM par conteneur (ID), lue pour les lignes à l'écran
	// (cf. stats.go). statsBusy évite d'empiler les lectures.
	ctrUsage  map[string]compose.ContainerUsage
	statsBusy bool

	// Terminal
	width  int
	height int

	// Thème de couleurs : themeName est le thème affiché (aperçu compris).
	// Sélecteur (t) : catalogue chargé à l'ouverture, filtre texte et par
	// luminosité, thème d'avant l'ouverture rétabli par esc.
	themeName      string
	themes         []theme.Palette
	themeErrs      []error
	themeOrig      string
	themeCursor    int
	themeFilter    string
	themeFiltering bool
	themeKind      int

	loading bool
	err     error
}

func New(cfg *config.Config) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filtrer..."
	ti.CharLimit = 64

	m := Model{
		cfg:          cfg,
		spinner:      sp,
		filter:       ti,
		selected:     make(map[string]bool),
		foldedGroups: make(map[string]bool),
		rowsCache:    &rowsCacheBox{},
		previewCache: &previewCacheBox{},
		svcCache:     &svcCacheBox{},
		loading:      true,
	}
	m.loadTheme()
	return m
}

// restyleWidgets recopie les couleurs du thème actif dans les composants qui
// gardent leur propre style (spinner, champ de filtre).
func (m *Model) restyleWidgets() {
	m.spinner.Style = styleCyan
	// v2: per-state styling via the Styles struct instead of PromptStyle/TextStyle/…
	tiStyles := m.filter.Styles()
	tiStyles.Focused.Prompt = styleCyan
	tiStyles.Focused.Text = lipgloss.NewStyle()
	tiStyles.Focused.Placeholder = styleDim
	tiStyles.Blurred.Prompt = styleCyan
	tiStyles.Blurred.Placeholder = styleDim
	m.filter.SetStyles(tiStyles)
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
		tickStats(statsFirstDelay),
		loadBackups(m.cfg.ConfigDir()), // pour le bandeau « stacks à relancer »
		m.startupStatusClear(),
	)
}

// startupStatusClear efface l'éventuel avertissement de New (thème de la
// config introuvable) après quelques secondes.
func (m Model) startupStatusClear() tea.Cmd {
	if m.status == "" {
		return nil
	}
	return clearStatusIn(6 * time.Second)
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
		// Connexion déjà établie (deux R rapprochés pendant l'init, ou init de
		// démarrage + R) : ignorer le doublon, sinon chaque client lancerait
		// son abonnement aux événements et son minuteur d'occupation.
		if m.client != nil {
			return m, nil
		}
		m.client = msg.client
		m.err = nil
		if m.cfg.StackDir == "" {
			m.loading = false
			m.view = viewDirPicker
			usage := m.startUsage()
			return m, tea.Batch(loadDirCmd("/"), subscribeDockerEvents(m.client),
				usage, tickUsage())
		}
		// La liste est l'écran d'accueil ; les stacks se chargent en arrière-plan
		m.view = viewList
		usage := m.startUsage()
		return m, tea.Batch(m.loadStacksCmd(), subscribeDockerEvents(m.client),
			usage, tickUsage())

	case stacksLoadedMsg:
		// Résultat périmé : un autre dossier (changé entre-temps), ou un
		// chargement parti avant celui déjà affiché. L'appliquer montrerait
		// des stacks d'un autre dossier, sur lesquelles les actions porteraient.
		if msg.dir != m.cfg.StackDir || msg.at.Before(m.stacksAt) {
			return m, nil
		}
		m.stacksAt = msg.at
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			m.refreshing = false
			m.manualR = false
			return m, nil
		}
		// Restaure la position du curseur par identité de ligne (stack ou
		// en-tête de groupe) : les indices bougent à chaque re-tri/refresh.
		anchor := m.cursorAnchor()
		// Ne pas toucher à m.spinning : il appartient aux opérations backup
		// (startOp/opDoneMsg) ; un refresh auto qui aboutit pendant une
		// capture/restauration effacerait son spinner avant la fin.
		m.stacks = sortByState(msg.stacks)
		m.invalidateRows()
		// Un chargement réussi efface l'erreur précédente : sans ça, un raté
		// transitoire (ex: daemon redémarré) laisserait l'écran d'erreur
		// affiché à vie alors que les refresh suivants aboutissent.
		m.err = nil
		m.loading = false
		m.refreshing = false
		m.lastRefresh = time.Now()
		m.seekCursor(anchor)
		// Un refresh manuel (R) confirme sa fin ; l'auto-refresh reste discret.
		if m.manualR {
			m.manualR = false
			m.setStatus("Liste actualisée", false)
			return m, clearStatusIn(2 * time.Second)
		}

	case usageMsg:
		m.usageBusy = false
		if msg.err != nil {
			// On garde la dernière mesure ; nouvel essai au tick, images
			// comprises si elles étaient demandées.
			if msg.opts.Images {
				m.imagesStale = true
			}
			return m, nil
		}
		u := msg.usage
		if u.VolumesOK {
			m.volumesAt = time.Now()
		} else if m.usage != nil && m.usage.VolumesOK {
			u.VolumesSize, u.VolumesOK = m.usage.VolumesSize, true
			u.VolumesByProject = m.usage.VolumesByProject
			u.LayersByProject = m.usage.LayersByProject
		}
		if !u.ImagesOK && m.usage != nil && m.usage.ImagesOK {
			u.ImagesSize, u.ImagesReclaimable, u.ImagesOK = m.usage.ImagesSize, m.usage.ImagesReclaimable, true
			u.ImagesExact = m.usage.ImagesExact
		}
		if u.RootDir != "" && (m.usage == nil || m.usage.RootDir != u.RootDir) {
			monitor.SetDiskPath(u.RootDir)
		}
		m.usage = &u

	case statsTickMsg:
		return m.onStatsTick()

	case statsMsg:
		m = m.onStatsResult(msg)

	case usageTickMsg:
		if m.client == nil || m.usageBusy {
			return m, tickUsage()
		}
		usage := m.startUsage()
		return m, tea.Batch(usage, tickUsage())

	case imagesRefreshMsg:
		// Mesure en cours : imagesStale reste levé, le prochain tick s'en
		// chargera.
		if m.client == nil || m.usageBusy || !m.imagesStale {
			return m, nil
		}
		usage := m.startUsage()
		return m, usage

	case backupsLoadedMsg:
		m.backups = []backup.Snapshot(msg)
		// Après une suppression, le curseur ne doit pas pointer au-delà.
		m.actionCursor = max(0, min(m.actionCursor, len(m.backups)-1))

	case backupDeletedMsg:
		if msg.err != nil {
			m.setStatus("Suppression : "+msg.err.Error(), true)
		} else {
			m.setStatus("Capture supprimée", false)
		}
		return m, tea.Batch(clearStatusIn(4*time.Second), loadBackups(m.cfg.ConfigDir()))

	case backupMetaMsg:
		if msg.err != nil {
			m.setStatus("Capture : "+msg.err.Error(), true)
		} else {
			m.setStatus(msg.done, false)
		}
		return m, tea.Batch(clearStatusIn(4*time.Second), loadBackups(m.cfg.ConfigDir()))

	case opDoneMsg:
		// Résultat d'une capture (les actions sur les stacks et la restauration
		// passent par la vue de progression). La vue n'est pas forcée :
		// l'utilisateur a pu naviguer ailleurs pendant l'écriture.
		m.spinning = false
		if msg.err != nil {
			m.setStatus(m.opLabel+" : "+msg.err.Error(), true)
			return m, tea.Batch(clearStatusIn(6*time.Second), m.loadStacksCmd())
		}
		// Le footer préfixe déjà « ✓ ».
		status := fmt.Sprintf("Capture enregistrée — %d stack(s) en marche", msg.n)
		if msg.pruned > 0 {
			status += fmt.Sprintf(" · %d ancienne(s) supprimée(s) (rotation, épingler pour garder)", msg.pruned)
		}
		m.setStatus(status, false)
		// Recharge la liste des captures : la nouvelle arrive en tête, le
		// curseur s'y pose pour que l'utilisateur la voie.
		if m.view == viewBackup {
			m.actionCursor = 0
		}
		return m, tea.Batch(clearStatusIn(4*time.Second), m.loadStacksCmd(), loadBackups(m.cfg.ConfigDir()))

	case progressEventMsg:
		// Message d'une op périmée : ne pas l'appliquer ni ré-armer sa pompe
		// (le verrou d'op rend le cas improbable, le seq le rend inoffensif).
		if msg.seq != m.progressSeq {
			return m, nil
		}
		for _, ev := range msg.evs {
			if m.progress != nil {
				m.progress.apply(ev)
			}
			if ev.StackDone {
				m.progressTotal++
				if ev.Err == nil {
					m.progressOk++
				}
			}
		}
		if msg.done {
			return m.progressFinished()
		}
		return m, readProgressCmd(m.progressCh, msg.seq)

	case planUpMsg:
		return m.handlePlanUp(msg)

	case cleanupLoadedMsg:
		return m.handleCleanupLoaded(msg)

	case progressAllDoneMsg:
		if msg.seq != m.progressSeq {
			return m, nil
		}
		return m.progressFinished()

	case logLinesMsg:
		if msg.seq != m.logSeq {
			return m, nil // session de logs périmée : ne pas réarmer la lecture
		}
		for _, entry := range msg.entries {
			line := "[" + sanitizeLogLine(entry.Name) + "] " + sanitizeLogLine(entry.Line)
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
			return m, tea.Batch(m.loadStacksCmd(), tickStacks())
		}
		return m, tickStacks()

	case dockerEventsStartedMsg:
		m.eventsCh = msg.ch
		return m, readDockerEventCmd(msg.ch)

	case dockerEventMsg:
		cmds := []tea.Cmd{readDockerEventCmd(m.eventsCh)}
		ev := compose.DockerEvent(msg)
		if compose.ImagesChanged(ev) && !m.imagesStale {
			m.imagesStale = true
			cmds = append(cmds, refreshImagesIn(imagesDelay))
		}
		if compose.IsRelevantDockerEvent(ev) {
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
			return m, m.loadStacksCmd()
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
		if m.spinning || (m.view == viewProgress && !m.progressDone) || (m.view == viewCleanup && m.cleanupLoading) {
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
		if m.view == viewCompose {
			// Molette : 3 lignes par cran (lire un fichier ligne à ligne serait
			// pénible), bornée comme le défilement clavier.
			const step = 3
			switch msg.Button {
			case tea.MouseWheelUp:
				m.composeScroll -= step
				if m.composeScroll < 0 {
					m.composeScroll = 0
				}
			case tea.MouseWheelDown:
				m.composeScroll += step
				if mx := m.composeMaxScroll(); m.composeScroll > mx {
					m.composeScroll = mx
				}
			}
			return m, nil
		}

	case tea.PasteMsg:
		return m.handlePaste(msg.Content)

	case tea.KeyPressMsg:
		// Changer de vue abandonne la prévision d'up en cours (voir planSeq),
		// sauf si c'est cette frappe même qui l'a demandée.
		from, seq := m.view, m.planSeq
		next, cmd := m.handleKey(msg)
		if nm, ok := next.(Model); ok && nm.view != from && nm.planSeq == seq {
			nm.planSeq++
			if nm.status == planStatus {
				nm.status = ""
			}
			return nm, cmd
		}
		return next, cmd
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Q (Maj ou verrouillage des majuscules) vaut q partout — sauf pendant une
	// saisie de texte, où c'est une lettre comme une autre.
	if msg.String() == "Q" && !m.typing() {
		msg = tea.KeyPressMsg{Code: 'q', Text: "q"}
	}
	if m.view == viewProgress {
		// Toute touche autre que q désarme la sortie, comme ailleurs : sans
		// ça, q puis esc puis q quittait depuis la liste.
		if msg.String() != "q" {
			m.quitArmed = false
		}
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
		case "g", "home":
			// Haut, comme partout ailleurs (g descendait en bas ici).
			m.progressScroll = m.progressMaxScroll()
			m.progressManualScroll = m.progressScroll > 0
		case "G", "end":
			m.progressScroll = 0
			m.progressManualScroll = false
		case "q":
			// Jamais de sortie en pleine opération : esc l'annule proprement.
			if m.progressDone {
				return m.armOrQuit()
			}
		case "esc", "ctrl+c":
			if m.progressDone {
				m.leaveProgressView()
				if m.view == viewCleanup {
					return m.startCleanupAnalysis()
				}
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
				if m.view == viewCleanup {
					return m.startCleanupAnalysis()
				}
			}
		default:
			// Toute autre touche désarme la confirmation d'annulation.
			m.progressConfirmCancel = false
		}
		return m, nil
	}
	// t ouvre le sélecteur de thème depuis la liste, mode services compris,
	// et depuis l'aide. T aussi (verrouillage des majuscules), comme Q. Pas
	// pendant une saisie : c'est alors une lettre du filtre.
	if k := msg.String(); (k == "t" || k == "T") && (m.view == viewList || m.view == viewHelp) && !m.typing() {
		m.quitArmed = false
		return m.openThemePicker()
	}
	// L'aide défile (↑↓) si elle dépasse l'écran et se ferme sur toute autre
	// touche (avant l'intercept global de r).
	if m.view == viewHelp {
		switch msg.String() {
		case "up", "k":
			m.helpScroll = max(m.helpScroll-1, 0)
		case "down", "j":
			m.helpScroll = min(m.helpScroll+1, m.helpMaxScroll())
		case "pgup":
			m.helpScroll = max(m.helpScroll-m.bodyHeight()/2, 0)
		case "pgdown", "space":
			m.helpScroll = min(m.helpScroll+m.bodyHeight()/2, m.helpMaxScroll())
		default:
			m.view = viewList
		}
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
	if msg.String() == "R" && !m.typing() && m.view != viewCleanup {
		if m.client == nil {
			m.loading = true
			m.err = nil
			return m, initClient()
		}
		if m.cfg.StackDir != "" {
			compose.InvalidateComposeCache()
			m.refreshing = true
			m.manualR = true
			return m, m.loadStacksCmd()
		}
	}
	// q quitte partout (deux frappes), esc revient : règle unique pour toutes
	// les vues. La liste et le mode services traitent q eux-mêmes (filtres
	// texte) ; la saisie d'un libellé de capture garde q comme lettre.
	if msg.String() == "q" && m.view != viewList && !m.typing() {
		return m.armOrQuit()
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
	case viewCompose:
		return m.handleComposeKey(msg)
	case viewTheme:
		return m.handleThemeKey(msg)
	case viewCleanup:
		return m.handleCleanupKey(msg)
	}
	return m, nil
}

// View wraps the rendered content in a tea.View. In bubbletea v2 the alt-screen
// and mouse mode are carried by the View itself (not program options).
func (m Model) View() tea.View {
	v := tea.NewView(m.viewContent())
	v.AltScreen = true
	// Thème Ghostty : fond et texte par défaut du terminal pris au thème
	// (bubbletea les rétablit en sortie). nil avec le thème par défaut.
	v.BackgroundColor = activeScheme.Bg
	v.ForegroundColor = activeScheme.Fg
	// Capture the mouse only in the progress view while the operation runs
	// (for wheel scrolling). Once it's done — and everywhere else — leave the
	// mouse free so the terminal's native text selection / copy works, e.g.
	// to copy an error message from the result. Keyboard scroll (↑↓/j/k)
	// still works after completion.
	if (m.view == viewProgress && !m.progressDone) || (m.view == viewCompose && !m.composeMouseOff) {
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
	case viewCompose:
		return renderComposeView(m)
	}

	header := newBlock(renderHeader(m.metrics, m.history, m.headerCounts(), m.usage, m.width))

	footerH := 1
	bodyH := m.height - len(header.lines) - footerH
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
	case viewTheme:
		body = renderThemePicker(m, bodyH)
	case viewCleanup:
		body = renderCleanup(m, bodyH)
	default:
		if m.loading {
			body = styleDim.Render("\n  Chargement...")
		} else if m.err != nil {
			body = renderError(m.err, m.width)
		} else {
			// Bandeau éventuel : une ligne prise sur la hauteur de la liste.
			banner := m.captureBanner()
			listH := bodyH
			if banner != "" {
				listH--
			}
			list := m.listBlock(listH)
			if banner != "" {
				list.prepend(newBlock(banner))
			}
			list.fit(bodyH)
			return stackBlocks(header, list, newBlock(m.renderFooter()))
		}
	}

	b := newBlock(body)
	b.fit(bodyH)
	return stackBlocks(header, b, newBlock(m.renderFooter()))
}

// captureBanner : rappel, au-dessus de la liste, des stacks de la dernière
// capture qui ne tournent plus (typiquement après un redémarrage du serveur).
// Masqué une fois la vue des captures ouverte, ou quand tout tourne.
func (m Model) captureBanner() string {
	if m.captureSeen || m.view != viewList || len(m.backups) == 0 || len(m.stacks) == 0 {
		return ""
	}
	snap := m.backups[0]
	if snap.StackDir != "" && snap.StackDir != m.cfg.StackDir {
		return ""
	}
	n := 0
	for _, e := range diffCapture(snap.Items(), m.stacks) {
		if e.kind == entryRelaunch {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	txt := fmt.Sprintf("↻ %d stack%s de la dernière capture (%s) ne tourne%s plus",
		n, plural(n), snap.Date.Format("02/01 15:04"), map[bool]string{true: "nt", false: ""}[n > 1])
	return "  " + styleYellow.Render(truncate(txt, max(m.width-24, 10))) + styleDim.Render("  ·  ") + styleCyan.Render("b") + styleDim.Render(" pour les relancer")
}

// fitHeight complète (ou coupe) s à exactement h lignes : sans ça, une vue
// plus courte que l'écran remonte le footer au milieu de l'écran. Une hauteur
// négative (terminal moins haut que l'en-tête) vaut 0 : la vue de progression
// paniquait, et l'application quittait en pleine opération.
func fitHeight(s string, h int) string {
	h = max(h, 0)
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// renderError : écran d'erreur de la liste. width sert à replier le message,
// souvent long (« failed to connect to the docker API at … »), que le
// terminal coupait sinon en bout de ligne.
func renderError(err error, width int) string {
	msg := err.Error()
	// « permission denied » seul viserait aussi un dossier de stacks illisible,
	// et « docker » n'importe où un chemin comme /srv/docker : on reconnaît
	// les messages du client Docker (« … connect to the docker API at … »,
	// « … connect to the Docker daemon socket … »).
	lower := strings.ToLower(msg)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(activeScheme.Danger).
		Padding(0, 2).
		MarginLeft(2)
	if strings.Contains(lower, "permission denied") && strings.Contains(lower, "connect to the docker") {
		content := strings.Join([]string{
			styleRed.Bold(true).Render("Accès Docker refusé"),
			"",
			// Pas de « sudo dockstack » : lancé en root, l'outil crée ~/.dockstack
			// en root et l'utilisateur ne peut plus y écrire ses captures ensuite.
			styleDim.Render("Rejoindre le groupe :") + "  " + styleYellow.Render("sudo usermod -aG docker $USER  && newgrp docker"),
			styleDim.Render("Ou démarrer le daemon :") + "  " + styleYellow.Render("sudo systemctl start docker"),
		}, "\n")

		return "\n" + box.Render(content)
	}
	// Démon arrêté, tombé en cours de route ou muet. Avant, la seule présence
	// de « docker.sock » dans le message (démon arrêté, ou flux coupé : EOF)
	// affichait « Accès Docker refusé » et le conseil de rejoindre le groupe
	// docker, sans le message lui-même.
	if daemonUnreachable(lower) {
		inner := max(width-8, 20)
		rows := []string{
			styleRed.Bold(true).Render("Démon Docker injoignable"),
			"",
			styleDim.Render("Démarrer le daemon :") + "  " + styleYellow.Render("sudo systemctl start docker"),
			styleDim.Render("puis R pour réessayer"),
			"",
		}
		for _, l := range wordWrap(msg, inner) {
			rows = append(rows, styleDim.Render(l))
		}
		return "\n" + box.Render(strings.Join(rows, "\n"))
	}
	lines := wordWrap("Erreur: "+msg, max(width-4, 20))
	return styleRed.Render("\n  " + strings.Join(lines, "\n  "))
}

// pasteMax borne un collage : les champs de saisie sont courts (le filtre de
// la liste plafonne à 64 caractères), et un long collage repassé caractère
// par caractère refiltrerait la liste à chaque fois.
const pasteMax = 64

// handlePaste insère un texte collé (bracketed paste, actif par défaut dans
// bubbletea v2) dans le champ en cours de saisie. Avant, le message était
// ignoré et coller ne faisait rien. Hors saisie, le collage est ignoré : il
// ne doit jamais devenir une suite de raccourcis (u, d, r…).
func (m Model) handlePaste(text string) (tea.Model, tea.Cmd) {
	if !m.typing() {
		return m, nil
	}
	// Une seule ligne : sauts de ligne et tabulations deviennent des espaces,
	// les autres caractères de contrôle (séquences d'échappement) disparaissent.
	var b strings.Builder
	n := 0
	for _, r := range text {
		if n >= pasteMax {
			break
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			r = ' '
		case unicode.IsControl(r):
			continue
		}
		b.WriteRune(r)
		n++
	}
	clean := b.String()
	if m.filtering {
		// Le champ du filtre gère lui-même le collage (et son plafond).
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(tea.PasteMsg{Content: clean})
		m.invalidateRows()
		m.cursor = 0
		return m, cmd
	}
	var next tea.Model = m
	for _, r := range clean {
		mm, ok := next.(Model)
		if !ok || !mm.typing() {
			break
		}
		next, _ = mm.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return next, nil
}

// daemonUnreachable reconnaît, dans un message en minuscules, les erreurs de
// connexion du client Docker (démon arrêté, connexion coupée) et la prise de
// contact sans réponse de compose.New.
func daemonUnreachable(lower string) bool {
	for _, k := range []string{"cannot connect to the docker daemon", "failed to connect to the docker api", "error during connect", "le démon docker ne répond pas"} {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
}

func (m Model) logsHeight() int {
	return m.height - 3
}

// progressFinished : fin de l'opération en cours (sentinelle AllDone lue).
func (m Model) progressFinished() (tea.Model, tea.Cmd) {
	m.progressDone = true
	m.progressCh = nil
	if m.progressCancel != nil {
		m.progressCancel()
		m.progressCancel = nil
	}
	return m, m.loadStacksCmd()
}

// headerCounts : compteurs de l'en-tête, marqués inconnus tant qu'aucune
// liste n'a pu être lue.
func (m Model) headerCounts() StackCounts {
	sc := m.stackCounts()
	sc.Unknown = len(m.stacks) == 0 && (m.loading || m.err != nil)
	return sc
}

func (m Model) stackCounts() StackCounts {
	if m.rowsCache.countsOK {
		return m.rowsCache.counts
	}
	sc := StackCounts{Dir: m.cfg.StackDir, Total: len(m.stacks)}
	for _, s := range m.stacks {
		sc.CtrRunning += s.Running
		sc.CtrTotal += s.Total
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
		return quitWarning()
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
		bar := styleCyan.
			Bold(true).
			PaddingLeft(2).
			Render("▶ " + m.opLabel + "  " + m.spinner.View())
		return bar
	}
	if m.err != nil {
		return renderBar([]footerKey{{"R", "réessayer"}, {"o", "dossier"}}, []footerKey{{"q", "quitter"}}, m.width, "")
	}
	back := []footerKey{{"esc", "retour"}}
	switch m.view {
	case viewConfirm:
		return renderBar([]footerKey{{m.confirmKeys(), "confirmer"}}, []footerKey{{"n / esc", "annuler"}}, m.width, "")
	case viewBackup:
		if m.captureLabeling {
			return renderBar([]footerKey{{"↩", "valider"}}, []footerKey{{"esc", "annuler"}}, m.width, "")
		}
		left := []footerKey{{"c", "capturer"}}
		if len(m.backups) > 0 {
			left = append(left, footerKey{"↩", "relancer"}, footerKey{"i", "à l'identique"},
				footerKey{"n", "nommer"}, footerKey{"*", "épingler"}, footerKey{"⌫", "supprimer"})
		}
		return renderBar(left, back, m.width, "")
	case viewAction:
		return renderBar([]footerKey{{"↑↓", "choisir"}, {"↩ / lettre", "valider"}}, back, m.width, "")
	case viewRestorePick:
		n := 0
		for _, v := range m.restoreSel {
			if v {
				n++
			}
		}
		return renderBar([]footerKey{{"␣", "cocher"}, {"ctrl+a", "tout"}, {"↩", fmt.Sprintf("relancer %d stack%s", n, plural(n))}},
			back, m.width, "")
	case viewTheme:
		left, right, info := m.themeFooter()
		return renderBar(left, right, m.width, info)
	case viewCleanup:
		return m.cleanupFooter()
	case viewHelp:
		var left []footerKey
		if m.helpMaxScroll() > 0 {
			left = []footerKey{{"↑↓", "défiler"}}
		}
		return renderBar(left, []footerKey{{"esc", "fermer"}}, m.width, "")
	case viewList:
		// Tout à droite : état du rafraîchissement puis position dans la liste.
		info := m.listPosition()
		switch {
		case m.refreshing:
			info = "↻ actualisation…   " + info
		case !m.lastRefresh.IsZero():
			info = "maj " + m.lastRefresh.Format("15:04:05") + "   " + info
		}
		left, right := m.listFooter()
		return renderBar(left, right, m.width, info)
	}
	return renderBar(nil, []footerKey{{"?", "aide"}, {"q", "quitter"}}, m.width, "")
}

// truncate coupe s à max caractères (suffixe « … ») en comptant des runes,
// pas des octets : couper au milieu d'un caractère multi-octets produirait
// un caractère invalide à l'affichage.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	// Largeur affichée, pas nombre de caractères : un caractère large (CJK,
	// emoji) occupe deux colonnes, et un nom qui en contenait sortait deux
	// fois trop large.
	return ansi.Truncate(s, max, "…")
}

// ansiSeq repère les séquences d'échappement qu'émettent les programmes
// pensés pour un terminal interactif : CSI (couleurs, déplacement du
// curseur, effacement…), OSC (titre de fenêtre, lien, presse-papiers via
// OSC 52), terminée par BEL ou ST, et les séquences à deux caractères. On
// les retire en bloc : ne garder que le filtrage caractère par caractère
// ci-dessous laisserait leur résidu textuel (« [2K », « ]0;titre »…)
// visible dans les logs.
var ansiSeq = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[@-Z\\\\-_])")

// sanitizeLogLine retire les séquences ANSI/escape et les caractères de
// contrôle qu'un conteneur peut écrire dans ses logs, et convertit les
// tabulations en espaces : non filtrés, ils
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
	// L'expression régulière ne sert que s'il y a une séquence d'échappement :
	// c'est rare, et elle coûtait autant que tout le reste pour chaque ligne.
	if strings.IndexByte(s, 0x1b) >= 0 {
		s = ansiSeq.ReplaceAllString(s, "")
	}
	// unicode.IsControl couvre aussi les contrôles C1 (U+0080 à U+009F) :
	// certains terminaux lisent U+009B comme un CSI, une séquence pouvait
	// donc passer sans caractère d'échappement.
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return expandTabs(s)
}

// sanitizeText rend affichable sur une ligne un texte venu de l'extérieur
// (message d'erreur du démon ou d'un registre, étiquette d'image, nom de
// dossier) : fins de ligne remplacées par des espaces, séquences
// d'échappement et caractères de contrôle retirés. Un texte en ASCII
// imprimable, le cas courant, est rendu tel quel sans allocation.
func sanitizeText(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	return sanitizeLogLine(lineBreaks.Replace(s))
}

// lineBreaks : fins de ligne d'un texte rendu sur une seule ligne.
var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// expandTabs remplace les tabulations par des espaces jusqu'au taquet suivant
// (tous les 8 caractères) : le moteur de rendu ignore '\t', ce qui collait les
// colonnes (« clé\tvaleur » affiché « clévaleur »).
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

// listPosition renvoie « i/N » pour la stack sous le curseur (indices dans la
// liste filtrée complète, repli ignoré pour que N reste stable).
func (m Model) listPosition() string {
	all := m.shownStacks()
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

// quitWarning : avertissement de sortie armée. Toutes les vues l'affichent,
// y compris celles qui ont leur propre pied de page (logs, compose,
// progression, dossiers) : sinon le second q quittait sans prévenir.
func quitWarning() string {
	return styleYellow.Bold(true).PaddingLeft(2).Render("⚠ Appuyer à nouveau pour quitter (q / ctrl+c)") +
		styleDim.Render("  ·  autre touche = annuler")
}

// typing dit si une saisie de texte est en cours (filtre de la liste, filtre
// des services, nom de capture) : les raccourcis globaux (R, Q) y sont des
// lettres ordinaires.
func (m Model) typing() bool {
	return m.filtering || (m.svcFocus && m.svcFiltering) || m.captureLabeling ||
		(m.view == viewTheme && m.themeFiltering)
}

// bodyHeight : hauteur du corps des vues sous l'en-tête (footer d'une ligne).
func (m Model) bodyHeight() int {
	return max(m.height-headerHeight(m.metrics.NCores, m.width)-1, 0)
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

// rowAnchor identifie la ligne sous le curseur indépendamment de son index.
type rowAnchor struct{ stack, group string }

func (m Model) cursorAnchor() rowAnchor {
	row, ok := m.cursorRow()
	switch {
	case !ok || row.sep || row.more:
		return rowAnchor{}
	case row.header:
		return rowAnchor{group: row.group}
	default:
		return rowAnchor{stack: row.stack.Name}
	}
}

// seekCursor replace le curseur sur la ligne identifiée par a (tête de liste
// si elle a disparu).
func (m *Model) seekCursor(a rowAnchor) {
	m.cursor = 0
	for i, r := range m.listRows() {
		if (a.stack != "" && r.isStack() && r.stack.Name == a.stack) ||
			(a.group != "" && r.header && r.group == a.group) {
			m.cursor = i
			return
		}
	}
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
	m.view = m.progressReturn
	m.progressReturn = viewList
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
	m.progressReturn = viewList
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

// startUsage lance une mesure d'occupation : volumes (et images) quand leur
// cadence est échue, images aussi quand un événement a pu les changer ou
// qu'elles sont encore inconnues. L'appelant vérifie client et usageBusy.
func (m *Model) startUsage() tea.Cmd {
	opts := compose.UsageOptions{Volumes: time.Since(m.volumesAt) >= volumesInterval}
	opts.Images = opts.Volumes || m.imagesStale || m.usage == nil || !m.usage.ImagesOK
	if opts.Images {
		m.imagesStale = false
	}
	m.usageBusy = true
	return fetchUsage(m.client, opts)
}
