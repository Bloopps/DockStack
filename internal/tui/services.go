package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Mode « services » : le focus passe de la liste des stacks au volet de
// détail, où l'on coche des services et où u/d/r/c/p/l n'agissent QUE sur eux,
// sans toucher aux autres services de la stack (compose restreint au modèle
// réduit, sans dépendances : cf. compose.Stack.Only).

// svcCacheEntry mémorise les services déclarés d'un compose, invalidés par
// (mtime, taille) du fichier.
type svcCacheEntry struct {
	mtime time.Time
	size  int64
	names []string
}

// svcCacheBox : cache par chemin de compose, pointeur partagé entre les copies
// du Model (comme rowsCache). Le chargement complet du projet n'a lieu que pour
// la stack sous le curseur, une fois par version du fichier.
type svcCacheBox struct{ m map[string]svcCacheEntry }

func (c *svcCacheBox) declared(s compose.Stack) []string {
	if s.ComposeFile == "" {
		return nil
	}
	fi, err := os.Stat(s.ComposeFile)
	if err != nil {
		return nil
	}
	if e, ok := c.m[s.ComposeFile]; ok && e.mtime.Equal(fi.ModTime()) && e.size == fi.Size() {
		return e.names
	}
	names, err := compose.DeclaredServices(s)
	if err != nil {
		names = nil // .env manquant, etc. : on retombe sur les services vus par Docker
	}
	if c.m == nil {
		c.m = make(map[string]svcCacheEntry)
	}
	c.m[s.ComposeFile] = svcCacheEntry{mtime: fi.ModTime(), size: fi.Size(), names: names}
	return names
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
		for _, n := range m.svcCache.declared(s) {
			if _, ok := by[n]; !ok {
				by[n] = compose.ServiceStatus{Name: n}
			}
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
		m.setStatus("Viser une stack (pas un dossier) pour gérer ses services", false)
		return m, clearStatusIn(3 * time.Second)
	}
	if m.width < minPreviewTermWidth {
		m.setStatus("Terminal trop étroit pour gérer les services (100 colonnes min.)", true)
		return m, clearStatusIn(4 * time.Second)
	}
	m.previewOn, m.previewCompose = true, false
	m.svcFocus, m.svcStack, m.svcCursor = true, s.Name, 0
	m.svcSel = make(map[string]bool)
	return m, nil
}

func (m Model) leaveServices() Model {
	m.svcFocus, m.svcStack, m.svcCursor = false, "", 0
	m.svcSel = nil
	return m
}

// targetServices : services cochés (ordre d'affichage), sinon celui sous le
// curseur.
func (m Model) targetServices(svcs []compose.ServiceStatus) []compose.ServiceStatus {
	var out []compose.ServiceStatus
	for _, sv := range svcs {
		if m.svcSel[sv.Name] {
			out = append(out, sv)
		}
	}
	if len(out) == 0 && m.svcCursor >= 0 && m.svcCursor < len(svcs) {
		out = []compose.ServiceStatus{svcs[m.svcCursor]}
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

func (m Model) handleServiceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	stack, ok := m.focusedStack()
	if !ok {
		return m.leaveServices(), nil // la stack a disparu (rescan)
	}
	svcs := m.stackServices(stack)
	if m.svcCursor >= len(svcs) {
		m.svcCursor = max(len(svcs)-1, 0)
	}

	switch key := msg.String(); key {
	case "ctrl+c", "q":
		return m.armOrQuit()
	case "?":
		m.view = viewHelp
	case "esc":
		if len(m.svcSel) > 0 {
			m.svcSel = make(map[string]bool)
		} else {
			return m.leaveServices(), nil
		}
	case "left", "h", "tab":
		return m.leaveServices(), nil
	case "up", "k":
		if m.svcCursor > 0 {
			m.svcCursor--
		}
	case "down", "j":
		if m.svcCursor < len(svcs)-1 {
			m.svcCursor++
		}
	case "pgup":
		m.svcCursor = max(m.svcCursor-10, 0)
	case "pgdown":
		m.svcCursor = min(m.svcCursor+10, max(len(svcs)-1, 0))
	case "home", "g":
		m.svcCursor = 0
	case "end", "G":
		m.svcCursor = max(len(svcs)-1, 0)
	case " ", "space":
		if len(svcs) > 0 {
			n := svcs[m.svcCursor].Name
			if m.svcSel[n] {
				delete(m.svcSel, n)
			} else {
				m.svcSel[n] = true
			}
			if m.svcCursor < len(svcs)-1 {
				m.svcCursor++
			}
		}
	case "ctrl+a":
		// Tout cocher, ou tout décocher si tout l'est déjà.
		if len(m.svcSel) == len(svcs) {
			m.svcSel = make(map[string]bool)
		} else {
			for _, sv := range svcs {
				m.svcSel[sv.Name] = true
			}
		}
	case "u", "d", "r", "c", "p", "l":
		if len(svcs) == 0 {
			return m, nil
		}
		return m.serviceAction(map[string]string{"u": "up", "d": "down", "r": "restart", "c": "recreate", "p": "pull", "l": "logs"}[key], stack, m.targetServices(svcs))
	}
	return m, nil
}

// serviceAction applique la règle du reste de l'app : ce qui perturbe un
// service se confirme (down/restart/recreate, up si un service ciblé tourne
// déjà) ; pull et logs sont directs.
func (m Model) serviceAction(action string, stack compose.Stack, targets []compose.ServiceStatus) (tea.Model, tea.Cmd) {
	scoped := scopedStack(stack, targets)
	run := func(m Model) (tea.Model, tea.Cmd) {
		m = m.leaveServices()
		return m.startStackAction(action, scoped)
	}
	needConfirm := action == "down" || action == "restart" || action == "recreate"
	warning := ""
	if action == "up" {
		for _, t := range targets {
			if t.Running > 0 {
				needConfirm, warning = true, upWarning
				break
			}
		}
	}
	if !needConfirm {
		return run(m)
	}
	label := fmt.Sprintf("%s — %s (%d service%s)", actionTitle[action], stack.Name, len(targets), plural(len(targets)))
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return m.askConfirm(label, warning, names, run)
}

// previewServices rend le volet en mode services : en-tête, position, puis une
// fenêtre défilante de services autour du curseur.
func previewServices(m Model, s compose.Stack, inner, height int) []string {
	svcs := m.stackServices(s)
	dot, dotStyle := stateStyle(s.State())
	out := []string{
		dotStyle.Render(dot) + " " + styleCyan.Bold(true).Render(truncate(s.Name, inner-2)),
		dotStyle.Render(stateWord(s.State())),
	}
	pos := fmt.Sprintf("Services · %d/%d", min(m.svcCursor+1, len(svcs)), len(svcs))
	if n := len(m.svcSel); n > 0 {
		pos += fmt.Sprintf(" · %d coché%s", n, plural(n))
	}
	out = append(out, styleDim.Render(pos), "")
	if len(svcs) == 0 {
		return append(out, styleDim.Render("(aucun service trouvé)"))
	}

	avail := max(height-len(out), 1)
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
	return out
}
