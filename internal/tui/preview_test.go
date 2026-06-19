package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewVisible(t *testing.T) {
	m := newTestModel(nil)
	m.width = 120
	if m.previewVisible() {
		t.Fatal("aperçu masqué tant qu'il n'est pas activé")
	}
	m.previewOn = true
	if !m.previewVisible() {
		t.Fatal("aperçu activé et terminal large : doit être visible")
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
