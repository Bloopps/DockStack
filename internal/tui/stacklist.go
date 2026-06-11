package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

var (
	// Ligne sous curseur : bande cyan, texte noir — pas de Reverse(true), qui
	// rend une bande grise dépendante du thème du terminal.
	styleSelected   = lipgloss.NewStyle().Background(lipgloss.Color("6")).Foreground(lipgloss.Color("0")).Bold(true)
	styleCursorMark = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	styleGroup      = styleMagenta.Bold(true)

	// Per-state name styles
	styleNameRunning   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	styleNamePartial   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleNameUnhealthy = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true)
	styleNameStopped   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleNameInactive  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Per-state count badge
	styleCountRunning   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Faint(true)
	styleCountPartial   = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Faint(true)
	styleCountUnhealthy = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Faint(true)
	styleCountStopped   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Faint(true)
)

func statePriority(s compose.State) int {
	switch s {
	case compose.StateUnhealthy:
		return 0
	case compose.StateRunning:
		return 1
	case compose.StatePartial:
		return 2
	case compose.StateStopped:
		return 3
	default:
		return 4
	}
}

func sortByState(stacks []compose.Stack) []compose.Stack {
	if len(stacks) == 0 {
		return stacks
	}
	type groupEntry struct {
		members      []compose.Stack
		bestPriority int
	}
	groups := make(map[string]*groupEntry)
	var order []string
	for _, s := range stacks {
		key := s.Group
		if _, ok := groups[key]; !ok {
			groups[key] = &groupEntry{bestPriority: 4}
			order = append(order, key)
		}
		g := groups[key]
		g.members = append(g.members, s)
		if p := statePriority(s.State()); p < g.bestPriority {
			g.bestPriority = p
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		pi := groups[order[i]].bestPriority
		pj := groups[order[j]].bestPriority
		if pi != pj {
			return pi < pj
		}
		return order[i] < order[j]
	})
	result := make([]compose.Stack, 0, len(stacks))
	for _, key := range order {
		members := groups[key].members
		sort.SliceStable(members, func(i, j int) bool {
			pi := statePriority(members[i].State())
			pj := statePriority(members[j].State())
			if pi != pj {
				return pi < pj
			}
			return members[i].Name < members[j].Name
		})
		result = append(result, members...)
	}
	return result
}

func stateStyle(s compose.State) (string, lipgloss.Style) {
	switch s {
	case compose.StateUnhealthy:
		return "●", styleOrange
	case compose.StateRunning:
		return "●", styleGreen
	case compose.StatePartial:
		return "●", styleYellow
	case compose.StateStopped:
		return "●", styleRed
	default:
		return "○", styleDim
	}
}

func stateLabel(stack compose.Stack) string {
	switch stack.State() {
	case compose.StateUnhealthy:
		return fmt.Sprintf("Unhealthy (%d/%d)", stack.Running, stack.Total)
	case compose.StateRunning:
		return fmt.Sprintf("Running (%d/%d)", stack.Running, stack.Total)
	case compose.StatePartial:
		return fmt.Sprintf("Partial (%d/%d)", stack.Running, stack.Total)
	case compose.StateStopped:
		return fmt.Sprintf("Stopped (0/%d)", stack.Total)
	default:
		return "Not deployed"
	}
}

func groupSizes(stacks []compose.Stack) map[string]int {
	m := make(map[string]int)
	for _, s := range stacks {
		m[s.Group]++
	}
	return m
}

// ---- row model ----

// listRow est une ligne affichable de la liste : en-tête de groupe (repliable,
// navigable), séparateur (sauté par le curseur) ou stack.
type listRow struct {
	header   bool
	sep      bool
	group    string
	size     int
	folded   bool
	gc       groupCounts
	stack    compose.Stack
	indented bool
}

type groupCounts struct{ run, part, unhealthy, stop, off int }

func groupCountsOf(stacks []compose.Stack) map[string]groupCounts {
	m := make(map[string]groupCounts)
	for _, s := range stacks {
		gc := m[s.Group]
		switch s.State() {
		case compose.StateUnhealthy:
			gc.unhealthy++
		case compose.StateRunning:
			gc.run++
		case compose.StatePartial:
			gc.part++
		case compose.StateStopped:
			gc.stop++
		default:
			gc.off++
		}
		m[s.Group] = gc
	}
	return m
}

// rowsCacheBox mémoïse les dérivés O(n stacks) consultés plusieurs fois par
// frappe ou par frame : lignes affichables (listRows), liste filtrée
// (filteredStacks) et compteurs d'états (stackCounts). Pointeur partagé entre
// les copies du Model ; invalidé via invalidateRows() quand stacks, filtre ou
// repli changent (stackCounts ne dépend que des stacks, l'invalider plus
// souvent est sans effet).
type rowsCacheBox struct {
	rows   []listRow
	rowsOK bool

	filtered   []compose.Stack
	filteredOK bool

	counts   StackCounts
	countsOK bool
}

func (m Model) invalidateRows() {
	m.rowsCache.rowsOK = false
	m.rowsCache.filteredOK = false
	m.rowsCache.countsOK = false
}

// listRows renvoie les lignes affichables, depuis le cache si valide.
func (m Model) listRows() []listRow {
	if m.rowsCache.rowsOK {
		return m.rowsCache.rows
	}
	rows := m.buildListRows()
	m.rowsCache.rows = rows
	m.rowsCache.rowsOK = true
	return rows
}

// buildListRows construit les lignes : en-tête pour chaque groupe multi-stacks
// (avec compteurs par état), séparateur avant les groupes entièrement
// inactifs, puis les stacks. Les membres d'un groupe replié sont masqués ;
// un filtre actif ignore le repli pour que les résultats restent visibles.
func (m Model) buildListRows() []listRow {
	stacks := m.filteredStacks()
	filtering := m.filter.Value() != ""

	// Find where fully-inactive groups begin: first stack of the first all-inactive group
	// that comes after the last group containing at least one active member.
	separatorBefore := -1
	lastActiveGroup := ""
	for _, s := range stacks {
		if s.State() != compose.StateUnknown {
			lastActiveGroup = s.Group
		}
	}
	if lastActiveGroup != "" {
		pastLastActive := false
		for i, s := range stacks {
			if !pastLastActive {
				if s.Group == lastActiveGroup && (i == len(stacks)-1 || stacks[i+1].Group != lastActiveGroup) {
					pastLastActive = true
				}
			} else if separatorBefore < 0 {
				separatorBefore = i
			}
		}
	}

	sizes := groupSizes(stacks)
	counts := groupCountsOf(stacks)

	var rows []listRow
	prevGroup := ""
	inMulti := false
	for i, s := range stacks {
		if i == separatorBefore {
			rows = append(rows, listRow{sep: true})
			prevGroup = ""
			inMulti = false
		}
		if s.Group != prevGroup {
			if sizes[s.Group] > 1 {
				rows = append(rows, listRow{
					header: true, group: s.Group, size: sizes[s.Group],
					folded: !filtering && m.foldedGroups[s.Group],
					gc:     counts[s.Group],
				})
				inMulti = true
			} else {
				inMulti = false
			}
			prevGroup = s.Group
		}
		if inMulti && !filtering && m.foldedGroups[s.Group] {
			continue
		}
		rows = append(rows, listRow{stack: s, indented: inMulti})
	}
	return rows
}

// cursorRow renvoie la ligne sous le curseur, clampé si la liste a rétréci.
func (m Model) cursorRow() (listRow, bool) {
	rows := m.listRows()
	if len(rows) == 0 {
		return listRow{}, false
	}
	c := m.cursor
	if c < 0 {
		c = 0
	}
	if c >= len(rows) {
		c = len(rows) - 1
	}
	return rows[c], true
}

// cursorStack renvoie la stack sous le curseur (rien si le curseur est sur un
// en-tête de groupe).
func (m Model) cursorStack() (compose.Stack, bool) {
	row, ok := m.cursorRow()
	if !ok || row.header || row.sep {
		return compose.Stack{}, false
	}
	return row.stack, true
}

// cursorGroup renvoie le groupe repliable sous le curseur : l'en-tête lui-même
// ou n'importe lequel de ses membres.
func (m Model) cursorGroup() (string, bool) {
	row, ok := m.cursorRow()
	if !ok || row.sep {
		return "", false
	}
	if row.header {
		return row.group, true
	}
	if row.indented {
		return row.stack.Group, true
	}
	return "", false
}

func (m Model) groupStacks(group string) []compose.Stack {
	var out []compose.Stack
	for _, s := range m.filteredStacks() {
		if s.Group == group {
			out = append(out, s)
		}
	}
	return out
}

func (m Model) headerRowIndex(group string) int {
	for i, r := range m.listRows() {
		if r.header && r.group == group {
			return i
		}
	}
	return 0
}

// moveCursor déplace le curseur de delta lignes en sautant les séparateurs.
func (m *Model) moveCursor(delta int) {
	rows := m.listRows()
	if len(rows) == 0 {
		m.cursor = 0
		return
	}
	c := m.cursor
	if c < 0 {
		c = 0
	}
	if c >= len(rows) {
		c = len(rows) - 1
	}
	for {
		c += delta
		if c < 0 || c >= len(rows) {
			return
		}
		if !rows[c].sep {
			m.cursor = c
			return
		}
	}
}

// visibleStacks renvoie les stacks effectivement affichées (repli appliqué).
func (m Model) visibleStacks() []compose.Stack {
	var out []compose.Stack
	for _, r := range m.listRows() {
		if !r.header && !r.sep {
			out = append(out, r.stack)
		}
	}
	return out
}

// ---- rendering ----

func renderStackList(m Model, height int) string {
	width := m.width
	filterBar := m.filterBar()
	if filterBar != "" {
		height--
	}

	stacks := m.filteredStacks()
	if len(stacks) == 0 {
		msg := styleYellow.Render("  Aucune stack trouvée.")
		if m.cfg.StackDir != "" {
			msg += "\n" + styleDim.Render(fmt.Sprintf("  Répertoire : %s", m.cfg.StackDir))
			msg += "\n\n" + styleDim.Render("  Appuyer sur ") + styleBold.Render("o") + styleDim.Render(" pour changer de répertoire")
			msg += "\n" + styleDim.Render("  Appuyer sur ") + styleBold.Render("R") + styleDim.Render(" pour rafraîchir")
		}
		if filterBar != "" {
			return filterBar + "\n" + msg
		}
		return msg
	}

	rows := m.listRows()
	cursor := m.cursor
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	// Window: keep only the visible rows, centered on the cursor. The two
	// edge lines show how much is hidden so long lists stay navigable.
	start, end := 0, len(rows)
	moreAbove, moreBelow := 0, 0
	if height > 0 && len(rows) > height {
		inner := height - 2
		if inner < 1 {
			inner = 1
		}
		start = cursor - inner/2
		if start < 0 {
			start = 0
		}
		if start+inner > len(rows) {
			start = len(rows) - inner
		}
		end = start + inner
		moreAbove = start
		moreBelow = len(rows) - end
	}

	// Render pass: style only the windowed rows (at ~600 stacks, styling the
	// whole list on every frame would be wasted work).
	lines := make([]string, 0, end-start+2)
	if moreAbove > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("  ··· %d ligne(s) au-dessus", moreAbove)))
	}
	for i := start; i < end; i++ {
		row := rows[i]
		switch {
		case row.sep:
			lines = append(lines, styleDim.Render("  "+strings.Repeat("─", max(width-6, 1))))
		case row.header:
			lines = append(lines, renderGroupHeader(row, i == cursor, width))
		default:
			lines = append(lines, renderStackLine(row.stack, i == cursor, m.selected[row.stack.Name], width, row.indented))
		}
	}
	if moreBelow > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("  ··· %d ligne(s) en-dessous", moreBelow)))
	}

	body := strings.Join(lines, "\n")
	if filterBar != "" {
		return filterBar + "\n" + body
	}
	return body
}

// renderGroupHeader affiche un en-tête de groupe : flèche de repli, nom,
// nombre de stacks et une pastille « état global » à la couleur du pire état
// du dossier (▼ 📁 media (4) ●).
func renderGroupHeader(row listRow, isCursor bool, width int) string {
	arrow := "▼"
	if row.folded {
		arrow = "▶"
	}

	var dotGlyph string
	var dotStyle lipgloss.Style
	switch {
	case row.gc.unhealthy > 0:
		dotGlyph, dotStyle = "●", styleOrange
	case row.gc.stop > 0:
		dotGlyph, dotStyle = "●", styleRed
	case row.gc.part > 0:
		dotGlyph, dotStyle = "●", styleYellow
	case row.gc.run > 0:
		dotGlyph, dotStyle = "●", styleGreen
	default:
		dotGlyph, dotStyle = "○", styleDim // rien de déployé
	}

	text := fmt.Sprintf("%s 📁 %s (%d) ", arrow, row.group, row.size)
	if isCursor {
		// Texte brut dans un unique style reverse : pas de séquence imbriquée,
		// pas d'artefact (cf. renderStackLine).
		line := text + dotGlyph
		pad := width - lipgloss.Width(line)
		if pad < 0 {
			pad = 0
		}
		return styleSelected.Render(line + strings.Repeat(" ", pad))
	}
	return styleGroup.Render(text) + dotStyle.Render(dotGlyph)
}

// renderStackLine styles a single stack row: marker, state dot, name and a
// right-aligned count, with full-width highlight on the cursor line.
func renderStackLine(s compose.Stack, isCursor, isSelected bool, width int, indented bool) string {
	// Display name: show full relative path so immich/immich stays distinct from immich
	displayName := s.Name

	dot, dotStyle := stateStyle(s.State())

	// Compteur en texte brut ; il n'est stylé que hors ligne curseur.
	var countText string
	switch s.State() {
	case compose.StateUnhealthy, compose.StateRunning, compose.StatePartial:
		countText = fmt.Sprintf("%d/%d", s.Running, s.Total)
	case compose.StateStopped:
		countText = fmt.Sprintf("0/%d", s.Total)
	}

	indent := " "
	if indented {
		indent = "   "
	}

	if isCursor {
		// Ligne curseur : flèche cyan HORS de la zone inversée, et contenu en
		// TEXTE BRUT dans un unique style reverse — toute séquence de couleur
		// imbriquée dans le reverse laisse des artefacts (resets au milieu).
		// L'état reste lisible via le glyphe ●/○ et le compteur.
		prefix := indent + styleCursorMark.Render("▶") + " "
		body := dot + " " + displayName
		prefixW := lipgloss.Width(prefix)
		bodyW := lipgloss.Width(body)

		var rest string
		if countText != "" {
			pad := width - prefixW - bodyW - len(countText) - 2
			if pad < 1 {
				pad = 1
			}
			rest = body + strings.Repeat(" ", pad) + countText + "  "
		} else {
			pad := width - prefixW - bodyW
			if pad < 0 {
				pad = 0
			}
			rest = body + strings.Repeat(" ", pad)
		}
		return prefix + styleSelected.Render(rest)
	}

	// Name style + count badge based on state
	var nameStyled, count string
	switch s.State() {
	case compose.StateUnhealthy:
		nameStyled = styleNameUnhealthy.Render(displayName)
		count = styleCountUnhealthy.Render(countText)
	case compose.StateRunning:
		nameStyled = styleNameRunning.Render(displayName)
		count = styleCountRunning.Render(countText)
	case compose.StatePartial:
		nameStyled = styleNamePartial.Render(displayName)
		count = styleCountPartial.Render(countText)
	case compose.StateStopped:
		nameStyled = styleNameStopped.Render(displayName)
		count = styleCountStopped.Render(countText)
	default:
		nameStyled = styleNameInactive.Render(displayName)
		count = ""
	}

	marker := " "
	if isSelected {
		marker = styleYellow.Render("┃")
	}

	// Build line with right-aligned count
	left := fmt.Sprintf("%s%s %s %s", indent, marker, dotStyle.Render(dot), nameStyled)
	leftW := lipgloss.Width(left)

	if count != "" {
		countW := lipgloss.Width(count)
		pad := width - leftW - countW - 2
		if pad < 1 {
			pad = 1
		}
		return left + strings.Repeat(" ", pad) + count
	}
	return left
}
