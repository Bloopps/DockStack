package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

var keyV = tea.KeyPressMsg{Code: 'v', Text: "v"}

// v ouvre directement le fichier compose en plein écran sur une stack (plus de
// double frappe) ; sur un en-tête de dossier, rien ne s'ouvre.
func TestVOpensCompose(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("solo", "", 1, 1, 0)})
	mm, _ := m.handleListKey(keyV)
	if m = mm.(Model); m.view != viewCompose {
		t.Fatalf("v sur une stack : vue compose attendue, eu %v", m.view)
	}

	m = newTestModel([]compose.Stack{stack("g/a", "g", 1, 1, 0), stack("g/b", "g", 1, 1, 0)})
	m.cursor = 0 // en-tête du dossier g
	mm, _ = m.handleListKey(keyV)
	if m = mm.(Model); m.view != viewList {
		t.Fatalf("v sur un dossier : reste dans la liste, eu %v", m.view)
	}
}

func TestComposeViewRawAndResolved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	raw := "services:\n  web:\n    image: nginx:latest\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	s := compose.Stack{Name: "web", Dir: dir, ComposeFile: path}

	m := newTestModel(nil)
	mm, _ := m.openComposeView(s)
	m = mm.(Model)

	if m.view != viewCompose {
		t.Fatalf("vue compose attendue, eu %v", m.view)
	}
	if m.composeResolved {
		t.Fatal("ouverture en mode brut attendue")
	}
	if m.composeErr != nil || len(m.composeLines) == 0 {
		t.Fatalf("contenu brut attendu: %v", m.composeErr)
	}
	if !strings.Contains(strings.Join(m.composeLines, "\n"), "image: nginx:latest") {
		t.Fatalf("ligne brute manquante: %q", m.composeLines)
	}

	// Bascule en config résolue : doit interpoler/normaliser et rester valide.
	m.composeResolved = true
	m.loadComposeContent()
	if m.composeErr != nil {
		t.Fatalf("résolution échouée: %v", m.composeErr)
	}
	if !strings.Contains(strings.Join(m.composeLines, "\n"), "nginx:latest") {
		t.Fatalf("image absente du rendu résolu: %q", m.composeLines)
	}
}

func TestComposeViewMissingFile(t *testing.T) {
	m := newTestModel(nil)
	mm, _ := m.openComposeView(compose.Stack{Name: "x", ComposeFile: ""})
	m = mm.(Model)
	if m.view != viewCompose || m.composeErr != nil || len(m.composeLines) != 0 {
		t.Fatalf("stack sans fichier : vue ouverte, contenu vide, sans erreur ; eu err=%v lines=%d", m.composeErr, len(m.composeLines))
	}
}

// Le compose plein écran défile aussi par page (pgup, pgdown), et home/end
// valent g/G, comme dans les autres vues.
func TestComposePageAndEdgeKeys(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("app", "", 1, 1, 0)})
	m.width, m.height = 80, 24
	m.view, m.composeLines = viewCompose, strings.Split(strings.Repeat("x: 1\n", 100), "\n")
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if want := page(m.composeHeight()); m.composeScroll != want {
		t.Fatalf("pgdown : défilement %d, want %d", m.composeScroll, want)
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if m.composeScroll != m.composeMaxScroll() {
		t.Errorf("end : défilement %d, want %d", m.composeScroll, m.composeMaxScroll())
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.composeScroll != m.composeMaxScroll() {
		t.Errorf("pgdown en bas : défilement %d, want borné à %d", m.composeScroll, m.composeMaxScroll())
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if m.composeScroll != 0 {
		t.Errorf("home : défilement %d, want 0", m.composeScroll)
	}
}
