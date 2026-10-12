package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
