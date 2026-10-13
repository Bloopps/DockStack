package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/fakedocker"
)

// Démon qui tombe en cours de route, simulé par un faux démon sur socket
// Unix (le vrai n'est jamais touché) : vrai client, vrai tea.Program.

func fakeDaemonStack(t *testing.T) (*fakedocker.Daemon, string) {
	t.Helper()
	d, err := fakedocker.Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	t.Setenv("DOCKER_HOST", d.Host())
	t.Setenv("DOCKER_CONTEXT", "")
	root := t.TempDir()
	st := writeStack(t, root, "zzdsprobefake", "services:\n  web:\n    image: busybox:latest\n")
	d.SetContainers([]map[string]any{{
		"Id": "c0ffee00000000000000000000000000000000000000000000000000000000aa", "Names": []string{"/zzdsprobefake-web-1"},
		"Image": "busybox:latest", "State": "running", "Status": "Up 1 minute",
		"Labels": map[string]string{
			"com.docker.compose.project":              "zzdsprobefake",
			"com.docker.compose.service":              "web",
			"com.docker.compose.project.working_dir":  st.Dir,
			"com.docker.compose.project.config_files": st.ComposeFile,
			"com.docker.compose.container-number":     "1",
			"com.docker.compose.oneoff":               "False",
			// Empreinte fausse : un up prévoit la recréation de web.
			"com.docker.compose.config-hash": "0000",
		},
	}})
	return d, root
}

func loadedOne(m Model) bool {
	return !m.loading && m.err == nil && len(m.stacks) == 1 && m.stacks[0].Running == 1
}

func TestDaemonDownAndBack(t *testing.T) {
	if testing.Short() {
		t.Skip("≈ 15 s")
	}
	d, root := fakeDaemonStack(t)
	h := startHarness(t, root, nil)
	h.waitFor("liste chargée", loadedOne, 15*time.Second)
	if err := d.WaitSubscriber(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	// Suivi de logs ouvert, puis le démon tombe.
	h.focusStack("zzdsprobefake")
	h.key("l")
	h.waitFor("logs reçus", func(m Model) bool {
		return m.view == viewLogs && len(m.logLines) > 0 && strings.Contains(m.logLines[len(m.logLines)-1].text, "tic")
	}, 10*time.Second)
	d.Kill()
	m := h.waitFor("fin du suivi signalée", func(m Model) bool { return m.logCh == nil }, 10*time.Second)
	last := m.logLines[len(m.logLines)-1].text
	if !strings.Contains(last, "flux de logs interrompu") {
		t.Errorf("dernière ligne des logs : %q, want l'interruption expliquée", last)
	}

	// Pendant la panne : réabonnement aux événements toutes les 2 s, pas de
	// boucle chaude, ni sur les événements ni ailleurs.
	ev0 := d.Refused("/events")
	time.Sleep(6 * time.Second)
	if n := d.Refused("/events") - ev0; n < 1 || n > 4 {
		t.Errorf("%d tentatives d'abonnement en 6 s, want 1 à 4 (une toutes les %v)", n, reconnectDelay)
	}
	for _, r := range []string{"/containers/json", "/info", "/system/df", "/_ping"} {
		if n := d.Refused(r); n > 6 {
			t.Errorf("%s : %d requêtes pendant la panne", r, n)
		}
	}

	// Retour à la liste et R : écran d'erreur.
	h.key("esc")
	h.p.Send(keyShiftR)
	h.waitFor("écran d'erreur", func(m Model) bool { return m.err != nil }, 10*time.Second)
	if v := h.screen(); !strings.Contains(v, "Démon Docker injoignable") || strings.Contains(v, "Accès Docker refusé") {
		t.Errorf("écran d'erreur inattendu :\n%s", v)
	}

	// Le démon revient : R depuis l'écran d'erreur rétablit la liste, et
	// l'abonnement aux événements reprend seul.
	d.Revive()
	h.p.Send(keyShiftR)
	h.waitFor("liste rétablie", loadedOne, 10*time.Second)
	if err := d.WaitSubscriber(5 * time.Second); err != nil {
		t.Fatal("pas de réabonnement aux événements après le retour du démon")
	}
}

// Démon muet au lancement : écran d'erreur au bout du délai de prise de
// contact, puis R une fois le démon revenu.
func TestDaemonMuteThenR(t *testing.T) {
	if testing.Short() {
		t.Skip("≈ 12 s (délai de prise de contact)")
	}
	d, root := fakeDaemonStack(t)
	d.Hang()
	h := startHarness(t, root, nil)
	m := h.waitFor("écran d'erreur du démon muet", func(m Model) bool { return m.err != nil }, 20*time.Second)
	if !strings.Contains(m.err.Error(), "ne répond pas") || m.client != nil {
		t.Fatalf("erreur %v, client %v", m.err, m.client)
	}
	if v := h.screen(); !strings.Contains(v, "Démon Docker injoignable") {
		t.Errorf("écran d'erreur inattendu :\n%s", v)
	}
	d.Revive()
	h.p.Send(keyShiftR)
	h.waitFor("liste chargée après R", loadedOne, 15*time.Second)
}

// Démon devenu muet en cours de session (connexion acceptée, aucune
// réponse) : la relecture expire. L'écran le dit (« Démon Docker
// injoignable », conseil et essai automatique) et l'en-tête ne donne plus
// les compteurs Docker pour actuels. Avant : « context deadline exceeded »
// brut, et « Conteneurs 21/27 » comme une mesure du moment.
func TestHungDaemonShownAsDown(t *testing.T) {
	d, root := fakeDaemonStack(t)
	client, err := compose.New()
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := client.ListStacks(context.Background(), root)
	if err != nil || len(stacks) != 1 {
		t.Fatalf("liste initiale : %v (%d stacks)", err, len(stacks))
	}
	d.Hang()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, listErr := client.ListStacks(ctx, root)
	d.Revive()
	if listErr == nil {
		t.Fatal("liste lue malgré le démon muet")
	}

	m := New(&config.Config{StackDir: root})
	m.client, m.loading = client, false
	m.width, m.height = 120, 36
	m.stacks = stacks
	m.invalidateRows()
	m.lastRefresh = time.Now().Add(-40 * time.Second)
	m.usage = &compose.DockerUsage{Running: 21, Containers: 27}
	m.usageAt = time.Now().Add(-40 * time.Second)
	m, _ = update(m, stacksLoadedMsg{err: listErr, dir: root, at: time.Now()})
	if sc := m.headerCounts(); !sc.DockerDown {
		t.Errorf("en-tête : %q, want les conteneurs marqués inconnus", ansi.Strip(dockerItems(m.usage, sc, true)[1]))
	}
	if out := ansi.Strip(m.viewContent()); !strings.Contains(out, "Démon Docker injoignable") {
		t.Errorf("écran d'erreur générique (%v) :\n%s", listErr, out)
	}
}
