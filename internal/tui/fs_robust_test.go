package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Système de fichiers qui change sous la TUI (faux démon, vrai client) :
// stack supprimée pendant l'affichage, fichier compose remplacé entre la
// prévision d'un up et sa confirmation.

func progressFinished(m Model) bool { return m.view == viewProgress && m.progressDone }

func TestStackDeletedWhileDisplayed(t *testing.T) {
	_, root := fakeDaemonStack(t)
	h := startHarness(t, root, nil)
	h.waitFor("liste chargée", loadedOne, 15*time.Second)
	h.focusStack("zzdsprobefake")
	if s := h.screen(); !strings.Contains(s, "busybox:latest") {
		t.Fatalf("volet sans le compose :\n%s", s)
	}
	h.key("v")
	h.waitFor("compose plein écran", func(m Model) bool { return m.view == viewCompose }, 5*time.Second)

	if err := os.RemoveAll(filepath.Join(root, "zzdsprobefake")); err != nil {
		t.Fatal(err)
	}
	// Bascule fichier ↔ config résolue : l'erreur s'affiche, sans panique.
	h.key("tab")
	h.waitScreen("erreur de lecture", func(m Model, s string) bool {
		return m.composeErr != nil && strings.Contains(s, "Erreur")
	}, 5*time.Second)
	h.key("esc")
	h.waitFor("retour à la liste", func(m Model) bool { return m.view == viewList }, 5*time.Second)
	h.key("tab") // volet : aperçu du compose
	h.waitScreen("aperçu du compose", func(_ Model, s string) bool { return strings.Contains(s, "Lecture impossible") }, 5*time.Second)

	// Down sur la stack disparue : échec expliqué, pas de blocage.
	h.key("d")
	h.waitFor("confirmation", func(m Model) bool { return m.view == viewConfirm }, 5*time.Second)
	h.key("y")
	h.waitFor("fin de l'opération", progressFinished, 15*time.Second)
	if sn := h.snapshot(); sn.m.progressOk != 0 || !strings.Contains(sn.screen, "no such file") {
		t.Errorf("down d'une stack supprimée : %d OK\n%s", sn.m.progressOk, sn.screen)
	}
}

func TestComposeReplacedBeforeConfirm(t *testing.T) {
	_, root := fakeDaemonStack(t)
	h := startHarness(t, root, nil)
	h.waitFor("liste chargée", loadedOne, 15*time.Second)
	h.focusStack("zzdsprobefake")
	h.key("u") // stack en marche : prévision puis confirmation
	m := h.waitFor("confirmation de l'up", func(m Model) bool { return m.view == viewConfirm }, 15*time.Second)
	if !strings.Contains(strings.Join(m.confirmNames, " "), "web") {
		t.Fatalf("la prévision devait annoncer la recréation de web : %v", m.confirmNames)
	}

	f := filepath.Join(root, "zzdsprobefake", "compose.yaml")
	if err := os.WriteFile(f, []byte("services:\n  web: [pas, un, service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.key("y")
	h.waitFor("fin de l'opération", progressFinished, 15*time.Second)
	if sn := h.snapshot(); sn.m.progressOk != 0 || !strings.Contains(strings.ToLower(sn.screen), "yaml") {
		t.Errorf("up après remplacement du compose : %d OK\n%s", sn.m.progressOk, sn.screen)
	}
}
