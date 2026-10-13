package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// Statut, fraîcheur des données et démarrage.

// errDaemonDown : message réel du client Docker, démon arrêté.
var errDaemonDown = errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")

// hasLoad dit si la commande relance la lecture de la liste (les minuteurs
// longs, abandonnés par runQuick, n'en font pas partie).
func hasLoad(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	for _, msg := range runQuick(cmd) {
		if _, ok := msg.(stacksLoadedMsg); ok {
			return true
		}
	}
	return false
}

// Chaque nature de statut a son symbole : une attente n'est plus une coche
// verte, un rappel n'est plus une croix rouge.
func TestStatusKindsFooter(t *testing.T) {
	m := backupModel(&fakeClient{}, []compose.Stack{stack("a", "", 1, 1, 0)})
	m.view = viewList
	for _, tc := range []struct {
		set  func(*Model)
		want string
		not  string
	}{
		{func(m *Model) { m.setStatus("Capture supprimée", false) }, "✓ Capture supprimée", "✕"},
		{func(m *Model) { m.setStatus("Suppression : disque plein", true) }, "✕ Suppression", "✓"},
		{func(m *Model) { m.setInfo("Rien de coché") }, "› Rien de coché", "✓"},
		{func(m *Model) { m.setBusy("Up — a : " + planStatus) }, "Up — a : analyse des changements…", "✓"},
	} {
		tc.set(&m)
		f := ansi.Strip(m.renderFooter())
		if !strings.Contains(f, tc.want) || strings.Contains(f, tc.not) {
			t.Errorf("pied %q, want %q sans %q", f, tc.want, tc.not)
		}
	}
	// L'attente est animée : le cadre courant du spinner la précède.
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, strings.TrimSpace(ansi.Strip(m.spinner.View()))) {
		t.Errorf("attente sans spinner : %q", f)
	}
}

// Le minuteur d'un ancien statut n'efface pas le suivant : les 2 s de
// « Liste actualisée » emportaient l'analyse d'un up lancée entre-temps.
func TestStaleClearStatusIgnored(t *testing.T) {
	fake := &fakeClient{}
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client, m.loading = fake, false
	m.setStatus("Liste actualisée", false)
	old := m.statusSeq

	m, cmd := update(m, keyMsg("u")) // stack en marche : prévision
	if m.statusKind != statusBusy || !strings.Contains(m.status, planStatus) {
		t.Fatalf("statut %q (%v), want l'attente de la prévision", m.status, m.statusKind)
	}
	// La prévision arme le spinner, qui tourne tant que l'attente dure.
	var tick, plan tea.Msg
	for _, msg := range runQuick(cmd) {
		switch msg.(type) {
		case spinner.TickMsg:
			tick = msg
		case planUpMsg:
			plan = msg
		}
	}
	if tick == nil {
		t.Fatal("pas de spinner.Tick avec la prévision")
	}
	if _, next := update(m, tick); next == nil {
		t.Error("le spinner s'arrête pendant l'attente")
	}

	m, _ = update(m, clearStatusMsg{seq: old})
	if m.status == "" {
		t.Fatal("l'ancien minuteur a effacé l'attente en cours")
	}
	// Tant que la prévision court, l'attente revient même après un
	// effacement à son numéro (cf. TestPlanBusyStatusRestored) ; c'est la
	// réponse de la prévision qui l'efface.
	m, _ = update(m, clearStatusMsg{seq: m.statusSeq})
	if !strings.Contains(m.status, planStatus) {
		t.Fatalf("statut %q, want l'attente tant que la prévision court", m.status)
	}
	if m, _ = update(m, plan); m.statusKind == statusBusy {
		t.Errorf("attente %q restée après la réponse de la prévision", m.status)
	}
}

// Un autre statut (R, un refus) remplace un moment l'attente d'une
// prévision d'up : à son effacement, l'attente reprend sa place, spinner
// relancé. Sinon rien ne disait plus que la prévision courait, et sa
// confirmation (ou l'up) survenait sans signe avant-coureur. Une fois la
// prévision revenue, l'attente n'est plus rétablie.
func TestPlanBusyStatusRestored(t *testing.T) {
	fake := &fakeClient{
		stacks: []compose.Stack{stack("web", "", 1, 1, 0)},
		plan:   []compose.Recreation{{Service: "app", Reason: "image modifiée"}},
	}
	m := newTestModel(fake.stacks)
	m.client, m.loading = fake, false
	m.width, m.height = 120, 40
	m.lastRefresh = time.Now()

	m, cmd := update(m, keyMsg("u"))
	plan := planMsgOf(t, cmd)
	m, cmd = update(m, keyShiftR)
	m, _ = update(m, loadOf(t, cmd))
	if !strings.Contains(m.status, "Liste actualisée") {
		t.Fatalf("statut %q, want la fin de R", m.status)
	}
	m, cmd = update(m, clearStatusMsg{seq: m.statusSeq})
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, planStatus) || cmd == nil {
		t.Errorf("prévision en cours : pied %q, spinner relancé %v", f, cmd != nil)
	}

	m, _ = update(m, plan)
	m, _ = update(m, clearStatusMsg{seq: m.statusSeq})
	if m.status != "" {
		t.Errorf("prévision revenue : statut %q, want effacé", m.status)
	}
}

// Refus et rappels sans gravité : ni coche verte ni croix rouge.
func TestRefusalsAreInfo(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("g/a", "g", 1, 1, 0), stack("g/b", "g", 1, 1, 0)})
	m.width, m.height, m.loading = 120, 40, false
	m.cursor = m.headerRowIndex("g")
	if row, ok := m.cursorRow(); !ok || !row.header {
		t.Fatal("curseur hors de l'en-tête du dossier")
	}
	got, _ := update(m, keyMsg("v"))
	if got.statusKind != statusInfo || got.status == "" {
		t.Errorf("v sur un dossier : statut %q (%v), want un rappel", got.status, got.statusKind)
	}
	out, _ := m.enterServices() // garde-fou : → sur un dossier le déplie
	if got := out.(Model); got.statusKind != statusInfo || got.status == "" {
		t.Errorf("services d'un dossier : statut %q (%v), want un rappel", got.status, got.statusKind)
	}
}

// Une erreur d'opération n'est plus coupée à 80 caractères : la cause, en
// fin de message, reste lisible quand l'écran est large.
func TestOpErrNotTruncated(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height, m.loading = 200, 40, false
	long := "ouverture des logs impossible : " + strings.Repeat("contexte ", 12) + "cause-finale"
	m, _ = update(m, opErrMsg{err: errors.New(long)})
	if !strings.HasSuffix(m.status, "cause-finale") || !strings.Contains(ansi.Strip(m.renderFooter()), "cause-finale") {
		t.Errorf("statut coupé : %q", m.status)
	}
}

// Un événement Docker reçu pendant une autre vue n'est plus oublié : la liste
// est relue au retour (avant : fausse jusqu'au tick suivant, 60 s).
func TestEventsDuringOtherViewReloadOnReturn(t *testing.T) {
	fake := &fakeClient{stacks: []compose.Stack{stack("web", "", 0, 1, 0)}}
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client, m.loading = fake, false
	m.width, m.height = 120, 40
	m.view = viewHelp

	m, cmd := update(m, debounceRefreshMsg{gen: m.refreshGen})
	if cmd != nil || !m.listDirty {
		t.Fatalf("pendant l'aide : rechargement %v, listDirty=%v, want rien et noté", cmd != nil, m.listDirty)
	}
	m, cmd = update(m, stackTickMsg{})
	if hasLoad(cmd) || !m.listDirty {
		t.Fatal("le tick pendant l'aide ne doit pas recharger mais noter")
	}

	m, cmd = update(m, keyMsg("x")) // ferme l'aide
	if m.view != viewList || !m.refreshing || m.listDirty || !hasLoad(cmd) {
		t.Fatalf("retour à la liste : vue %v, refreshing=%v, listDirty=%v, rechargement=%v",
			m.view, m.refreshing, m.listDirty, hasLoad(cmd))
	}

	// Sans événement entre-temps, le retour ne relit rien.
	m.refreshing = false
	m, _ = update(m, keyMsg("?"))
	if _, cmd = update(m, keyMsg("x")); hasLoad(cmd) {
		t.Error("retour sans événement : rechargement inutile")
	}
}

// Un chargement parti après le dernier événement noté (fin d'une capture,
// par exemple) rattrape la liste : le retour n'a plus à la relire. Un
// chargement plus ancien, non.
func TestLaterLoadClearsListDirty(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client, m.loading = &fakeClient{}, false
	m = withPanel(m)
	before := time.Now()
	m, _ = update(m, stackTickMsg{})
	if !m.listDirty {
		t.Fatal("tick pendant le panneau d'actions : non noté")
	}
	m, _ = update(m, stacksLoadedMsg{stacks: m.stacks, dir: "/tmp", at: before})
	if !m.listDirty {
		t.Error("chargement parti avant l'événement : listDirty effacé à tort")
	}
	m, _ = update(m, stacksLoadedMsg{stacks: m.stacks, dir: "/tmp", at: time.Now()})
	if m.listDirty {
		t.Error("chargement parti après l'événement : listDirty resté")
	}
}

// Captures : la liste est relue à l'ouverture et sur événement, et c refuse
// d'enregistrer un état en cours de relecture.
func TestCaptureUsesFreshList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeClient{stacks: []compose.Stack{stack("web", "", 1, 1, 0)}}
	m := backupModel(fake, []compose.Stack{stack("web", "", 0, 1, 0)})
	m.lastRefresh = time.Now()

	m, cmd := update(m, keyMsg("b"))
	if m.view != viewBackup || !m.refreshing || !hasLoad(cmd) {
		t.Fatalf("b : vue %v, refreshing=%v, rechargement=%v", m.view, m.refreshing, hasLoad(cmd))
	}
	m, _ = update(m, keyMsg("c"))
	if m.spinning || m.statusKind != statusInfo {
		t.Fatalf("c pendant la relecture : spinning=%v, statut %q (%v)", m.spinning, m.status, m.statusKind)
	}
	m, _ = update(m, stacksLoadedMsg{stacks: fake.stacks, dir: "/srv", at: time.Now()})
	m, _ = update(m, keyMsg("c"))
	if !m.spinning {
		t.Fatalf("c après la relecture : capture non lancée (statut %q)", m.status)
	}

	// Un événement reçu dans la vue des captures recharge directement.
	m.spinning = false
	if m, cmd = update(m, debounceRefreshMsg{gen: m.refreshGen}); !hasLoad(cmd) || m.listDirty {
		t.Error("événement dans les captures : rechargement attendu")
	}
}

// Deux relectures en vol (ouverture des captures, puis événement) : la plus
// ancienne, arrivée la première, ne met plus fin à l'actualisation. c
// capturait alors l'état d'avant l'événement, et l'écrivait dans
// active_stacks, que lisent les scripts.
func TestCaptureWaitsForLatestReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeClient{stacks: []compose.Stack{stack("web", "", 1, 1, 0)}}
	m := backupModel(fake, fake.stacks)
	m.lastRefresh = time.Now()

	m, cmd := update(m, keyMsg("b"))
	before := loadOf(t, cmd)                                 // web encore en marche
	time.Sleep(time.Millisecond)                             // les deux relectures partent à des instants distincts
	fake.stacks = []compose.Stack{stack("web", "", 0, 1, 0)} // web s'arrête
	m, cmd = update(m, debounceRefreshMsg{gen: m.refreshGen})
	after := loadOf(t, cmd)
	m, _ = update(m, before)
	if m, _ = update(m, keyMsg("c")); m.spinning {
		t.Fatal("c accepté pendant la relecture qui suit l'événement : capture de l'état d'avant")
	}
	m, _ = update(m, after)
	if m, _ = update(m, keyMsg("c")); !m.spinning {
		t.Errorf("c après la dernière relecture : capture non lancée (statut %q)", m.status)
	}
}

// Un événement reçu juste avant la fin d'une opération est vu par le
// chargement de fin, parti après lui : le retour à la liste ne la relit pas
// une seconde fois. L'anti-rebond, qui note l'événement 300 ms plus tard, le
// datait du moment où il le notait, après le départ de ce chargement.
func TestNoDoubleReloadAfterOperation(t *testing.T) {
	fake := &fakeClient{stacks: []compose.Stack{stack("web", "", 1, 1, 0)}}
	m := newTestModel([]compose.Stack{stack("web", "", 0, 1, 0)})
	m.client, m.loading = fake, false
	m.width, m.height = 120, 40
	m.lastRefresh = time.Now()
	m.view, m.progress, m.progressSeq = viewProgress, newProgressState(), 1

	m, _ = update(m, dockerEventMsg{Type: "container", Action: "start"})
	m, cmd := update(m, progressAllDoneMsg{seq: 1})
	end := loadOf(t, cmd)        // chargement de fin, parti après l'événement
	time.Sleep(time.Millisecond) // l'anti-rebond note l'événement après ce départ
	m, _ = update(m, debounceRefreshMsg{gen: m.refreshGen})
	m, _ = update(m, end)
	if m.listDirty {
		t.Error("chargement de fin parti après l'événement : listDirty resté")
	}
	if _, cmd = update(m, keyMsg("enter")); hasLoad(cmd) {
		t.Error("retour à la liste : seconde relecture complète")
	}
}

// Fermeture du flux d'événements (démon tombé) : la liste est relue tout de
// suite, sauf si l'erreur est déjà affichée (le flux se referme alors toutes
// les 2 s, et relire à chaque fois ferait une boucle).
func TestEventsClosedReloadsOnce(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client, m.loading = &fakeClient{}, false
	if _, cmd := update(m, dockerEventsClosedMsg{}); !hasLoad(cmd) {
		t.Error("flux fermé, liste sans erreur : relecture attendue")
	}
	m.err = errDaemonDown
	if _, cmd := update(m, dockerEventsClosedMsg{}); hasLoad(cmd) {
		t.Error("flux fermé pendant la panne : pas de relecture")
	}
}

// eventsRefusedClient : abonnement aux événements refusé (proxy du socket,
// règle d'autorisation) alors que la liste se lit : le flux se referme
// aussitôt ouvert.
type eventsRefusedClient struct{ *fakeClient }

func (eventsRefusedClient) SubscribeEvents(context.Context) <-chan compose.DockerEvent {
	ch := make(chan compose.DockerEvent)
	close(ch)
	return ch
}

// Flux d'événements refusé, liste lisible : le flux se referme à chaque
// réabonnement (toutes les 2 s). Seule la première fermeture relit la
// liste ; avant, chaque fermeture la relisait, sans fin. Un flux qui avait
// vécu (démon tombé) la relit toujours.
func TestEventsRefusedNoReloadLoop(t *testing.T) {
	fake := &fakeClient{stacks: []compose.Stack{stack("web", "", 1, 1, 0)}}
	m := newTestModel(fake.stacks)
	m.client, m.loading = eventsRefusedClient{fake}, false
	m.lastRefresh = time.Now()

	var cmd tea.Cmd
	next, loads := tea.Msg(dockerEventsClosedMsg{}), 0
	for range 10 {
		if m, cmd = update(m, next); hasLoad(cmd) {
			loads++
		}
		m, cmd = update(m, resubscribeEventsMsg{})
		m, cmd = update(m, runQuick(cmd)[0]) // flux ouvert…
		next = runQuick(cmd)[0]              // … et refermé aussitôt
	}
	if loads != 1 {
		t.Errorf("%d relectures pour 10 fermetures du flux refusé, want 1", loads)
	}
	m.eventsSince = time.Now().Add(-time.Minute)
	if _, cmd = update(m, dockerEventsClosedMsg{}); !hasLoad(cmd) {
		t.Error("flux qui avait vécu refermé : relecture attendue")
	}
}

// Démon tombé en cours de session : les compteurs d'avant ne s'affichent
// plus comme actuels, et l'heure de la dernière liste est donnée.
func TestHeaderStaleCounts(t *testing.T) {
	m := backupModel(&fakeClient{}, []compose.Stack{stack("a", "", 1, 1, 0), stack("b", "", 0, 1, 0)})
	m.lastRefresh = time.Date(2026, 10, 12, 18, 29, 10, 0, time.Local)
	m.usageAt = time.Date(2026, 10, 12, 18, 29, 40, 0, time.Local)
	m.usage = &compose.DockerUsage{Running: 21, Containers: 27, ImagesSize: 88 << 30, ImagesOK: true, VolumesSize: 31 << 30, VolumesOK: true}
	m.err = errDaemonDown
	items := dockerItems(m.usage, m.headerCounts(), true)
	for i, want := range []string{"? (dernière liste 18:29:10)", "? (21/27 à 18:29)", "? (88.0 Go)", "? (31.0 Go)"} {
		if got := ansi.Strip(items[i]); !strings.Contains(got, want) {
			t.Errorf("ligne %d : %q, want %q", i, got, want)
		}
	}

	// Erreur de dossier : Docker répond, ses mesures restent actuelles ;
	// seules les stacks sont périmées, et l'écart « hors stack » n'a plus
	// de sens.
	m.err = errors.New("open /srv: permission denied")
	items = dockerItems(m.usage, m.headerCounts(), true)
	if got := ansi.Strip(items[0]); !strings.Contains(got, "? (dernière liste 18:29:10)") {
		t.Errorf("stacks : %q", got)
	}
	if got := ansi.Strip(items[1]); strings.Contains(got, "?") || !strings.Contains(got, "21/27") || strings.Contains(got, "hors stack") {
		t.Errorf("conteneurs : %q, want 21/27 actuels sans écart", got)
	}
}

// L'heure de la dernière liste est visible à toutes les largeurs, en ligne 1
// (elle n'apparaissait qu'en 180 colonnes, au pied de page), avec « o
// changer » malgré un chemin long.
func TestHeaderFreshnessVisible(t *testing.T) {
	now := time.Now()
	m := New(&config.Config{StackDir: "/home/ubuntu/un/chemin/de/stacks/vraiment/tres/long/pour/tester"})
	m.loading = false
	m.stacks = []compose.Stack{stack("a", "", 1, 1, 0)}
	m.invalidateRows()
	m.lastRefresh = now.Add(-20 * time.Second)
	for _, tc := range []struct {
		w    int
		want []string
	}{
		{80, []string{"maj " + m.lastRefresh.Format("15:04:05"), "o changer", "…"}},
		{120, []string{"🕐", "maj à l'instant", "o changer"}},
	} {
		line := ansi.Strip(strings.Split(renderHeader(m.metrics, m.history, m.headerCounts(), nil, tc.w), "\n")[0])
		for _, want := range tc.want {
			if !strings.Contains(line, want) {
				t.Errorf("w=%d : %q absent de %q", tc.w, want, line)
			}
		}
		if strings.HasSuffix(line, "…") {
			t.Errorf("w=%d : fin de ligne coupée : %q", tc.w, line)
		}
	}
	m.lastRefresh = now.Add(-3 * time.Minute)
	if line := ansi.Strip(renderHeader(m.metrics, m.history, m.headerCounts(), nil, 120)); !strings.Contains(line, "maj il y a 3 min") {
		t.Errorf("liste ancienne : %q", strings.Split(line, "\n")[0])
	}
	m.refreshing = true
	if line := ansi.Strip(renderHeader(m.metrics, m.history, m.headerCounts(), nil, 80)); !strings.Contains(strings.Split(line, "\n")[0], "↻ actualisation…") {
		t.Errorf("relecture en cours : %q", strings.Split(line, "\n")[0])
	}
}

// L'écran d'erreur dit que la liste se réessaie seule et de quand date la
// dernière liste valide ; sans client (connexion initiale ratée), R seul.
func TestErrorScreenRetryNote(t *testing.T) {
	m := backupModel(&fakeClient{}, []compose.Stack{stack("a", "", 1, 1, 0)})
	m.lastRefresh = time.Date(2026, 10, 12, 18, 29, 10, 0, time.Local)
	m.err = errDaemonDown
	out := ansi.Strip(m.viewContent())
	for _, want := range []string{"Démon Docker injoignable", "Nouvel essai automatique toutes les 60 s", "R maintenant", "dernière liste valide : 18:29:10"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q absent de l'écran d'erreur", want)
		}
	}
	m.client = nil
	out = ansi.Strip(m.viewContent())
	if strings.Contains(out, "automatique") || !strings.Contains(out, "puis R pour réessayer") {
		t.Errorf("sans client : essai automatique annoncé à tort\n%s", out)
	}
}

// Changement de dossier : aucune liste du nouveau n'a été lue. Ni l'écran
// d'erreur (« dernière liste valide ») ni l'en-tête (« maj il y a … ») ne
// datent plus l'ancien dossier.
func TestDirChangeForgetsLastRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "autre"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(&config.Config{StackDir: "/srv"})
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 120, 36, false
	m.stacks = []compose.Stack{stack("web", "", 1, 1, 0)}
	m.invalidateRows()
	m.lastRefresh = time.Now().Add(-10 * time.Minute)
	m.view = viewDirPicker
	m, _ = update(m, loadDirCmd(filepath.Join(root, "autre"))())
	m, _ = update(m, keyMsg("enter"))
	if m.cfg.StackDir != filepath.Join(root, "autre") {
		t.Fatalf("dossier %q", m.cfg.StackDir)
	}
	m, _ = update(m, stacksLoadedMsg{err: errors.New("open " + m.cfg.StackDir + ": permission denied"), dir: m.cfg.StackDir, at: time.Now()})
	if out := ansi.Strip(m.viewContent()); strings.Contains(out, "dernière liste valide") {
		i := strings.Index(out, "dernière liste")
		t.Errorf("nouveau dossier jamais lu, l'écran date l'ancien : %q", out[i:min(i+40, len(out))])
	}
	if line := ansi.Strip(renderHeader(m.metrics, m.history, m.headerCounts(), nil, 120)); strings.Contains(line, "maj il y a") {
		t.Errorf("en-tête : %q", strings.Split(line, "\n")[0])
	}
}

// Démarrage : l'étape en cours, sa durée et un spinner qui tourne, au lieu
// d'un « Chargement... » figé ; le pied ne propose que de quitter.
func TestLoadingScreenAnimated(t *testing.T) {
	m := New(&config.Config{StackDir: "/srv/stacks"})
	m.width, m.height = 120, 36
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "Connexion au démon Docker… 0 s") {
		t.Errorf("étape de connexion absente :\n%s", out)
	}
	lines := strings.Split(out, "\n")
	if foot := lines[len(lines)-1]; !strings.Contains(foot, "quitter") || strings.Contains(foot, "filtre") {
		t.Errorf("pied pendant le chargement : %q", foot)
	}
	if _, cmd := update(m, m.spinner.Tick()); cmd == nil {
		t.Error("le spinner ne tourne pas pendant le chargement")
	}

	m.loadingSince = time.Now().Add(-6 * time.Second)
	if out = ansi.Strip(m.viewContent()); !strings.Contains(out, "le démon répond lentement (abandon à 10 s)") {
		t.Errorf("connexion lente non signalée :\n%s", out)
	}
	m.client = &fakeClient{}
	m.loadingSince = time.Now()
	if out = ansi.Strip(m.viewContent()); !strings.Contains(out, "Lecture des stacks de /srv/stacks…") {
		t.Errorf("étape de lecture absente :\n%s", out)
	}
	m.loading = false
	if _, cmd := update(m, m.spinner.Tick()); cmd != nil {
		t.Error("spinner encore animé une fois la liste chargée")
	}
}

// Chemin de stacks long en 80 colonnes : il cède la place (début coupé),
// et la durée écoulée reste affichée. La coupe de fin de ligne l'emportait.
func TestLoadingScreenLongPathKeepsElapsed(t *testing.T) {
	m := New(&config.Config{StackDir: "/home/utilisateur/infrastructure/docker/compose/stacks-production"})
	m.width, m.height = 80, 24
	m.client = &fakeClient{}
	m.loadingSince = time.Now().Add(-12 * time.Second)
	if out := ansi.Strip(m.loadingView()); !strings.Contains(out, "stacks-production… 12 s") {
		t.Errorf("fin du chemin ou durée absente :\n%s", out)
	}
}

// Pendant l'analyse du nettoyage, le pied se réduit à la sortie.
func TestCleanupFooterWhileAnalyzing(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.view, m.cleanupLoading = 120, viewCleanup, true
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, "retour") || strings.Contains(f, "réanalyser") {
		t.Errorf("pied pendant l'analyse : %q", f)
	}
}

// Sans dossier configuré, le sélecteur part du dossier courant, à défaut
// du home, et plus de « / » (un ↩ réflexe y choisissait la racine).
func TestPickerStart(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	if got := pickerStart(); got != wd {
		t.Errorf("départ %q, want le dossier courant %q", got, wd)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir("/")
	if got := pickerStart(); got != home {
		t.Errorf("lancé depuis / : départ %q, want le home %q", got, home)
	}
}

// Sélecteur de dossier : ← et ⌫ remontent (curseur sur le dossier quitté),
// → ouvre le sous-dossier visé sans le choisir, g/G aux extrémités ; ↩ dit
// dans le pied ce qu'il fera.
func TestDirPickerArrows(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a/b", "a/c", "a/b/d"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := New(&config.Config{StackDir: "/srv"})
	m.width, m.height, m.loading, m.view = 100, 30, false, viewDirPicker
	m = drive(t, m, loadDirCmd(filepath.Join(root, "a", "b"))(), 0)

	m = drive(t, m, keyMsg("left"), 0)
	if m.dirPath != filepath.Join(root, "a") || m.dirEntries[m.dirCursor] != "b/" {
		t.Fatalf("← : %q, curseur sur %q, want a/ et b/", m.dirPath, m.dirEntries[m.dirCursor])
	}
	if f := ansi.Strip(renderDirPicker(m)); !strings.Contains(f, "↩ / →  ouvrir") {
		t.Errorf("↩ sur un sous-dossier : pied %q", f[strings.LastIndex(f, "\n")+1:])
	}
	m = drive(t, m, keyMsg("G"), 0)
	if m.dirEntries[m.dirCursor] != "c/" {
		t.Errorf("G : curseur sur %q", m.dirEntries[m.dirCursor])
	}
	m = drive(t, m, keyMsg("g"), 0)
	if m.dirCursor != 0 || !strings.Contains(ansi.Strip(renderDirPicker(m)), "↩  choisir") {
		t.Errorf("g : curseur %d, ou ↩ ne dit pas « choisir »", m.dirCursor)
	}
	m = drive(t, m, keyMsg("right"), 0) // sur « Choisir ce répertoire » : rien
	if m.dirPath != filepath.Join(root, "a") || m.cfg.StackDir != "/srv" {
		t.Fatalf("→ sur « Choisir » : %q, dossier %q", m.dirPath, m.cfg.StackDir)
	}
	m.dirCursor = 2 // b/
	m = drive(t, m, keyMsg("right"), 0)
	if m.dirPath != filepath.Join(root, "a", "b") || m.view != viewDirPicker {
		t.Fatalf("→ : %q (vue %v), want a/b ouvert sans choix", m.dirPath, m.view)
	}
	m = drive(t, m, keyMsg("backspace"), 0)
	if m.dirPath != filepath.Join(root, "a") {
		t.Errorf("⌫ : %q, want le parent", m.dirPath)
	}
}

// Sélecteur ouvert sur un stack_dir saisi à la main, avec barre finale ou
// relatif : ← remonte au parent, curseur sur le dossier quitté (avant : le
// même dossier, ou « . » sans fin). Choisir ce même dossier garde la
// sélection.
func TestDirPickerTypedStackDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "stacks", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, dir := range []string{filepath.Join(root, "stacks") + "/", "./stacks", "stacks/"} {
		m := New(&config.Config{StackDir: dir})
		m.width, m.height, m.loading = 100, 30, false
		m.client = &fakeClient{}
		m.stacks = []compose.Stack{stack("web", "", 1, 1, 0)}
		m.selected["web"] = true
		m.invalidateRows()
		m = drive(t, m, keyMsg("o"), 0)
		m = drive(t, m, keyMsg("left"), 0)
		if m.dirPath != root || m.dirEntries[m.dirCursor] != "stacks/" {
			t.Errorf("← depuis %q : %q, curseur sur %q, want %q et stacks/", dir, m.dirPath, m.dirEntries[m.dirCursor], root)
			continue
		}
		m = drive(t, m, keyMsg("right"), 0)
		m = drive(t, m, keyMsg("enter"), 0) // « Choisir ce répertoire »
		if m.view != viewList || !m.selected["web"] {
			t.Errorf("%q choisi de nouveau : vue %v, sélection %v", dir, m.view, m.selected)
		}
	}
}

// loadOf extrait la relecture de la liste d'une commande (sans l'appliquer).
func loadOf(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	for _, msg := range runQuick(cmd) {
		if _, ok := msg.(stacksLoadedMsg); ok {
			return msg
		}
	}
	t.Fatal("aucune relecture de la liste")
	return nil
}

// La cible du panneau d'actions est figée à son ouverture : la relecture de
// rattrapage au retour des logs, arrivée panneau ouvert, masquait la stack
// visée (arrêtée entre-temps, liste par défaut), le curseur passait sur une
// autre, et p lançait son pull sans confirmation.
func TestActionPanelTargetSurvivesReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeClient{stacks: []compose.Stack{stack("api", "", 1, 1, 0), stack("web", "", 0, 1, 0)}}
	m := newTestModel([]compose.Stack{stack("api", "", 1, 1, 0), stack("web", "", 1, 1, 0)})
	m.client, m.loading = fake, false
	m.width, m.height = 120, 40
	m.lastRefresh = time.Now()
	m = seekStack(m, "web")
	m, _ = update(m, keyMsg("l"))
	m, _ = update(m, debounceRefreshMsg{gen: m.refreshGen}) // web s'arrête pendant les logs
	m, cmd := update(m, keyMsg("esc"))
	loaded := loadOf(t, cmd)
	m, _ = update(m, keyMsg("enter"))
	if m.view != viewAction {
		t.Fatalf("vue %v, want le panneau", m.view)
	}
	m, _ = update(m, loaded) // arrive panneau ouvert : web masquée
	if s, _ := m.cursorStack(); s.Name == "web" {
		t.Fatal("web encore sous le curseur : le cas n'est pas reproduit")
	}
	m, _ = update(m, keyMsg("p"))
	if got := fake.called("pull"); len(got) != 1 || got[0].Name != "web" {
		t.Errorf("panneau ouvert sur web : pull lancé sur %v", got)
	}
}

// Même règle pour un panneau ouvert sur la sélection : la relecture qui
// masque la cochée ne fait pas retomber les lettres sur la stack sous le
// curseur.
func TestActionPanelSelectionSurvivesReload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeClient{stacks: []compose.Stack{stack("api", "", 1, 1, 0), stack("web", "", 0, 1, 0)}}
	m := newTestModel([]compose.Stack{stack("api", "", 1, 1, 0), stack("web", "", 1, 1, 0)})
	m.client, m.loading = fake, false
	m.width, m.height = 120, 40
	m.lastRefresh = time.Now()
	m.selected["web"] = true
	m.view, m.helpReturn = viewHelp, viewList
	m, _ = update(m, debounceRefreshMsg{gen: m.refreshGen})
	m, cmd := update(m, keyMsg("x")) // ferme l'aide : relecture de rattrapage
	loaded := loadOf(t, cmd)
	m, _ = update(m, keyMsg("enter"))
	if m.view != viewAction || !m.actionGroup {
		t.Fatalf("vue %v, groupe %v : want le panneau de la sélection", m.view, m.actionGroup)
	}
	m, _ = update(m, loaded)
	m, _ = update(m, keyMsg("p"))
	if got := fake.called("pull"); len(got) != 1 || got[0].Name != "web" {
		t.Errorf("panneau ouvert sur la sélection (web) : pull lancé sur %v", got)
	}
}

// Une relecture où la cible du panneau a disparu (dossier supprimé) le
// ferme et le dit, au lieu de laisser un panneau vide.
func TestActionPanelClosesWhenTargetGone(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("api", "", 1, 1, 0), stack("web", "", 1, 1, 0)})
	m.client, m.loading = &fakeClient{}, false
	m.width, m.height = 120, 40
	m = withPanel(seekStack(m, "web"))
	m, _ = update(m, stacksLoadedMsg{stacks: []compose.Stack{stack("api", "", 1, 1, 0)}, dir: "/tmp", at: time.Now()})
	if m.view != viewList || m.statusKind != statusInfo || !strings.Contains(m.status, "disparu") {
		t.Errorf("vue %v, statut %q : want la liste et un mot d'explication", m.view, m.status)
	}
}
