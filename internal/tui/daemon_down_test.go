package tui

import (
	"strings"
	"testing"
	"time"

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
