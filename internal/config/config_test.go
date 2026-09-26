package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadDefaults : sans fichier, Load renvoie la config par défaut (pas
// d'erreur, MaxParallel 4) ; le premier lancement ne doit pas échouer.
func TestLoadDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load sans fichier : %v", err)
	}
	if cfg.MaxParallel != 4 || cfg.StackDir != "" {
		t.Errorf("défauts = %+v, want MaxParallel 4, StackDir vide", cfg)
	}
}

// TestSaveLoadRoundTrip : Save crée ~/.dockstack puis Load relit les valeurs.
func TestSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := (&Config{StackDir: "/srv/stacks", MaxParallel: 8}).Save(); err != nil {
		t.Fatalf("Save : %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".dockstack", "config.json")); err != nil {
		t.Fatalf("config.json absent : %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load : %v", err)
	}
	if cfg.StackDir != "/srv/stacks" || cfg.MaxParallel != 8 {
		t.Errorf("relu = %+v", cfg)
	}
}

// TestLoadCorrupt : un JSON invalide est une erreur explicite, pas une config
// vide silencieuse (qui ferait rechoisir le dossier au lancement).
func TestLoadCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".dockstack"), 0o755)
	os.WriteFile(filepath.Join(home, ".dockstack", "config.json"), []byte("{pas du json"), 0o644)
	if _, err := Load(); err == nil {
		t.Error("JSON invalide : erreur attendue")
	}
}
