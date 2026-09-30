package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

// composeHeight est le nombre de lignes de contenu de la vue compose : hauteur
// totale moins le titre, le séparateur et le pied d'aide.
func (m Model) composeHeight() int {
	h := m.height - 3
	if h < 0 {
		h = 0
	}
	return h
}

func (m Model) composeMaxScroll() int {
	return max(0, len(m.composeLines)-m.composeHeight())
}

// openComposeView ouvre la vue compose plein écran sur la stack donnée, en mode
// brut par défaut, curseur en haut.
func (m Model) openComposeView(s compose.Stack) (tea.Model, tea.Cmd) {
	m.composeStack = s
	m.composeResolved = false
	m.composeScroll = 0
	m.loadComposeContent()
	m.view = viewCompose
	return m, nil
}

// loadComposeContent (re)charge le contenu selon le mode courant : fichier brut
// (depuis le cache d'aperçu) ou config résolue (docker compose config). La
// résolution n'a lieu qu'à l'ouverture ou à la bascule, jamais en continu.
func (m *Model) loadComposeContent() {
	if m.composeResolved {
		yaml, err := compose.RenderResolvedConfig(m.composeStack)
		if err != nil {
			m.composeErr, m.composeLines = err, nil
			return
		}
		m.composeErr = nil
		m.composeLines = strings.Split(strings.TrimRight(yaml, "\n"), "\n")
		return
	}
	if m.composeStack.ComposeFile == "" {
		m.composeErr, m.composeLines = nil, nil
		return
	}
	lines, err := m.previewCache.readFile(m.composeStack.ComposeFile)
	if err != nil {
		m.composeErr, m.composeLines = err, nil
		return
	}
	m.composeErr = nil
	m.composeLines = lines
}

func renderComposeView(m Model) string {
	mode := "brut"
	if m.composeResolved {
		mode = "résolu"
	}
	title := styleBold.Foreground(lipgloss.Color("6")).Render("📄 "+m.composeStack.Name) +
		styleDim.Render("  ("+mode+")")
	// Hint dépendant du mode souris, tronqué à la largeur : un hint qui wrappe
	// ajouterait une ligne et décalerait le calcul de hauteur du corps.
	mouse := footerKey{"m", "sélectionner du texte"}
	if m.composeMouseOff {
		mouse = footerKey{"m", "molette"}
	}
	hint := renderBar([]footerKey{{"↑↓", "défiler"}, {"g/G", "haut/bas"}, {"tab", "brut/résolu"}, mouse},
		[]footerKey{{"esc", "retour"}, {"q", "quitter"}}, m.width, "")
	if m.quitArmed {
		hint = quitWarning()
	}
	sep := styleDim.Render(strings.Repeat("─", max(m.width, 1)))

	bodyH := m.composeHeight()

	var body string
	switch {
	case m.composeErr != nil:
		body = styleRed.Render(truncate("  Erreur : "+m.composeErr.Error(), m.width))
	case m.composeStack.ComposeFile == "":
		body = styleDim.Render("  (aucun fichier compose détecté pour cette stack)")
	case len(m.composeLines) == 0:
		body = styleDim.Render("  (fichier vide)")
	default:
		start := m.composeScroll
		if start < 0 {
			start = 0
		}
		if start > len(m.composeLines) {
			start = len(m.composeLines)
		}
		end := start + bodyH
		if end > len(m.composeLines) {
			end = len(m.composeLines)
		}
		lines := make([]string, 0, end-start)
		for _, l := range m.composeLines[start:end] {
			// Préfixe de 2 colonnes ; coloration tronquée à la largeur utile.
			lines = append(lines, "  "+highlightComposeLine(l, max(m.width-2, 1)))
		}
		body = strings.Join(lines, "\n")
	}

	return lipgloss.JoinVertical(lipgloss.Left, title, sep, body, hint)
}
