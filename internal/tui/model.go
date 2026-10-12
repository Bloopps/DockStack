package tui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
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

// clearStatusMsg efface le statut s'il n'a pas changé depuis (seq vaut
// statusSeq au moment de l'affichage) : sinon le minuteur d'un ancien
// message effaçait le suivant, par exemple les 2 s de « Liste actualisée »
// emportaient l'analyse d'un up lancée entre-temps.
type clearStatusMsg struct{ seq int }
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

	lastRefresh time.Time // dernier chargement des stacks (affiché dans l'en-tête)
	stacksAt    time.Time // départ du chargement affiché (cf. stacksLoadedMsg)
	manualR     bool      // refresh déclenché par R : confirmer la fin par un statut
	// listDirty : un événement Docker ou le tick périodique est arrivé
	// pendant une vue où la liste ne se recharge pas (cf. stackTickMsg) ; elle
	// sera rechargée au retour, au lieu de rester fausse jusqu'au tick suivant.
	// listDirtyAt date le dernier : un chargement parti après lui suffit.
	listDirty   bool
	listDirtyAt time.Time

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
	logStack string
	logLines []logLine
	// logScroll : première ligne affichée, en lignes visibles (celles que
	// retient le filtre), pas en lignes physiques (cf. logsMaxScroll).
	logScroll int
	logCh     <-chan compose.LogEntry
	logCancel context.CancelFunc
	logSeq    int
	// Suivi du flux. La valeur zéro suit le direct : chaque lot recolle la
	// vue en bas. En pause (on a remonté), la vue ne bouge plus et les lignes
	// arrivées depuis logPauseAt (indice dans logLines) sont comptées ;
	// logAnchor (indice dans logLines) est la ligne où reprendre quand le
	// filtre change. logEnded : le flux s'est fermé (stack sans conteneur,
	// conteneurs arrêtés ou supprimés, démon perdu).
	logPaused  bool
	logPauseAt int
	logAnchor  int
	logEnded   bool
	// logNoCtr : les services suivis n'avaient aucun conteneur à l'ouverture,
	// ce qui explique un flux fermé sans une ligne.
	logNoCtr bool
	// Préfixes : logNames donne le nom affiché de chaque source (logAlias
	// raccourcit « svc-1 » quand le service n'a qu'un conteneur), logNameW la
	// largeur du plus long, sur laquelle les messages s'alignent.
	logNames map[string]string
	logAlias map[string]string
	logNameW int
	// Filtre (/) : logMatch liste, dans l'ordre, les indices des lignes de
	// logLines qui le contiennent. logWrap replie les lignes longues au lieu
	// de les couper ; il est gardé d'une session à l'autre.
	logFilter    string
	logFiltering bool
	logMatch     []int
	logWrap      bool

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

	// Barre de statut (éphémère). statusSeq avance à chaque nouveau statut,
	// comme quitSeq : un minuteur d'effacement périmé est ignoré.
	status     string
	statusKind statusKind
	statusSeq  int

	// Défilement de l'aide (?) quand elle dépasse la hauteur de l'écran, et
	// vue où la fermer (elle s'ouvre de partout).
	helpScroll int
	helpReturn view

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
	// Début et fin de l'opération : temps écoulé dans le titre, durée au
	// bilan (figée à la fin).
	progressStart time.Time
	progressEnd   time.Time

	// lastFail : dernier échec de chaque stack, gardé après la fermeture de
	// la progression pour que le volet en dise la cause. Effacé par une
	// réussite de la même stack et au changement de dossier.
	lastFail map[string]stackFailure

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
	cleanupTone    tone // couleur de cleanupResult : réussite, échec partiel, échec
	// cleanupOutcome : issue de la progression, relevée en la quittant. La
	// réanalyse chiffre l'espace libéré, seule la progression savait ce qui
	// avait échoué ou été annulé.
	cleanupOutcome cleanupOutcome

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
	confirmEffect  string // ce que fait l'action (cf. actionEffect), en gris
	confirmNames   []string
	confirmScroll  int // première ligne affichée de confirmNames (↑↓ pour relire)
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
	usageAt     time.Time // dernière mesure réussie (en-tête, démon injoignable)
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

	// loading : premier chargement (connexion au démon puis lecture des
	// stacks) ; loadingSince date l'étape en cours, affichée avec sa durée.
	loading      bool
	loadingSince time.Time
	err          error
}

// statusKind : nature du message de la barre de statut, qui fixe son
// symbole. Une coche verte pour une attente ou un simple refus faisait
// croire à une réussite, et une croix rouge pour un rappel à une panne.
type statusKind int

const (
	statusOK   statusKind = iota // ✓ vert : action réussie
	statusErr                    // ✕ rouge : échec
	statusInfo                   // › cyan : rappel ou refus sans gravité
	statusBusy                   // spinner cyan : attente en cours, sans minuteur
)

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
		loadingSince: time.Now(),
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
		m.spinner.Tick, // anime l'écran de chargement (cf. spinner.TickMsg)
	)
}

// startupStatusClear efface l'éventuel avertissement de New (thème de la
// config introuvable) après quelques secondes.
func (m Model) startupStatusClear() tea.Cmd {
	if m.status == "" {
		return nil
	}
	return m.clearStatusIn(6 * time.Second)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Le bas des logs dépend de la hauteur, et de la largeur une fois les
		// lignes repliées : sans recalcul, un terminal réduit cachait la fin
		// du direct.
		if m.view == viewLogs {
			m.syncLogScroll()
		}

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
			return m, tea.Batch(loadDirCmd(pickerStart()), subscribeDockerEvents(m.client),
				usage, tickUsage())
		}
		// La liste est l'écran d'accueil ; les stacks se chargent en arrière-plan
		m.view = viewList
		m.loadingSince = time.Now() // étape suivante : lecture des stacks
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
		if m.listDirty && !msg.at.Before(m.listDirtyAt) {
			m.listDirty = false // chargement parti après le dernier événement
		}
		m.seekCursor(anchor)
		// Un refresh manuel (R) confirme sa fin ; l'auto-refresh reste discret.
		if m.manualR {
			m.manualR = false
			m.setStatus("Liste actualisée", false)
			return m, m.clearStatusIn(2 * time.Second)
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
		m.usageAt = time.Now()
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
		return m, tea.Batch(m.clearStatusIn(4*time.Second), loadBackups(m.cfg.ConfigDir()))

	case backupMetaMsg:
		if msg.err != nil {
			m.setStatus("Capture : "+msg.err.Error(), true)
		} else {
			m.setStatus(msg.done, false)
		}
		return m, tea.Batch(m.clearStatusIn(4*time.Second), loadBackups(m.cfg.ConfigDir()))

	case opDoneMsg:
		// Résultat d'une capture (les actions sur les stacks et la restauration
		// passent par la vue de progression). La vue n'est pas forcée :
		// l'utilisateur a pu naviguer ailleurs pendant l'écriture.
		m.spinning = false
		if msg.err != nil {
			m.setStatus(m.opLabel+" : "+msg.err.Error(), true)
			return m, tea.Batch(m.clearStatusIn(6*time.Second), m.loadStacksCmd())
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
		return m, tea.Batch(m.clearStatusIn(4*time.Second), m.loadStacksCmd(), loadBackups(m.cfg.ConfigDir()))

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
				m.stackDone(ev)
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
		m.appendLogs(msg.entries)
		return m, readLogCmd(m.logCh, m.logSeq)

	case logsStartedMsg:
		if msg.seq != m.logSeq {
			return m, nil // la vue a été fermée avant l'ouverture du flux
		}
		m.logCh = msg.ch
		return m, readLogCmd(msg.ch, msg.seq)

	case clearStatusMsg:
		if msg.seq == m.statusSeq {
			m.status, m.statusKind = "", statusOK
		}

	case quitDisarmMsg:
		if int(msg) == m.quitSeq {
			m.quitArmed = false
		}

	case stackTickMsg:
		// Auto-refresh seulement depuis la liste (et les captures, cf.
		// reloadableView) : recharger pendant qu'un menu d'action ou une
		// confirmation est ouvert re-trie les stacks et peut déplacer le
		// curseur, donc faire viser une autre stack au moment d'exécuter
		// l'action. Ailleurs, le rechargement est noté et fait au retour.
		if cmd := m.refreshOrMark(); cmd != nil {
			return m, tea.Batch(cmd, tickStacks())
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
		resub := tea.Tick(reconnectDelay, func(time.Time) tea.Msg {
			return resubscribeEventsMsg{}
		})
		// Souvent le signe d'un démon tombé : relire la liste tout de suite
		// fait apparaître la panne, au lieu d'attendre le tick (jusqu'à 60 s).
		// Seulement sans erreur affichée : pendant la panne, le flux se
		// referme à chaque réabonnement (toutes les 2 s), et chaque fermeture
		// relancerait la liste.
		if m.err == nil {
			if cmd := m.refreshOrMark(); cmd != nil {
				return m, tea.Batch(resub, cmd)
			}
		}
		return m, resub

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
		// Appel avant le return : refreshOrMark modifie m (Go ne fixe pas
		// l'ordre entre la lecture de m et l'appel dans un même return).
		cmd := m.refreshOrMark()
		return m, cmd

	case logDoneMsg:
		if msg.seq != m.logSeq {
			return m, nil
		}
		m.logCh = nil
		// Affiché dans le titre de la vue (« ■ flux terminé ») : la barre de
		// statut, où l'annonce partait avant, n'y est jamais rendue.
		m.logEnded = true
		return m, nil

	case dirLoadedMsg:
		m.dirPath = msg.path
		m.dirEntries = msg.entries
		m.dirErr = msg.err
		m.dirCursor = 0
		if msg.focus != "" {
			if i := slices.Index(msg.entries, msg.focus); i >= 0 {
				m.dirCursor = i
			}
		}

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
		// Pas de coupe à 80 caractères : renderFooter coupe déjà à la largeur
		// de l'écran, et la cause est souvent en fin de message.
		m.setStatus(sanitizeText(msg.err.Error()), true)
		reload := m.catchUpList()
		return m, tea.Batch(m.clearStatusIn(6*time.Second), reload)

	case spinner.TickMsg:
		// Keep the spinner animating both for footer ops (m.spinning) and for the
		// live progress view while at least one task is still running.
		// Aussi pendant le premier chargement et une attente affichée en barre
		// de statut (prévision d'un up) : figés, ils laissaient croire à un
		// blocage.
		// L'analyse du nettoyage anime son spinner même sous l'aide (la
		// chaîne de ticks rompue, il restait figé au retour) ; leaveCleanup
		// remet cleanupLoading à zéro en quittant la page.
		if m.spinning || (m.view == viewProgress && !m.progressDone) || m.cleanupLoading ||
			m.loading || (m.statusKind == statusBusy && m.status != "") {
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
		from, seq := m.view, m.planSeq
		next, cmd := m.handleKey(msg)
		nm, ok := next.(Model)
		if !ok || nm.view == from {
			return next, cmd
		}
		// Changer de vue abandonne la prévision d'up en cours (voir planSeq),
		// sauf si c'est cette frappe même qui l'a demandée.
		if nm.planSeq == seq {
			nm.planSeq++
			if nm.statusKind == statusBusy {
				nm.setStatus("", false)
			}
		}
		// Retour sur la liste (ou les captures) : rattraper les événements
		// reçus pendant l'autre vue.
		if c := nm.catchUpList(); c != nil {
			cmd = tea.Batch(cmd, c)
		}
		return nm, cmd
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
		case "pgup":
			m.progressScroll = min(m.progressScroll+page(m.progressBodyHeight()), m.progressMaxScroll())
			m.progressManualScroll = m.progressScroll > 0
		case "pgdown":
			m.progressScroll = max(m.progressScroll-page(m.progressBodyHeight()), 0)
			m.progressManualScroll = m.progressScroll > 0
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
		case "l":
			// Fin d'opération : logs de la première stack en échec, sans
			// avoir à la retrouver dans la liste après une opération groupée.
			// Le curseur s'y pose : en sortant des logs, le volet en montre
			// l'échec.
			if s, ok := m.failedLogsTarget(); ok {
				m.leaveProgressView()
				for i, r := range m.listRows() {
					if r.isStack() && r.stack.Name == s.Name {
						m.cursor = i
						break
					}
				}
				return m.startStackAction("logs", s)
			}
			m.progressConfirmCancel = false
		default:
			// Toute autre touche désarme la confirmation d'annulation.
			m.progressConfirmCancel = false
		}
		return m, nil
	}
	// t ouvre le sélecteur de thème depuis la liste, mode services compris,
	// et depuis l'aide ouverte sur la liste (le sélecteur revient à la
	// liste : ouvert depuis l'aide des logs, il aurait laissé leur flux
	// tourner). T aussi (verrouillage des majuscules), comme Q. Pas pendant
	// une saisie : c'est alors une lettre du filtre.
	if k := msg.String(); (k == "t" || k == "T") && (m.view == viewList || (m.view == viewHelp && m.helpReturn == viewList)) && !m.typing() {
		m.quitArmed = false
		return m.openThemePicker()
	}
	// L'aide défile si elle dépasse l'écran. q arme la sortie comme partout
	// (deux frappes : en la fermant, il en fallait trois), et toute autre
	// touche la ferme, avant l'intercept global de r : on revient à la vue
	// d'où on l'a ouverte.
	if m.view == viewHelp {
		key := msg.String()
		if key != "q" && key != "ctrl+c" {
			m.quitArmed = false
		}
		step := page(m.bodyHeight() - 1) // la dernière ligne dit ce qui reste caché
		switch key {
		case "q", "ctrl+c":
			return m.armOrQuit()
		case "up", "k":
			m.helpScroll = max(m.helpScroll-1, 0)
		case "down", "j":
			m.helpScroll = min(m.helpScroll+1, m.helpMaxScroll())
		case "pgup":
			m.helpScroll = max(m.helpScroll-step, 0)
		case "pgdown", "space":
			m.helpScroll = min(m.helpScroll+step, m.helpMaxScroll())
		case "g", "home":
			m.helpScroll = 0
		case "G", "end":
			m.helpScroll = m.helpMaxScroll()
		default:
			m.view = m.helpReturn
			if m.view == viewLogs {
				m.syncLogScroll() // le terminal a pu changer de taille entre-temps
			}
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
			m.loadingSince = time.Now()
			m.err = nil
			return m, tea.Batch(initClient(), m.spinner.Tick)
		}
		if m.cfg.StackDir != "" {
			compose.InvalidateComposeCache()
			m.refreshing = true
			m.listDirty = false
			m.manualR = true
			return m, m.loadStacksCmd()
		}
	}
	// ? ouvre l'aide depuis toutes les vues : seules la liste et les
	// services la proposaient, pas la page Nettoyage, la plus riche en
	// touches propres. Sauf pendant une saisie, où c'est une lettre, et dans
	// une confirmation, qui attend sa réponse ; la progression, qui garde la
	// main, ne passe pas par ici.
	if msg.String() == "?" && !m.typing() && m.view != viewConfirm {
		m.helpReturn, m.view, m.helpScroll = m.view, viewHelp, 0
		return m, nil
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

	header := newBlock(m.headerBlock())

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
			body = m.loadingView()
		} else if m.err != nil {
			body = renderError(m.err, m.width, m.retryNote())
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
// terminal coupait sinon en bout de ligne. retry (cf. retryNote) dit quand
// la liste sera relue et de quand date la dernière valide ; vide, l'écran
// rappelle seulement R.
func renderError(err error, width int, retry string) string {
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
	inner := max(width-8, 20)
	retryRows := func() []string {
		var rows []string
		for _, l := range wordWrap(retry, inner) {
			rows = append(rows, styleDim.Render(l))
		}
		return rows
	}
	if dockerDenied(lower) {
		rows := []string{
			styleRed.Bold(true).Render("Accès Docker refusé"),
			"",
			// Pas de « sudo dockstack » : lancé en root, l'outil crée ~/.dockstack
			// en root et l'utilisateur ne peut plus y écrire ses captures ensuite.
			styleDim.Render("Rejoindre le groupe :") + "  " + styleYellow.Render("sudo usermod -aG docker $USER  && newgrp docker"),
			styleDim.Render("Ou démarrer le daemon :") + "  " + styleYellow.Render("sudo systemctl start docker"),
		}
		if retry != "" {
			rows = append(append(rows, ""), retryRows()...)
		}
		return "\n" + box.Render(strings.Join(rows, "\n"))
	}
	// Démon arrêté, tombé en cours de route ou muet. Avant, la seule présence
	// de « docker.sock » dans le message (démon arrêté, ou flux coupé : EOF)
	// affichait « Accès Docker refusé » et le conseil de rejoindre le groupe
	// docker, sans le message lui-même.
	if daemonUnreachable(lower) {
		rows := []string{
			styleRed.Bold(true).Render("Démon Docker injoignable"),
			"",
			styleDim.Render("Démarrer le daemon :") + "  " + styleYellow.Render("sudo systemctl start docker"),
		}
		if retry != "" {
			rows = append(rows, retryRows()...)
		} else {
			rows = append(rows, styleDim.Render("puis R pour réessayer"))
		}
		rows = append(rows, "")
		for _, l := range wordWrap(msg, inner) {
			rows = append(rows, styleDim.Render(l))
		}
		return "\n" + box.Render(strings.Join(rows, "\n"))
	}
	lines := wordWrap("Erreur: "+msg, max(width-4, 20))
	out := styleRed.Render("\n  " + strings.Join(lines, "\n  "))
	if retry != "" {
		out += "\n\n  " + styleDim.Render(strings.Join(wordWrap(retry, max(width-4, 20)), "\n  "))
	}
	return out
}

// retryNote : ce que l'écran d'erreur dit de la suite. Le tick relit la
// liste toutes les 60 s tant qu'un client existe ; sans client (connexion
// initiale ratée), seul R relance, et l'écran le dit déjà.
func (m Model) retryNote() string {
	if m.client == nil || m.cfg.StackDir == "" {
		return ""
	}
	note := fmt.Sprintf("Nouvel essai automatique toutes les %d s · R maintenant", int(stacksTickInterval/time.Second))
	if !m.lastRefresh.IsZero() {
		note += " · dernière liste valide : " + m.lastRefresh.Format("15:04:05")
	}
	return note
}

// loadingView : corps de la liste pendant le premier chargement. L'étape et
// sa durée, avec un spinner : un démon lent peut faire attendre la prise de
// contact (compose.PingTimeout) puis la lecture (loadStacksTimeout), et un
// « Chargement... » figé ressemblait à un blocage.
func (m Model) loadingView() string {
	step, limit := "Connexion au démon Docker…", compose.PingTimeout()
	if m.client != nil {
		step, limit = "Lecture des stacks de "+sanitizeText(m.cfg.StackDir)+"…", loadStacksTimeout
	}
	elapsed := time.Duration(0)
	if !m.loadingSince.IsZero() {
		elapsed = time.Since(m.loadingSince)
	}
	w := max(m.width-4, 10)
	line := "\n  " + m.spinner.View() + styleDim.Render(truncate(fmt.Sprintf("%s %d s", step, int(elapsed/time.Second)), w-2))
	if elapsed >= slowLoading {
		line += "\n    " + styleYellow.Render(truncate(fmt.Sprintf("le démon répond lentement (abandon à %d s)", int(limit/time.Second)), w-2))
	}
	return line
}

// slowLoading : au-delà, l'écran de chargement prévient que le démon traîne.
const slowLoading = 5 * time.Second

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

// dockerDenied reconnaît, dans un message en minuscules, un refus d'accès au
// socket Docker (utilisateur hors du groupe docker).
func dockerDenied(lower string) bool {
	return strings.Contains(lower, "permission denied") && strings.Contains(lower, "connect to the docker")
}

// setStatus affiche une réussite (✓) ou un échec (✕) en barre de statut.
func (m *Model) setStatus(s string, isErr bool) {
	kind := statusOK
	if isErr {
		kind = statusErr
	}
	m.putStatus(s, kind)
}

// setInfo : rappel ou refus sans gravité (›), ni réussite ni panne.
func (m *Model) setInfo(s string) { m.putStatus(s, statusInfo) }

// setBusy : attente en cours, animée par le spinner. Pas de minuteur :
// l'appelant remplace ou efface le statut quand l'attente se termine.
func (m *Model) setBusy(s string) { m.putStatus(s, statusBusy) }

func (m *Model) putStatus(s string, kind statusKind) {
	m.status, m.statusKind = s, kind
	m.statusSeq++
}

// clearStatusIn efface le statut courant après d (seulement s'il n'a pas été
// remplacé entre-temps, cf. clearStatusMsg).
func (m Model) clearStatusIn(d time.Duration) tea.Cmd {
	seq := m.statusSeq
	return tea.Tick(d, func(time.Time) tea.Msg { return clearStatusMsg{seq: seq} })
}

// reloadableView : vues où la liste peut se recharger sous l'utilisateur. La
// liste elle-même, et les captures, dont le curseur indexe les captures et
// pas les stacks : c y enregistre m.stacks, qui doit être à jour.
func (m Model) reloadableView() bool {
	return m.view == viewList || m.view == viewBackup
}

// refreshOrMark : recharge la liste (événement Docker, tick) si la vue le
// permet, sinon note qu'elle sera à recharger au retour (listDirty).
// Pendant une capture (spinning), rien : sa fin recharge la liste.
func (m *Model) refreshOrMark() tea.Cmd {
	if m.client == nil || m.cfg.StackDir == "" || m.spinning {
		return nil
	}
	if !m.reloadableView() {
		m.listDirty, m.listDirtyAt = true, time.Now()
		return nil
	}
	m.refreshing = true
	m.listDirty = false
	return m.loadStacksCmd()
}

// catchUpList recharge la liste si des événements sont arrivés pendant une
// autre vue (listDirty) et qu'on revient sur une vue rechargeable. Aucun menu
// n'est alors ouvert, et seekCursor garde le curseur sur la même stack.
func (m *Model) catchUpList() tea.Cmd {
	if !m.listDirty || m.loading {
		return nil
	}
	return m.refreshOrMark()
}

// logsHeight : lignes du corps de la vue logs (titre, séparateur et pied ôtés).
func (m Model) logsHeight() int {
	return max(m.height-3, 0)
}

// progressFinished : fin de l'opération en cours (sentinelle AllDone lue).
func (m Model) progressFinished() (tea.Model, tea.Cmd) {
	m.progressDone = true
	m.progressEnd = time.Now()
	m.progressCh = nil
	if m.progressCancel != nil {
		m.progressCancel()
		m.progressCancel = nil
	}
	return m, m.loadStacksCmd()
}

// headerCounts : compteurs de l'en-tête, marqués inconnus tant qu'aucune
// liste n'a pu être lue, et périmés quand la dernière lecture a échoué (la
// liste d'avant reste affichée).
func (m Model) headerCounts() StackCounts {
	sc := m.stackCounts()
	sc.Unknown = len(m.stacks) == 0 && (m.loading || m.err != nil)
	sc.LastRefresh, sc.Refreshing, sc.UsageAt = m.lastRefresh, m.refreshing, m.usageAt
	if m.err != nil {
		sc.Stale = len(m.stacks) > 0
		lower := strings.ToLower(m.err.Error())
		sc.DockerDown = daemonUnreachable(lower) || dockerDenied(lower)
	}
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

// selectedStacks : stacks cochées ET affichées (filtre et a appliqués), celles
// que visent les actions. Une stack cochée puis masquée n'est jamais visée :
// une action sans confirmation (p, u sur une stack arrêtée) ne doit pas
// toucher ce qu'on ne voit pas — la règle du mode services.
func (m Model) selectedStacks() []compose.Stack {
	if len(m.selected) == 0 {
		return nil
	}
	var out []compose.Stack
	for _, s := range m.shownStacks() {
		if m.selected[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// selState : stacks cochées, visibles (visées par les actions) et masquées.
type selState struct {
	shown, hidden int
	byFilter      bool // masquées par le filtre (esc l'efface), sinon par a
}

func (s selState) any() bool { return s.shown+s.hidden > 0 }

// selection compte les stacks cochées, visibles ou masquées. Les noms cochés
// d'une stack disparue depuis (dossier supprimé) ne comptent pas.
func (m Model) selection() selState {
	if len(m.selected) == 0 {
		return selState{}
	}
	total := 0
	for _, s := range m.stacks {
		if m.selected[s.Name] {
			total++
		}
	}
	shown := len(m.selectedStacks())
	return selState{shown: shown, hidden: total - shown, byFilter: m.filter.Value() != ""}
}

// refuseHiddenSelection : des stacks sont cochées mais toutes masquées. Les
// lettres d'action ne retombent pas sur la stack sous le curseur : on la
// croirait visée à la place de la sélection, et p ou u partent sans
// confirmation.
func (m Model) refuseHiddenSelection(sel selState) (tea.Model, tea.Cmd) {
	if sel.byFilter {
		m.setInfo("Stacks cochées masquées par le filtre : esc pour l'effacer")
	} else {
		m.setInfo("Stacks cochées masquées : a pour les afficher, esc pour les décocher")
	}
	return m, m.clearStatusIn(4 * time.Second)
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
		switch m.statusKind {
		case statusErr:
			return styleRed.PaddingLeft(2).Render("✕ " + msg)
		case statusInfo:
			return styleCyan.PaddingLeft(2).Render("› " + msg)
		case statusBusy:
			// Le cadre « ⣾ » du spinner porte son espace.
			return "  " + m.spinner.View() + styleCyan.Render(msg)
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
	// Premier chargement : rien d'autre n'est encore utilisable.
	if m.loading && m.view == viewList {
		return renderBar(nil, []footerKey{{"q", "quitter"}}, m.width, "")
	}
	if m.err != nil {
		return renderBar([]footerKey{{"R", "réessayer"}, {"o", "dossier"}}, []footerKey{{"q", "quitter"}}, m.width, "")
	}
	back := []footerKey{{"esc", "retour"}}
	switch m.view {
	case viewConfirm:
		left := []footerKey{{m.confirmKeys(), "confirmer"}}
		if m.confirmMaxScroll() > 0 {
			// La liste déborde du cadre : on la relit avant de valider.
			left = append([]footerKey{{"↑↓", "lire"}}, left...)
		}
		return renderBar(left, []footerKey{{"n / esc", "annuler"}}, m.width, "")
	case viewBackup:
		if m.captureLabeling {
			return renderBar([]footerKey{{"↩", "valider"}}, []footerKey{{"esc", "annuler"}}, m.width, "")
		}
		left := []footerKey{{"c", "capturer"}}
		if len(m.backups) > 0 {
			left = append(left, footerKey{"↩", "relancer"}, footerKey{"i", "à l'identique"},
				footerKey{"n", "nommer"}, footerKey{"*", "épingler"}, footerKey{"⌫", "supprimer"})
		}
		return renderBar(left, append(back, footerKey{"?", "aide"}), m.width, "")
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
			left = []footerKey{{"↑↓", "défiler"}, {"pgup/pgdn", "page"}, {"g/G", "haut/bas"}}
		}
		return renderBar(left, []footerKey{{"esc", "fermer"}, {"q", "quitter"}}, m.width, "")
	case viewList:
		// Tout à droite : position dans la liste. L'heure de la dernière
		// liste est dans l'en-tête, où elle tient à toutes les largeurs (ici,
		// elle cédait la place aux touches sous ~180 colonnes).
		left, right := m.listFooter()
		return renderBar(left, right, m.width, m.listPosition())
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

// stacksTickInterval : relecture périodique de la liste, en filet du flux
// d'événements (annoncée par l'écran d'erreur).
const stacksTickInterval = 60 * time.Second

func tickStacks() tea.Cmd {
	return tea.Tick(stacksTickInterval, func(time.Time) tea.Msg { return stackTickMsg{} })
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

// typing dit si une saisie de texte est en cours (filtre de la liste, des
// services, des thèmes ou des logs, nom de capture) : les raccourcis globaux
// (R, Q, q) y sont des lettres ordinaires.
func (m Model) typing() bool {
	return m.filtering || (m.svcFocus && m.svcFiltering) || m.captureLabeling ||
		(m.view == viewTheme && m.themeFiltering) || (m.view == viewLogs && m.logFiltering)
}

// bodyHeight : hauteur du corps des vues sous l'en-tête (footer d'une ligne).
func (m Model) bodyHeight() int {
	return max(m.height-m.headerLines()-1, 0)
}

// fullHeader : le tableau de bord complet (CPU, RAM, Docker) ne sert qu'à
// la liste, et au sélecteur de thème dont il est l'aperçu en direct (cf.
// themeview.go). Ailleurs (aide, nettoyage, captures, panneau d'actions,
// confirmation, progression), il prenait 8 lignes ou plus sans rapport avec
// la tâche : l'aide et le nettoyage s'y retrouvaient coupés, le panneau
// d'actions perdait l'état et les services.
func (m Model) fullHeader() bool {
	return m.view == viewList || m.view == viewTheme
}

// viewTitles : nom des vues à en-tête réduit, à droite du dossier.
var viewTitles = map[view]string{
	viewHelp:        "Aide",
	viewConfirm:     "Confirmation",
	viewAction:      "Actions",
	viewBackup:      "Captures d'état",
	viewRestorePick: "Relance d'une capture",
	viewCleanup:     "Nettoyage",
	viewProgress:    "Opération",
}

// headerBlock : en-tête de la vue courante, complet ou réduit à une ligne
// de titre et au séparateur (cf. fullHeader).
func (m Model) headerBlock() string {
	if m.fullHeader() {
		return renderHeader(m.metrics, m.history, m.headerCounts(), m.usage, m.width)
	}
	return renderTitleBar(m.cfg.StackDir, viewTitles[m.view], m.width)
}

// headerLines : hauteur de headerBlock, sans le construire (cf.
// headerHeight). bodyHeight et progressBodyHeight en dépendent : elle doit
// rester celle de l'en-tête réellement rendu, sinon le défilement de l'aide
// ou d'une confirmation se décale.
func (m Model) headerLines() int {
	if m.fullHeader() {
		return headerHeight(m.metrics.NCores, m.width)
	}
	return titleBarLines
}

// page : pas de pgup/pgdown pour une fenêtre de h lignes. Deux lignes de
// recouvrement gardent le contexte d'une page à l'autre.
func page(h int) int {
	return max(h-2, 1)
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
	return m, m.clearStatusIn(3 * time.Second)
}

func (m Model) startOp(label string, op tea.Cmd) (tea.Model, tea.Cmd) {
	if m.opInProgress() {
		return m.refuseOp()
	}
	m.spinning = true
	m.opLabel = label
	m.setStatus("", false)
	return m, tea.Batch(op, m.spinner.Tick)
}

// leaveProgressView revient à la liste et libère l'état de progression : un
// gros pull peut accumuler des dizaines de milliers de ressources dans
// progressState, inutile de les retenir une fois de retour sur la liste.
func (m *Model) leaveProgressView() {
	if m.progressReturn == viewCleanup && m.progress != nil {
		m.cleanupOutcome = m.progress.cleanupOutcome()
	}
	m.view = m.progressReturn
	m.progressReturn = viewList
	m.progress = nil
}

// stackFailure : dernier échec d'une stack (cf. lastFail).
type stackFailure struct {
	op     string // « Pull », « Up », « Relance »… (cf. opName)
	reason string // failureReason, sur une ligne
	at     time.Time
}

// stackDone tient le bilan d'une stack terminée. Arrêtée par l'annulation
// demandée, elle n'est ni réussie ni en échec (sans ça, chaque stack en file
// s'affichait en échec « context canceled »). Sinon son échec est retenu
// pour le volet de la liste, et une réussite l'efface. Les catégories du
// nettoyage, qui passent par la même vue, ne sont pas des stacks.
func (m *Model) stackDone(ev compose.ProgressEvent) {
	var st *stackProgress
	if m.progress != nil {
		st = m.progress.byName[ev.StackName]
	}
	if m.progressCancelling && isCanceled(ev.Err) {
		if st != nil {
			st.cancelled = true
		}
		return
	}
	if m.progressReturn == viewCleanup {
		return
	}
	s, ok := m.stackNamed(ev.StackName)
	if !ok {
		return
	}
	if ev.Err == nil {
		delete(m.lastFail, s.Name)
		return
	}
	reason := oneLine(ev.Err.Error())
	if st != nil {
		reason = failureReason(st)
	}
	if m.lastFail == nil {
		m.lastFail = make(map[string]stackFailure)
	}
	m.lastFail[s.Name] = stackFailure{op: opName(m.progressTitle), reason: reason, at: time.Now()}
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
	m.progressStart, m.progressEnd = time.Now(), time.Time{}
	m.spinning = false
	m.setStatus("", false)
	m.view = viewProgress
	m.progressReturn = viewList
	m.progressCh = ch
	m.progressSeq++ // nouvelle op : périme les messages en vol de la précédente
	return m, tea.Batch(readProgressCmd(ch, m.progressSeq), m.spinner.Tick)
}

// filterBar : ligne du filtre au-dessus de la liste, visible pendant la
// saisie et tant qu'un filtre validé reste appliqué. Figé, il est grisé et
// compte les stacks retenues : les deux états étaient identiques, et une
// lettre tapée en croyant encore saisir lançait une action (p : pull sans
// confirmation).
func (m Model) filterBar(width int) string {
	switch {
	case m.filtering:
		line := m.filter.View()
		hint := styleDim.Render("↩ valider · esc effacer")
		if pad := width - lipgloss.Width(line) - lipgloss.Width(hint) - 2; pad > 0 {
			line += strings.Repeat(" ", pad) + hint
		}
		return line
	case m.filter.Value() != "":
		n := len(m.filteredStacks())
		return styleDim.Render(truncate(fmt.Sprintf("/ %s  (%d stack%s · / modifier · esc effacer)",
			m.filter.Value(), n, plural(n)), max(width, 1)))
	}
	return ""
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
