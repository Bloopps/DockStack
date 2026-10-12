package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Mode « services » : le focus passe de la liste des stacks au volet de
// détail, où l'on coche des services et où u/d/r/c/p/l n'agissent QUE sur eux,
// sans toucher aux autres services de la stack (compose restreint au modèle
// réduit, sans dépendances : cf. compose.Stack.Only).

// svcCacheEntry mémorise les services déclarés d'un compose, invalidés par la
// signature de ses fichiers : compose, override et .env (qui peut activer
// des profils, COMPOSE_PROFILES), pas seulement le compose.
type svcCacheEntry struct {
	sig  string
	svcs []compose.DeclaredService
}

// svcCacheBox : cache par chemin de compose, pointeur partagé entre les copies
// du Model (comme rowsCache). Le chargement complet du projet n'a lieu que pour
// la stack sous le curseur, une fois par version du fichier.
type svcCacheBox struct{ m map[string]svcCacheEntry }

func (c *svcCacheBox) declared(s compose.Stack) []compose.DeclaredService {
	if s.ComposeFile == "" {
		return nil
	}
	if _, err := os.Stat(s.ComposeFile); err != nil {
		return nil
	}
	sig := compose.FilesSignature(s)
	if e, ok := c.m[s.ComposeFile]; ok && e.sig == sig {
		return e.svcs
	}
	svcs, err := compose.DeclaredServices(s)
	if err != nil {
		svcs = nil // .env manquant, etc. : on retombe sur les services vus par Docker
	}
	if c.m == nil {
		c.m = make(map[string]svcCacheEntry)
	}
	c.m[s.ComposeFile] = svcCacheEntry{sig: sig, svcs: svcs}
	return svcs
}

// stackServices fusionne les services déclarés dans le compose et ceux vus par
// Docker : un service arrêté puis retiré (down) ou jamais déployé reste
// listé, donc sélectionnable pour un « up » ciblé. Trié par nom.
func (m Model) stackServices(s compose.Stack) []compose.ServiceStatus {
	by := make(map[string]compose.ServiceStatus, len(s.Services))
	for _, sv := range s.Services {
		by[sv.Name] = sv
	}
	if m.svcCache != nil {
		for _, d := range m.svcCache.declared(s) {
			sv := by[d.Name] // zéro si le service n'a aucun conteneur
			sv.Name, sv.DependsOn = d.Name, d.DependsOn
			by[d.Name] = sv
		}
	}
	out := make([]compose.ServiceStatus, 0, len(by))
	for _, sv := range by {
		out = append(out, sv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// focusedStack retrouve la stack du mode services par son NOM (la liste est
// re-triée toutes les 15 s : jamais par index).
func (m Model) focusedStack() (compose.Stack, bool) {
	for _, s := range m.stacks {
		if s.Name == m.svcStack {
			return s, true
		}
	}
	return compose.Stack{}, false
}

// enterServices donne le focus au volet sur la stack sous le curseur.
func (m Model) enterServices() (tea.Model, tea.Cmd) {
	s, ok := m.cursorStack()
	if !ok {
		m.setInfo("Viser une stack (pas un dossier) pour gérer ses services")
		return m, m.clearStatusIn(3 * time.Second)
	}
	if m.width < minPreviewTermWidth {
		m.setStatus("Terminal trop étroit pour gérer les services (100 colonnes min.)", true)
		return m, m.clearStatusIn(4 * time.Second)
	}
	m.previewCompose = false
	m.svcFocus, m.svcStack, m.svcCursor = true, s.Name, 0
	m.svcSel, m.svcFilter, m.svcFiltering, m.svcDeps = make(map[string]bool), "", false, false
	return m, nil
}

func (m Model) leaveServices() Model {
	m.svcFocus, m.svcStack, m.svcCursor = false, "", 0
	m.svcSel, m.svcFilter, m.svcFiltering, m.svcDeps = nil, "", false, false
	return m
}

// visibleServices applique le filtre du mode services (sous-chaîne, sans
// casse). Le curseur et les cibles d'action ne portent que sur ces lignes.
func (m Model) visibleServices(all []compose.ServiceStatus) []compose.ServiceStatus {
	if m.svcFilter == "" {
		return all
	}
	q := strings.ToLower(m.svcFilter)
	out := make([]compose.ServiceStatus, 0, len(all))
	for _, sv := range all {
		if strings.Contains(strings.ToLower(sv.Name), q) {
			out = append(out, sv)
		}
	}
	return out
}

// targetServices : services cochés ET visibles (ordre d'affichage), sinon
// celui sous le curseur. Un service masqué par le filtre n'est jamais ciblé :
// une action sans confirmation ne doit pas toucher ce qu'on ne voit pas.
func (m Model) targetServices(vis []compose.ServiceStatus) []compose.ServiceStatus {
	var out []compose.ServiceStatus
	for _, sv := range vis {
		if m.svcSel[sv.Name] {
			out = append(out, sv)
		}
	}
	if len(out) == 0 && m.svcCursor >= 0 && m.svcCursor < len(vis) {
		out = []compose.ServiceStatus{vis[m.svcCursor]}
	}
	return out
}

// scopedStack renvoie une copie de la stack restreinte aux services ciblés.
func scopedStack(s compose.Stack, targets []compose.ServiceStatus) compose.Stack {
	s.Only = make([]string, len(targets))
	for i, t := range targets {
		s.Only[i] = t.Name
	}
	return s
}

// stackLabel : « stack » ou « stack · a, b (+N) » quand l'action est ciblée.
func stackLabel(s compose.Stack) string {
	if len(s.Only) == 0 {
		return s.Name
	}
	shown := s.Only
	extra := ""
	if len(shown) > 3 {
		extra = fmt.Sprintf(" (+%d)", len(shown)-3)
		shown = shown[:3]
	}
	return s.Name + " · " + strings.Join(shown, ", ") + extra
}

// serviceKeys : actions du mode services, les mêmes lettres que dans la liste.
// s ARRÊTE (compose stop : le conteneur est conservé, le redémarrage est
// immédiat) ; d le retire (down).
var serviceKeys = map[string]string{"u": "up", "s": "stop", "d": "down", "r": "restart", "c": "recreate", "p": "pull", "l": "logs"}

func (m Model) handleServiceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	stack, ok := m.focusedStack()
	if !ok {
		return m.leaveServices(), nil // la stack a disparu (rescan)
	}
	all := m.stackServices(stack)
	vis := m.visibleServices(all)
	if m.svcCursor >= len(vis) {
		m.svcCursor = max(len(vis)-1, 0)
	}
	key := msg.String()

	if m.svcFiltering {
		switch key {
		case "esc":
			m.svcFilter, m.svcFiltering, m.svcCursor = "", false, 0
		case "enter":
			m.svcFiltering = false
		case "backspace":
			if r := []rune(m.svcFilter); len(r) > 0 {
				m.svcFilter = string(r[:len(r)-1])
				m.svcCursor = 0
			}
		case "up":
			m.svcCursor = max(m.svcCursor-1, 0)
		case "down":
			m.svcCursor = min(m.svcCursor+1, max(len(vis)-1, 0))
		case "ctrl+c":
			return m.armOrQuit()
		default:
			if t := msg.Key().Text; t != "" {
				m.svcFilter += t
				m.svcCursor = 0
			}
		}
		return m, nil
	}

	switch key {
	case "ctrl+c", "q":
		return m.armOrQuit()
	case "?":
		m.view, m.helpScroll = viewHelp, 0
	case "/":
		m.svcFiltering = true
	case "esc":
		switch {
		case m.svcFilter != "":
			m.svcFilter, m.svcCursor = "", 0
		case len(m.svcSel) > 0:
			m.svcSel = make(map[string]bool)
		default:
			return m.leaveServices(), nil
		}
	case "left":
		// Sortie : ← (ou esc). h et tab n'y servent plus : tab bascule
		// l'affichage ailleurs, h n'avait pas de pendant pour entrer.
		return m.leaveServices(), nil
	case "up", "k":
		m.svcCursor = max(m.svcCursor-1, 0)
	case "down", "j":
		m.svcCursor = min(m.svcCursor+1, max(len(vis)-1, 0))
	case "pgup":
		m.svcCursor = max(m.svcCursor-10, 0)
	case "pgdown":
		m.svcCursor = min(m.svcCursor+10, max(len(vis)-1, 0))
	case "home", "g":
		m.svcCursor = 0
	case "end", "G":
		m.svcCursor = max(len(vis)-1, 0)
	case " ", "space":
		if len(vis) > 0 {
			n := vis[m.svcCursor].Name
			if m.svcSel[n] {
				delete(m.svcSel, n)
			} else {
				m.svcSel[n] = true
			}
			m.svcCursor = min(m.svcCursor+1, len(vis)-1)
		}
	case "ctrl+a":
		// Tout cocher (parmi les visibles), ou tout décocher si tout l'est déjà.
		allChecked := len(vis) > 0
		for _, sv := range vis {
			allChecked = allChecked && m.svcSel[sv.Name]
		}
		for _, sv := range vis {
			if allChecked {
				delete(m.svcSel, sv.Name)
			} else {
				m.svcSel[sv.Name] = true
			}
		}
	case "e":
		// Coche les services en défaut (pas « en marche » sain) : arrêtés,
		// partiels, unhealthy, non créés. Remplace la sélection.
		sel := make(map[string]bool)
		for _, sv := range vis {
			if sv.State() != compose.StateRunning {
				sel[sv.Name] = true
			}
		}
		if len(sel) == 0 {
			m.setStatus("Aucun service en défaut", false)
			return m, m.clearStatusIn(3 * time.Second)
		}
		m.svcSel = sel
	case "+":
		// Inclure les dépendances (depends_on) pour up/recreate/pull.
		m.svcDeps = !m.svcDeps
	case "u", "s", "d", "r", "c", "p", "l":
		if len(vis) == 0 {
			return m, nil
		}
		return m.serviceAction(serviceKeys[key], stack, m.targetServices(vis))
	}
	return m, nil
}

// serviceAction applique la règle du reste de l'app : ce qui perturbe un
// service se confirme (stop/down/restart/recreate, up si un service ciblé
// tourne déjà) ; pull et logs sont directs. Le focus et la sélection sont
// conservés : on retrouve ses services cochés après la vue de progression.
func (m Model) serviceAction(action string, stack compose.Stack, targets []compose.ServiceStatus) (tea.Model, tea.Cmd) {
	scoped := scopedStack(stack, targets)
	scoped.WithDeps = m.svcDeps && (action == "up" || action == "recreate" || action == "pull")
	run := func(m Model) (tea.Model, tea.Cmd) { return m.startStackAction(action, scoped) }

	label := fmt.Sprintf("%s — %s (%d service%s)", actionTitle[action], stack.Name, len(targets), plural(len(targets)))
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	switch action {
	case "stop", "down", "restart", "recreate":
		return m.openConfirm(label, "", actionEffect(action, true), false, names, run)
	case "up":
		for _, t := range targets {
			if t.Running > 0 {
				// Un service ciblé tourne : on prévoit ce que up recréerait.
				return m.confirmUpWithPlan(label, names, []compose.Stack{scoped}, run)
			}
		}
	}
	return run(m)
}

// depWarning renvoie l'avertissement de dépendances d'un service : celles qui
// ne tournent pas, hors sélection déjà prévue.
func depWarning(sv compose.ServiceStatus, all []compose.ServiceStatus, withDeps bool) string {
	if withDeps {
		return ""
	}
	by := make(map[string]compose.ServiceStatus, len(all))
	for _, a := range all {
		by[a.Name] = a
	}
	var bad []string
	for _, d := range sv.DependsOn {
		if dep, ok := by[d]; ok && dep.Running == 0 {
			state := "arrêté"
			if dep.Total == 0 {
				state = "non créé"
			}
			bad = append(bad, d+" ("+state+")")
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return "⚠ dépend de " + strings.Join(bad, ", ")
}

// serviceDetail : lignes de détail du service sous le curseur, sous la liste.
func serviceDetail(sv compose.ServiceStatus, all []compose.ServiceStatus, withDeps bool, inner int, ex previewExtra) []string {
	_, st := stateStyle(sv.State())
	row := func(k, v string) string {
		return styleDim.Render(fmt.Sprintf("%-6s", k)) + " " + truncate(v, max(inner-7, 1))
	}
	out := []string{styleDim.Render(strings.Repeat("─", max(inner, 1)))}
	if sv.Image != "" {
		out = append(out, row("image", sv.Image))
	}
	if sv.Ports != "" {
		out = append(out, row("ports", sv.Ports))
	}
	if sv.Running > 0 {
		cpu, mem := usageCells(sv.IDs, ex.usage)
		out = append(out, row("conso", "CPU "+cpu+" · RAM "+mem))
		rd, wr := ioCells(sv.IDs, ex.usage)
		out = append(out, row("io", "↓ "+rd+"/s · ↑ "+wr+"/s"))
	}
	status := sv.Status
	if status == "" {
		status = "aucun conteneur"
	}
	out = append(out, styleDim.Render(fmt.Sprintf("%-6s", "état"))+" "+st.Render(truncate(status, max(inner-7, 1))))
	if w := depWarning(sv, all, withDeps); w != "" {
		out = append(out, styleYellow.Render(truncate(w, inner)), styleDim.Render(truncate("+ pour inclure les dépendances", inner)))
	}
	return out
}

// previewServices rend le volet en mode services : en-tête, filtre, fenêtre
// défilante de services autour du curseur, puis le détail du service courant.
func previewServices(m Model, s compose.Stack, inner, height int) []string {
	all := m.stackServices(s)
	svcs := m.visibleServices(all)
	dot, dotStyle := stateStyle(s.State())
	out := []string{
		dotStyle.Render(dot) + " " + styleCyan.Bold(true).Render(truncate(s.Name, inner-2)),
		dotStyle.Render(stateWord(s.State())),
	}
	pos := fmt.Sprintf("Services · %d/%d", min(m.svcCursor+1, len(svcs)), len(svcs))
	if len(svcs) != len(all) {
		pos += fmt.Sprintf(" sur %d", len(all))
	}
	if n := len(m.svcSel); n > 0 {
		pos += fmt.Sprintf(" · %d coché%s", n, plural(n))
	}
	if m.svcDeps {
		pos += " · +dépendances"
	}
	out = append(out, styleDim.Render(truncate(pos, inner)))
	switch {
	case m.svcFiltering:
		// On voit la fin de ce qu'on tape : une saisie plus large que le
		// volet débordait de l'écran.
		text := m.svcFilter
		if w, room := ansi.StringWidth(text), inner-3; w > room {
			text = "…" + ansi.TruncateLeft(text, w-room+1, "")
		}
		out = append(out, styleCyan.Render("/ ")+text+styleCyan.Render("▏"))
	case m.svcFilter != "":
		out = append(out, styleDim.Render(truncate("/ "+m.svcFilter+"  (esc : effacer)", inner)))
	default:
		out = append(out, "")
	}
	if len(svcs) == 0 {
		return append(out, styleDim.Render("(aucun service ne correspond)"))
	}

	var detail []string
	if m.svcCursor < len(svcs) {
		detail = serviceDetail(svcs[m.svcCursor], all, m.svcDeps, inner, m.previewExtraFor(s))
		// Actions : sur les services cochés ET visibles, sinon sur celui du
		// curseur — exactement ce que vise targetServices. Compter tous les
		// cochés annonçait « 3 services cochés » quand le filtre en masquait,
		// et l'action partait sur un seul (voire sur celui du curseur).
		targets := m.targetServices(svcs)
		target := "Actions sur " + svcs[m.svcCursor].Name
		if n := len(targets); n > 0 && m.svcSel[targets[0].Name] {
			target = fmt.Sprintf("Actions sur %d service%s coché%s", n, plural(n), plural(n))
			if hidden := len(m.svcSel) - n; hidden > 0 {
				target = fmt.Sprintf("Actions sur %d coché%s visible%s (%d masqué%s)", n, plural(n), plural(n), hidden, plural(hidden))
			}
		}
		detail = append(detail, "", styleDim.Render(truncate(target, inner)))
		detail = append(detail, keyGrid([]footerKey{
			{"u", "up"}, {"s", "stop"}, {"d", "down"}, {"r", "restart"}, {"c", "recreate"}, {"p", "pull"}, {"l", "logs"},
		}, inner)...)
	}
	avail := max(height-len(out)-len(detail), 1)
	start := 0
	if len(svcs) > avail {
		start = min(max(m.svcCursor-avail/2, 0), len(svcs)-avail)
	}
	nameW := 0
	for _, sv := range svcs {
		nameW = max(nameW, lipgloss.Width(sv.Name))
	}
	nameW = min(nameW, max(inner-18, 8))
	for i := start; i < len(svcs) && i < start+avail; i++ {
		sv := svcs[i]
		d, st := stateStyle(sv.State())
		name := truncate(sv.Name, nameW)
		name += strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))
		word := stateWord(sv.State())
		if i == m.svcCursor {
			// Ligne sous le curseur : texte brut dans UN seul style (les spans
			// stylés imbriqués casseraient le reverse), flèche hors zone.
			plain := d + " " + name + "  " + word
			pad := max(inner-2-lipgloss.Width(plain), 0)
			mark := " "
			if m.svcSel[sv.Name] {
				mark = styleYellow.Render("┃")
			}
			out = append(out, styleCyan.Render("▶")+mark+styleSelected.Render(plain+strings.Repeat(" ", pad)))
			continue
		}
		mark := " "
		if m.svcSel[sv.Name] {
			mark = styleYellow.Render("┃")
			name = styleBold.Render(name)
		}
		out = append(out, " "+mark+st.Render(d)+" "+name+"  "+st.Render(word))
	}
	// Le détail reste collé en bas du volet.
	for len(out) < height-len(detail) {
		out = append(out, "")
	}
	return append(out, detail...)
}
