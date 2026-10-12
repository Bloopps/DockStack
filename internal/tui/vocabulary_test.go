package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// Les messages accordent leurs comptes (« 1 introuvable », « 2 anciennes
// supprimées », « 1 arrêt ») au lieu d'écrire « (s) ».
func TestPluralsAgree(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	for _, c := range []struct {
		n, pruned int
		want      string
	}{
		{1, 1, "Capture enregistrée — 1 stack en marche · 1 ancienne supprimée"},
		{3, 2, "Capture enregistrée — 3 stacks en marche · 2 anciennes supprimées"},
	} {
		if got, _ := update(m, opDoneMsg{n: c.n, pruned: c.pruned}); !strings.HasPrefix(got.status, c.want) {
			t.Errorf("capture de %d stacks, %d supprimées : %q, want %q…", c.n, c.pruned, got.status, c.want)
		}
	}

	m = New(&config.Config{StackDir: "/srv"})
	m.width, m.view, m.restoreLabel, m.restoreSel = 100, viewRestorePick, "capture", map[string]bool{}
	m.restoreEntries = []captureEntry{{name: "x", kind: entryMissing}, {name: "y", kind: entryMissing}}
	if s := ansi.Strip(renderRestorePick(m, 20)); !strings.Contains(s, "2 introuvables") {
		t.Errorf("relance : « 2 introuvables » absent\n%s", s)
	}

	// Alignement avec une seule stack à arrêter.
	stacks := []compose.Stack{capStack("down", svc("x", 0)), capStack("stranger", svc("s", 1))}
	m = backupModel(&fakeClient{}, stacks, backup.Snapshot{Date: time.Now(), StackDir: "/srv", Stacks: []string{"down"}})
	m.view = viewBackup
	if m = press(m, "i"); !strings.HasPrefix(m.confirmWarning, "1 arrêt :") {
		t.Errorf("alignement : avertissement %q", m.confirmWarning)
	}

	for _, c := range []struct {
		stopped, undeployed int
		want                string
	}{
		{2, 0, "■ 2 arrêtées masquées"},
		{0, 1, "○ 1 non déployée masquée"},
		{1, 1, "■ 1 arrêtée et ○ 1 non déployée masquées"},
	} {
		if got := hiddenNote(c.stopped, c.undeployed); !strings.HasPrefix(got, c.want) {
			t.Errorf("hiddenNote(%d, %d) = %q, want %q…", c.stopped, c.undeployed, got, c.want)
		}
	}
}

// Le repère de l'aide qui défile accorde « lignes » et écrit « en
// dessous » sans trait d'union, comme les autres listes.
func TestScrollMarksWording(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height, m.view = 60, 12, viewHelp
	if s := ansi.Strip(renderHelpView(m, 10)); !strings.Contains(s, "lignes en dessous") || strings.Contains(s, "(s)") {
		t.Errorf("aide : repère du bas\n%s", s)
	}
	m = New(&config.Config{StackDir: "/srv"})
	m.width, m.view, m.restoreLabel, m.restoreSel = 100, viewRestorePick, "capture", map[string]bool{}
	for i := range 30 {
		m.restoreEntries = append(m.restoreEntries, captureEntry{name: fmt.Sprintf("s%02d", i)})
	}
	if s := ansi.Strip(renderRestorePick(m, 12)); !strings.Contains(s, "en dessous") || strings.Contains(s, "en-dessous") {
		t.Errorf("relance : repère du bas\n%s", s)
	}
}

// L'en-tête nomme la durée de fonctionnement « Uptime » (« Actif » se
// lisait comme un état) et l'espace récupérable des images « libérables »,
// comme la colonne « Libérable » du nettoyage : « libres » y désigne le
// disque.
func TestHeaderWording(t *testing.T) {
	sc := StackCounts{Dir: "/srv/stacks", Total: 12, Running: 8, CtrRunning: 21, CtrTotal: 27}
	du := &compose.DockerUsage{Running: 21, Containers: 27, ImagesSize: 88 << 30, ImagesReclaimable: 24 << 30, ImagesExact: true, ImagesOK: true}
	h := ansi.Strip(renderHeader(narrowMetrics(8), metricsHistory{}, sc, du, 160))
	if !strings.Contains(h, "Uptime 12j 3h") || strings.Contains(h, "Actif") {
		t.Errorf("durée de fonctionnement :\n%s", h)
	}
	if !strings.Contains(h, "(24.0 Go libérables)") || strings.Contains(h, "Go libres)") {
		t.Errorf("images libérables :\n%s", h)
	}
}

// « dossier » est réservé aux groupes 📁 de la liste : o change de
// répertoire des stacks, dans le pied de l'écran d'erreur, l'aide et le
// sélecteur.
func TestDirectoryWording(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.width, m.height, m.loading = 120, 30, false
	m.err = errors.New("boom")
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, "o  répertoire") || strings.Contains(f, "dossier") {
		t.Errorf("pied de l'écran d'erreur : %q", f)
	}
	m.err = nil
	if s := ansi.Strip(helpBox(m)); !strings.Contains(s, "o  répertoire") || strings.Contains(s, "o  dossier") {
		t.Errorf("aide : o n'est pas « répertoire »\n%s", s)
	}
	m.view, m.dirPath, m.dirEntries = viewDirPicker, "/srv", []string{dirChooseEntry, "../"}
	if s := ansi.Strip(renderDirPicker(m)); !strings.Contains(s, "[*] Choisir ce répertoire") {
		t.Errorf("sélecteur :\n%s", s)
	}
}

// Écran d'erreur et saisie : « démon » partout (la boîte disait « Démon
// Docker injoignable » puis « Démarrer le daemon »), « Erreur : » avec
// l'espace française, « filtrer… » avec une vraie ellipse.
func TestErrorAndFilterWording(t *testing.T) {
	for msg, want := range map[string]string{
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?": "Démarrer le démon :",
		"permission denied while trying to connect to the docker API at unix:///var/run/docker.sock":        "Ou démarrer le démon :",
		"open /srv/stacks: no such file or directory":                                                       "Erreur : open /srv/stacks",
	} {
		out := ansi.Strip(renderError(errors.New(msg), 120, ""))
		if !strings.Contains(out, want) || strings.Contains(out, "le daemon") || strings.Contains(out, "Erreur:") {
			t.Errorf("%q : %q absent\n%s", msg, want, out)
		}
	}
	if p := New(&config.Config{}).filter.Placeholder; p != "filtrer…" {
		t.Errorf("filtre : %q", p)
	}
}
