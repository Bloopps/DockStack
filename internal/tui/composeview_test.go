package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

var keyV = tea.KeyPressMsg{Code: 'v', Text: "v"}

func TestDoubleVEscalatesToCompose(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("solo", "", 1, 1, 0)})
	m.previewOn = false

	// Premier v : ouvre le volet, reste dans la liste.
	mm, _ := m.handleListKey(keyV)
	m = mm.(Model)
	if !m.previewOn || m.view != viewList {
		t.Fatalf("premier v : volet ouvert et toujours dans la liste ; eu on=%v view=%v", m.previewOn, m.view)
	}

	// Second v rapproché : escalade en vue compose plein écran.
	mm, _ = m.handleListKey(keyV)
	m = mm.(Model)
	if m.view != viewCompose {
		t.Fatalf("second v rapproché : vue compose attendue, eu %v", m.view)
	}

	// Deux v espacés : pas d'escalade, simple bascule du volet.
	m = newTestModel([]compose.Stack{stack("solo", "", 1, 1, 0)})
	m.previewOn = false
	mm, _ = m.handleListKey(keyV)
	m = mm.(Model)
	m.lastVAt = time.Now().Add(-time.Second) // appui « ancien »
	mm, _ = m.handleListKey(keyV)
	m = mm.(Model)
	if m.view != viewList || m.previewOn {
		t.Fatalf("v espacés : volet refermé sans escalade ; eu on=%v view=%v", m.previewOn, m.view)
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
