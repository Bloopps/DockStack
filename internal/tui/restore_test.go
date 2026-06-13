package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveRestoreStacks : les noms d'une capture sont résolus vers des stacks
// restaurables ; ceux sans répertoire/compose sur le disque, ou tentant une
// traversée de chemin, sont signalés (missing) au lieu d'être ignorés.
func TestResolveRestoreStacks(t *testing.T) {
	root := t.TempDir()
	// "ok" existe avec un compose ; "gone" n'existe plus.
	okDir := filepath.Join(root, "ok")
	if err := os.MkdirAll(okDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(okDir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	names := []string{"ok", "gone", "../escape"}
	stacks, missing := resolveRestoreStacks(names, root)

	if len(stacks) != 1 || stacks[0].Name != "ok" {
		t.Fatalf("stacks = %v, want [ok]", stacks)
	}
	if stacks[0].ComposeFile != filepath.Join(okDir, "compose.yaml") {
		t.Errorf("ComposeFile = %q", stacks[0].ComposeFile)
	}
	// "gone" (pas de compose) et "../escape" (traversée) sont introuvables.
	if len(missing) != 2 {
		t.Errorf("missing = %v, want [gone ../escape]", missing)
	}
}
