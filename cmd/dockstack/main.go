package main

import (
	"fmt"
	"os"
	"runtime/debug"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/tui"
)

// renderFPS : cadence maximale du moteur de rendu de bubbletea.
const renderFPS = 20

// Renseignés par GoReleaser (ldflags par défaut -X main.version=…).
var (
	version = ""
	commit  = ""
)

const usage = `dockstack : gestion des stacks Docker Compose dans le terminal.

Usage :
  dockstack              lancer l'interface
  dockstack --version    afficher la version
  dockstack --help       afficher cette aide

Configuration : ~/.dockstack/config.json (aide complète : touche ? dans l'interface).
`

// versionString : version injectée à la compilation, sinon celle du module
// (go install …@v0.x) ou de la révision VCS, sinon « dev ».
func versionString() string {
	v, c := version, commit
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && c == "" {
				c = s.Value
			}
		}
	}
	if v == "" {
		v = "dev"
	}
	if len(c) > 12 {
		c = c[:12]
	}
	if c != "" {
		return fmt.Sprintf("dockstack %s (%s)", v, c)
	}
	return "dockstack " + v
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-v", "--version", "version":
			fmt.Println(versionString())
			return
		case "-h", "--help", "help":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "argument inconnu : %s\n\n%s", os.Args[1], usage)
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "erreur config: %v\n", err)
		os.Exit(1)
	}

	// In bubbletea v2, alt-screen and mouse mode are no longer program options:
	// they're set on the View returned by Model.View() (see tui.Model.View).
	// 20 images/s au lieu de 60 : le moteur de rendu se réveille à cette
	// cadence même quand rien ne change, et c'était le premier poste de CPU
	// au repos (≈ 5 % → 3,4 % d'un cœur mesuré). Reste fluide pour le spinner
	// et le défilement.
	p := tea.NewProgram(tui.New(cfg), tea.WithFPS(renderFPS))

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "erreur TUI: %v\n", err)
		os.Exit(1)
	}
}
