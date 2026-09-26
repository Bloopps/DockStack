package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/monitor"
)

var (
	styleGreen   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleYellow  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleRed     = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleCyan    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleMagenta = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleOrange  = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))
	styleBold    = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Faint(true)
	// Sillon des barres (partie non remplie) : un gris discret peint en fond
	// rend une gouttière nette et pleine (pas d'interstices entre glyphes).
	trackColor = lipgloss.Color("236")
	styleTrack = lipgloss.NewStyle().Background(trackColor)
)

var styleSep = styleMagenta.Render(" │ ")

const (
	metricsHistoryLen = 60 // samples kept (≈2 min at the 2s tick)
	avgSparkWidth     = 12 // trend width for CPU avg / RAM
	// Largeur des jauges principales (CPU avg, RAM, Disk). Plus large = plus de
	// cases pleines = plus de finesse sans recourir à un glyphe partiel (qui
	// casserait la netteté). 20 cases ⇒ 5 % par case.
	mainBarW = 20
	// En dessous de wideHeaderMinWidth colonnes, les jauges repassent à
	// narrowBarW cases pour que les lignes RAM/Disk tiennent.
	wideHeaderMinWidth = 110
	narrowBarW         = 8
	// À partir de compactHeaderMinWidth colonnes, l'en-tête passe en deux
	// colonnes (ressources | cœurs), plus lisible et plus court.
	compactHeaderMinWidth = 120
	compactBarW           = 12
	coreBarW              = 6
	resourceRows          = 4 // CPU, RAM, Swap, Disque
)

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// metricsHistory keeps a rolling window of recent samples so the header can draw
// a trend sparkline on the aggregate CPU line. RAM/Disk vary too slowly for a
// sparkline to be useful (it just renders as a flat solid bar), so only CPU is
// tracked.
type metricsHistory struct {
	cpu []int
}

func (h *metricsHistory) push(m monitor.Metrics) {
	h.cpu = pushSample(h.cpu, m.CPUPercent)
}

func pushSample(s []int, v int) []int {
	if len(s) < metricsHistoryLen {
		return append(s, v)
	}
	copy(s, s[1:])
	s[len(s)-1] = v
	return s
}

// sparkline renders the last w samples as block characters, right-aligned and
// left-padded to exactly w runes so the layout never jitters as history fills.
// While history is still filling, the left padding repeats the oldest known
// sample (a flat baseline at the real level) rather than the lowest block, so the
// metric doesn't look like it ramped up from zero on startup.
func sparkline(vals []int, w int) string {
	if w <= 0 {
		return ""
	}
	if len(vals) == 0 {
		return strings.Repeat(string(sparkRunes[0]), w)
	}
	if len(vals) > w {
		vals = vals[len(vals)-w:]
	}
	var sb strings.Builder
	for i := len(vals); i < w; i++ {
		sb.WriteRune(sparkRune(vals[0]))
	}
	for _, v := range vals {
		sb.WriteRune(sparkRune(v))
	}
	return sb.String()
}

func sparkRune(v int) rune {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return sparkRunes[v*(len(sparkRunes)-1)/100]
}

// StackCounts holds the aggregated stack states for the header.
type StackCounts struct {
	Dir       string
	Total     int
	Running   int
	Partial   int
	Unhealthy int
	Stopped   int
	NotDep    int
}

func colorForPct(pct int) lipgloss.Style {
	if pct >= 85 {
		return styleRed
	}
	if pct >= 60 {
		return styleYellow
	}
	return styleGreen
}

func bar(pct, width int) string {
	// Cases pleines uniquement, arrondi au plus proche. Peindre une fraction de
	// case imposerait un glyphe partiel (▏▎▍…) dont la teinte et la forme
	// diffèrent du fond plein voisin et cassent la netteté ; la valeur exacte
	// étant affichée en chiffres à côté, la barre n'a qu'un rôle de coup d'œil.
	// Remplissage peint en fond (espaces colorés) plutôt qu'avec le glyphe « █ »
	// qui, selon la police, laisse de fins liserés noirs entre les cases.
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := (pct*width + 50) / 100
	if pct > 0 && filled == 0 {
		filled = 1 // garde une trace visible pour les valeurs très faibles
	}
	c := colorForPct(pct).GetForeground()
	return lipgloss.NewStyle().Background(c).Render(strings.Repeat(" ", filled)) +
		styleTrack.Render(strings.Repeat(" ", width-filled))
}

func fmtRate(kbs int) string {
	switch {
	case kbs >= 10240:
		return fmt.Sprintf("%dM", kbs/1024)
	case kbs >= 1024:
		return fmt.Sprintf("%d.%dM", kbs/1024, kbs*10/1024%10)
	default:
		return fmt.Sprintf("%dK", kbs)
	}
}

func renderHeader(m monitor.Metrics, hist metricsHistory, sc StackCounts, du *compose.DockerUsage, width int) string {
	L := styleCyan
	sep := styleSep

	// ── Ligne 1 : titre │ chemin │ stacks │ heure ──────────────────────────
	title := styleBold.Foreground(lipgloss.Color("2")).Render("🐳 Docker Stack Manager")
	path := L.Render("📂") + " " + sc.Dir
	stacksInfo := L.Render("📦") + " " + stackLegend(sc)
	now := styleCyan.Render("🕐") + " " + time.Now().Format("15:04:05")
	line1 := title + sep + path + sep + stacksInfo + sep + now

	if width >= compactHeaderMinWidth {
		return renderCompactHeader(m, du, line1, width)
	}

	// Jauges larges seulement si la place le permet : à 20 cases, les lignes
	// RAM/Disk dépassent ~95 colonnes et leur fin (Uptime, Load) serait coupée.
	barW := mainBarW
	if width < wideHeaderMinWidth {
		barW = narrowBarW
	}

	// ── Ligne 2 : CPU avg + grille par cœur ───────────────────────────────
	cpuBar := bar(m.CPUPercent, barW)
	cpuPct := colorForPct(m.CPUPercent).Render(fmt.Sprintf("%3d%%", m.CPUPercent))
	cpuLabel := L.Render("CPU")
	cpuSpark := colorForPct(m.CPUPercent).Render(sparkline(hist.cpu, avgSparkWidth))
	// Label first so the aggregate line reads as a heading and isn't mistaken for a
	// 9th core: the per-core grid cells below start with a bare bar, this one starts
	// with text.
	line2 := fmt.Sprintf("  %s avg %s %s  %s (%d cores)", cpuLabel, cpuBar, cpuPct, cpuSpark, m.NCores)

	coreGrid := renderCoreGrid(m.CPUCores, width)

	// ── Ligne 3 : RAM │ Swap │ Uptime ─────────────────────────────────────
	memBar := bar(m.MemPercent, barW)
	memPct := colorForPct(m.MemPercent).Render(fmt.Sprintf("%3d%%", m.MemPercent))
	swapCol := swapStyle(m)
	line3 := fmt.Sprintf("  %s %s  %s %.1fG/%.1fG%s%s %s %.1fG/%.1fG%s%s %s",
		memBar, memPct,
		L.Render("RAM"), m.MemUsedGB, m.MemTotalGB,
		sep,
		L.Render("Swap"), swapCol.Render(fmt.Sprintf("%3d%%", m.SwapPercent)),
		m.SwapUsedGB, m.SwapTotalGB,
		sep,
		L.Render("Up"), m.Uptime,
	)

	// ── Ligne 4 : Disk │ IO │ Load ────────────────────────────────────────
	diskBar := bar(m.DiskPercent, barW)
	diskPct := colorForPct(m.DiskPercent).Render(fmt.Sprintf("%3d%%", m.DiskPercent))

	ioRCol := ioColor(m.DiskReadKBs)
	ioWCol := ioColor(m.DiskWriteKBs)
	ioStr := fmt.Sprintf("%s %s↓%s %s/s %s↑%s %s/s",
		L.Render("IO"),
		styleCyan.Render(""), ioRCol.Render(fmtRate(m.DiskReadKBs)),
		styleCyan.Render(""),
		styleMagenta.Render(""), ioWCol.Render(fmtRate(m.DiskWriteKBs)),
		styleMagenta.Render(""),
	)

	l1c, l5c, l15c := loadColor(m.Load1, m.NCores), loadColor(m.Load5, m.NCores), loadColor(m.Load15, m.NCores)
	loadStr := fmt.Sprintf("%s 1m:%s 5m:%s 15m:%s",
		L.Render("Load"),
		l1c.Render(m.Load1), l5c.Render(m.Load5), l15c.Render(m.Load15),
	)

	line4 := fmt.Sprintf("  %s %s  %s %dG/%dG%s%s%s%s",
		diskBar, diskPct,
		L.Render("Disk"), m.DiskUsedGB, m.DiskTotalGB,
		sep, ioStr, sep, loadStr,
	)

	// Inner separator (dashed) delimits the CPU block from the system-resource
	// block; the outer divider (solid) separates the whole header from the list.
	innerSep := styleDim.Render(strings.Repeat("┈", width))
	divider := styleDim.Render(strings.Repeat("─", width))

	parts := []string{line1, "", line2}
	if coreGrid != "" {
		parts = append(parts, coreGrid)
	}
	parts = append(parts, innerSep, line3, line4, dockerLine(du), divider)
	// Filet de sécurité : le moteur de rendu coupe au bord de l'écran, autant
	// couper nous-mêmes proprement (chemin long, terminal très étroit).
	// Ligne par ligne : la grille des cœurs est un bloc multi-lignes.
	lines := strings.Split(strings.Join(parts, "\n"), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

// coreGridCols renvoie le nombre de colonnes de la grille par cœur pour n
// cœurs et une largeur de terminal donnés. Partagé entre renderCoreGrid et
// headerHeight pour que la hauteur calculée reste exacte.
func coreGridCols(n, termW int) int {
	const barW = 8
	const indent = 2
	labelW := len(fmt.Sprintf("%d", n-1))
	cellW := labelW + 1 + barW + 1 + 4 + 3 // "NN ████████ 100%" + " │ "

	cols := (termW - indent) / cellW
	if cols > 8 {
		cols = 8
	}
	if colsTarget := (n + 1) / 2; cols > colsTarget {
		cols = colsTarget
	}
	if cols < 1 {
		cols = 1
	}
	if cols > n {
		cols = n
	}
	return cols
}

// headerHeight renvoie la hauteur (en lignes) de renderHeader sans le
// construire : mesurer le header en le rendant coûte un rendu stylé complet,
// que progressBodyHeight déclencherait à chaque frappe/molette. Doit rester
// aligné sur la structure assemblée à la fin de renderHeader.
func headerHeight(nCores, width int) int {
	if width >= compactHeaderMinWidth {
		rows, _ := compactCoreLayout(nCores, width)
		return 2 + rows + 3 // titre + titres de colonnes, ressources/cœurs, Docker + Système + séparateur
	}
	h := 8 // ligne1, vide, CPU avg, séparateur interne, RAM, Disk, Docker, divider
	if nCores > 0 {
		cols := coreGridCols(nCores, width)
		h += (nCores + cols - 1) / cols
	}
	return h
}

// renderCoreGrid renders per-core CPU bars in a multi-column grid. Per-core uses
// static bars (not sparklines) so the grid reads cleanly at a glance; trend
// sparklines are reserved for the aggregate CPU/RAM lines.
func renderCoreGrid(cores []int, termW int) string {
	n := len(cores)
	if n == 0 {
		return ""
	}

	const barW = 8
	// Each core is numbered (faint) so it can't be mistaken for the aggregate
	// "CPU avg" line above. labelW keeps the numbers right-aligned for 1/2-digit
	// core counts.
	labelW := len(fmt.Sprintf("%d", n-1))
	cols := coreGridCols(n, termW)

	sep := styleMagenta.Render(" │")

	var sb strings.Builder
	for i, pct := range cores {
		col := i % cols
		if col == 0 {
			sb.WriteString("  ")
		} else {
			sb.WriteString(sep + " ")
		}

		num := styleDim.Render(fmt.Sprintf("%*d", labelW, i))
		b := bar(pct, barW)
		pctStr := colorForPct(pct).Render(fmt.Sprintf("%3d%%", pct))
		sb.WriteString(fmt.Sprintf("%s %s %s", num, b, pctStr))

		if col == cols-1 && i < n-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func ioColor(kbs int) lipgloss.Style {
	if kbs >= 102400 {
		return styleRed
	}
	if kbs >= 10240 {
		return styleYellow
	}
	return styleGreen
}

func loadColor(load string, ncores int) lipgloss.Style {
	if ncores <= 0 {
		ncores = 1
	}
	var f float64
	fmt.Sscanf(load, "%f", &f)
	pct := int(f / float64(ncores) * 100)
	return colorForPct(pct)
}

// stackLegend résume les états en clair (« 62 stacks · ● 14 en marche · ○ 48
// non déployées »), sans les états à zéro : une rangée de pastilles colorées
// sans libellé n'était lisible que si l'on connaissait le code couleur.
func stackLegend(sc StackCounts) string {
	parts := []string{fmt.Sprintf("%d stacks", sc.Total)}
	add := func(n int, st lipgloss.Style, glyph, one, many string) {
		if n == 0 {
			return
		}
		label := many
		if n == 1 {
			label = one
		}
		// Espace entre le rond et le nombre : le glyphe ● est de largeur
		// « ambiguë » et certains terminaux le rendent sur ~2 cellules.
		parts = append(parts, st.Render(fmt.Sprintf("%s %d %s", glyph, n, label)))
	}
	add(sc.Running, styleGreen, "●", "en marche", "en marche")
	add(sc.Unhealthy, styleOrange, "●", "unhealthy", "unhealthy")
	add(sc.Partial, styleYellow, "●", "partielle", "partielles")
	add(sc.Stopped, styleRed, "●", "arrêtée", "arrêtées")
	add(sc.NotDep, styleDim, "○", "non déployée", "non déployées")
	return strings.Join(parts, styleDim.Render(" · "))
}

// compactLeftW renvoie la largeur de barre et la largeur totale de la
// colonne des ressources pour une largeur de terminal donnée.
func compactLeftW(width int) (barW, leftW int) {
	barW = compactBarW
	if width >= 150 {
		barW = mainBarW
	}
	// "  " + libellé 7 + " " + barre + " 100%" + "  " + détail 14
	return barW, 2 + 7 + 1 + barW + 5 + 2 + 14
}

// compactCoreLayout calcule la grille des cœurs (lignes × colonnes, remplie
// colonne par colonne) : au moins resourceRows lignes pour s'aligner sur les
// ressources, davantage si les colonnes ne tiennent pas dans la largeur.
func compactCoreLayout(n, width int) (rows, cols int) {
	if n <= 0 {
		return resourceRows, 0
	}
	_, leftW := compactLeftW(width)
	labelW := len(fmt.Sprintf("%d", n-1))
	cellW := labelW + 1 + coreBarW + 1 + 4 + 3
	maxCols := max(1, (width-leftW-3)/cellW)
	rows = max(resourceRows, (n+maxCols-1)/maxCols)
	cols = (n + rows - 1) / rows
	return rows, cols
}

// renderCompactHeader : en-tête en deux colonnes pour terminal large. À
// gauche une ligne par ressource (libellés et barres alignés), à droite la
// grille des cœurs avec barre et pourcentage, puis Load/IO/Uptime en pied.
func renderCompactHeader(m monitor.Metrics, du *compose.DockerUsage, line1 string, width int) string {
	L := styleCyan
	barW, leftW := compactLeftW(width)
	pct := func(p int) string { return colorForPct(p).Render(fmt.Sprintf("%4d%%", p)) }
	res := func(label string, p int, detail string) string {
		return fmt.Sprintf("  %s %s%s  %s", L.Render(fmt.Sprintf("%-7s", label)), bar(p, barW), pct(p), detail)
	}
	// Swap : neutre tant que la RAM n'est pas sous pression (cf. swapStyle).
	sw := swapStyle(m)
	swapRow := fmt.Sprintf("  %s %s%s  %s", L.Render(fmt.Sprintf("%-7s", "Swap")),
		barStyled(m.SwapPercent, barW, sw), sw.Render(fmt.Sprintf("%4d%%", m.SwapPercent)),
		fmt.Sprintf("%.1f / %.1f Go", m.SwapUsedGB, m.SwapTotalGB))
	// Détail en clair (« utilisé / total Go ») ; pour le CPU, dire sur combien
	// de cœurs porte le chiffre et pointer vers leur détail à droite.
	cpuDetail := ""
	if n := len(m.CPUCores); n > 0 {
		cpuDetail = styleDim.Render(fmt.Sprintf("%d cœurs →", n))
	}
	left := []string{
		res("CPU", m.CPUPercent, cpuDetail),
		res("RAM", m.MemPercent, fmt.Sprintf("%.1f / %.1f Go", m.MemUsedGB, m.MemTotalGB)),
		swapRow,
		res("Disque", m.DiskPercent, fmt.Sprintf("%d / %d Go", m.DiskUsedGB, m.DiskTotalGB)),
	}

	rows, cols := compactCoreLayout(len(m.CPUCores), width)
	labelW := len(fmt.Sprintf("%d", max(len(m.CPUCores)-1, 0)))
	sep := styleMagenta.Render(" │ ")
	body := make([]string, rows)
	for r := 0; r < rows; r++ {
		l := ""
		if r < len(left) {
			l = left[r]
		}
		l += strings.Repeat(" ", max(leftW-lipgloss.Width(l), 0))
		var cells []string
		for c := 0; c < cols; c++ {
			i := c*rows + r // remplissage colonne par colonne : 0 1 2 3 | 4 5 6 7
			if i >= len(m.CPUCores) {
				break
			}
			p := m.CPUCores[i]
			cells = append(cells, fmt.Sprintf("%s %s%s",
				styleDim.Render(fmt.Sprintf("%*d", labelW, i)), bar(p, coreBarW), pct(p)))
		}
		body[r] = l + sep + strings.Join(cells, "   ")
	}

	dot := styleDim.Render(" · ")
	l1c, l5c, l15c := loadColor(m.Load1, m.NCores), loadColor(m.Load5, m.NCores), loadColor(m.Load15, m.NCores)
	foot := fmt.Sprintf("  %s %s %s%s%s%s%s %s  %s  %s ↓%s/s ↑%s/s  %s  %s %s",
		L.Render(fmt.Sprintf("%-7s", "Système")), L.Render("Load"), l1c.Render(m.Load1), dot, l5c.Render(m.Load5), dot, l15c.Render(m.Load15),
		styleDim.Render("(1/5/15 min)"),
		styleDim.Render("·"), L.Render("IO"),
		ioColor(m.DiskReadKBs).Render(fmtRate(m.DiskReadKBs)), ioColor(m.DiskWriteKBs).Render(fmtRate(m.DiskWriteKBs)),
		styleDim.Render("·"), L.Render("Uptime"), m.Uptime,
	)

	// Titres des deux colonnes sur la ligne de respiration : sans eux, les
	// numéros de cœurs se lisaient comme la suite de la colonne de gauche.
	titles := "  " + styleDim.Render("Ressources")
	if m.DiskMount != "" {
		// Le disque mesuré est la partition des données Docker (cf. monitor).
		titles += styleDim.Render(" · disque : " + m.DiskMount)
	}
	titles += strings.Repeat(" ", max(leftW-lipgloss.Width(titles), 0))
	if len(m.CPUCores) > 0 {
		titles += "   " + styleDim.Render("Détail par cœur")
	}
	lines := append([]string{line1, titles}, body...)
	lines = append(lines, dockerLine(du), foot, styleDim.Render(strings.Repeat("─", width)))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

// swapStyle : le Swap n'est coloré (vert→rouge) que si la RAM est elle-même
// sous pression. Sous Linux, un Swap rempli avec de la RAM libre est courant
// et sans gravité (pages froides déplacées) : l'afficher en orange alarmait
// pour rien.
func swapStyle(m monitor.Metrics) lipgloss.Style {
	if m.MemPercent >= 60 {
		return colorForPct(m.SwapPercent)
	}
	return styleDim
}

// barStyled : comme bar, mais remplie dans la couleur de st.
func barStyled(pct, width int, st lipgloss.Style) string {
	pct = max(0, min(pct, 100))
	filled := (pct*width + 50) / 100
	if pct > 0 && filled == 0 {
		filled = 1
	}
	fill := st.GetForeground()
	if _, none := fill.(lipgloss.NoColor); none || fill == nil {
		fill = lipgloss.Color("244") // styleDim n'a pas de couleur propre
	}
	return lipgloss.NewStyle().Background(fill).Render(strings.Repeat(" ", filled)) +
		styleTrack.Render(strings.Repeat(" ", width-filled))
}

// fmtBytes : taille lisible en Go (Mo en dessous de 1 Go).
func fmtBytes(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f Go", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d Mo", b>>20)
}

// dockerLine : conteneurs en marche et place prise par Docker — ce qu'on
// regarde avant un Pull ou un nettoyage.
func dockerLine(du *compose.DockerUsage) string {
	label := "  " + styleCyan.Render(fmt.Sprintf("%-7s", "Docker")) + " "
	if du == nil {
		return label + styleDim.Render("mesure en cours…")
	}
	dot := styleDim.Render("  ·  ")
	running := styleGreen
	if du.Running < du.Containers {
		running = styleYellow
	}
	s := label + running.Render(fmt.Sprintf("%d / %d", du.Running, du.Containers)) + " conteneurs en marche" +
		dot + "images " + fmtBytes(du.ImagesSize)
	if du.ImagesReclaimable > 0 {
		s += styleDim.Render(" (dont " + fmtBytes(du.ImagesReclaimable) + " inutilisées)")
	}
	s += dot + "volumes "
	if du.VolumesOK {
		s += fmtBytes(du.VolumesSize)
	} else {
		s += styleDim.Render("…")
	}
	return s
}
