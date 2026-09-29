package tui

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isole TOUS les tests du paquet dans un HOME temporaire : plusieurs
// touches (a, x, choix du dossier) enregistrent la config, et un test qui
// oubliait t.Setenv("HOME", …) écrasait ~/.dockstack/config.json de
// l'utilisateur (constaté le 2026-09-29).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "dockstack-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
