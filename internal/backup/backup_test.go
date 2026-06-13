package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveThenList : un Save écrit le contenu dans `active_stacks` ET dans
// `active_stacks.<ts>` (même date, même nombre de stacks) ; List doit
// dédupliquer pour ne renvoyer qu'une seule capture, avec les bonnes stacks.
func TestSaveThenList(t *testing.T) {
	dir := t.TempDir()
	want := []string{"immich", "traefik", "jellyfin"}
	if err := Save(dir, "/srv/stacks", want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snaps, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("List = %d captures, want 1 (dédup base + .<ts>)", len(snaps))
	}
	s := snaps[0]
	if s.StackDir != "/srv/stacks" {
		t.Errorf("StackDir = %q, want /srv/stacks", s.StackDir)
	}
	if len(s.Stacks) != len(want) {
		t.Fatalf("Stacks = %v, want %v", s.Stacks, want)
	}
	for i := range want {
		if s.Stacks[i] != want[i] {
			t.Errorf("Stacks[%d] = %q, want %q", i, s.Stacks[i], want[i])
		}
	}
}

// writeSnap écrit un fichier de capture brut au format attendu par load().
func writeSnap(t *testing.T, path, ts string, stacks []string) {
	t.Helper()
	content := fmt.Sprintf("# Sauvegarde\n# Timestamp: %s\n# Stack dir: /srv\n# Nombre de stacks: %d\n#\n",
		ts, len(stacks))
	for _, s := range stacks {
		content += s + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestListSortAndDistinct : deux captures à des dates différentes restent
// distinctes et sont triées de la plus récente à la plus ancienne.
func TestListSortAndDistinct(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "active_stacks")
	writeSnap(t, base+".2026-06-13_10-00-00", "2026-06-13_10-00-00", []string{"a"})
	writeSnap(t, base+".2026-06-13_12-00-00", "2026-06-13_12-00-00", []string{"a", "b"})

	snaps, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("List = %d, want 2", len(snaps))
	}
	// Tri décroissant : la capture de 12h d'abord.
	if len(snaps[0].Stacks) != 2 || len(snaps[1].Stacks) != 1 {
		t.Errorf("ordre inattendu : %v puis %v (want 2 puis 1 stacks)", snaps[0].Stacks, snaps[1].Stacks)
	}
}

// TestPruneOld : pruneOld ne conserve que les `max` archives les plus récentes
// (les noms horodatés se trient lexicographiquement comme chronologiquement).
func TestPruneOld(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "active_stacks")
	for h := 1; h <= 12; h++ {
		ts := fmt.Sprintf("2026-06-13_%02d-00-00", h)
		writeSnap(t, base+"."+ts, ts, []string{"a"})
	}

	pruneOld(base, 10)

	matches, _ := filepath.Glob(base + ".*")
	if len(matches) != 10 {
		t.Fatalf("après prune : %d archives, want 10", len(matches))
	}
	// Les deux plus anciennes (01h, 02h) doivent avoir disparu.
	for _, gone := range []string{base + ".2026-06-13_01-00-00", base + ".2026-06-13_02-00-00"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s aurait dû être supprimé", gone)
		}
	}
}
