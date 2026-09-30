package theme

import (
	_ "embed"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

//go:embed ghostty-themes.txt
var bundle string

var (
	builtinOnce sync.Once
	builtin     []Palette
	builtinErr  error
)

// Builtin : les thèmes livrés avec Ghostty, lus une seule fois.
func Builtin() ([]Palette, error) {
	builtinOnce.Do(func() {
		builtin, builtinErr = parseBundle(bundle)
	})
	return builtin, builtinErr
}

// UserDirs : dossiers des thèmes personnels, par priorité croissante. Celui de
// Ghostty permet de réutiliser tel quel un thème déjà écrit pour lui ; celui
// de dockstack l'emporte en cas de doublon.
func UserDirs(configDir string) []string {
	var dirs []string
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xdg = filepath.Join(home, ".config")
		}
	}
	if xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "ghostty", "themes"))
	}
	if configDir != "" {
		dirs = append(dirs, filepath.Join(configDir, "themes"))
	}
	return dirs
}

// Catalog : thèmes embarqués puis thèmes personnels (un fichier par thème,
// nommé comme le thème, format Ghostty). Un thème personnel remplace un thème
// embarqué de même nom. Trié par nom, sans tenir compte de la casse. Les
// fichiers illisibles sont ignorés et remontés dans errs, sans bloquer le
// reste du catalogue.
func Catalog(configDir string) (themes []Palette, errs []error) {
	b, err := Builtin()
	if err != nil {
		errs = append(errs, err)
	}
	byName := make(map[string]Palette, len(b))
	for _, p := range b {
		byName[strings.ToLower(p.Name)] = p
	}
	for _, dir := range UserDirs(configDir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // dossier absent : cas normal
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			f, err := os.Open(path)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			p, err := Parse(e.Name(), f)
			f.Close()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			p.Path = path
			byName[strings.ToLower(p.Name)] = p
		}
	}
	themes = make([]Palette, 0, len(byName))
	for _, p := range byName {
		themes = append(themes, p)
	}
	sort.Slice(themes, func(i, j int) bool {
		a, b := strings.ToLower(themes[i].Name), strings.ToLower(themes[j].Name)
		if a != b {
			return a < b
		}
		return themes[i].Name < themes[j].Name
	})
	return themes, errs
}

// Find cherche un thème par nom (casse ignorée).
func Find(themes []Palette, name string) (Palette, bool) {
	for _, p := range themes {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Palette{}, false
}
