//go:build integration

package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// journeyClient : lectures par le vrai client (stacks, occupation, prévision
// d'un up), actions et logs simulés (aucun up/down/pull réel).
type journeyClient struct {
	*compose.Client
	fake *fakeClient
}

func (j journeyClient) LogsAsync(ctx context.Context, s compose.Stack) (<-chan compose.LogEntry, error) {
	return j.fake.LogsAsync(ctx, s)
}
func (j journeyClient) UpManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.UpManyLive(ctx, s, p)
}
func (j journeyClient) DownManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.DownManyLive(ctx, s, p)
}
func (j journeyClient) StopManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.StopManyLive(ctx, s, p)
}
func (j journeyClient) RestartManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.RestartManyLive(ctx, s, p)
}
func (j journeyClient) RecreateManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.RecreateManyLive(ctx, s, p)
}
func (j journeyClient) PullManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.PullManyLive(ctx, s, p)
}
func (j journeyClient) RemoveManyLive(ctx context.Context, s []compose.Stack, p int) <-chan compose.ProgressEvent {
	return j.fake.RemoveManyLive(ctx, s, p)
}

// Commandes prune simulées : jamais de prune réel depuis le parcours.
func (j journeyClient) PruneLive(ctx context.Context, p compose.PruneCommand) <-chan compose.ProgressEvent {
	return j.fake.PruneLive(ctx, p)
}

// Nettoyage simulé : jamais de suppression réelle depuis le parcours.
func (j journeyClient) CleanupLive(ctx context.Context, items []compose.CleanupItem) <-chan compose.ProgressEvent {
	return j.fake.CleanupLive(ctx, items)
}

// TestLiveJourney rejoue les parcours principaux sur les vraies stacks de
// /home/ubuntu (ou de DOCKSTACK_JOURNEY_DIR, par ex. demo-stacks), HOME isolé
// (TestMain), actions simulées.
func TestLiveJourney(t *testing.T) {
	c, err := compose.New()
	if err != nil {
		t.Skip(err)
	}
	dir := os.Getenv("DOCKSTACK_JOURNEY_DIR")
	if dir == "" {
		dir = "/home/ubuntu"
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Skipf("dossier de stacks %s absent (DOCKSTACK_JOURNEY_DIR pour en choisir un)", dir)
	}
	fake := &fakeClient{autoDone: true}
	cfg := &config.Config{StackDir: dir, MaxParallel: 4}
	m := New(cfg)
	m.width, m.height = 190, 50
	m = drive(t, m, clientReadyMsg{client: journeyClient{Client: c, fake: fake}}, 0)
	// Le chargement de la liste est plus long que runQuick : on le rejoue.
	m = drive(t, m, loadStacks(m.client, cfg.StackDir, nil)(), 0)
	screen := func() string { return ansi.Strip(m.viewContent()) }
	key := func(k string) { m = drive(t, m, keyMsg(k), 0) }
	if len(m.stacks) == 0 || m.err != nil {
		t.Fatalf("liste non chargée : %v", m.err)
	}
	if !strings.Contains(screen(), "CPU") {
		t.Fatal("en-tête absent")
	}

	// a : tout afficher ; on vise une stack non déployée pour un up.
	key("a")
	target := -1
	for i, r := range m.listRows() {
		if r.isStack() && r.stack.State() == compose.StateUnknown {
			target = i
			break
		}
	}
	if target < 0 {
		t.Skip("aucune stack non déployée pour le parcours")
	}
	m.cursor = target
	name := m.listRows()[target].stack.Name
	if !strings.Contains(screen(), name) {
		t.Fatalf("stack visée %q absente de l'écran", name)
	}

	// ↩ : panneau d'actions, chaque entrée affichée.
	key("enter")
	if m.view != viewAction {
		t.Fatalf("↩ : vue %d", m.view)
	}
	for _, a := range stackActions {
		if !strings.Contains(screen(), a.label) {
			t.Errorf("panneau : entrée %q absente", a.label)
		}
	}
	// Compose depuis le panneau.
	for m.actionCursor < len(stackActions)-1 && stackActions[m.actionCursor].id != "compose" {
		key("down")
	}
	key("enter")
	if m.view != viewCompose || !strings.Contains(screen(), "services") {
		t.Fatalf("Compose : vue %d\n%s", m.view, screen())
	}
	key("esc")

	// u sur une stack non déployée : up direct, sans confirmation.
	key("u")
	if got := fake.called("up"); len(got) != 1 || got[0].Name != name {
		t.Fatalf("up : %v", got)
	}
	if m.view != viewProgress {
		t.Fatalf("up : vue %d", m.view)
	}
	key("enter") // fermer la progression terminée
	if m.view != viewList {
		t.Fatalf("après la progression : vue %d", m.view)
	}

	// v : compose plein écran ; → : mode services ; l : logs (faux flux).
	key("v")
	if m.view != viewCompose {
		t.Fatalf("v : vue %d", m.view)
	}
	key("esc")
	key("right")
	if !m.svcFocus {
		t.Fatal("→ : mode services attendu")
	}
	key("left")
	logs := make(chan compose.LogEntry, 2)
	logs <- compose.LogEntry{Name: "web-1", Line: "bonjour"}
	close(logs)
	fake.logsCh = logs
	// Les lignes arrivent par lots d'une image (logFrame) : plus que le délai
	// ordinaire de runQuick.
	quickWait = logFrame + 50*time.Millisecond
	key("l")
	quickWait = 20 * time.Millisecond
	if m.view != viewLogs || !strings.Contains(screen(), "bonjour") {
		t.Fatalf("l : vue %d\n%s", m.view, screen())
	}
	key("esc")

	// Stack en marche : up confirmé seulement si la prévision réelle annonce
	// des recréations (PlanUp lit le démon, sans rien changer).
	// Étape sautée si rien ne tourne (dossier de démonstration).
	running := false
	for i, r := range m.listRows() {
		if r.isStack() && r.stack.State() == compose.StateRunning {
			m.cursor, running = i, true
			break
		}
	}
	if running {
		key("u")
		if m.view == viewConfirm {
			key("n")
		}
	}

	// b : captures ; c : capturer (HOME isolé) ; ↩ : relance ; esc.
	key("b")
	if m.view != viewBackup {
		t.Fatalf("b : vue %d", m.view)
	}
	key("c")
	m = drive(t, m, saveBackup(cfg.ConfigDir(), cfg.StackDir, m.stacks)(), 0)
	m = drive(t, m, loadBackups(cfg.ConfigDir())(), 0)
	if len(m.backups) == 0 {
		t.Fatal("capture non enregistrée")
	}
	key("enter")
	if m.view != viewRestorePick {
		t.Fatalf("relance : vue %d", m.view)
	}
	key("esc")
	key("esc")

	// t : thèmes, ↓ aperçu, esc rétablit.
	before := m.themeName
	key("t")
	key("down")
	key("esc")
	if m.themeName != before {
		t.Fatalf("esc doit rétablir le thème %q, eu %q", before, m.themeName)
	}

	// / : filtre.
	key("/")
	for _, r := range name {
		m = drive(t, m, tea.KeyPressMsg{Code: r, Text: string(r)}, 0)
	}
	key("enter")
	if rows := m.visibleStacks(); len(rows) == 0 || !strings.Contains(strings.ToLower(rows[0].Name), strings.ToLower(name)) {
		t.Fatalf("filtre %q : %v", name, rows)
	}
	t.Logf("parcours complet sur %d stacks, cible %s", len(m.stacks), name)
}
