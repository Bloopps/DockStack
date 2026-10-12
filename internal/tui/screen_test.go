package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
)

// lipglossScreen : l'assemblage d'origine de l'écran principal, par
// Style.Width/Height, JoinHorizontal et JoinVertical. Référence de
// TestScreenMatchesLipgloss.
func lipglossScreen(m Model) string {
	header := renderHeader(m.metrics, m.history, m.headerCounts(), m.usage, m.width)
	bodyH := max(m.height-lipgloss.Height(header)-1, 0)
	var body string
	switch m.view {
	case viewHelp:
		body = renderHelpView(m, bodyH)
	case viewConfirm:
		body = renderConfirmView(m, bodyH)
	case viewRestorePick:
		body = renderRestorePick(m, bodyH)
	case viewAction:
		body = renderActionMenu(m, bodyH)
	case viewBackup:
		body = renderBackupView(m, bodyH)
	case viewTheme:
		body = renderThemePicker(m, bodyH)
	default:
		if m.loading {
			body = styleDim.Render("\n  Chargement...")
		} else if m.err != nil {
			body = renderError(m.err, m.width)
		} else {
			banner := m.captureBanner()
			listH := bodyH
			if banner != "" {
				listH--
			}
			if !m.previewVisible() {
				body = renderStackList(m, listH, m.width)
			} else {
				paneW := m.previewWidth()
				listW := m.width - paneW
				list := lipgloss.NewStyle().Width(listW).Height(listH).
					Render(renderStackList(m, listH, listW))
				body = lipgloss.JoinHorizontal(lipgloss.Top, list, renderPreviewPane(m, listH, paneW))
			}
			if banner != "" {
				body = banner + "\n" + body
			}
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, fitHeight(body, bodyH), m.renderFooter())
}

// screenModel : écran chargé et varié — dossiers, états, services, noms
// larges (emoji, CJK), mesures système et Docker.
func screenModel(n int) Model {
	var stacks []compose.Stack
	for i := range n {
		group := []string{"", "media", "infra", "outils-très-longs"}[i%4]
		name := fmt.Sprintf("stack-%02d", i)
		switch i % 7 {
		case 1:
			name = "📦 colis-" + name
		case 3:
			name = "服务-" + name
		case 5:
			name = "un-nom-de-stack-vraiment-très-long-pour-tester-la-troncature-" + name
		}
		if group != "" {
			name = group + "/" + name
		}
		s := capStack(name, svc("app", i%3), svc("db", 1), svc("cache-x", 1))
		s.Group, s.Project = group, fmt.Sprintf("p%d", i)
		if i%5 == 0 {
			s.Unhealthy = 1
		}
		stacks = append(stacks, s)
	}
	m := New(&config.Config{StackDir: "/srv", ShowAll: true})
	m.stacks = sortByState(stacks)
	m.loading = false
	m.metrics = monitor.Metrics{CPUPercent: 45, CPUCores: make([]int, 8), NCores: 8,
		MemUsedGB: 12.3, MemTotalGB: 31.2, MemPercent: 40, DiskUsedGB: 149, DiskTotalGB: 194,
		DiskPercent: 82, Load1: "1.52", Load5: "1.61", Load15: "1.70", Uptime: "12j 3h"}
	m.usage = &compose.DockerUsage{Running: 58, Containers: 69, ImagesSize: 85 << 30,
		ImagesReclaimable: 24 << 30, ImagesOK: true, VolumesSize: 5 << 30, VolumesOK: true}
	m.invalidateRows()
	return m
}

// TestScreenMatchesLipgloss : la mise en page directe produit exactement les
// mêmes octets que l'assemblage lipgloss, dans toutes les vues de l'écran
// principal et à toutes les tailles.
func TestScreenMatchesLipgloss(t *testing.T) {
	type variant struct {
		name string
		set  func(*Model)
	}
	variants := []variant{
		{"liste", func(*Model) {}},
		{"curseur bas", func(m *Model) { m.cursor = len(m.listRows()) - 1 }},
		{"en-tête de dossier", func(m *Model) {
			for i, r := range m.listRows() {
				if r.header {
					m.cursor = i
					break
				}
			}
		}},
		{"sélection", func(m *Model) {
			m.selected = map[string]bool{m.stacks[0].Name: true, m.stacks[3].Name: true}
		}},
		{"services", func(m *Model) { m.svcFocus = true }},
		{"compose", func(m *Model) { m.previewCompose = true }},
		{"en marche seulement", func(m *Model) { m.cfg.ShowAll = false; m.invalidateRows() }},
		{"bandeau", func(m *Model) {
			m.backups = []backup.Snapshot{{Date: time.Now(), StackDir: "/srv",
				Stacks: []string{m.stacks[0].Name, m.stacks[1].Name}}}
			for i := range 2 {
				m.stacks[i].Running = 0
				for j := range m.stacks[i].Services {
					m.stacks[i].Services[j].Running = 0
				}
			}
			m.invalidateRows()
		}},
		{"chargement", func(m *Model) { m.loading = true }},
		{"erreur", func(m *Model) { m.err = errors.New("dial unix /var/run/docker.sock: permission denied") }},
		{"aide", func(m *Model) { m.view = viewHelp }},
		{"captures", func(m *Model) { m.view = viewBackup }},
		{"panneau", func(m *Model) { m.view = viewAction }},
		{"thèmes", func(m *Model) {
			out, _ := m.Update(keyMsg("t"))
			*m = out.(Model)
			if m.view != viewTheme {
				panic("t n'a pas ouvert le sélecteur de thèmes")
			}
		}},
		{"confirmation", func(m *Model) {
			out, _ := m.askConfirm("Down", "attention", []string{"a", "b"}, nil)
			*m = out.(Model)
		}},
	}
	sizes := [][2]int{{40, 10}, {60, 24}, {80, 24}, {99, 30}, {100, 30}, {120, 40}, {190, 50}, {260, 70}}
	for _, v := range variants {
		for _, sz := range sizes {
			m := screenModel(40)
			m.width, m.height = sz[0], sz[1]
			v.set(&m)
			got, want := m.viewContent(), lipglossScreen(m)
			if got != want {
				// L'horloge de l'en-tête a pu changer de seconde entre les
				// deux rendus : un second essai tranche.
				got, want = m.viewContent(), lipglossScreen(m)
			}
			if got != want {
				gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
				line := 0
				for line < min(len(gl), len(wl)) && gl[line] == wl[line] {
					line++
				}
				t.Errorf("%s %dx%d : rendu différent (%d/%d lignes), 1re différence ligne %d", v.name, sz[0], sz[1], len(gl), len(wl), line)
				if line < min(len(gl), len(wl)) {
					t.Logf("  obtenu  %q\n  attendu %q", gl[line], wl[line])
				}
			}
		}
	}
}

// Rendu d'un écran complet de la liste avec le volet (190 × 50, 63 stacks),
// par la mise en page directe puis par l'assemblage lipgloss d'origine.
func BenchmarkViewList(b *testing.B) {
	m := screenModel(63)
	m.width, m.height = 190, 50
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewContent()
	}
}

func BenchmarkViewListLipgloss(b *testing.B) {
	m := screenModel(63)
	m.width, m.height = 190, 50
	b.ReportAllocs()
	for b.Loop() {
		_ = lipglossScreen(m)
	}
}
