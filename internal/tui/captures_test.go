package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

func svc(name string, running int) compose.ServiceStatus {
	return compose.ServiceStatus{Name: name, Running: running, Total: 1}
}

func capStack(name string, svcs ...compose.ServiceStatus) compose.Stack {
	s := compose.Stack{Name: name, Services: svcs}
	for _, sv := range svcs {
		s.Total += sv.Total
		s.Running += sv.Running
	}
	return s
}

// Une capture avec services est jugée service par service.
func TestDiffCaptureByService(t *testing.T) {
	stacks := []compose.Stack{
		capStack("web", svc("app", 1), svc("db", 0), svc("cache", 1)),
		capStack("ok", svc("app", 1)),
	}
	items := []backup.Entry{{Name: "web", Services: []string{"app", "db"}}, {Name: "ok", Services: []string{"app"}}}
	got := diffCapture(items, stacks)
	if len(got) != 2 || got[0].name != "web" || got[0].kind != entryRelaunch || strings.Join(got[0].down, ",") != "db" {
		t.Fatalf("web : db capturé mais arrêté → à relancer (db) ; eu %+v", got)
	}
	if got[1].name != "ok" || got[1].kind != entryRunning {
		t.Errorf("ok : tous les services capturés tournent → en marche ; eu %+v", got[1])
	}
}

// La capture ne mémorise que les services qui tournent.
func TestCaptureEntriesOnlyRunningServices(t *testing.T) {
	stacks := []compose.Stack{
		capStack("web", svc("app", 1), svc("db", 0)),
		capStack("off", svc("x", 0)),
	}
	got := captureEntries(stacks)
	if len(got) != 1 || got[0].Name != "web" || strings.Join(got[0].Services, ",") != "app" {
		t.Fatalf("captureEntries = %+v, want web/[app] seulement", got)
	}
}

// Alignement : démarre ce qui manque, arrête les stacks hors capture et les
// services hors capture ; une capture ancienne (sans services) n'arrête aucun
// service isolé.
func TestPlanAlign(t *testing.T) {
	stacks := []compose.Stack{
		capStack("web", svc("app", 1), svc("extra", 1), svc("db", 0)),
		capStack("stranger", svc("s", 1)),
		capStack("idle", svc("i", 0)),
		capStack("legacy", svc("a", 1), svc("b", 1)),
	}
	items := []backup.Entry{{Name: "web", Services: []string{"app", "db"}}, {Name: "legacy"}}
	p := planAlign(items, stacks)
	if len(p.relaunch) != 1 || p.relaunch[0].name != "web" {
		t.Errorf("relaunch = %+v, want web", p.relaunch)
	}
	if len(p.stopStacks) != 1 || p.stopStacks[0].Name != "stranger" {
		t.Errorf("stopStacks = %+v, want stranger (idle est déjà arrêtée)", p.stopStacks)
	}
	if len(p.stopServices) != 1 || p.stopServices[0].Name != "web" || strings.Join(p.stopServices[0].Only, ",") != "extra" {
		t.Errorf("stopServices = %+v, want extra de web", p.stopServices)
	}
	if planAlign([]backup.Entry{{Name: "idle"}}, []compose.Stack{capStack("idle", svc("i", 0))}).empty() == false {
		// idle capturée mais arrêtée → à relancer : le plan n'est PAS vide
	} else {
		t.Error("une stack capturée arrêtée doit donner un plan non vide")
	}
}

func TestChainLive(t *testing.T) {
	mk := func(names ...string) func() <-chan compose.ProgressEvent {
		return func() <-chan compose.ProgressEvent {
			ch := make(chan compose.ProgressEvent, 10)
			for _, n := range names {
				ch <- compose.ProgressEvent{StackName: n, StackDone: true}
			}
			ch <- compose.ProgressEvent{AllDone: true}
			return ch
		}
	}
	out := chainLive(context.Background(), []livePhase{{" — arrêt", mk("a")}, {"", mk("a", "b")}})
	var got []string
	for ev := range out {
		if ev.AllDone {
			break
		}
		got = append(got, ev.StackName)
	}
	if strings.Join(got, "|") != "a — arrêt|a|b" {
		t.Fatalf("événements = %v, want étapes en ordre avec suffixe sur la 1re", got)
	}

	// Contexte annulé : aucune étape ne démarre, la sentinelle arrive quand même.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := false
	out = chainLive(ctx, []livePhase{{"", func() <-chan compose.ProgressEvent { started = true; return doneCh() }}})
	if ev := <-out; !ev.AllDone || started {
		t.Errorf("annulé : AllDone=%v étape démarrée=%v", ev.AllDone, started)
	}
}

func backupModel(fake *fakeClient, stacks []compose.Stack, snaps ...backup.Snapshot) Model {
	m := New(&config.Config{StackDir: "/srv"})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	m.stacks = stacks
	m.backups = snaps
	m.invalidateRows()
	return m
}

// Bandeau : visible quand la dernière capture a des stacks à relancer, masqué
// une fois la vue des captures ouverte, ou quand tout tourne.
func TestCaptureBanner(t *testing.T) {
	snap := backup.Snapshot{Date: time.Now(), StackDir: "/srv", Stacks: []string{"a", "b"}}
	stacks := []compose.Stack{capStack("a", svc("x", 0)), capStack("b", svc("y", 1))}
	m := backupModel(&fakeClient{}, stacks, snap)
	if b := ansi.Strip(m.captureBanner()); !strings.Contains(b, "1 stack de la dernière capture") || !strings.Contains(b, "b pour les relancer") {
		t.Fatalf("bandeau attendu, eu %q", b)
	}
	if !strings.Contains(ansi.Strip(m.viewContent()), "pour les relancer") {
		t.Error("le bandeau doit apparaître dans la vue liste")
	}
	m2 := press(m, "b")
	if m2.captureBanner() != "" || m2.view != viewBackup {
		t.Error("le bandeau doit disparaître une fois les captures ouvertes")
	}
	other := snap
	other.StackDir = "/ailleurs"
	if backupModel(&fakeClient{}, stacks, other).captureBanner() != "" {
		t.Error("capture d'un autre répertoire : pas de bandeau")
	}
	allUp := []compose.Stack{capStack("a", svc("x", 1)), capStack("b", svc("y", 1))}
	if backupModel(&fakeClient{}, allUp, snap).captureBanner() != "" {
		t.Error("tout tourne : pas de bandeau")
	}
}

// Libellé (n) et épinglage (p) via la vue des captures.
func TestCaptureLabelAndPin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{StackDir: "/srv"}
	if _, err := backup.Save(cfg.ConfigDir(), "/srv", []backup.Entry{{Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	snaps, _ := backup.List(cfg.ConfigDir())
	m := New(cfg)
	m.width, m.height, m.loading = 120, 40, false
	m.view, m.backups = viewBackup, snaps

	exec := func(m Model, k tea.KeyPressMsg) Model {
		out, cmd := m.Update(k)
		if cmd != nil {
			if msg, ok := cmd().(backupMetaMsg); ok {
				if msg.err != nil {
					t.Fatalf("SetMeta : %v", msg.err)
				}
				s, _ := backup.List(cfg.ConfigDir())
				out, _ = out.Update(backupsLoadedMsg(s))
			}
		}
		return out.(Model)
	}
	m = exec(m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if !m.captureLabeling {
		t.Fatal("n doit ouvrir la saisie du libellé")
	}
	for _, r := range "avant maj" {
		m = exec(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = exec(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.captureLabeling || m.backups[0].Label != "avant maj" {
		t.Fatalf("libellé non enregistré : %+v", m.backups[0])
	}
	m = exec(m, tea.KeyPressMsg{Code: '*', Text: "*"})
	if !m.backups[0].Pinned {
		t.Error("* doit épingler la capture")
	}
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "★") || !strings.Contains(out, "avant maj") {
		t.Errorf("étoile et libellé attendus dans la vue :\n%s", out)
	}
}

// Alignement : la confirmation liste les démarrages ET les arrêts ; à la
// validation, les arrêts passent avant les démarrages.
func TestAlignFlow(t *testing.T) {
	fake := &fakeClient{autoDone: true}
	stacks := []compose.Stack{capStack("down", svc("x", 0)), capStack("stranger", svc("s", 1))}
	snap := backup.Snapshot{Date: time.Now(), StackDir: "/srv", Stacks: []string{"down"}}
	m := backupModel(fake, stacks, snap)
	m.view = viewBackup

	m = press(m, "i")
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Mettre à l'identique") {
		t.Fatalf("i doit ouvrir la confirmation d'alignement (vue %v)", m.view)
	}
	txt := strings.Join(m.confirmNames, "|")
	if !strings.Contains(txt, "▶ démarrer down") || !strings.Contains(txt, "■ arrêter stranger") || m.confirmWarning == "" {
		t.Fatalf("plan incomplet : %q / %q", txt, m.confirmWarning)
	}
	if len(fake.called("stop")) != 0 {
		t.Fatal("rien ne doit partir avant la confirmation")
	}
	m = press(m, "esc")
	if m.view != viewBackup {
		t.Errorf("annuler ramène aux captures, vue %v", m.view)
	}
	press(m, "i", "y")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(fake.called("stop")) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if st := fake.called("stop"); len(st) != 1 || st[0].Name != "stranger" {
		t.Errorf("stop attendu sur stranger : %+v", st)
	}
	// resolveRestoreStacks lit le disque : /srv/down n'existe pas ici, donc rien à
	// démarrer côté up ; l'important est l'ordre et le ciblage des arrêts.
}

// Restauration avec une stack déjà en marche cochée : la prévision passe avant.
func TestRestoreRunningGoesThroughPlan(t *testing.T) {
	root := t.TempDir()
	writeCompose := func(name string) {
		writeTestCompose(t, root, name)
	}
	writeCompose("up")
	fake := &fakeClient{plan: []compose.Recreation{{Service: "app", Reason: "image mise à jour"}}}
	m := backupModel(fake, []compose.Stack{capStack("up", svc("app", 1))},
		backup.Snapshot{Date: time.Now(), Stacks: []string{"up"}})
	m.cfg.StackDir = root
	m.view = viewBackup

	m = press(m, "enter")  // sélecteur : « up » est en marche, donc décoché
	m = press(m, "ctrl+a") // le coche à la main
	m = press(m, "enter")  // → prévision
	if m.view != viewConfirm || !strings.Contains(strings.Join(m.confirmNames, "|"), "app · image mise à jour") {
		t.Fatalf("confirmation avec prévision attendue (vue %v, %v)", m.view, m.confirmNames)
	}
	m = press(m, "esc")
	if m.view != viewRestorePick {
		t.Errorf("annuler ramène au sélecteur, vue %v", m.view)
	}
}

// Le sélecteur de restauration ne déborde jamais de l'écran, même avec des noms
// et des listes de services longs.
func TestRestorePickFits(t *testing.T) {
	long := "une-stack-au-nom-vraiment-tres-long-qui-deborde-du-panneau"
	stacks := []compose.Stack{capStack(long, svc("db", 0), svc("un-service-au-nom-long-aussi", 0)), capStack("ok", svc("a", 1))}
	snap := backup.Snapshot{Date: time.Now(), StackDir: "/srv", Stacks: []string{long, "ok", "gone"},
		Entries: []backup.Entry{{Name: long, Services: []string{"db", "un-service-au-nom-long-aussi"}}, {Name: "ok", Services: []string{"a"}}, {Name: "gone"}}}
	for _, w := range []int{60, 80, 120} {
		m := backupModel(&fakeClient{}, stacks, snap)
		m.width = w
		m.view = viewBackup
		m = press(m, "enter")
		for _, l := range strings.Split(ansi.Strip(renderRestorePick(m, 20)), "\n") {
			if lw := ansi.StringWidth(l); lw > w {
				t.Errorf("w=%d : ligne de %d colonnes : %q", w, lw, l)
			}
		}
	}
}
