package compose

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Noms tirés par le fuzzing : fichiers compose de toutes priorités, fichiers
// voisins, dossiers sautés à la racine ou partout, dossiers cachés.
var fuzzNames = []string{
	"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml",
	"compose.override.yaml", ".env", "README.md", "a", "b", "stack",
	"node_modules", ".git", "proc", "tmp", ".cache", "docker-compose.yml.bak",
}

// FuzzScanComposeDir : arborescences générées. Propriétés : pas de panique,
// au plus un fichier par dossier, et c'est le plus prioritaire ; tout dossier
// atteignable (profondeur, dossiers sautés) qui contient un compose est
// trouvé.
func FuzzScanComposeDir(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3})
	f.Add([]byte{0x47, 0x10, 0x23, 0x31, 0x52, 0x60, 0x7f})
	f.Add([]byte{0x9a, 0x8b, 0xc3, 0xd0, 0x0e, 0xff, 0x11, 0x22, 0x33})
	f.Fuzz(func(t *testing.T, data []byte) {
		root := t.TempDir()
		// Chaque octet : 2 bits de profondeur relative, 4 bits de nom,
		// 1 bit dossier/fichier. On construit un chemin courant.
		var cur []string
		for _, b := range data {
			depth := int(b>>6) % 3
			if depth < len(cur) {
				cur = cur[:depth]
			}
			name := fuzzNames[int(b>>1)%len(fuzzNames)]
			p := filepath.Join(append([]string{root}, append(cur, name)...)...)
			if b&1 == 1 && len(cur) < 6 {
				if os.MkdirAll(p, 0o755) == nil {
					cur = append(cur, name)
				}
				continue
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				continue
			}
			_ = os.WriteFile(p, []byte("services: {}\n"), 0o644)
		}

		var got []string
		if err := scanComposeDir(root, 0, &got); err != nil {
			t.Fatal(err)
		}

		// Référence indépendante : WalkDir avec les mêmes règles.
		var want []string
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			depth := 0
			if rel != "." {
				depth = strings.Count(rel, string(filepath.Separator)) + 1
				n := d.Name()
				if skipDirsAlways[n] || (depth == 1 && skipDirsRoot[n]) || strings.HasPrefix(n, ".") || depth > maxScanDepth {
					return filepath.SkipDir
				}
			}
			for _, n := range composeNamesByPriority {
				if st, err := os.Stat(filepath.Join(p, n)); err == nil && !st.IsDir() {
					want = append(want, filepath.Join(p, n))
					break
				}
			}
			return nil
		})
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("scan = %v\nréférence = %v", got, want)
		}
	})
}

// FuzzLoadProjectDotEnv : un .env quelconque (lu par compose-go, dépendance
// tierce) ne fait jamais paniquer le chargement du projet ; une erreur est
// acceptable.
func FuzzLoadProjectDotEnv(f *testing.F) {
	f.Add("A=1\nB=${A}\n")
	f.Add("key with space=1\n")
	f.Add("X='unterminated\n")
	f.Add("export Y=\"a\\nb\"\n#c\n=\n")
	f.Add("IMG=${UNSET:?requis}\n")
	f.Fuzz(func(t *testing.T, env string) {
		dir := filepath.Join(t.TempDir(), "zzdsprobefuzz")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		cf := filepath.Join(dir, "compose.yaml")
		yaml := "services:\n  web:\n    image: busybox:${IMG:-latest}\n    environment:\n      - A=${A:-x}\n"
		if err := os.WriteFile(cf, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = loadProject(Stack{Name: "zzdsprobefuzz", Dir: dir, ComposeFile: cf})
	})
}
