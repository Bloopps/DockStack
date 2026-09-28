package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func names(es ...string) []Entry {
	out := make([]Entry, len(es))
	for i, n := range es {
		out[i] = Entry{Name: n}
	}
	return out
}

// Save écrit une capture v2 ET le fichier texte « dernier état » ; List ne
// renvoie qu'une capture, avec les services conservés.
func TestSaveThenList(t *testing.T) {
	dir := t.TempDir()
	want := []Entry{{Name: "immich", Services: []string{"db", "server"}}, {Name: "traefik"}, {Name: "jellyfin"}}
	if _, err := Save(dir, "/srv/stacks", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "active_stacks")); err != nil {
		t.Error("le fichier texte active_stacks doit rester écrit (scripts externes)")
	}
	snaps, err := List(dir)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("List = %d captures, %v, want 1 (le texte est un doublon)", len(snaps), err)
	}
	s := snaps[0]
	if s.Legacy || s.StackDir != "/srv/stacks" || len(s.Entries) != 3 {
		t.Fatalf("capture inattendue : %+v", s)
	}
	if strings.Join(s.Entries[0].Services, ",") != "db,server" || s.Stacks[1] != "traefik" {
		t.Errorf("services/noms perdus : %+v", s)
	}
}

// writeLegacy écrit une capture au format texte historique.
func writeLegacy(t *testing.T, path, ts string, stacks []string) {
	t.Helper()
	content := fmt.Sprintf("# Sauvegarde\n# Timestamp: %s\n# Stack dir: /srv\n# Nombre de stacks: %d\n#\n%s\n",
		ts, len(stacks), strings.Join(stacks, "\n"))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Les anciennes captures restent lisibles, triées, sans détail de services ;
// les dates sont lues en heure locale (pas en UTC).
func TestListLegacyAndSort(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "active_stacks")
	writeLegacy(t, b+".2026-06-13_10-00-00", "2026-06-13_10-00-00", []string{"a"})
	writeLegacy(t, b+".2026-06-13_12-00-00", "2026-06-13_12-00-00", []string{"a", "b"})
	writeLegacy(t, b, "2026-06-13_12-00-00", []string{"a", "b"}) // doublon du dernier état

	snaps, _ := List(dir)
	if len(snaps) != 2 || len(snaps[0].Stacks) != 2 || len(snaps[1].Stacks) != 1 {
		t.Fatalf("attendu 2 captures (12h puis 10h), eu %+v", snaps)
	}
	if !snaps[0].Legacy || len(snaps[0].Items()) != 2 || len(snaps[0].Items()[0].Services) != 0 {
		t.Errorf("capture ancienne : Items = %+v", snaps[0].Items())
	}
	if h := snaps[0].Date.Hour(); h != 12 || snaps[0].Date.Location() != time.Local {
		t.Errorf("date lue en %v à %dh, want heure locale 12h", snaps[0].Date.Location(), h)
	}
}

// Une date illisible ne fait pas disparaître la capture : repli sur la date du fichier.
func TestListBadTimestampKept(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "active_stacks")
	writeLegacy(t, b+".x1", "n'importe quoi", []string{"a"})
	writeLegacy(t, b+".x2", "encore pire", []string{"a"}) // même nombre de stacks : l'ancienne dédup les fusionnait
	snaps, _ := List(dir)
	if len(snaps) != 2 || snaps[0].Date.IsZero() {
		t.Fatalf("captures à date illisible perdues ou datées 0 : %+v", snaps)
	}
}

// Deux captures dans la même seconde ne s'écrasent pas.
func TestSaveSameSecond(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := Save(dir, "/srv", names(fmt.Sprintf("s%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if snaps, _ := List(dir); len(snaps) != 3 {
		t.Fatalf("%d captures, want 3 (aucune écrasée)", len(snaps))
	}
}

// La rotation garde KeepUnpinned captures, ne supprime jamais une épinglée et
// annonce le nombre supprimé.
func TestPruneKeepsPinned(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "active_stacks")
	for h := 1; h <= 12; h++ {
		ts := fmt.Sprintf("2026-06-13_%02d-00-00", h)
		writeLegacy(t, b+"."+ts, ts, []string{"a"})
	}
	// La plus ancienne (01h) est épinglée : elle doit survivre.
	snaps, _ := List(dir)
	oldest := snaps[len(snaps)-1]
	if err := SetMeta(dir, oldest, "précieuse", true); err != nil {
		t.Fatal(err)
	}
	pruned, err := Save(dir, "/srv", names("z"))
	if err != nil {
		t.Fatal(err)
	}
	snaps, _ = List(dir)
	pinned := 0
	for _, s := range snaps {
		if s.Pinned {
			pinned++
			if s.Label != "précieuse" {
				t.Errorf("libellé perdu : %q", s.Label)
			}
		}
	}
	// 12 anciennes + 1 nouvelle = 13, dont 1 épinglée : on garde 10 + l'épinglée.
	if pinned != 1 || len(snaps) != KeepUnpinned+1 || pruned != 13-1-KeepUnpinned {
		t.Fatalf("épinglées=%d total=%d supprimées=%d, want 1 / %d / %d", pinned, len(snaps), pruned, KeepUnpinned+1, 13-1-KeepUnpinned)
	}
}

// Libellé et épinglage : une capture v2 est modifiée sur place, une ancienne
// est convertie sans doublon ni perte de date.
func TestSetMeta(t *testing.T) {
	dir := t.TempDir()
	b := filepath.Join(dir, "active_stacks")
	writeLegacy(t, b+".2026-06-13_10-00-00", "2026-06-13_10-00-00", []string{"a", "b"})
	snaps, _ := List(dir)
	date := snaps[0].Date
	if err := SetMeta(dir, snaps[0], "  avant maj  ", true); err != nil {
		t.Fatal(err)
	}
	snaps, _ = List(dir)
	if len(snaps) != 1 || snaps[0].Legacy || snaps[0].Label != "avant maj" || !snaps[0].Pinned || !snaps[0].Date.Equal(date) || len(snaps[0].Stacks) != 2 {
		t.Fatalf("conversion v2 incorrecte : %+v", snaps)
	}
	if err := SetMeta(dir, snaps[0], "", false); err != nil {
		t.Fatal(err)
	}
	if snaps, _ = List(dir); snaps[0].Label != "" || snaps[0].Pinned {
		t.Errorf("libellé/épinglage non retirés : %+v", snaps[0])
	}
}

// Delete retire la capture et son doublon texte, sans toucher aux autres.
func TestDelete(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "/srv", names("a")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := Save(dir, "/srv", names("a", "b")); err != nil {
		t.Fatal(err)
	}
	snaps, _ := List(dir)
	if err := Delete(dir, snaps[0]); err != nil {
		t.Fatal(err)
	}
	snaps, _ = List(dir)
	if len(snaps) != 1 || len(snaps[0].Stacks) != 1 {
		t.Fatalf("après Delete : %+v, want seule la 1re capture", snaps)
	}
	if _, err := os.Stat(filepath.Join(dir, "active_stacks")); !os.IsNotExist(err) {
		t.Error("le doublon texte de la capture supprimée aurait dû l'être aussi")
	}
}

// L'écriture ne laisse aucun fichier temporaire, et crée le dossier manquant.
func TestSaveCreatesDirNoTemp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	if _, err := Save(dir, "/srv", names("a")); err != nil {
		t.Fatalf("Save dans un dossier absent : %v", err)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, ".capture-*")); len(tmp) != 0 {
		t.Errorf("fichiers temporaires restants : %v", tmp)
	}
	if snaps, _ := List(dir); len(snaps) != 1 {
		t.Errorf("List = %d capture(s), want 1", len(snaps))
	}
}
