package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

func TestPreviewVisible(t *testing.T) {
	m := newTestModel(nil)
	m.width = 120
	if !m.previewVisible() {
		t.Fatal("terminal large : le volet est toujours visible")
	}
	m.width = minPreviewTermWidth - 1
	if m.previewVisible() {
		t.Fatal("terminal trop étroit : aperçu masqué même si activé")
	}
	m.width = 120
	m.view = viewLogs
	if m.previewVisible() {
		t.Fatal("aperçu réservé à la vue liste")
	}
}

func TestPreviewWidthBounds(t *testing.T) {
	m := newTestModel(nil)
	for _, w := range []int{100, 200, 400} {
		m.width = w
		pw := m.previewWidth()
		if pw < previewMinWidth || pw > previewMaxWidth {
			t.Fatalf("largeur %d hors bornes pour terminal %d", pw, w)
		}
		if pw >= w {
			t.Fatalf("le volet (%d) ne doit pas dévorer toute la largeur (%d)", pw, w)
		}
	}
}

func TestPreviewCacheRereadsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("services:\n  web:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &previewCacheBox{}

	lines, err := c.readFile(path)
	if err != nil || len(lines) == 0 || lines[0] != "services:" {
		t.Fatalf("première lecture inattendue: %v %q", err, lines)
	}

	// Réécriture avec une taille différente : le cache doit relire.
	if err := os.WriteFile(path, []byte("version: '3'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err = c.readFile(path)
	if err != nil || lines[0] != "version: '3'" {
		t.Fatalf("relecture après modif attendue, eu: %v %q", err, lines)
	}

	// Fichier absent : erreur remontée, pas de panique.
	if _, err := c.readFile(filepath.Join(dir, "absent.yaml")); err == nil {
		t.Fatal("fichier absent doit remonter une erreur")
	}
}

func TestHighlightComposeLine(t *testing.T) {
	// Le surlignage ne doit pas altérer le texte visible (seulement les codes
	// ANSI), et doit tronquer aux colonnes disponibles.
	if got := highlightComposeLine("services:", 40); !strings.Contains(got, "services") {
		t.Fatalf("clé de premier niveau perdue: %q", got)
	}
	long := strings.Repeat("x", 50)
	got := highlightComposeLine(long, 10)
	if strings.Contains(got, long) {
		t.Fatalf("ligne non tronquée à 10 colonnes: %q", got)
	}
}

func TestPreviewDetailAndTab(t *testing.T) {
	st := stack("sonarr", "", 1, 3, 0)
	st.Services = []compose.ServiceStatus{{Name: "app", Running: 1, Total: 1}, {Name: "db", Running: 0, Total: 2}}
	m := newTestModel([]compose.Stack{st})
	m.width, m.height = 120, 40
	out := ansi.Strip(strings.Join(previewDetail("", st, st.Services, 60, 30, selState{}, previewExtra{}), "\n"))
	// Stack partielle : ses actions (vocabulaire docker) sont listées ici.
	if !strings.Contains(out, "Services") || !strings.Contains(out, "u up") || !strings.Contains(out, "s stop") {
		t.Fatalf("détail incomplet :\n%s", out)
	}
	if strings.Index(out, "db") > strings.Index(out, "app") {
		t.Fatalf("le service en défaut doit venir avant le service sain :\n%s", out)
	}
	mm, _ := m.handleListKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if !mm.(Model).previewCompose {
		t.Fatal("tab doit basculer vers le fichier compose")
	}
}

// 30 services : la liste est tronquée mais les actions restent visibles, et les
// services en défaut passent avant les sains.
func TestPreviewDetailManyServices(t *testing.T) {
	st := compose.Stack{Name: "big", Running: 29, Total: 30, Services: nil}
	for i := 0; i < 30; i++ {
		r := 1
		if i == 20 {
			r = 0
		}
		st.Services = append(st.Services, compose.ServiceStatus{Name: fmt.Sprintf("svc-%02d", i), Running: r, Total: 1})
	}
	for _, h := range []int{15, 24, 40} {
		lines := previewDetail("", st, st.Services, 60, h, selState{}, previewExtra{})
		if len(lines) > h {
			t.Errorf("h=%d : %d lignes, dépasse la hauteur", h, len(lines))
		}
		out := ansi.Strip(strings.Join(lines, "\n"))
		if !strings.Contains(out, "Actions") || !strings.Contains(out, "svc-20") {
			t.Errorf("h=%d : actions ou service en défaut absents :\n%s", h, out)
		}
	}
}

// Une stack à la fois unhealthy et partielle se voit proposer u : up relance
// le conteneur arrêté sans toucher aux autres. Pas pour une unhealthy dont
// tout tourne.
func TestPreviewOffersUpToUnhealthyPartial(t *testing.T) {
	has := func(s compose.Stack) bool {
		out := ansi.Strip(strings.Join(previewDetail("/srv", s, nil, 60, 40, selState{}, previewExtra{}), "\n"))
		return strings.Contains(out, "u up")
	}
	if !has(stack("nextcloud", "", 2, 3, 1)) {
		t.Error("unhealthy 2/3 : u up attendu")
	}
	if has(stack("nextcloud", "", 3, 3, 1)) {
		t.Error("unhealthy 3/3 : rien à relancer, pas de u up")
	}
}
