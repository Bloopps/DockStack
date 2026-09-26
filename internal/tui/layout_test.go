package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
)

// TestHeaderFooterFitWidth : aucune ligne d'en-tête ni de footer ne dépasse la
// largeur du terminal (le moteur de rendu couperait en silence), et les
// raccourcis épinglés (aide, quitter) restent visibles même en 60 colonnes.
func TestHeaderFooterFitWidth(t *testing.T) {
	metrics := monitor.Metrics{CPUPercent: 45, CPUCores: make([]int, 16), NCores: 16,
		MemUsedGB: 12.3, MemTotalGB: 31.2, MemPercent: 40, SwapUsedGB: 1.2, SwapTotalGB: 4,
		SwapPercent: 30, DiskUsedGB: 149, DiskTotalGB: 194, DiskPercent: 82,
		DiskReadKBs: 12345, DiskWriteKBs: 2345, Load1: "10.52", Load5: "10.61", Load15: "10.70", Uptime: "112d 13h"}

	for _, w := range []int{60, 80, 100, 110, 120, 140, 200} {
		m := New(&config.Config{StackDir: "/home/ubuntu/un/chemin/de/stacks/assez/long"})
		m.width, m.height, m.loading = w, 40, false
		m.metrics = metrics
		m.stacks = []compose.Stack{{Name: "a", Running: 1, Total: 1}}
		m.invalidateRows()

		h := renderHeader(m.metrics, m.history, m.stackCounts(), w)
		for _, l := range strings.Split(h, "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("w=%d : ligne d'en-tête de %d colonnes", w, lw)
			}
		}
		if got, want := lipgloss.Height(h), headerHeight(m.metrics.NCores, w); got != want {
			t.Errorf("w=%d : en-tête de %d lignes, headerHeight=%d", w, got, want)
		}

		for _, sel := range []bool{false, true} {
			m.selected = map[string]bool{"a": sel}
			f := m.renderFooter()
			if lw := lipgloss.Width(f); lw > w {
				t.Errorf("w=%d sélection=%v : footer de %d colonnes", w, sel, lw)
			}
			if !strings.Contains(f, "aide") || !strings.Contains(f, "quitter") {
				t.Errorf("w=%d sélection=%v : aide/quitter absents du footer %q", w, sel, f)
			}
		}
	}
}
