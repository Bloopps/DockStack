package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// logLine : une ligne de log assainie et la source qui l'a écrite (nom brut
// reçu de compose). Les deux restent séparés : le préfixe s'aligne au rendu
// sur le plus long nom reçu, comme docker compose logs, ce qu'une chaîne
// « [nom] texte » composée à la réception empêchait.
type logLine struct{ name, text string }

// resetLogSession prépare la vue pour une nouvelle session : suivi du direct,
// sans filtre, noms raccourcis d'après les services de la stack. Le repli (w)
// est gardé : c'est une préférence de lecture, pas un état du flux.
func (m *Model) resetLogSession(stack compose.Stack) {
	m.logStack = stackLabel(stack)
	m.logLines, m.logScroll = nil, 0
	m.logPaused, m.logPauseAt, m.logAnchor, m.logEnded = false, 0, 0, false
	m.logFilter, m.logFiltering, m.logMatch = "", false, nil
	m.logNames, m.logNameW = map[string]string{}, 0
	m.logAlias, m.logNoCtr = logAliases(stack)
}

// logAliases : compose nomme la source d'une ligne « service-1 », et le
// numéro ne sert qu'à distinguer les répliques d'un service scalé. Un service
// à un seul conteneur s'affiche donc sous son nom, ce qui raccourcit la
// colonne des préfixes. Renvoie aussi si les services suivis n'ont aucun
// conteneur : le flux se fermera alors sans une ligne.
func logAliases(s compose.Stack) (map[string]string, bool) {
	only := make(map[string]bool, len(s.Only))
	for _, n := range s.Only {
		only[n] = true
	}
	alias := make(map[string]string, len(s.Services))
	ctrs := 0
	for _, sv := range s.Services {
		if len(only) > 0 && !only[sv.Name] {
			continue
		}
		ctrs += sv.Total
		if sv.Total <= 1 {
			alias[sv.Name+"-1"] = sv.Name
		}
	}
	if len(only) == 0 {
		return alias, s.Total == 0
	}
	return alias, ctrs == 0
}

// learnLogName retient le nom affiché d'une source vue pour la première fois.
// Une réplique « svc-2 » révèle un service scalé : « svc-1 » reprend alors
// son numéro, sinon les deux se confondraient sous « svc ».
func (m *Model) learnLogName(raw string) {
	if m.logNames == nil {
		m.logNames = map[string]string{}
	}
	name := sanitizeLogLine(raw)
	disp := name
	if a, ok := m.logAlias[name]; ok {
		disp = a
	}
	m.logNames[raw] = disp
	m.logNameW = max(m.logNameW, ansi.StringWidth(disp))
	i := strings.LastIndexByte(name, '-')
	if i <= 0 {
		return
	}
	if n, err := strconv.Atoi(name[i+1:]); err != nil || n == 1 {
		return
	}
	first := name[:i] + "-1"
	delete(m.logAlias, first)
	if d, ok := m.logNames[first]; ok && d != first {
		m.logNames[first] = first
		m.logNameW = max(m.logNameW, ansi.StringWidth(first))
	}
}

// logName : nom affiché de la source d'une ligne.
func (m Model) logName(l logLine) string {
	if d, ok := m.logNames[l.name]; ok {
		return d
	}
	return sanitizeLogLine(l.name)
}

// appendLogs ajoute un lot reçu du flux : assainissement, filtre, plafond du
// tampon, puis suivi. En direct, la vue est recollée en bas quelle que soit
// la taille du lot : le seuil de deux lignes d'avant décrochait dès
// l'ouverture (500 lignes d'historique par conteneur arrivent d'un coup) et
// laissait l'écran sur la ligne la plus ancienne.
func (m *Model) appendLogs(entries []compose.LogEntry) {
	q := strings.ToLower(m.logFilter)
	for _, e := range entries {
		if _, ok := m.logNames[e.Name]; !ok {
			m.learnLogName(e.Name)
		}
		l := logLine{name: e.Name, text: sanitizeLogLine(e.Line)}
		if q != "" && logMatches(l, q) {
			m.logMatch = append(m.logMatch, len(m.logLines))
		}
		m.logLines = append(m.logLines, l)
	}
	m.trimLogs()
	if !m.logPaused {
		m.logScroll = m.logsMaxScroll()
	}
}

// trimLogs applique le plafond glissant : le suivi est continu (Follow), sans
// limite la mémoire grandit indéfiniment. On taille par paquets pour ne pas
// recopier à chaque ligne, et on réalloue pour libérer l'ancien tampon. Les
// repères (défilement, filtre, début de la pause) suivent le décalage.
func (m *Model) trimLogs() {
	if len(m.logLines) <= maxLogLines+logTrimChunk {
		return
	}
	drop := len(m.logLines) - maxLogLines
	m.logLines = append([]logLine(nil), m.logLines[drop:]...)
	gone := drop // lignes visibles retirées
	if m.logFilter != "" {
		k := sort.SearchInts(m.logMatch, drop)
		match := make([]int, len(m.logMatch)-k)
		for i, idx := range m.logMatch[k:] {
			match[i] = idx - drop
		}
		m.logMatch, gone = match, k
	}
	m.logScroll = max(m.logScroll-gone, 0)
	m.logPauseAt = max(m.logPauseAt-drop, 0)
	m.logAnchor = max(m.logAnchor-drop, 0)
}

// logMatches : sous-chaîne sans tenir compte de la casse, dans le message ou
// le nom de la source (« / redis » isole un service). Le nom reçu plutôt que
// le nom affiché : il contient ce dernier, et ne change pas quand une
// réplique rend son numéro à « svc-1 ». q est déjà en minuscules.
func logMatches(l logLine, q string) bool {
	return strings.Contains(strings.ToLower(l.text), q) ||
		strings.Contains(strings.ToLower(l.name), q)
}

// setLogFilter change le filtre et recalcule les lignes retenues. En direct,
// la vue reste en bas. En pause, elle reste sur la même ligne du tampon, ou
// sur la suivante retenue : on filtre « error », on remonte jusqu'à
// l'erreur, et esc rend son contexte au lieu de renvoyer en bas.
func (m *Model) setLogFilter(f string) {
	// Repère : la ligne du haut de l'écran. Quand le filtre ne retient plus
	// rien, le précédent est gardé : une faute de frappe corrigée ne renvoie
	// pas au début.
	if m.logPaused && m.logScroll < m.logCount() {
		m.logAnchor = m.logIndex(m.logScroll)
	}
	prev := strings.ToLower(m.logFilter)
	m.logFilter = f
	q := strings.ToLower(f)
	switch {
	case q == "":
		m.logMatch = nil
	case prev != "" && strings.Contains(q, prev):
		// Filtre précisé (une lettre de plus) : les lignes retenues sont
		// parmi les précédentes, inutile de relire tout le tampon.
		match := make([]int, 0, len(m.logMatch))
		for _, i := range m.logMatch {
			if logMatches(m.logLines[i], q) {
				match = append(match, i)
			}
		}
		m.logMatch = match
	default:
		var match []int
		for i, l := range m.logLines {
			if logMatches(l, q) {
				match = append(match, i)
			}
		}
		m.logMatch = match
	}
	if m.logPaused {
		pos := m.logAnchor
		if q != "" {
			pos = sort.SearchInts(m.logMatch, pos)
		}
		m.logScroll = pos
	}
	m.syncLogScroll()
}

// logCount : nombre de lignes visibles (toutes, ou celles du filtre).
func (m Model) logCount() int {
	if m.logFilter == "" {
		return len(m.logLines)
	}
	return len(m.logMatch)
}

// logIndex : indice dans logLines de la i-ième ligne visible.
func (m Model) logIndex(i int) int {
	if m.logFilter == "" {
		return i
	}
	return m.logMatch[i]
}

// logNew : lignes visibles arrivées depuis la pause.
func (m Model) logNew() int {
	if !m.logPaused {
		return 0
	}
	if m.logFilter == "" {
		return max(len(m.logLines)-m.logPauseAt, 0)
	}
	return len(m.logMatch) - sort.SearchInts(m.logMatch, m.logPauseAt)
}

// syncLogScroll recolle la vue en bas en direct, et la borne en pause. À
// appeler quand le bas change sans nouvelle ligne : taille du terminal,
// repli, filtre.
func (m *Model) syncLogScroll() {
	if !m.logPaused {
		m.logScroll = m.logsMaxScroll()
		return
	}
	m.logScroll = max(min(m.logScroll, m.logsMaxScroll()), 0)
}

// scrollLogs déplace la vue (↑↓ j k, pgup pgdown, g G home end), puis
// applique la règle du suivi : la vue suit le direct tant qu'elle est en
// bas, et se met en pause dès qu'on la quitte. Redescendre jusqu'en bas (↓,
// pgdown ou G) relance le suivi. Touches ignorées : rien ne change.
func (m *Model) scrollLogs(key string) {
	bottom := m.logsMaxScroll()
	switch key {
	case "up", "k":
		m.logScroll = max(m.logScroll-1, 0)
	case "down", "j":
		m.logScroll = min(m.logScroll+1, bottom)
	case "pgup":
		m.logScroll = max(m.logScroll-m.logPage(-1), 0)
	case "pgdown":
		m.logScroll = min(m.logScroll+m.logPage(1), bottom)
	case "g", "home":
		m.logScroll = 0
	case "G", "end":
		m.logScroll = bottom
	default:
		return
	}
	paused := m.logScroll < bottom
	if paused && !m.logPaused {
		m.logPauseAt = len(m.logLines)
	}
	m.logPaused = paused
}

// logPage : nombre de lignes de log d'une page (cf. page), vers le bas
// (dir = 1) ou vers le haut (dir = -1) depuis la ligne du haut de l'écran.
// Repliées, elles occupent plusieurs lignes d'écran : on les compte jusqu'à
// remplir la page, sinon pgdown sauterait du texte jamais affiché.
func (m Model) logPage(dir int) int {
	p := page(m.logsHeight())
	if !m.logWrap {
		return p
	}
	n, rows := 0, 0
	for i := m.logScroll; i >= 0 && i < m.logCount(); i += dir {
		if dir < 0 && i == m.logScroll {
			continue // vers le haut, la page commence au-dessus de l'écran
		}
		if rows += m.logRowCount(m.logLines[m.logIndex(i)]); rows > p && n > 0 {
			break
		}
		n++
	}
	return max(n, 1)
}

// logPrefixW : largeur de la colonne des noms, bornée au tiers de l'écran
// pour laisser au message l'essentiel de la ligne.
func (m Model) logPrefixW() int {
	return min(m.logNameW, max(m.width/3, 6))
}

// logMsgW : largeur laissée au message, après « ␣␣nom │ ».
func (m Model) logMsgW() int {
	return max(m.width-m.logPrefixW()-5, 1)
}

// logRowCount : lignes d'écran qu'occupe une ligne de log.
func (m Model) logRowCount(l logLine) int {
	if !m.logWrap {
		return 1
	}
	return strings.Count(ansi.Wrap(l.text, m.logMsgW(), ""), "\n") + 1
}

// appendLogRows ajoute à dst les lignes d'écran d'une ligne de log. Sans
// repli, une seule, coupée à la largeur. Repliée, la suite du message passe
// sous son début, à la largeur restante ; ansi.Wrap plutôt que wordWrap, qui
// fusionne les espaces : l'indentation d'une pile d'appels et les colonnes
// alignées d'un message se perdaient. Aucune ligne ne dépasse l'écran : une
// ligne repliée par le terminal ne serait comptée nulle part et décalerait
// toute la vue (titre et pied compris). prefixes garde, le temps d'une
// image, le préfixe déjà stylé de chaque source : un rendu lipgloss par ligne
// doublait le coût de la vue.
func (m Model) appendLogRows(dst []string, l logLine, prefixes map[string]string) []string {
	pw, msgW := m.logPrefixW(), m.logMsgW()
	prefix, ok := prefixes[l.name]
	if !ok {
		name := truncate(m.logName(l), pw)
		prefix = "  " + styleCyan.Render(name) + strings.Repeat(" ", max(pw-ansi.StringWidth(name), 0)) + styleDim.Render(" │ ")
		prefixes[l.name] = prefix
	}
	if !m.logWrap {
		return append(dst, prefix+truncate(l.text, msgW))
	}
	indent := strings.Repeat(" ", pw+5)
	for i, r := range strings.Split(ansi.Wrap(l.text, msgW, ""), "\n") {
		if i == 0 {
			dst = append(dst, prefix+truncate(r, msgW))
		} else {
			dst = append(dst, indent+truncate(r, msgW))
		}
	}
	return dst
}

// logsMaxScroll : première ligne affichée quand la vue est en bas. Repliée,
// une ligne de log occupe plusieurs lignes d'écran : on remonte depuis la
// fin tant que l'écran n'est pas plein, soit au plus une hauteur d'écran de
// lignes à replier, quelle que soit la taille du tampon. Le défilement
// compte des lignes de log : une ligne plus haute que l'écran à elle seule
// ne se lit qu'en partie (son début en pause, sa fin en direct).
func (m Model) logsMaxScroll() int {
	n, h := m.logCount(), m.logsHeight()
	if !m.logWrap {
		return max(n-h, 0)
	}
	used := 0
	for i := n - 1; i >= 0; i-- {
		used += m.logRowCount(m.logLines[m.logIndex(i)])
		if used > h {
			return min(i+1, n-1)
		}
	}
	return 0
}

// logsTitle : nom de la stack, filtre éventuel, et à droite l'état du flux,
// qui ne se voyait nulle part : en direct, en pause (avec ce qui est arrivé
// depuis), ou terminé. Sur un écran étroit, l'état garde sa place et c'est
// le nom qui est coupé.
func (m Model) logsTitle() string {
	left := styleTitle.Render("📋 Logs — " + m.logStack)
	if m.logFiltering || m.logFilter != "" {
		cursor := ""
		if m.logFiltering {
			cursor = styleCyan.Render("▏")
		}
		left += "   " + styleCyan.Render("/ ") + m.logFilter + cursor
		if m.logFilter != "" {
			n := m.logCount()
			left += styleDim.Render(fmt.Sprintf(" · %d ligne%s sur %d", n, plural(n), len(m.logLines)))
		}
	}
	// Une colonne de marge à droite : un glyphe que le terminal dessine plus
	// large que prévu ne renvoie pas la fin du titre à la ligne.
	room := m.width - 1
	full, short := m.logState()
	lw := lipgloss.Width(left)
	for _, st := range []string{full, short} {
		if sw := lipgloss.Width(st); lw+2+sw <= room {
			return left + strings.Repeat(" ", room-lw-sw) + st
		}
	}
	if sw := lipgloss.Width(short); room-sw-1 >= 12 {
		return truncate(left, room-sw-1) + " " + short
	}
	return truncate(left, max(room, 1))
}

// logState : état du flux, en version complète et courte (sans le rappel de
// la touche, que le pied de page donne aussi).
func (m Model) logState() (full, short string) {
	n := m.logNew()
	switch {
	case m.logEnded:
		s := "■ flux terminé"
		if n > 0 {
			s += fmt.Sprintf(" · +%d ligne%s plus bas", n, plural(n))
		}
		return styleDim.Render(s), styleDim.Render(s)
	case m.logPaused:
		s := "⏸ en pause"
		if n > 0 {
			s += fmt.Sprintf(" · +%d ligne%s", n, plural(n))
		}
		return styleYellow.Render(s + " · G suivre"), styleYellow.Render(s)
	}
	return styleGreen.Render("● en direct"), styleGreen.Render("● en direct")
}

// logsFooter : touches de la vue. Pendant la saisie du filtre, seules celles
// de la saisie (q y est une lettre) ; en pause, G relance le suivi.
func (m Model) logsFooter() string {
	if m.quitArmed {
		return quitWarning(m.width)
	}
	if m.logFiltering {
		return renderBar([]footerKey{{"↩", "valider"}, {"esc", "effacer"}, {"↑↓ pgup/pgdn", "défiler"}}, nil, m.width, "")
	}
	nav := edgeKey
	if m.logPaused && !m.logEnded {
		nav = footerKey{"G", "suivre"}
	}
	wrap := footerKey{"w", "replier"}
	if m.logWrap {
		wrap = footerKey{"w", "tronquer"}
	}
	// esc efface d'abord le filtre. Le pied perd d'abord « / filtre » (la
	// touche est la même partout), puis, en 80 colonnes, les descriptions du
	// défilement et g/G, mais garde w, qui rend le message lisible.
	back := footerKey{"esc", "retour"}
	if m.logFilter != "" {
		back = footerKey{"esc", "effacer"}
	}
	return renderBarFit([][]footerKey{
		{scrollKey, nav, wrap, {"/", "filtre"}},
		{scrollKey, nav, wrap},
		{scrollKeyShort, wrap, nav},
	}, []footerKey{back, {"?", "aide"}, {"q", "quitter"}}, m.width, "")
}

func renderLogsView(m Model) string {
	sep := styleDim.Render(strings.Repeat("─", max(m.width, 1)))
	bodyH := m.logsHeight()

	var lines []string
	switch {
	case len(m.logLines) == 0 && m.logEnded:
		// Flux fermé sans une ligne : « pour l'instant » promettait des
		// lignes qui ne viendraient pas.
		msg := "  Aucune ligne : le flux s'est terminé sans rien recevoir."
		if m.logNoCtr {
			msg = "  Aucune ligne : la stack n'a aucun conteneur (non déployée ?)"
		}
		lines = append(lines, styleDim.Render(truncate(msg, m.width)))
	case len(m.logLines) == 0:
		lines = append(lines, styleDim.Render("  Aucune ligne pour l'instant…"))
	case m.logCount() == 0:
		lines = append(lines, styleDim.Render(truncate("  Aucune ligne ne contient « "+m.logFilter+" ».", m.width)))
	default:
		bottom := m.logsMaxScroll()
		prefixes := map[string]string{}
		if m.logPaused {
			for i := max(min(m.logScroll, bottom), 0); i < m.logCount() && len(lines) < bodyH; i++ {
				lines = m.appendLogRows(lines, m.logLines[m.logIndex(i)], prefixes)
			}
			break
		}
		// En direct, le bas est recalculé ici (la vue reste juste même si un
		// chemin de mise à jour a oublié de recoller logScroll), et le
		// dernier message touche le pied, comme dans un terminal : replié,
		// la ligne au-dessus de bottom n'est visible qu'en partie, au lieu
		// de laisser des lignes vides sous le direct.
		for i := max(bottom-1, 0); i < m.logCount(); i++ {
			lines = m.appendLogRows(lines, m.logLines[m.logIndex(i)], prefixes)
		}
		lines = lines[max(len(lines)-bodyH, 0):]
	}
	// Hauteur fixe : un flux encore court remontait le pied de page au
	// milieu de l'écran, et une ligne repliée plus haute que le reste de
	// l'écran est coupée ici.
	body := fitHeight(strings.Join(lines, "\n"), bodyH)
	return lipgloss.JoinVertical(lipgloss.Left, m.logsTitle(), sep, body, m.logsFooter())
}
