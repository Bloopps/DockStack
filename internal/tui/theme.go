package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/theme"
)

// Toutes les couleurs de l'interface viennent du thème actif (activeScheme) :
// les styles ci-dessous sont reconstruits par applyScheme à chaque changement
// de thème, aucune vue n'écrit de couleur en dur. Les noms styleGreen,
// styleCyan… désignent l'emplacement ANSI du thème (vert = emplacement 2),
// pas une teinte fixe.
//
// Thème par défaut : couleurs d'état prises dans les 16 couleurs du terminal,
// mises en évidence dans les indices 16-255 de la palette 256 couleurs : fixes
// (les thèmes de terminal ne redéfinissent que les 16 premières) et rendus à
// l'identique même sans couleurs 24 bits (SSH sans COLORTERM), là où un code
// hexadécimal serait approché, parfois loin (gris devenu bleu canard).
var activeScheme theme.Scheme

var (
	styleGreen   lipgloss.Style
	styleYellow  lipgloss.Style
	styleRed     lipgloss.Style
	styleCyan    lipgloss.Style
	styleMagenta lipgloss.Style
	styleOrange  lipgloss.Style
	styleBold    lipgloss.Style
	// styleDim : texte secondaire. Attribut « faint » avec le thème par
	// défaut, vraie couleur (Muted) avec un thème Ghostty : faint est rendu
	// très différemment d'un terminal à l'autre.
	styleDim lipgloss.Style
	// Sillon des barres (partie non remplie) : un gris discret peint en fond
	// rend une gouttière nette et pleine (pas d'interstices entre glyphes).
	// 238 et non 236 : sur un thème sombre, 236 se confondait avec le fond et
	// la longueur totale des barres devenait invisible.
	styleTrack lipgloss.Style
	styleSep   string

	// Ligne sous curseur : bande pleine — pas de Reverse(true), qui rend une
	// bande grise dépendante du thème du terminal.
	//
	// Couleurs fixes et non de palette : « noir sur cyan » de la palette
	// dépend du thème du terminal et devenait gris foncé sur bleu moyen
	// (Windows Terminal), illisible. Bleu nuit + blanc : contraste élevé sur
	// tout thème sombre.
	styleSelected   lipgloss.Style
	styleCursorMark lipgloss.Style
	styleGroup      lipgloss.Style

	// Nom de stack selon son état.
	styleNameRunning   lipgloss.Style
	styleNamePartial   lipgloss.Style
	styleNameUnhealthy lipgloss.Style
	styleNameStopped   lipgloss.Style
	styleNameInactive  lipgloss.Style

	// Compteur de conteneurs selon l'état.
	styleCountRunning   lipgloss.Style
	styleCountPartial   lipgloss.Style
	styleCountUnhealthy lipgloss.Style
	styleCountStopped   lipgloss.Style

	// Touche en bleu clair gras sur fond gris ardoise : lisible sur tout thème
	// (l'ancien noir sur cyan de palette ne l'était pas), et moins criard que
	// des pavés pleins répétés sur toute la barre.
	styleFooterKey  lipgloss.Style
	styleFooterDesc lipgloss.Style
	styleFooterSep  lipgloss.Style
	// styleFooterBlockSep sépare les touches de l'écran de la sortie et de l'aide.
	styleFooterBlockSep string

	stylePanel        lipgloss.Style
	stylePanelTitle   lipgloss.Style
	styleCapturePanel lipgloss.Style
	styleDetailPanel  lipgloss.Style
	// styleTitle : titres des vues plein écran (logs, compose, dossiers).
	styleTitle lipgloss.Style

	// Coloration YAML de l'aperçu : mêmes rôles que le reste de l'interface.
	styleYamlKey    lipgloss.Style // clé indentée
	styleYamlTopKey lipgloss.Style // clé de premier niveau (section)
	styleYamlPunct  lipgloss.Style // « : », « - », commentaires
	styleYamlString lipgloss.Style // valeur entre guillemets / scalaire libre
	styleYamlNumber lipgloss.Style // entier / flottant / port
	styleYamlBool   lipgloss.Style // true/false/null/yes/no
	styleYamlEnvVar lipgloss.Style // interpolation ${VAR}
)

func init() { applyScheme(theme.Default()) }

// applyScheme active un thème : tous les styles globaux sont reconstruits.
// Appelé depuis Update (boucle unique de bubbletea), jamais en concurrence
// avec un rendu.
func applyScheme(s theme.Scheme) {
	activeScheme = s
	fg := func(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

	styleGreen = fg(s.OK)
	styleYellow = fg(s.Warn)
	styleRed = fg(s.Danger)
	styleCyan = fg(s.Accent)
	styleMagenta = fg(s.Group)
	styleOrange = fg(s.Degraded)
	styleBold = lipgloss.NewStyle().Bold(true)
	if s.Muted != nil {
		styleDim = fg(s.Muted)
	} else {
		styleDim = lipgloss.NewStyle().Faint(true)
	}
	styleTrack = lipgloss.NewStyle().Background(s.Track)
	styleSep = styleMagenta.Render(" │ ")

	styleSelected = lipgloss.NewStyle().Background(s.SelBg).Foreground(s.SelFg).Bold(true)
	styleCursorMark = fg(s.Key).Bold(true)
	styleGroup = styleMagenta.Bold(true)

	styleNameRunning = fg(s.OK).Bold(true)
	styleNamePartial = fg(s.Warn)
	styleNameUnhealthy = fg(s.Degraded).Bold(true)
	styleNameStopped = fg(s.Danger)
	styleNameInactive = fg(s.Inactive)

	count := func(c color.Color) lipgloss.Style {
		if soft, ok := s.Soften(c); ok {
			return fg(soft)
		}
		return fg(c).Faint(true)
	}
	styleCountRunning = count(s.OK)
	styleCountPartial = count(s.Warn)
	styleCountUnhealthy = count(s.Degraded)
	styleCountStopped = count(s.Danger)

	styleFooterKey = fg(s.Key).Background(s.KeyBg).Bold(true).Padding(0, 1)
	styleFooterDesc = fg(s.KeyText)
	styleFooterSep = fg(s.Rule).SetString(" · ")
	styleFooterBlockSep = fg(s.BlockSep).Render("   │   ")

	stylePanel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.Border).Padding(0, 1)
	stylePanelTitle = fg(s.Strong).Bold(true)
	styleCapturePanel = stylePanel
	styleDetailPanel = stylePanel.BorderForeground(s.BorderMuted)
	styleTitle = styleCyan.Bold(true)

	styleYamlKey = styleCyan
	styleYamlTopKey = styleCyan.Bold(true)
	styleYamlPunct = styleDim
	styleYamlString = styleGreen
	styleYamlNumber = styleYellow
	styleYamlBool = styleMagenta
	styleYamlEnvVar = styleOrange
}

// schemeByName : le thème de ce nom dans le catalogue, ou le thème par
// défaut (ok=false si le nom est inconnu).
func schemeByName(themes []theme.Palette, name string) (theme.Scheme, bool) {
	if name == "" || name == theme.DefaultName {
		return theme.Default(), true
	}
	p, ok := theme.Find(themes, name)
	if !ok {
		return theme.Default(), false
	}
	return theme.FromPalette(p), true
}

// slotStyle : style d'un emplacement de couleur nommé (panneau d'actions).
func slotStyle(slot string) lipgloss.Style {
	switch slot {
	case "green":
		return styleGreen
	case "yellow":
		return styleYellow
	case "red":
		return styleRed
	case "cyan":
		return styleCyan
	default:
		return lipgloss.NewStyle().Foreground(activeScheme.BorderMuted)
	}
}
