package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/theme"
)

// Sélecteur de thème (touche t) : catalogue Ghostty embarqué + thèmes
// personnels, filtrable. Le thème sous le curseur est appliqué en direct à
// toute l'interface (en-tête, pied de page, aperçu) ; ↩ l'enregistre dans la
// config, esc revient au thème d'avant.

// themeRow : une ligne du sélecteur.
type themeRow struct {
	name string
	pal  *theme.Palette // nil : thème par défaut de dockstack
}

// Onglets du sélecteur, parcourus avec tab : la sélection des classiques
// d'abord (le catalogue complet est long à parcourir), puis tout le
// catalogue, filtrable par luminosité du fond.
const (
	themePopular = iota
	themeAll
	themeDark
	themeLight
)

var themeKindLabels = [...]string{"populaires", "tous", "sombres", "clairs"}

// themePickerW : largeur de la colonne des thèmes.
func (m Model) themePickerW() int {
	return min(48, max(32, m.width*2/5))
}

// loadTheme applique au démarrage le thème enregistré dans la config.
func (m *Model) loadTheme() {
	name := ""
	if m.cfg != nil {
		name = m.cfg.Theme
	}
	var themes []theme.Palette
	if name != "" && name != theme.DefaultName {
		themes, _ = theme.Catalog(m.cfg.ConfigDir())
	}
	s, ok := schemeByName(themes, name)
	applyScheme(s)
	m.themeName = s.Name
	m.restyleWidgets()
	if !ok {
		m.setStatus(fmt.Sprintf("Thème « %s » introuvable : thème par défaut", name), true)
	}
}

// openThemePicker ouvre le sélecteur, curseur sur le thème actif.
func (m Model) openThemePicker() (tea.Model, tea.Cmd) {
	m.themes, m.themeErrs = theme.Catalog(m.cfg.ConfigDir())
	m.themeOrig = m.themeName
	m.themeFilter, m.themeFiltering, m.themeKind = "", false, themePopular
	// Thème actif hors de la sélection : ouvrir sur tout le catalogue, pour
	// que le curseur tombe dessus.
	if m.themeName != theme.DefaultName && !theme.IsPopular(m.themeName) {
		m.themeKind = themeAll
	}
	m.themeCursor = 0
	for i, r := range m.themeRows() {
		if strings.EqualFold(r.name, m.themeName) {
			m.themeCursor = i
			break
		}
	}
	m.view = viewTheme
	return m, nil
}

// themeRows : thèmes visibles selon l'onglet et le filtre texte. Une
// recherche depuis « populaires » porte sur tout le catalogue : un nom tapé
// doit être trouvé même s'il n'est pas dans la sélection. Le thème par défaut
// n'apparaît pas sous « sombres » ni « clairs » : il suit le terminal.
func (m Model) themeRows() []themeRow {
	return m.themeRowsOf(m.themeKind)
}

func (m Model) themeRowsOf(kind int) []themeRow {
	f := strings.ToLower(m.themeFilter)
	if kind == themePopular && f != "" {
		kind = themeAll
	}
	var rows []themeRow
	if (kind == themePopular || kind == themeAll) && strings.Contains(theme.DefaultName, f) {
		rows = append(rows, themeRow{name: theme.DefaultName})
	}
	for i := range m.themes {
		p := &m.themes[i]
		if (kind == themeDark && p.Light()) || (kind == themeLight && !p.Light()) ||
			(kind == themePopular && !theme.IsPopular(p.Name)) {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(p.Name), f) {
			continue
		}
		rows = append(rows, themeRow{name: p.Name, pal: p})
	}
	return rows
}

// useThemeRow active le thème d'une ligne (aperçu, rien n'est enregistré).
func (m *Model) useThemeRow(r themeRow) {
	s := theme.Default()
	if r.pal != nil {
		s = theme.FromPalette(*r.pal)
	}
	applyScheme(s)
	m.themeName = s.Name
	m.restyleWidgets()
}

// previewThemeCursor borne le curseur et applique le thème qu'il désigne.
func (m *Model) previewThemeCursor() {
	rows := m.themeRows()
	if len(rows) == 0 {
		m.themeCursor = 0
		return
	}
	m.themeCursor = min(max(m.themeCursor, 0), len(rows)-1)
	if r := rows[m.themeCursor]; !strings.EqualFold(r.name, m.themeName) {
		m.useThemeRow(r)
	}
}

// closeThemePicker quitte le sélecteur ; save enregistre le thème affiché,
// sinon le thème d'avant l'ouverture est rétabli.
func (m Model) closeThemePicker(save bool) (tea.Model, tea.Cmd) {
	m.view = viewList
	m.themeFiltering = false
	if !save {
		s, _ := schemeByName(m.themes, m.themeOrig)
		applyScheme(s)
		m.themeName = s.Name
		m.restyleWidgets()
		return m, nil
	}
	prev := m.cfg.Theme
	m.cfg.Theme = m.themeName
	if m.themeName == theme.DefaultName {
		m.cfg.Theme = "" // défaut : rien à écrire
	}
	if err := m.cfg.Save(); err != nil {
		m.cfg.Theme = prev
		m.setStatus("Thème non enregistré : "+err.Error(), true)
		return m, m.clearStatusIn(5 * time.Second)
	}
	m.setStatus(fmt.Sprintf("Thème « %s » enregistré", m.themeName), false)
	return m, m.clearStatusIn(3 * time.Second)
}

func (m Model) handleThemeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	page := max(m.bodyHeight()-6, 1)
	if m.themeFiltering {
		switch msg.String() {
		case "esc":
			m.themeFiltering, m.themeFilter = false, ""
		case "enter":
			m.themeFiltering = false
		case "backspace":
			if r := []rune(m.themeFilter); len(r) > 0 {
				m.themeFilter = string(r[:len(r)-1])
			}
			m.themeCursor = 0
		case "up":
			m.themeCursor--
		case "down":
			m.themeCursor++
		default:
			if t := msg.Key().Text; t != "" {
				m.themeFilter += t
				m.themeCursor = 0
			}
		}
		m.previewThemeCursor()
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		m.themeCursor--
	case "down", "j":
		m.themeCursor++
	case "pgup":
		m.themeCursor -= page
	case "pgdown":
		m.themeCursor += page
	case "g", "home":
		m.themeCursor = 0
	case "G", "end":
		m.themeCursor = len(m.themeRows()) - 1
	case "/":
		m.themeFiltering = true
	case "tab":
		// Garde le thème courant sous le curseur s'il reste visible.
		cur := m.themeName
		m.themeKind = (m.themeKind + 1) % len(themeKindLabels)
		m.themeCursor = 0
		for i, r := range m.themeRows() {
			if strings.EqualFold(r.name, cur) {
				m.themeCursor = i
				break
			}
		}
	case "enter":
		if len(m.themeRows()) > 0 {
			return m.closeThemePicker(true)
		}
	case "esc":
		if m.themeFilter != "" {
			m.themeFilter = ""
			m.themeCursor = 0
			for i, r := range m.themeRows() {
				if strings.EqualFold(r.name, m.themeName) {
					m.themeCursor = i
					break
				}
			}
			break
		}
		return m.closeThemePicker(false)
	default:
		return m, nil
	}
	m.previewThemeCursor()
	return m, nil
}

func (m Model) themeFooter() (left, right []footerKey, info string) {
	rows := m.themeRows()
	if len(rows) > 0 {
		info = fmt.Sprintf("%d/%d", m.themeCursor+1, len(rows))
	}
	if m.themeFiltering {
		return []footerKey{{"↩", "valider"}, {"↑↓", "choisir"}}, []footerKey{{"esc", "effacer"}}, info
	}
	return []footerKey{{"↑↓", "choisir"}, {"/", "chercher"}, {"tab", "onglet"}, {"↩", "appliquer"}},
		[]footerKey{{"esc", "annuler"}}, info
}

// themeSwatch : échantillon d'un thème, les six couleurs d'état sur son
// propre fond — indépendant du thème actif, pour comparer d'un coup d'œil.
func themeSwatch(r themeRow) string {
	if r.pal == nil {
		// Thème par défaut : couleurs du terminal, sans fond imposé.
		var b strings.Builder
		b.WriteString(" ")
		for _, i := range []string{"1", "2", "3", "4", "5", "6"} {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(i)).Render("●"))
		}
		return b.String() + " "
	}
	bg := lipgloss.NewStyle().Background(r.pal.Bg)
	var b strings.Builder
	b.WriteString(bg.Render(" "))
	for i := 1; i <= 6; i++ {
		b.WriteString(bg.Foreground(r.pal.ANSI[i]).Render("●"))
	}
	return b.String() + bg.Render(" ")
}

const themeSwatchW = 8

// renderThemePicker : liste des thèmes à gauche, aperçu à droite.
func renderThemePicker(m Model, height int) string {
	lw := m.themePickerW()
	left := themePickerColumn(m, lw, height)
	rw := m.width - lw
	if rw < 20 {
		return strings.Join(left, "\n")
	}
	right := themePreviewColumn(m, rw, height)
	lines := make([]string, height)
	for i := range lines {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		l = ansi.Truncate(l, lw-1, "…")
		lines[i] = l + strings.Repeat(" ", max(lw-lipgloss.Width(l), 0)) + r
	}
	return strings.Join(lines, "\n")
}

func themePickerColumn(m Model, w, height int) []string {
	rows := m.themeRows()
	inner := w - 2
	out := []string{"  " + styleTitle.Render("🎨 Thème")}

	// Onglets ; l'actif porte son nombre de thèmes (hors filtre texte).
	var kinds []string
	noFilter := m
	noFilter.themeFilter = ""
	for i, k := range themeKindLabels {
		if i == m.themeKind {
			label := fmt.Sprintf("%s %d", k, len(noFilter.themeRowsOf(i)))
			kinds = append(kinds, styleCursorMark.Underline(true).Render(label))
		} else {
			kinds = append(kinds, styleDim.Render(k))
		}
	}
	out = append(out, "  "+strings.Join(kinds, styleDim.Render(" · ")))
	switch {
	case m.themeFiltering:
		out = append(out, "  "+styleCyan.Render("/ ")+m.themeFilter+styleCyan.Render("▏"))
	case m.themeFilter != "":
		out = append(out, "  "+styleDim.Render(truncate("/ "+m.themeFilter+"  (esc : effacer)", inner)))
	default:
		out = append(out, "  "+styleDim.Render("/ chercher dans les "+fmt.Sprint(len(m.themes)+1)+" thèmes"))
	}
	if len(m.themeErrs) > 0 {
		out = append(out, "  "+styleRed.Render(truncate(fmt.Sprintf("%d thème%s perso illisible%s : %v", len(m.themeErrs), plural(len(m.themeErrs)), plural(len(m.themeErrs)), m.themeErrs[0]), inner)))
	}
	out = append(out, "")

	if len(rows) == 0 {
		return append(out, "  "+styleDim.Render("(aucun thème ne correspond)"))
	}
	listH := max(height-len(out), 1)
	start := min(max(m.themeCursor-listH/2, 0), max(len(rows)-listH, 0))
	end := min(start+listH, len(rows))
	nameW := max(inner-themeSwatchW-4, 8)
	for i := start; i < end; i++ {
		r := rows[i]
		name := r.name
		if r.pal == nil {
			name += " (terminal)"
		} else if r.pal.Path != "" {
			name += " (perso)"
		}
		name = truncate(name, nameW)
		saved := " "
		if strings.EqualFold(r.name, m.cfg.Theme) || (m.cfg.Theme == "" && r.pal == nil) {
			saved = styleGreen.Render("✓")
		}
		pad := strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))
		var line string
		if i == m.themeCursor {
			// Texte brut dans une seule bande : pas de spans stylés imbriqués.
			line = styleCursorMark.Render("▶") + saved + styleSelected.Render(" "+name+pad+" ")
		} else {
			line = " " + saved + " " + name + pad + " "
		}
		out = append(out, line+themeSwatch(r))
	}
	return out
}

// themePreviewColumn : échantillon de tous les éléments colorés, puis la vraie
// liste des stacks sur la hauteur restante.
func themePreviewColumn(m Model, w, height int) []string {
	inner := w - 2
	rule := styleDim.Render("│") + " "
	yaml := []string{
		"services:",
		"  web:",
		`    image: "nginx:1.27"`,
		"    ports: [8080]",
		"    read_only: true",
		"    environment:",
		"      - TZ=${TZ:-Europe/Paris} # fuseau",
	}
	sample := []string{
		styleDim.Render("Aperçu") + "  " + styleBold.Render(m.themeName),
		"",
		stateDot(compose.StateRunning) + " " + styleNameRunning.Render("en marche") + " " + styleCountRunning.Render("2/2") + "   " +
			stateDot(compose.StatePartial) + " " + styleNamePartial.Render("partielle") + " " + styleCountPartial.Render("1/3"),
		stateDot(compose.StateUnhealthy) + " " + styleNameUnhealthy.Render("unhealthy") + " " + styleCountUnhealthy.Render("1/1") + "   " +
			stateDot(compose.StateStopped) + " " + styleNameStopped.Render("arrêtée") + " " + styleCountStopped.Render("0/2") + "   " +
			stateDot(compose.StateUnknown) + " " + styleNameInactive.Render("non déployée"),
		styleCursorMark.Render("▶") + styleSelected.Render(" ligne sous le curseur "+strings.Repeat(" ", 6)) + "  " + styleDim.Render("texte secondaire"),
		"",
		barText(35, 14, colorForPct(35), "35 %") + "  " + barText(70, 14, colorForPct(70), "70 %") + "  " + barText(92, 14, colorForPct(92), "92 %"),
		"",
		styleFooterKey.Render("u") + " " + styleFooterDesc.Render("up") + styleFooterSep.String() +
			styleFooterKey.Render("/") + " " + styleFooterDesc.Render("filtre") + styleFooterBlockSep +
			styleFooterKey.Render("?") + " " + styleFooterDesc.Render("aide"),
		"",
	}
	for _, l := range yaml {
		sample = append(sample, highlightComposeLine(l, inner))
	}
	sample = append(sample, "", styleDim.Render("Vos stacks"))

	out := make([]string, 0, height)
	for _, l := range sample {
		out = append(out, rule+ansi.Truncate(l, inner, "…"))
	}
	if listH := height - len(out); listH > 2 {
		for _, l := range strings.Split(renderStackList(m, listH, inner), "\n") {
			out = append(out, rule+l)
		}
	}
	for len(out) < height {
		out = append(out, strings.TrimRight(rule, " "))
	}
	return out[:height]
}
