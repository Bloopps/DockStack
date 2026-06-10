package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/user/dockerstack/internal/config"
	"github.com/user/dockerstack/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "erreur config: %v\n", err)
		os.Exit(1)
	}

	// In bubbletea v2, alt-screen and mouse mode are no longer program options:
	// they're set on the View returned by Model.View() (see tui.Model.View).
	p := tea.NewProgram(tui.New(cfg))

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "erreur TUI: %v\n", err)
		os.Exit(1)
	}
}
