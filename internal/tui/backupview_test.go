package tui

import (
	"strings"
	"testing"
	"time"

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
	got := diffCapture([]string{"up", "gone", "down", "partial"}, stacks)

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
