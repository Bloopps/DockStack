package compose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Liens symboliques dans le dossier des stacks : comportement actuel,
// verrouillé. Un lien vers un dossier n'est pas suivi (comme filepath.WalkDir,
// dont le scan est issu), donc ni doublon ni boucle possible ; un fichier
// compose qui est lui-même un lien est pris.
func TestScanSymlinks(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("services: {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vraie/compose.yaml")
	write("ailleurs/source.yaml")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "lienfichier"), 0o755))
	must(os.Symlink(filepath.Join(root, "ailleurs/source.yaml"), filepath.Join(root, "lienfichier/compose.yaml")))
	must(os.Symlink(filepath.Join(root, "vraie"), filepath.Join(root, "liendossier")))
	must(os.Symlink(root, filepath.Join(root, "vraie/boucle")))               // boucle vers la racine
	must(os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "x"))) // lien cassé

	var files []string
	done := make(chan error, 1)
	go func() { done <- scanComposeDir(root, 0, &files) }()
	select {
	case err := <-done:
		must(err)
	case <-time.After(5 * time.Second):
		t.Fatal("scan bloqué (boucle de liens ?)")
	}
	slices.Sort(files)
	want := []string{filepath.Join(root, "lienfichier/compose.yaml"), filepath.Join(root, "vraie/compose.yaml")}
	if !slices.Equal(files, want) {
		t.Errorf("fichiers = %v, want %v", files, want)
	}
}

// memory.current absent (contrôleur mémoire non délégué) : la lecture
// échoue, le conteneur passe par l'API, le chemin est oublié.
func TestCgroupMemoryCurrentMissing(t *testing.T) {
	root := cgroupRoot(t)
	d := fakeCgroup(t, root, "docker/"+testID, 10, 10, 0, "")
	if err := os.Remove(filepath.Join(d, "memory.current")); err != nil {
		t.Fatal(err)
	}
	r := newCgroupReader(root, nil)
	if _, ok := r.sample(context.Background(), testID); ok {
		t.Error("lecture acceptée sans memory.current")
	}
	if _, ok := r.paths[testID]; ok {
		t.Error("chemin conservé malgré l'échec")
	}
}

// io.stat présent mais vide (aucun périphérique bloc encore touché) : débits
// nuls, CPU et mémoire lus.
func TestCgroupEmptyIOStat(t *testing.T) {
	root := cgroupRoot(t)
	d := fakeCgroup(t, root, "docker/"+testID, 10, 20, 0, "")
	if err := os.WriteFile(filepath.Join(d, "io.stat"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := newCgroupReader(root, nil)
	s, ok := r.sample(context.Background(), testID)
	if !ok || s.cpuUsec != 10 || s.mem != 20 || s.read != 0 || s.write != 0 {
		t.Errorf("lecture %+v (ok=%v)", s, ok)
	}
}
