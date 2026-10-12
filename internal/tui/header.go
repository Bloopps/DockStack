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
	// Conteneurs rattachés aux stacks (en marche / total) : le compteur du
	// démon les compte tous, y compris ceux lancés hors compose (docker run).
	CtrRunning, CtrTotal int
	// Unknown : la liste n'a pas encore pu être lue (chargement, démon
	// injoignable). Sans lui, l'en-tête affichait « aucune déployée ».
	Unknown bool
	// Fraîcheur des données. LastRefresh : dernière liste lue, Refreshing :
	// lecture en cours, UsageAt : dernière mesure d'occupation réussie.
	// Stale : la dernière lecture a échoué, les compteurs sont ceux d'avant ;
	// DockerDown : le démon ne répond plus, les mesures Docker aussi sont
	// figées (une erreur de dossier, elle, laisse Docker répondre).
	LastRefresh, UsageAt time.Time
	Refreshing           bool
	Stale, DockerDown    bool
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
		return fmt.Sprintf("%dMo", kbs/1024)
	case kbs >= 1024:
		return fmt.Sprintf("%d.%dMo", kbs/1024, kbs*10/1024%10)
	default:
		return fmt.Sprintf("%dKo", kbs)
	}
}

func renderHeader(m monitor.Metrics, hist metricsHistory, sc StackCounts, du *compose.DockerUsage, width int) string {
	L := styleCyan
	sep := styleSep

	// ── Ligne 1 : titre │ chemin │ stacks │ heure ──────────────────────────
	title := styleGreen.Bold(true).Render("🐳 Docker Stack Manager")
	// Le dossier scanné et la touche pour en changer, côte à côte : sans ce
	// rappel, un nouvel utilisateur ne sait pas comment quitter un mauvais
	// dossier (le raccourci o ne tient pas toujours dans le pied de page).
	change := styleDim.Render("  ") + styleCyan.Bold(true).Render("o") + styleDim.Render(" changer")
	// L'état des stacks est sur la ligne Docker (dockerLine), avec les
	// conteneurs : un seul endroit pour « est-ce que tout tourne ? ».
	clock := freshnessText(sc, time.Now(), width)
	// Un chemin long cède la place (début coupé, la fin est la plus parlante)
	// plutôt que l'heure et « o changer », que la coupe de fin de ligne
	// emportait en premier.
	dir := sc.Dir
	if room := width - lipgloss.Width(title+sep+L.Render("📂")+" "+change+sep+clock); lipgloss.Width(dir) > room && room >= 8 {
		dir = ansi.TruncateLeft(dir, lipgloss.Width(dir)-room+1, "…")
	}
	path := L.Render("📂") + " " + dir + change
	line1 := title + sep + path + sep + clock

	if width >= compactHeaderMinWidth {
		return renderCompactHeader(m, du, sc, line1, width)
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
		L.Render("Actif"), m.Uptime,
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
		L.Render("Charge"),
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
	parts = append(parts, innerSep, line3, line4, dockerLine(du, sc), divider)
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
		rows, _, dockerCol := compactLayout(nCores, width)
		h := 2 + rows + 2 // titre + titres de colonnes, ressources/cœurs, Système + séparateur
		if !dockerCol {
			h++ // Docker repasse en ligne
		}
		return h
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

// freshnessWideMin : à partir de cette largeur, l'heure courante et l'âge de
// la liste tiennent ensemble en ligne 1 ; en dessous, l'heure de la liste
// remplace l'horloge.
const freshnessWideMin = 100

// staleAfter : âge de la liste au-delà duquel « maj » passe au jaune. Le
// tick relit la liste toutes les 60 s, les événements Docker plus souvent :
// une liste plus vieille signale un flux d'événements ou des lectures en
// échec.
const staleAfter = 2 * time.Minute

// freshnessText : fin de la ligne 1, l'heure et la fraîcheur de la liste.
// « maj » était au pied de page, où il cédait la place aux touches sous
// ~180 colonnes : on ne savait pas de quand dataient les chiffres.
func freshnessText(sc StackCounts, now time.Time, width int) string {
	clock := styleCyan.Render("🕐") + " " + now.Format("15:04:05")
	var fresh string
	switch {
	case sc.Refreshing:
		fresh = styleDim.Render("↻ actualisation…")
	case !sc.LastRefresh.IsZero():
		// Jaune : liste ancienne, ou dernière lecture en échec (l'heure est
		// alors celle de la dernière liste valide).
		st := styleDim
		if sc.Stale || now.Sub(sc.LastRefresh) > staleAfter {
			st = styleYellow
		}
		if width >= freshnessWideMin {
			fresh = st.Render("maj " + relativeTime(sc.LastRefresh, now))
		} else {
			fresh = st.Render("maj " + sc.LastRefresh.Format("15:04:05"))
		}
	}
	switch {
	case fresh == "":
		return clock
	case width >= freshnessWideMin:
		return clock + styleDim.Render(" · ") + fresh
	}
	return fresh
}

// stacksSummary : verdict sur les stacks déployées. « ✔ 15/15 » si tout
// tourne, sinon « ⚠ 14/15 (● 1 arrêtée) » avec chaque
// problème dans sa couleur. Les stacks jamais déployées n'y figurent pas :
// la liste les résume déjà, et elles gonflaient le total pour rien.
func stacksSummary(sc StackCounts) string {
	if sc.Unknown {
		return styleDim.Render("…")
	}
	deployed := sc.Total - sc.NotDep
	if deployed == 0 {
		return styleDim.Render("aucune déployée")
	}
	if sc.Running == deployed {
		return styleGreen.Render(fmt.Sprintf("✔ %d/%d", sc.Running, deployed))
	}
	var probs []string
	add := func(n int, st lipgloss.Style, one, many string) {
		if n == 0 {
			return
		}
		label := many
		if n == 1 {
			label = one
		}
		probs = append(probs, st.Render(fmt.Sprintf("● %d %s", n, label)))
	}
	add(sc.Unhealthy, styleOrange, "unhealthy", "unhealthy")
	add(sc.Partial, styleYellow, "partielle", "partielles")
	add(sc.Stopped, styleRed, "arrêtée", "arrêtées")
	return styleYellow.Render(fmt.Sprintf("⚠ %d/%d", sc.Running, deployed)) +
		" (" + strings.Join(probs, styleDim.Render(" · ")) + ")"
}

// compactLeftW renvoie la largeur de barre et la largeur totale de la
// colonne des ressources pour une largeur de terminal donnée.
func compactLeftW(width int) (barW, leftW int) {
	barW = compactBarW
	if width >= 150 {
		barW = mainBarW
	}
	// "  " + libellé 7 + " " + barre + " 100%" (la valeur est dans la barre)
	return barW, 2 + 7 + 1 + barW + 5
}

// dockerColW : largeur de la colonne Docker (libellé 11 + valeur).
const dockerColW = 36

// coreCellW : largeur d'une cellule de cœur (« 12 ██████ 100% »), hors
// espacement.
func coreCellW(n int) int {
	return len(fmt.Sprintf("%d", max(n-1, 0))) + 1 + coreBarW + 5
}

// compactLayout calcule la grille des cœurs (lignes × colonnes, remplie
// colonne par colonne) et dit si la colonne Docker tient à droite. Priorité :
// cœurs sur resourceRows lignes + colonne Docker ; sinon (terminal trop
// étroit, beaucoup de cœurs) la grille s'allonge et Docker repasse en ligne.
func compactLayout(n, width int) (rows, cols int, dockerCol bool) {
	_, leftW := compactLeftW(width)
	cellW := coreCellW(n)
	gridW := func(c int) int { return c*cellW + max(c-1, 0)*3 }
	need := (n + resourceRows - 1) / resourceRows
	if leftW+3+gridW(need)+3+dockerColW <= width {
		return resourceRows, need, true
	}
	if n <= 0 {
		return resourceRows, 0, false
	}
	maxCols := max(1, (width-leftW-3+3)/(cellW+3))
	rows = max(resourceRows, (n+maxCols-1)/maxCols)
	return rows, (n + rows - 1) / rows, false
}

// renderCompactHeader : en-tête en deux colonnes pour terminal large. À
// gauche une ligne par ressource (libellés et barres alignés), à droite la
// grille des cœurs avec barre et pourcentage, puis Load/IO/Uptime en pied.
func renderCompactHeader(m monitor.Metrics, du *compose.DockerUsage, sc StackCounts, line1 string, width int) string {
	L := styleCyan
	barW, leftW := compactLeftW(width)
	pct := func(p int) string { return colorForPct(p).Render(fmt.Sprintf("%4d%%", p)) }
	// Valeur absolue écrite DANS la barre (« 5.8 / 11.7 Go ») : une colonne
	// de détail à part élargissait l'en-tête pour peu d'information.
	res := func(label string, p int, st lipgloss.Style, texts ...string) string {
		return fmt.Sprintf("  %s %s%s", L.Render(fmt.Sprintf("%-7s", label)),
			barText(p, barW, st, texts...), st.Render(fmt.Sprintf("%4d%%", p)))
	}
	gb := func(used, total float64) []string {
		return []string{fmt.Sprintf("%.1f / %.1f Go", used, total), fmt.Sprintf("%.1f/%.1fG", used, total)}
	}
	left := []string{
		res("CPU", m.CPUPercent, colorForPct(m.CPUPercent)),
		res("RAM", m.MemPercent, colorForPct(m.MemPercent), gb(m.MemUsedGB, m.MemTotalGB)...),
		// Swap : neutre tant que la RAM n'est pas sous pression (cf. swapStyle).
		res("Swap", m.SwapPercent, swapStyle(m), gb(m.SwapUsedGB, m.SwapTotalGB)...),
		res("Disque", m.DiskPercent, colorForPct(m.DiskPercent),
			fmt.Sprintf("%d / %d Go", m.DiskUsedGB, m.DiskTotalGB), fmt.Sprintf("%d/%dG", m.DiskUsedGB, m.DiskTotalGB)),
	}

	rows, cols, dockerCol := compactLayout(len(m.CPUCores), width)
	labelW := len(fmt.Sprintf("%d", max(len(m.CPUCores)-1, 0)))
	gridW := cols*coreCellW(len(m.CPUCores)) + max(cols-1, 0)*3
	sep := styleMagenta.Render(" │ ")
	var dockerRows []string
	if dockerCol {
		dockerRows = dockerItems(du, sc, true)
	}
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
		if dockerCol {
			body[r] += strings.Repeat(" ", max(leftW+3+gridW-lipgloss.Width(body[r]), 0)) + sep
			if r < len(dockerRows) {
				body[r] += dockerRows[r]
			}
		}
	}

	dot := styleDim.Render(" · ")
	l1c, l5c, l15c := loadColor(m.Load1, m.NCores), loadColor(m.Load5, m.NCores), loadColor(m.Load15, m.NCores)
	foot := fmt.Sprintf("  %s %s %s%s%s%s%s %s  %s  %s ↓%s/s ↑%s/s  %s  %s %s",
		L.Render(fmt.Sprintf("%-7s", "Système")), L.Render("Charge"), l1c.Render(m.Load1), dot, l5c.Render(m.Load5), dot, l15c.Render(m.Load15),
		styleDim.Render("(1/5/15 min)"),
		styleDim.Render("·"), L.Render("IO"),
		ioColor(m.DiskReadKBs).Render(fmtRate(m.DiskReadKBs)), ioColor(m.DiskWriteKBs).Render(fmtRate(m.DiskWriteKBs)),
		styleDim.Render("·"), L.Render("Actif"), m.Uptime,
	)

	// Titres des deux colonnes sur la ligne de respiration : sans eux, les
	// numéros de cœurs se lisaient comme la suite de la colonne de gauche.
	titles := "  " + styleDim.Render("Ressources")
	if m.DiskMount != "" && m.DiskMount != "/" {
		// Le disque mesuré est la partition des données Docker (cf. monitor) ;
		// ne le préciser que lorsque ce n'est pas simplement la racine.
		titles += styleDim.Render(" · Disque = partition " + m.DiskMount + " (données Docker)")
	}
	titles += strings.Repeat(" ", max(leftW-lipgloss.Width(titles), 0))
	if len(m.CPUCores) > 0 {
		titles += "   " + styleDim.Render("Détail par cœur")
	}
	if dockerCol {
		titles += strings.Repeat(" ", max(leftW+3+gridW+3-lipgloss.Width(titles), 0)) + styleDim.Render("Docker")
	}
	lines := append([]string{line1, titles}, body...)
	if !dockerCol {
		lines = append(lines, dockerLine(du, sc))
	}
	lines = append(lines, foot, styleDim.Render(strings.Repeat("─", width)))
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

// barText : barre remplie dans la couleur de st, avec un texte centré écrit
// dedans — le premier des textes proposés qui tient (du plus lisible au plus
// court), aucun si rien ne tient. Noir sur la partie remplie, gris clair sur
// le sillon.
func barText(pct, width int, st lipgloss.Style, texts ...string) string {
	pct = max(0, min(pct, 100))
	filled := (pct*width + 50) / 100
	if pct > 0 && filled == 0 {
		filled = 1
	}
	fill := st.GetForeground()
	if _, none := fill.(lipgloss.NoColor); none {
		fill = activeScheme.NeutralFill // styleDim n'a pas de couleur propre
	}
	cells := []rune(strings.Repeat(" ", width))
	for _, t := range texts {
		if r := []rune(t); len(r) <= width-2 {
			copy(cells[(width-len(r))/2:], r)
			break
		}
	}
	return lipgloss.NewStyle().Background(fill).Foreground(activeScheme.OnFill(fill)).Render(string(cells[:filled])) +
		styleTrack.Foreground(activeScheme.TrackText).Render(string(cells[filled:]))
}

// fmtBytes : taille lisible en Go (Mo en dessous de 1 Go).
func fmtBytes(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f Go", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d Mo", b>>20)
}

// dockerItems : paires libellé/valeur de l'état Docker. En colonne (col),
// une paire par ligne avec libellés alignés ; sinon libellés courts pour la
// ligne de repli.
func dockerItems(du *compose.DockerUsage, sc StackCounts, col bool) []string {
	item := func(label, value string) string {
		if col {
			return styleCyan.Render(fmt.Sprintf("%-11s", label)) + value
		}
		return styleCyan.Render(label) + " " + value
	}
	wait := styleDim.Render("…")
	ctrs, images, volumes := wait, wait, wait
	stacks := stacksSummary(sc)
	if sc.Stale {
		// Dernière lecture en échec : les compteurs d'avant, affichés comme
		// actuels au-dessus de l'écran d'erreur, laissaient croire que tout
		// tournait encore. On dit de quand date la dernière liste.
		stacks = styleDim.Render("?")
		if !sc.LastRefresh.IsZero() {
			stacks += styleDim.Render(" (dernière liste " + sc.LastRefresh.Format("15:04:05") + ")")
		}
	}
	if du != nil && sc.DockerDown {
		// Démon injoignable : les dernières mesures, en gris et entre
		// parenthèses, derrière un « ? ».
		at := ""
		if !sc.UsageAt.IsZero() {
			at = " à " + sc.UsageAt.Format("15:04")
		}
		ctrs = styleDim.Render(fmt.Sprintf("? (%d/%d%s)", du.Running, du.Containers, at))
		if du.ImagesOK {
			images = styleDim.Render("? (" + fmtBytes(du.ImagesSize) + ")")
		}
		if du.VolumesOK {
			volumes = styleDim.Render("? (" + fmtBytes(du.VolumesSize) + ")")
		}
	} else if du != nil {
		st := styleGreen
		if du.Running < du.Containers {
			st = styleYellow
		}
		ctrs = st.Render(fmt.Sprintf("%d/%d", du.Running, du.Containers))
		// Écart avec les stacks : conteneurs lancés hors compose, dont on
		// signale ceux qui sont arrêtés (c'est souvent eux qui jaunissent le
		// compteur). Sans stacks chargées, ou avec une liste périmée, rien à
		// comparer.
		if out := du.Containers - sc.CtrTotal; sc.Total > 0 && !sc.Stale && out > 0 {
			note := fmt.Sprintf(" · %d hors stack", out)
			if stopped := (du.Containers - du.Running) - (sc.CtrTotal - sc.CtrRunning); stopped > 0 {
				note += fmt.Sprintf(" (%d arrêté%s)", stopped, plural(stopped))
			}
			ctrs += styleDim.Render(note)
		}
		if du.ImagesOK {
			images = fmtBytes(du.ImagesSize)
			if du.ImagesReclaimable > 0 {
				// Calcul exact couche par couche (compose.Usage) ; « ≈ » si une
				// image n'a pas pu être détaillée.
				approx := ""
				if !du.ImagesExact {
					approx = "≈ "
				}
				images += styleDim.Render(" (" + approx + fmtBytes(du.ImagesReclaimable) + " libres)")
			}
		}
		if du.VolumesOK {
			volumes = fmtBytes(du.VolumesSize)
		}
	}
	return []string{
		item("Stacks", stacks),
		item("Conteneurs", ctrs),
		item("Images", images),
		item("Volumes", volumes),
	}
}

// dockerLine : repli sur une ligne quand la colonne Docker ne tient pas.
func dockerLine(du *compose.DockerUsage, sc StackCounts) string {
	return "  " + styleCyan.Render(fmt.Sprintf("%-7s", "Docker")) + " " +
		strings.Join(dockerItems(du, sc, false), styleMagenta.Render(" │ "))
}
