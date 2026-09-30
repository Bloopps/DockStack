package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
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
	note     string // texte d'une ligne séparatrice informative (non navigable)
	more     bool   // ligne « N … masquées » : informative, sautée par le curseur (a affiche tout)
	group    string
	size     int
	folded   bool
	gc       groupCounts
	stack    compose.Stack
	indented bool
}

// isStack : la ligne représente une stack (ni en-tête, ni séparateur, ni
// ligne « … masquées »).
func (r listRow) isStack() bool { return !r.header && !r.sep && !r.more }

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

	// Stacks affichées (celles qui ne tournent pas sont masquées) et nombre
	// de masquées, dont arrêtées (le reste n'a jamais été déployé).
	shown         []compose.Stack
	hidden        int
	hiddenStopped int
	shownOK       bool

	nameW int // largeur de la colonne des noms (calculée avec rows)
}

func (m Model) invalidateRows() {
	m.rowsCache.rowsOK = false
	m.rowsCache.filteredOK = false
	m.rowsCache.countsOK = false
	m.rowsCache.shownOK = false
}

// dormant : la stack ne tourne pas du tout (arrêtée ou jamais déployée). Une
// stack partielle ou unhealthy tourne encore : elle reste toujours affichée.
func dormant(s compose.Stack) bool {
	st := s.State()
	return st == compose.StateStopped || st == compose.StateUnknown
}

// hidingDormant : par défaut la liste ne montre que les stacks qui tournent,
// sauf si l'utilisateur affiche tout (a) ou filtre (on doit pouvoir tout
// retrouver).
func (m Model) hidingDormant() bool {
	return !m.cfg.ShowAll && m.filter.Value() == ""
}

// shownStacks : stacks filtrées, moins celles qui ne tournent pas si elles
// sont masquées.
// C'est la liste que voit l'utilisateur : lignes, groupes, position i/N.
func (m Model) shownStacks() []compose.Stack {
	if m.rowsCache.shownOK {
		return m.rowsCache.shown
	}
	all := m.filteredStacks()
	shown, hidden, stopped := all, 0, 0
	if m.hidingDormant() {
		shown = make([]compose.Stack, 0, len(all))
		for _, s := range all {
			if dormant(s) {
				hidden++
				if s.State() == compose.StateStopped {
					stopped++
				}
				continue
			}
			shown = append(shown, s)
		}
	}
	m.rowsCache.shown, m.rowsCache.hidden, m.rowsCache.hiddenStopped, m.rowsCache.shownOK = shown, hidden, stopped, true
	return shown
}

// listRows renvoie les lignes affichables, depuis le cache si valide.
func (m Model) listRows() []listRow {
	if m.rowsCache.rowsOK {
		return m.rowsCache.rows
	}
	rows := m.buildListRows()
	// Colonne des noms commune à toutes les lignes (pas seulement aux lignes
	// visibles) : sinon elle bougerait en faisant défiler.
	nameW := 0
	for _, r := range rows {
		if r.isStack() {
			w := lipgloss.Width(r.stack.Name)
			if r.stack.Orphaned {
				w += 2
			}
			if r.indented {
				w += 2
			}
			nameW = max(nameW, w)
		}
	}
	m.rowsCache.rows = rows
	m.rowsCache.nameW = nameW
	m.rowsCache.rowsOK = true
	return rows
}

// buildListRows construit les lignes : en-tête pour chaque groupe multi-stacks
// (avec compteurs par état), séparateur avant les groupes entièrement
// inactifs, puis les stacks. Les membres d'un groupe replié sont masqués ;
// un filtre actif ignore le repli pour que les résultats restent visibles.
func (m Model) buildListRows() []listRow {
	stacks := m.shownStacks()
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
	if n := m.rowsCache.hidden; n > 0 {
		rows = append(rows, listRow{more: true, note: hiddenNote(m.rowsCache.hiddenStopped, n-m.rowsCache.hiddenStopped)})
	}
	return rows
}

// hiddenHint : fin de la ligne « masquées ». La touche (première lettre) est
// rendue comme celles du pied de page pour qu'on la repère.
const hiddenHint = "a pour tout afficher"

// hiddenNote : texte de la ligne qui résume les stacks masquées.
func hiddenNote(stopped, undeployed int) string {
	var parts []string
	if stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d arrêtée%s", stopped, plural(stopped)))
	}
	if undeployed > 0 {
		parts = append(parts, fmt.Sprintf("%d non déployée%s", undeployed, plural(undeployed)))
	}
	n := stopped + undeployed
	return fmt.Sprintf("○ %s masquée%s — %s", strings.Join(parts, " et "), plural(n), hiddenHint)
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
	if !ok || !row.isStack() {
		return compose.Stack{}, false
	}
	return row.stack, true
}

// cursorGroup renvoie le groupe repliable sous le curseur : l'en-tête lui-même
// ou n'importe lequel de ses membres.
func (m Model) cursorGroup() (string, bool) {
	row, ok := m.cursorRow()
	if !ok || row.sep || row.more {
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
	for _, s := range m.shownStacks() {
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
		// La ligne « masquées » est informative : le curseur la saute, sinon
		// ↩ dessus semblait être le moyen d'afficher (seul a l'est).
		if !rows[c].sep && !rows[c].more {
			m.cursor = c
			return
		}
	}
}

// cursorToEdge place le curseur sur la première (ou dernière) ligne
// navigable (g / G).
func (m *Model) cursorToEdge(last bool) {
	rows := m.listRows()
	for k := range rows {
		i := k
		if last {
			i = len(rows) - 1 - k
		}
		if !rows[i].sep && !rows[i].more {
			m.cursor = i
			return
		}
	}
}

// clampCursor ramène le curseur dans la liste (qui a pu rétrécir) et hors
// d'un séparateur.
func (m *Model) clampCursor() {
	rows := m.listRows()
	m.cursor = max(0, min(m.cursor, len(rows)-1))
	if m.cursor < len(rows) && (rows[m.cursor].sep || rows[m.cursor].more) {
		m.moveCursor(-1)
	}
}

// visibleStacks renvoie les stacks effectivement affichées (repli appliqué).
func (m Model) visibleStacks() []compose.Stack {
	var out []compose.Stack
	for _, r := range m.listRows() {
		if r.isStack() {
			out = append(out, r.stack)
		}
	}
	return out
}

// ---- rendering ----

func renderStackList(m Model, height, width int) string {
	filterBar := m.filterBar()
	if filterBar != "" {
		height--
	}
	colHeader := m.listColumnHeader(width)
	if colHeader != "" {
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
		case row.more:
			text := truncate("  "+row.note, max(width, 1))
			if i == cursor {
				lines = append(lines, styleSelected.Render(text+strings.Repeat(" ", max(width-lipgloss.Width(text), 0))))
			} else {
				lines = append(lines, renderHiddenNote(text))
			}
		case row.sep:
			lines = append(lines, styleDim.Render("  "+strings.Repeat("─", max(width-6, 1))))
		case row.header:
			lines = append(lines, renderGroupHeader(row, i == cursor, width))
		default:
			lines = append(lines, renderStackLine(row.stack, i == cursor, m.selected[row.stack.Name], width, row.indented, m.rowsCache.nameW, m.rowUsageFor(row.stack)))
		}
	}
	if moreBelow > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("  ··· %d ligne(s) en-dessous", moreBelow)))
	}

	body := strings.Join(lines, "\n")
	if colHeader != "" {
		body = colHeader + "\n" + body
	}
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

// stateWord : libellé d'état court affiché dans la colonne de droite.
func stateWord(st compose.State) string {
	switch st {
	case compose.StateUnhealthy:
		return "unhealthy"
	case compose.StateRunning:
		return "en marche"
	case compose.StatePartial:
		return "partielle"
	case compose.StateStopped:
		return "arrêtée"
	default:
		return "non déployée"
	}
}

// renderStackLine rend une ligne de stack : marqueur, pastille, nom, puis le
// compteur de conteneurs et l'état alignés en colonne juste après le nom le
// plus long (nameW) — et non collés au bord droit, loin des noms sur un écran
// large. La ligne sous le curseur est surlignée sur toute la largeur.
func renderStackLine(s compose.Stack, isCursor, isSelected bool, width int, indented bool, nameW int, use rowUsage) string {
	dot, dotStyle := stateStyle(s.State())

	var countText string
	if s.Total > 0 {
		countText = fmt.Sprintf("%d/%d", s.Running, s.Total)
	}
	word := stateWord(s.State())

	indent := " "
	if indented {
		indent = "   "
	}
	// Colonnes : [indent][marque][ ][●][ ][nom…][  ][compteur 5][  ][état 9][  ][CPU 6][ ][RAM 7][ ][disque 7][ ][lect 7][ ][écr 7]
	// L'état est calé sur 9 colonnes (« en marche », « partielle », « unhealthy »)
	// pour aligner les colonnes suivantes. CPU/RAM : stacks qui tournent ;
	// disque : toute stack qui a des conteneurs ; lecture/écriture disque : stacks
	// qui tournent. Largeurs partagées avec listColumnHeader (stats.go).
	const countW, wordW, cpuW, memW, diskW, ioW = colCount, colWord, colCPU, colMem, colDisk, colIO
	lead := len(indent) + 4 // indent + marque + espace + pastille + espace
	cols := use.show || use.disk != ""
	wordPad := 0
	if cols {
		wordPad = max(wordW-lipgloss.Width(word), 0)
	}
	// Les colonnes IO (lecture, écriture) ne se montrent que derrière le disque :
	// elles partent en premier quand la place manque.
	showWord, showCols, showDisk, showIO := true, cols, use.disk != "", use.show && use.disk != ""
	tailFor := func() int {
		w := 2 + countW
		if showWord {
			w += 2 + lipgloss.Width(word)
			if showCols {
				w += wordPad + 2 + cpuW + 1 + memW
				if showDisk {
					w += 1 + diskW
					if showIO {
						w += 2 * (1 + ioW)
					}
				}
			}
		}
		return w
	}
	tailW := tailFor()

	// Largeur réservée au nom sur cette ligne : colonne commune, réduite si
	// la ligne ne tient pas (volet d'aperçu, terminal étroit). Trop étroit :
	// le disque part d'abord, puis CPU/RAM, puis l'état, puis le nom est tronqué.
	colW := nameW - (len(indent) - 1)
	if colW < 1 {
		colW = 1
	}
	if lead+colW+tailW > width && showIO {
		showIO = false
		tailW = tailFor()
	}
	if lead+colW+tailW > width && showDisk {
		showDisk = false
		tailW = tailFor()
	}
	if lead+colW+tailW > width && showCols {
		showCols, showDisk, showIO = false, false, false
		tailW = tailFor()
	}
	if lead+colW+tailW > width {
		showWord = false
		tailW = tailFor()
	}
	if lead+colW+tailW > width {
		colW = max(width-lead-tailW, 1)
	}
	orphan := ""
	if s.Orphaned {
		orphan = " ⟳"
	}
	name := truncate(s.Name, max(colW-lipgloss.Width(orphan), 1))
	namePad := max(colW-lipgloss.Width(name)-lipgloss.Width(orphan), 0)

	tail := "  " + fmt.Sprintf("%*s", countW, countText)
	if showWord {
		tail += "  " + word
		if showCols {
			tail += strings.Repeat(" ", wordPad) + "  "
			if use.show {
				tail += fmt.Sprintf("%*s %*s", cpuW, use.cpu, memW, use.mem)
			} else {
				tail += strings.Repeat(" ", cpuW+1+memW)
			}
			if showDisk {
				tail += fmt.Sprintf(" %*s", diskW, use.disk)
				if showIO {
					tail += fmt.Sprintf(" %*s %*s", ioW, use.rd, ioW, use.wr)
				}
			}
		}
	}

	if isCursor {
		// Ligne curseur : flèche cyan HORS de la zone surlignée, et contenu en
		// TEXTE BRUT dans un unique style — toute séquence de couleur imbriquée
		// laisse des artefacts (resets au milieu).
		mark := " "
		if isSelected {
			mark = styleYellow.Render("┃") // sinon la sélection est invisible sous le curseur
		}
		prefix := indent + styleCursorMark.Render("▶") + mark
		body := dot + " " + name + orphan + strings.Repeat(" ", namePad) + tail
		pad := max(width-lipgloss.Width(prefix)-lipgloss.Width(body), 0)
		return prefix + styleSelected.Render(body+strings.Repeat(" ", pad))
	}

	var nameStyle, countStyle lipgloss.Style
	switch s.State() {
	case compose.StateUnhealthy:
		nameStyle, countStyle = styleNameUnhealthy, styleCountUnhealthy
	case compose.StateRunning:
		nameStyle, countStyle = styleNameRunning, styleCountRunning
	case compose.StatePartial:
		nameStyle, countStyle = styleNamePartial, styleCountPartial
	case compose.StateStopped:
		nameStyle, countStyle = styleNameStopped, styleCountStopped
	default:
		nameStyle, countStyle = styleNameInactive, styleDim
	}

	marker := " "
	if isSelected {
		marker = styleYellow.Render("┃")
	}
	line := indent + marker + " " + dotStyle.Render(dot) + " " + nameStyle.Render(name)
	if orphan != "" {
		// Conteneur(s) rattaché(s) par nom faute de label projet (orphelin
		// watchtower) : sera ré-adopté au prochain up/recreate.
		line += styleDim.Render(orphan)
	}
	line += strings.Repeat(" ", namePad) + "  " + countStyle.Render(fmt.Sprintf("%*s", countW, countText))
	if showWord {
		line += "  " + styleDim.Render(word)
		if showCols {
			line += strings.Repeat(" ", wordPad) + "  "
			if use.show {
				line += usageCell(use.cpu, cpuW) + " " + usageCell(use.mem, memW)
			} else {
				line += strings.Repeat(" ", cpuW+1+memW)
			}
			if showDisk {
				line += " " + usageCell(use.disk, diskW)
				if showIO {
					line += " " + usageCell(use.rd, ioW) + " " + usageCell(use.wr, ioW)
				}
			}
		}
	}
	return line
}

// usageCell : valeur de CPU ou de RAM alignée à droite ; « … » (pas encore
// mesuré) en grisé pour ne pas se lire comme une valeur.
func usageCell(v string, w int) string {
	pad := strings.Repeat(" ", max(w-lipgloss.Width(v), 0))
	if v == "…" || v == "—" || v == "0" || v == "<1 Ko" {
		return pad + styleDim.Render(v)
	}
	return pad + v
}

func plural(n int) string {
	if n > 1 {
		return "s"
	}
	return ""
}

// renderHiddenNote rend la ligne « masquées » en grisé, avec la touche « a » en
// accent. Si la ligne a été tronquée (terminal étroit), retombe sur le grisé seul.
func renderHiddenNote(text string) string {
	prefix, ok := strings.CutSuffix(text, hiddenHint)
	if !ok {
		return styleDim.Render(text)
	}
	return styleDim.Render(prefix) +
		styleCursorMark.Render("a") +
		styleDim.Render(strings.TrimPrefix(hiddenHint, "a"))
}
