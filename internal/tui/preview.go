package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Largeur de terminal en-dessous de laquelle l'aperçu est masqué même s'il est
// activé : amputer la liste (compteurs alignés à droite, ~600 stacks) la
// rendrait illisible sur un écran étroit.
const minPreviewTermWidth = 100

// Bornes de largeur du volet d'aperçu : ~40 % de la largeur totale, plafonné
// pour laisser la liste respirer sur écran large et rester utile sur écran
// juste assez grand.
const (
	previewMinWidth = 40
	previewMaxWidth = 80
)

// previewVisible dit si le volet de détail doit être rendu : toujours dans la
// liste, si le terminal est assez large.
func (m Model) previewVisible() bool {
	return m.view == viewList && m.width >= minPreviewTermWidth
}

// previewWidth renvoie la largeur du volet (rule comprise) ; suppose
// previewVisible() vrai.
func (m Model) previewWidth() int {
	w := m.width * 2 / 5
	if w < previewMinWidth {
		w = previewMinWidth
	}
	if w > previewMaxWidth {
		w = previewMaxWidth
	}
	return w
}

// previewCacheBox mémoïse le contenu du dernier fichier compose lu, indexé par
// chemin + (mtime, taille) : tant que le curseur reste sur la même stack et que
// le fichier ne change pas, on ne relit pas le disque à chaque frame. Pointeur
// partagé entre les copies du Model, comme rowsCache.
type previewCacheBox struct {
	path    string
	modUnix int64
	size    int64
	lines   []string
	err     error
}

// readFile renvoie les lignes du fichier path, depuis le cache si le fichier
// n'a pas changé (mtime + taille). Lecture paresseuse appelée au rendu : un
// stat par frame suffit, la lecture complète n'a lieu qu'au changement de
// stack ou si le fichier est édité.
func (c *previewCacheBox) readFile(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		c.path = path
		c.lines, c.err = nil, err
		return nil, err
	}
	if path == c.path && st.ModTime().Unix() == c.modUnix && st.Size() == c.size && c.err == nil {
		return c.lines, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		c.path, c.lines, c.err = path, nil, err
		return nil, err
	}
	raw := strings.Split(string(data), "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = sanitizeLogLine(l) // retire \r de CRLF et caractères de contrôle
	}
	c.path = path
	c.modUnix = st.ModTime().Unix()
	c.size = st.Size()
	c.lines, c.err = lines, nil
	return lines, nil
}

// renderPreviewPane construit le volet d'aperçu (largeur paneW, hauteur height),
// avec une règle verticale à gauche. Le contenu dépend de la ligne sous le
// curseur : fichier compose d'une stack, résumé d'un groupe, ou rien.
func renderPreviewPane(m Model, height, paneW int) string {
	inner := paneW - 2 // 2 colonnes pour la règle « │ »
	if inner < 1 {
		inner = 1
	}

	var content []string
	focusedStack, focused := compose.Stack{}, false
	if m.svcFocus {
		focusedStack, focused = m.focusedStack()
	}
	switch row, ok := m.cursorRow(); {
	case focused:
		content = previewServices(m, focusedStack, inner, height)
	case !ok || row.sep || row.more:
		content = []string{styleDim.Render("(rien à afficher)")}
	case row.header:
		content = previewGroup(m, row.group, inner, height, len(m.selectedStacks()))
	case m.previewCompose:
		content = previewStack(m, row.stack, inner, height)
	default:
		content = previewDetail(m.cfg.StackDir, row.stack, m.stackServices(row.stack), inner, height, len(m.selectedStacks()))
	}

	// Hauteur fixe : compléter ou tronquer pour aligner sur la liste.
	if len(content) > height {
		content = content[:height]
	}
	ruleStyle := styleDim
	if focused {
		ruleStyle = styleCyan // le focus est dans le volet
	}
	rule := ruleStyle.Render("│") + " "
	lines := make([]string, height)
	for i := 0; i < height; i++ {
		if i < len(content) {
			lines[i] = rule + content[i]
		} else {
			lines[i] = ruleStyle.Render("│")
		}
	}
	return strings.Join(lines, "\n")
}

// previewStack rend l'aperçu du fichier compose d'une stack : en-tête (chemin
// relatif + note d'override éventuelle) puis le contenu brut tronqué.
func previewStack(m Model, s compose.Stack, inner, height int) []string {
	if s.ComposeFile == "" {
		return []string{styleDim.Render("(pas de fichier compose)")}
	}

	var head []string
	rel := s.ComposeFile
	if r, err := filepath.Rel(m.cfg.StackDir, s.ComposeFile); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	head = append(head, styleCyan.Bold(true).Render(truncate(rel, inner)))
	if ov := compose.FindOverrideFile(s.Dir); ov != "" {
		head = append(head, styleDim.Render(truncate("+ override : "+filepath.Base(ov), inner)))
	}
	head = append(head, "")

	lines, err := m.previewCache.readFile(s.ComposeFile)
	if err != nil {
		head = append(head, styleRed.Render(truncate("Lecture impossible : "+err.Error(), inner)))
		return head
	}

	avail := height - len(head)
	if avail < 1 {
		avail = 1
	}
	body := make([]string, 0, avail)
	more := 0
	for i, l := range lines {
		if i >= avail {
			more = len(lines) - avail
			break
		}
		body = append(body, highlightComposeLine(l, inner))
	}
	if more > 0 {
		// La dernière ligne devient le compteur de troncature.
		body[len(body)-1] = styleDim.Render(fmt.Sprintf("… +%d ligne(s)", more+1))
	}
	return append(head, body...)
}

// previewGroup rend un résumé d'un groupe (dossier) : nom, récap agrégé des
// états, puis chaque stack membre avec sa pastille d'état colorée et son
// compteur conteneurs (x/y). Un dossier regroupe plusieurs stacks ayant chacune
// son propre compose : pas de fichier unique à prévisualiser, d'où ce bilan.
func previewGroup(m Model, group string, inner, height, selected int) []string {
	members := m.groupStacks(group)

	out := []string{
		styleMagenta.Bold(true).Render(truncate(fmt.Sprintf("📁 %s", group), inner)),
	}
	if rollup := groupRollup(members); rollup != "" {
		out = append(out, rollup)
	}
	out = append(out, "")

	// Actions sur tout le dossier, sous la liste des membres (place réservée).
	actions := append([]string{"", styleDim.Render("Actions sur tout le dossier")}, keyGrid([]footerKey{
		{"u", "up"}, {"s", "stop"}, {"d", "down"}, {"r", "restart"}, {"c", "recreate"}, {"p", "pull"}, {"←", "replier"},
	}, inner)...)
	if selected > 0 {
		actions = append([]string{""}, selectionActions(selected, inner)...)
	}
	avail := height - len(out) - len(actions)
	for i, s := range members {
		if i >= avail {
			out[len(out)-1] = styleDim.Render(fmt.Sprintf("… +%d", len(members)-avail+1))
			break
		}
		dot, dotStyle := stateStyle(s.State())
		name := dotStyle.Render(dot) + " " + truncate(s.Name, inner-12)
		if s.Total > 0 {
			count := fmt.Sprintf("%d/%d", s.Running, s.Total)
			pad := inner - lipgloss.Width(name) - len(count)
			if pad < 1 {
				pad = 1
			}
			name += strings.Repeat(" ", pad) + dotStyle.Render(count)
		}
		out = append(out, name)
	}
	return append(out, actions...)
}

// groupRollup résume les états des stacks d'un groupe en segments colorés
// (seuls les états présents sont affichés), p. ex. « ● 2  ◐ 1  ○ 3 ».
func groupRollup(members []compose.Stack) string {
	var run, part, unhealthy, stop, off int
	for _, s := range members {
		switch s.State() {
		case compose.StateUnhealthy:
			unhealthy++
		case compose.StateRunning:
			run++
		case compose.StatePartial:
			part++
		case compose.StateStopped:
			stop++
		default:
			off++
		}
	}
	var segs []string
	add := func(n int, glyph string, st lipgloss.Style) {
		if n > 0 {
			segs = append(segs, st.Render(fmt.Sprintf("%s %d", glyph, n)))
		}
	}
	add(unhealthy, "●", styleOrange)
	add(run, "●", styleGreen)
	add(part, "●", styleYellow)
	add(stop, "●", styleRed)
	add(off, "○", styleDim)
	return strings.Join(segs, "  ")
}

// Styles de coloration YAML de l'aperçu. Réutilise la palette du header pour
// rester cohérent avec le reste de la TUI.
var (
	styleYamlKey    = styleCyan            // clé indentée
	styleYamlTopKey = styleCyan.Bold(true) // clé de premier niveau (section)
	styleYamlPunct  = styleDim             // « : », « - », commentaires
	styleYamlString = styleGreen           // valeur entre guillemets / scalaire libre
	styleYamlNumber = styleYellow          // entier / flottant / port
	styleYamlBool   = styleMagenta         // true/false/null/yes/no
	styleYamlEnvVar = styleOrange          // interpolation ${VAR}
)

// highlightComposeLine colore une ligne de YAML compose : indentation
// préservée, clés mises en valeur (gras au premier niveau), valeurs colorées
// par type, commentaires grisés. Tronque d'abord à inner colonnes pour que les
// codes ANSI ajoutés ensuite ne faussent pas la largeur affichée. Coloration
// par heuristiques, pas un vrai parseur YAML.
func highlightComposeLine(line string, inner int) string {
	line = truncate(line, inner)
	// Indentation conservée telle quelle.
	body := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(body)]
	if body == "" {
		return line
	}
	if strings.HasPrefix(body, "#") {
		return indent + styleYamlPunct.Render(body)
	}

	var b strings.Builder
	b.WriteString(indent)

	// Marqueur(s) de liste « - » (imbrication « - - » possible mais rare).
	for strings.HasPrefix(body, "- ") {
		b.WriteString(styleYamlPunct.Render("-") + " ")
		body = body[2:]
	}
	if body == "-" {
		b.WriteString(styleYamlPunct.Render("-"))
		return b.String()
	}

	// « clé: valeur » ou « clé: » (bloc imbriqué). La clé s'arrête au premier
	// « : » suivi d'une espace ou en fin de ligne ; un « : » sans espace
	// (ex. image: nginx:latest) reste dans la valeur.
	if key, val, ok := splitKeyValue(body); ok {
		keyStyle := styleYamlKey
		if indent == "" {
			keyStyle = styleYamlTopKey
		}
		b.WriteString(keyStyle.Render(key) + styleYamlPunct.Render(":"))
		if val != "" {
			b.WriteString(" " + colorizeYamlValue(strings.TrimPrefix(val, " ")))
		}
		return b.String()
	}

	// Pas de clé : scalaire seul (continuation de bloc, élément de liste simple).
	b.WriteString(colorizeYamlValue(body))
	return b.String()
}

// splitKeyValue sépare « clé: valeur » sur le premier « : » suivi d'une espace
// ou placé en fin de ligne. val garde l'espace de tête éventuelle.
func splitKeyValue(s string) (key, val string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' && (i == len(s)-1 || s[i+1] == ' ') {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// colorizeYamlValue colore une valeur scalaire selon son type, en isolant un
// éventuel commentaire de fin de ligne (« valeur  # note »).
func colorizeYamlValue(v string) string {
	if !strings.HasPrefix(v, `"`) && !strings.HasPrefix(v, "'") {
		if i := strings.Index(v, " #"); i >= 0 {
			return colorizeYamlScalar(v[:i]) + " " + styleYamlPunct.Render(v[i+1:])
		}
	}
	return colorizeYamlScalar(v)
}

func colorizeYamlScalar(v string) string {
	t := strings.TrimSpace(v)
	switch {
	case t == "":
		return v
	case strings.Contains(v, "${"):
		return styleYamlEnvVar.Render(v)
	case (strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`)) ||
		(strings.HasPrefix(t, "'") && strings.HasSuffix(t, "'")):
		return styleYamlString.Render(v)
	case t == "true" || t == "false" || t == "null" || t == "~" || t == "yes" || t == "no":
		return styleYamlBool.Render(v)
	case isYamlNumber(t):
		return styleYamlNumber.Render(v)
	}
	// Scalaire libre (nom d'image, chemin, commande…) : vert doux pour le
	// distinguer des clés sans surcharger.
	return styleYamlString.Render(v)
}

// isYamlNumber dit si t est un entier ou flottant simple (y compris « 8080 »
// ou « 1.5 »). Les versions « 3.8 » entre guillemets passent par la branche
// chaîne ; non quotées, les afficher en nombre convient.
func isYamlNumber(t string) bool {
	if _, err := strconv.ParseFloat(t, 64); err == nil {
		return true
	}
	return false
}

// previewDetail rend le détail d'une stack : état global, services (les
// problèmes d'abord), remarque éventuelle, puis les actions possibles adaptées
// à l'état. C'est la vue par défaut du volet ; tab bascule vers le compose.
func previewDetail(stackDir string, s compose.Stack, services []compose.ServiceStatus, inner, height, selected int) []string {
	dot, dotStyle := stateStyle(s.State())
	out := []string{dotStyle.Render(dot) + " " + styleCyan.Bold(true).Render(truncate(s.Name, inner-2))}

	state := dotStyle.Render(stateWord(s.State()))
	if s.Total > 0 {
		state += styleDim.Render(fmt.Sprintf("  %d/%d conteneurs", s.Running, s.Total))
	}
	out = append(out, state)

	dir := s.Dir
	if r, err := filepath.Rel(stackDir, s.Dir); err == nil && !strings.HasPrefix(r, "..") {
		dir = r
	}
	// Le nom d'une stack EST son chemin relatif au dossier scanné : la ligne
	// du dossier le répétait mot pour mot. Elle ne reste que si elle apporte
	// quelque chose (stack hors du dossier scanné).
	if dir != s.Name {
		out = append(out, styleDim.Render(truncate(dir, inner)))
	}
	out = append(out, "")

	if len(services) > 0 {
		out = append(out, styleDim.Render("Services"))
		svcs := append([]compose.ServiceStatus(nil), services...)
		sort.SliceStable(svcs, func(i, j int) bool {
			return serviceOrder(svcs[i].State()) < serviceOrder(svcs[j].State())
		})
		nameW := 0
		for _, sv := range svcs {
			if w := lipgloss.Width(sv.Name); w > nameW {
				nameW = w
			}
		}
		if nameW > inner-16 {
			nameW = max(inner-16, 8)
		}
		// Place restante pour les services : on réserve l'en-tête déjà rendu,
		// la note d'orphelins éventuelle et le bloc d'actions (4 lignes + pied).
		reserved := len(out) + 1 + 6
		if s.Orphaned {
			reserved += 2
		}
		avail := max(height-reserved, 3)
		hidden, hiddenWhat := 0, ""
		if len(svcs) > avail {
			rest := svcs[avail-1:]
			hidden = len(rest)
			svcs = svcs[:avail-1]
			hiddenWhat = "services"
			same := true
			for _, r := range rest {
				same = same && r.State() == rest[0].State()
			}
			if same {
				hiddenWhat = "services " + stateWord(rest[0].State())
			}
		}
		for _, sv := range svcs {
			d, st := stateStyle(sv.State())
			name := truncate(sv.Name, nameW)
			pad := strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))
			out = append(out, "  "+st.Render(d)+" "+name+pad+"  "+st.Render(stateWord(sv.State())))
		}
		if hidden > 0 {
			out = append(out, styleDim.Render(fmt.Sprintf("  … +%d autres %s", hidden, hiddenWhat)))
		}
		out = append(out, "")
	}
	if s.Orphaned {
		out = append(out, styleYellow.Render(truncate("⚠ conteneurs rattachés par nom (recréés hors compose ?)", inner)), "")
	}

	// Actions utiles selon l'état (vocabulaire docker compose) : c'est ICI
	// qu'on les lit, le pied de page ne les répète pas. Pas de Down proposé à
	// une stack arrêtée.
	var acts []footerKey
	switch s.State() {
	case compose.StateStopped:
		acts = []footerKey{{"u", "up"}, {"p", "pull"}, {"l", "logs"}}
	case compose.StateUnknown:
		acts = []footerKey{{"u", "up"}, {"p", "pull"}}
	case compose.StatePartial:
		acts = []footerKey{{"u", "up"}, {"s", "stop"}, {"d", "down"}, {"r", "restart"}, {"l", "logs"}, {"p", "pull"}}
	case compose.StateUnhealthy:
		acts = []footerKey{{"l", "logs"}, {"r", "restart"}, {"c", "recreate"}, {"s", "stop"}, {"d", "down"}}
	default:
		acts = []footerKey{{"r", "restart"}, {"s", "stop"}, {"d", "down"}, {"c", "recreate"}, {"p", "pull"}, {"l", "logs"}}
	}
	acts = append(acts, footerKey{"v", "compose"}, footerKey{"→", "services"})
	if selected > 0 {
		// Avec des stacks cochées, u/s/d/r… visent la sélection, pas la stack
		// affichée : lister ses actions ici induirait en erreur.
		return append(out, selectionActions(selected, inner)...)
	}
	out = append(out, styleDim.Render("Actions"))
	return append(out, keyGrid(acts, inner)...)
}

// selectionActions : bloc d'actions du volet quand des stacks sont cochées —
// les lettres visent alors la sélection, quelle que soit la ligne affichée.
func selectionActions(n, inner int) []string {
	title := styleYellow.Render(fmt.Sprintf("Actions sur les %d stack%s cochée%s", n, plural(n), plural(n)))
	if n == 1 {
		title = styleYellow.Render("Actions sur la stack cochée")
	}
	return append([]string{title}, keyGrid([]footerKey{
		{"u", "up"}, {"s", "stop"}, {"d", "down"}, {"r", "restart"}, {"c", "recreate"}, {"p", "pull"}, {"↩", "toutes"},
	}, inner)...)
}

// keyGrid dispose des raccourcis « touche action » en colonnes alignées (trois
// au plus), la touche dans la couleur d'accent comme dans le pied de page.
func keyGrid(items []footerKey, inner int) []string {
	colW := 0
	for _, it := range items {
		colW = max(colW, lipgloss.Width(it.key)+1+lipgloss.Width(it.desc))
	}
	colW += 3
	cols := min(3, max(1, (inner-2)/colW))
	var out []string
	for i := 0; i < len(items); i += cols {
		line := " "
		for j := i; j < min(i+cols, len(items)); j++ {
			w := lipgloss.Width(items[j].key) + 1 + lipgloss.Width(items[j].desc)
			line += " " + styleCursorMark.Render(items[j].key) + " " + items[j].desc
			if j < min(i+cols, len(items))-1 {
				line += strings.Repeat(" ", max(colW-w, 1))
			}
		}
		out = append(out, line)
	}
	return out
}

// serviceOrder classe les services : ceux qui posent problème d'abord.
func serviceOrder(s compose.State) int {
	switch s {
	case compose.StateUnhealthy:
		return 0
	case compose.StatePartial:
		return 1
	case compose.StateStopped:
		return 2
	default:
		return 3
	}
}
