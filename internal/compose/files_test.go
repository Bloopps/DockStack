package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkComposeDir crée dir et y dépose un compose.yaml minimal.
func mkComposeDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "compose.yaml"), "services: {}\n")
}

// TestScanComposeDirSkipsRootOnly : les noms « racine seulement » (tmp, go…) ne
// sont sautés qu'à la racine du scan ; node_modules est sauté à toute profondeur.
func TestScanComposeDirSkipsRootOnly(t *testing.T) {
	root := t.TempDir()
	mkComposeDir(t, filepath.Join(root, "tmp"))                 // racine : sauté
	mkComposeDir(t, filepath.Join(root, "app", "tmp"))          // profond : gardé
	mkComposeDir(t, filepath.Join(root, "app", "node_modules")) // toujours sauté

	var files []string
	scanComposeDir(root, 0, &files)

	want := filepath.Join(root, "app", "tmp", "compose.yaml")
	if len(files) != 1 || files[0] != want {
		t.Fatalf("files = %v, want [%s] (tmp racine + node_modules sautés)", files, want)
	}
}

func TestFindOverrideFile(t *testing.T) {
	dir := t.TempDir()
	if got := findOverrideFile(dir); got != "" {
		t.Errorf("sans override : got %q, want \"\"", got)
	}
	want := filepath.Join(dir, "compose.override.yaml")
	writeFile(t, want, "services: {}\n")
	if got := findOverrideFile(dir); got != want {
		t.Errorf("findOverrideFile = %q, want %q", got, want)
	}
}

// TestLoadProjectMergesOverride : parité CLI — le fichier d'override est
// fusionné par-dessus la base. La base redéfinie (image) doit l'emporter et un
// service ajouté par l'override doit apparaître, ce qui prouve que les deux
// fichiers ont été chargés dans le bon ordre.
func TestLoadProjectMergesOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	writeFile(t, base, "services:\n  web:\n    image: nginx:1.0\n")
	writeFile(t, filepath.Join(dir, "compose.override.yaml"),
		"services:\n  web:\n    image: nginx:2.0\n  sidecar:\n    image: busybox\n")

	proj, err := loadProject(Stack{Name: "t", Dir: dir, ComposeFile: base})
	if err != nil {
		t.Fatalf("loadProject: %v", err)
	}
	if got := proj.Services["web"].Image; got != "nginx:2.0" {
		t.Errorf("image web = %q, want nginx:2.0 (override non appliqué)", got)
	}
	if _, ok := proj.Services["sidecar"]; !ok {
		t.Error("le service ajouté par l'override (sidecar) est absent")
	}
	// Le label ConfigFilesLabel doit lister base + override.
	if len(proj.ComposeFiles) != 2 {
		t.Errorf("ComposeFiles = %v, want base + override", proj.ComposeFiles)
	}
}

// TestLoadProjectNoOverride : sans override, seule la base est chargée.
func TestLoadProjectNoOverride(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "compose.yaml")
	writeFile(t, base, "services:\n  web:\n    image: nginx:1.0\n")

	proj, err := loadProject(Stack{Name: "t", Dir: dir, ComposeFile: base})
	if err != nil {
		t.Fatalf("loadProject: %v", err)
	}
	if got := proj.Services["web"].Image; got != "nginx:1.0" {
		t.Errorf("image web = %q, want nginx:1.0", got)
	}
	if len(proj.Services) != 1 {
		t.Errorf("services = %d, want 1", len(proj.Services))
	}
	if len(proj.ComposeFiles) != 1 {
		t.Errorf("ComposeFiles = %v, want 1 fichier", proj.ComposeFiles)
	}
}
