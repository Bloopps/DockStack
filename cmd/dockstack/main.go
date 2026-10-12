package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/tui"
)

// renderFPS : cadence maximale du moteur de rendu de bubbletea.
const renderFPS = 20

func main() {
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
