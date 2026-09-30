package theme

import "strings"

// Popular : sélection des thèmes classiques, proposée d'abord par le
// sélecteur pour ne pas avoir à parcourir tout le catalogue. Chaque nom doit
// exister dans le catalogue embarqué (vérifié par les tests, à revoir après
// scripts/update-themes.sh).
var Popular = []string{
	// Sombres
	"Atom One Dark", "Ayu Mirage", "Builtin Tango Dark", "Catppuccin Frappe",
	"Catppuccin Macchiato", "Catppuccin Mocha", "Cobalt2", "Doom One", "Dracula",
	"Everforest Dark Med", "Flexoki Dark", "GitHub Dark Default", "GitHub Dark Dimmed",
	"Gruvbox Dark", "Gruvbox Material Dark", "Horizon", "Iceberg Dark",
	"iTerm2 Solarized Dark", "Kanagawa Dragon", "Kanagawa Wave", "Material Ocean",
	"Modus Vivendi", "Monokai Classic", "Monokai Pro", "Night Owl", "Nightfox", "Nord",
	"Oceanic Next", "One Half Dark", "Oxocarbon", "Poimandres", "Rose Pine",
	"Rose Pine Moon", "Snazzy", "Sonokai", "Synthwave", "TokyoNight", "TokyoNight Moon",
	"TokyoNight Storm", "Tomorrow Night", "Tomorrow Night Eighties", "Ubuntu", "Vesper",
	"Zenburn",
	// Clairs
	"Atom One Light", "Ayu Light", "Builtin Tango Light", "Catppuccin Latte",
	"Everforest Light Med", "Flexoki Light", "GitHub Light Default", "Gruvbox Light",
	"iTerm2 Solarized Light", "Kanagawa Lotus", "Modus Operandi", "Nord Light",
	"One Half Light", "Rose Pine Dawn", "TokyoNight Day", "Tomorrow",
}

var popularSet = func() map[string]bool {
	s := make(map[string]bool, len(Popular))
	for _, n := range Popular {
		s[strings.ToLower(n)] = true
	}
	return s
}()

// IsPopular dit si le thème fait partie de la sélection des classiques.
func IsPopular(name string) bool { return popularSet[strings.ToLower(name)] }
