package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// TestDiffCapture : chaque stack de la capture est classée selon l'état actuel,
// triée à relancer → introuvable → en marche, l'ordre de la capture conservé.
func TestDiffCapture(t *testing.T) {
	stacks := []compose.Stack{
		{Name: "up", Running: 2, Total: 2},
		{Name: "down", Running: 0, Total: 2},
		{Name: "partial", Running: 1, Total: 2},
	}
	got := diffCapture(backup.Snapshot{Stacks: []string{"up", "gone", "down", "partial"}}.Items(), stacks)

	want := []struct {
		name string
		kind captureEntryKind
	}{
		{"down", entryRelaunch}, {"partial", entryRelaunch}, {"gone", entryMissing}, {"up", entryRunning},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entrées, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].name != w.name || got[i].kind != w.kind {
			t.Errorf("[%d] = %s/%d, want %s/%d", i, got[i].name, got[i].kind, w.name, w.kind)
		}
	}
}

// TestRenderBackupViewFits : aucune ligne ne déborde de l'écran, que la liste
// de la capture tienne ou soit tronquée.
func TestRenderBackupViewFits(t *testing.T) {
	m := New(&config.Config{StackDir: "/srv"})
	m.width = 100
	m.view = viewBackup
	var names []string
	for _, n := range []string{"beszel", "crowdboard", "goaccess", "homepage", "gitea", "jellyfin", "wallos", "immich", "traefik", "a-very-long-stack-name-that-overflows-the-panel"} {
		names = append(names, n)
		m.stacks = append(m.stacks, compose.Stack{Name: n, Running: 1, Total: 1})
	}
	m.stacks[5].Running = 0
	now := time.Now()
	m.backups = []backup.Snapshot{
		{Date: now.Add(-2 * time.Minute), StackDir: "/srv", Stacks: append(names, "oldstack")},
		{Date: now.Add(-26 * time.Hour), StackDir: "/old", Stacks: names[:3]},
	}

	for _, h := range []int{8, 20} {
		out := renderBackupView(m, h)
		t.Logf("hauteur %d :\n%s", h, out)
		for _, l := range strings.Split(out, "\n") {
			if w := lipgloss.Width(l); w > m.width {
				t.Errorf("ligne de %d colonnes > %d : %q", w, m.width, l)
			}
		}
		if n := strings.Count(out, "\n") + 1; n > h {
			t.Errorf("hauteur %d : rendu de %d lignes", h, n)
		}
	}
}

// TestBackupDeleteFlow : x ouvre la confirmation ; esc revient aux captures
// sans rien supprimer ; y supprime puis la liste rechargée n'a plus la capture.
func TestBackupDeleteFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{StackDir: "/srv"}
	if _, err := backup.Save(cfg.ConfigDir(), "/srv", []backup.Entry{{Name: "a"}, {Name: "b"}}); err != nil {
		t.Fatal(err)
	}
	snaps, _ := backup.List(cfg.ConfigDir())
	m := New(cfg)
	m.view, m.backups = viewBackup, snaps

	keyX := tea.KeyPressMsg{Code: tea.KeyDelete}
	out, _ := m.Update(keyX)
	if got := out.(Model); got.view != viewConfirm {
		t.Fatalf("delete : view = %v, want viewConfirm", got.view)
	}
	// Annulation : retour aux captures, fichier intact.
	back, _ := out.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := back.(Model); got.view != viewBackup {
		t.Errorf("esc : view = %v, want viewBackup", got.view)
	}
	if s, _ := backup.List(cfg.ConfigDir()); len(s) != 1 {
		t.Fatal("annuler ne doit rien supprimer")
	}

	// Confirmation : suppression effective, retour sur la vue captures.
	out, cmd := out.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if got := out.(Model); got.view != viewBackup || cmd == nil {
		t.Fatalf("y : view = %v (cmd nil=%v), want viewBackup + commande", got.view, cmd == nil)
	}
	if msg, ok := cmd().(backupDeletedMsg); !ok || msg.err != nil {
		t.Fatalf("y : message %T %+v, want backupDeletedMsg sans erreur", msg, msg)
	}
	if s, _ := backup.List(cfg.ConfigDir()); len(s) != 0 {
		t.Errorf("après y : %d capture(s), want 0", len(s))
	}
}

// TestRestorePickFlow : seules les stacks à relancer sont cochées ; les déjà en
// marche et les introuvables ne le sont pas ; ␣ ne coche jamais un introuvable ;
// ↩ ne lance rien tant qu'aucune stack restaurable n'est sélectionnée.
func TestRestorePickFlow(t *testing.T) {
	m := New(&config.Config{StackDir: t.TempDir()})
	m.view = viewBackup
	m.stacks = []compose.Stack{
		{Name: "down", Running: 0, Total: 1},
		{Name: "up", Running: 1, Total: 1},
	}
	m.backups = []backup.Snapshot{{Date: time.Now(), Stacks: []string{"down", "up", "gone"}}}

	out, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.view != viewRestorePick || !got.restoreSel["down"] || got.restoreSel["up"] || got.restoreSel["gone"] {
		t.Fatalf("↩ : view=%v sel=%v, want seul « down » coché", got.view, got.restoreSel)
	}
	// Ordre : à relancer, introuvable, en marche → le curseur est sur « down ».
	out, _ = out.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if got = out.(Model); got.restoreSel["down"] {
		t.Error("␣ aurait dû décocher down")
	}
	// Curseur sur « gone » (introuvable) : ␣ n'y change rien.
	out, _ = out.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if got = out.(Model); got.restoreSel["gone"] {
		t.Error("␣ ne doit pas cocher une stack introuvable")
	}
	// Plus rien de coché : pas d'op lancée, un simple rappel (pas une panne).
	out, _ = out.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = out.(Model)
	if got.view != viewRestorePick || got.statusKind != statusInfo || !strings.Contains(got.status, "Aucune") {
		t.Errorf("↩ sans sélection : view=%v status=%q (%v)", got.view, got.status, got.statusKind)
	}
}

// captureScreen : la vue des captures de l'audit (une capture nommée, une
// stack à relancer, une introuvable, deux en marche et sept hors capture).
func captureScreen(width int) Model {
	m := New(&config.Config{StackDir: "/srv"})
	m.width, m.view = width, viewBackup
	m.stacks = []compose.Stack{{Name: "media/radarr", Total: 1}, {Name: "traefik", Running: 1, Total: 1}, {Name: "immich", Running: 1, Total: 1}}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		m.stacks = append(m.stacks, compose.Stack{Name: n, Running: 1, Total: 1})
	}
	m.backups = []backup.Snapshot{
		{Date: time.Now(), StackDir: "/srv", Label: "avant mise à jour immich", Stacks: []string{"media/radarr", "ancienne-stack", "traefik", "immich"}},
		{Date: time.Now().Add(-26 * time.Hour), StackDir: "/srv", Stacks: []string{"traefik"}},
	}
	return m
}

// En 80 colonnes, le bilan d'une capture se replie entre deux comptes, dans
// la hauteur prévue : replié par le cadre, il prenait une ligne de trop et
// le bas de l'écran coupait la bordure basse. « 1 introuvable » s'accorde.
func TestCaptureSummaryWrapsInsideFrame(t *testing.T) {
	m := captureScreen(80)
	const h = 21
	lines := strings.Split(ansi.Strip(renderBackupView(m, h)), "\n")
	if len(lines) != h || strings.Count(lines[h-1], "╰") != 2 {
		t.Fatalf("%d lignes, want %d avec les deux bordures basses en dernier :\n%s", len(lines), h, strings.Join(lines, "\n"))
	}
	s := strings.Join(lines, "\n")
	for _, want := range []string{"1 à relancer · 1 introuvable", "2 en marche · 7 hors capture"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q absent (bilan coupé au milieu d'un compte ?) :\n%s", want, s)
		}
	}
	if strings.Contains(s, "(s)") {
		t.Errorf("« (s) » au lieu de l'accord :\n%s", s)
	}
}

// Le volet des captures s'élargit pour montrer le libellé en entier (il
// n'en montrait que 15 colonnes à toutes les largeurs), sans dépasser les
// 2/5 de l'écran ni descendre sous 38 colonnes.
func TestCaptureListWidensForLabel(t *testing.T) {
	m := captureScreen(120)
	if s := ansi.Strip(renderBackupView(m, 20)); !strings.Contains(s, "avant mise à jour immich │") {
		t.Errorf("120 colonnes : libellé coupé\n%s", s)
	}
	m.backups[0].Label = strings.Repeat("x", 60)
	if got := m.captureListW(); got != 48 {
		t.Errorf("libellé de 60 en 120 colonnes : volet de %d, want 48 (2/5)", got)
	}
	m = captureScreen(80)
	if got := m.captureListW(); got != captureListMinW {
		t.Errorf("80 colonnes : volet de %d, want %d", got, captureListMinW)
	}
	if s := ansi.Strip(renderBackupView(m, 20)); !strings.Contains(s, "Capture du") {
		t.Errorf("80 colonnes : plus de place pour le détail\n%s", s)
	}
}

// Le statut de chaque stack suit son nom, aligné sur le plus long nom + 2,
// au lieu d'être collé au bord droit (à 120 colonnes du nom en 180).
func TestCaptureDetailStatusNextToName(t *testing.T) {
	m := captureScreen(180)
	s := ansi.Strip(renderBackupView(m, 20))
	col := func(line, sub string) int { return ansi.StringWidth(line[:strings.Index(line, sub)]) }
	nameCol, statusCol := -1, -1
	for _, l := range strings.Split(s, "\n") {
		for _, e := range [][2]string{{"■ media/radarr", "à relancer"}, {"✕ ancienne-stack", "introuvable"}, {"● traefik", "en marche"}, {"● immich", "en marche"}} {
			if !strings.Contains(l, e[0]) {
				continue
			}
			n, st := col(l, e[0])+2, col(l, e[1])
			if nameCol < 0 {
				nameCol, statusCol = n, st
			}
			if n != nameCol || st != statusCol {
				t.Errorf("%q : nom en %d, statut en %d ; want %d et %d", e[0], n, st, nameCol, statusCol)
			}
		}
	}
	if want := nameCol + len("ancienne-stack") + 2; statusCol != want {
		t.Errorf("statuts en colonne %d, want %d (plus long nom + 2)\n%s", statusCol, want, s)
	}
}
