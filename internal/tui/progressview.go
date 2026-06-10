package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/user/dockerstack/internal/compose"
)

// ---- progress state ----
//
// L'état est replié événement par événement (O(1) par event) au lieu de
// rejouer tout l'historique à chaque frame : un pull peut émettre des
// dizaines de milliers d'événements de progression.

type resourceProgress struct {
	id      string
	status  string // texte compose ("Started", "Downloading"…)
	details string // détail compose ("[==>   ] 12MB/45MB")
	percent int
	state   string // "working" | "done" | "warning" | "error"

	firstSeen time.Time
	doneAt    time.Time // zéro tant que la ressource travaille
}

type stackProgress struct {
	name     string
	done     bool
	err      error
	roots    []*resourceProgress            // ressources de premier niveau, ordre premier-vu
	children map[string][]*resourceProgress // layers/sous-ressources par parent
	byID     map[string]*resourceProgress
}

type progressState struct {
	order  []*stackProgress // stacks dans l'ordre premier-vu
	byName map[string]*stackProgress
}

func newProgressState() *progressState {
	return &progressState{byName: make(map[string]*stackProgress)}
}

func (ps *progressState) stack(name string) *stackProgress {
	if st, ok := ps.byName[name]; ok {
		return st
	}
	st := &stackProgress{
		name:     name,
		children: make(map[string][]*resourceProgress),
		byID:     make(map[string]*resourceProgress),
	}
	ps.byName[name] = st
	ps.order = append(ps.order, st)
	return st
}

func (ps *progressState) apply(ev compose.ProgressEvent) {
	st := ps.stack(ev.StackName)
	if ev.StackDone {
		st.done = true
		if ev.Err != nil && st.err == nil {
			st.err = ev.Err
		}
		return
	}

	r, ok := st.byID[ev.Container]
	if !ok {
		r = &resourceProgress{id: ev.Container, firstSeen: time.Now()}
		st.byID[ev.Container] = r
		if ev.ParentID != "" {
			st.children[ev.ParentID] = append(st.children[ev.ParentID], r)
		} else {
			st.roots = append(st.roots, r)
		}
	}
	r.status = ev.Status
	r.details = ev.Details
	r.percent = ev.Percent
	r.state = ev.State
	if ev.State != "working" && r.doneAt.IsZero() {
		r.doneAt = time.Now()
	}
}

// ---- rendering ----

// progressBodyHeight is the number of content rows available between the header
// and the footer. The footer is always a single line in this view.
func (m Model) progressBodyHeight() int {
	bodyH := m.height - headerHeight(m.metrics.NCores, m.width) - 1 - 3 // 1 = footer, 3 = title + blank + separator
	if bodyH < 1 {
		bodyH = 1
	}
	return bodyH
}

// progressMaxScroll is the largest valid value of progressScroll (lines hidden
// above the bottom). Used to clamp scrolling so it never overshoots.
func (m Model) progressMaxScroll() int {
	_, total := m.progressLines(0, 0)
	max := total - m.progressBodyHeight()
	if max < 0 {
		max = 0
	}
	return max
}

// progressLines mime la sortie de `docker compose` : un bloc par stack avec
// compteur n/n, une ligne par ressource (préfixe complet « Container x »,
// statut, durée), les layers de pull indentés sous leur parent.
// Seules les lignes de [start, end) sont construites et stylées, les autres
// ne sont que comptées (total) : un pull groupé peut accumuler des milliers
// de ressources, toutes les styler à chaque tick du spinner (~10 fps) ferait
// du rendu le point chaud. Appeler avec (0, 0) pour ne compter que le total.
func (m Model) progressLines(start, end int) (visible []string, total int) {
	ps := m.progress
	if ps == nil {
		return nil, 0
	}
	now := time.Now()

	idx := 0
	add := func(build func() string) {
		if idx >= start && idx < end {
			visible = append(visible, build())
		}
		idx++
	}

	for _, st := range ps.order {
		// Stack header with an aggregate indicator and a docker-style counter.
		add(func() string {
			doneRoots := 0
			anyWorking := false
			for _, r := range st.roots {
				if r.state == "working" {
					anyWorking = true
				} else if r.state != "" {
					doneRoots++
				}
			}
			var hi string
			switch {
			case st.err != nil:
				hi = styleRed.Render("✕")
			case anyWorking || !st.done:
				hi = styleYellow.Render(m.spinner.View())
			default:
				hi = styleGreen.Render("✔")
			}
			header := "  " + hi + " " + styleBold.Render(st.name)
			if len(st.roots) > 0 {
				header += "  " + styleDim.Render(fmt.Sprintf("%d/%d", doneRoots, len(st.roots)))
			}
			return header
		})

		for _, r := range st.roots {
			add(func() string { return m.renderResourceLine(r, st.done, now, "      ") })
			for _, c := range st.children[r.id] {
				add(func() string { return m.renderResourceLine(c, st.done, now, "         ") })
			}
		}

		// Stack-level error (e.g. image not found), wrapped under the stack.
		if err := st.err; err != nil {
			maxErrW := m.width - 8
			if maxErrW < 20 {
				maxErrW = 20
			}
			for _, l := range wordWrap(err.Error(), maxErrW) {
				add(func() string { return "      " + styleRed.Render(l) })
			}
		}
	}

	if m.progressDone {
		add(func() string { return "" })
		add(func() string {
			summary := fmt.Sprintf("%d/%d OK", m.progressOk, m.progressTotal)
			col := styleGreen
			if m.progressOk < m.progressTotal {
				col = styleYellow
			}
			if m.progressOk == 0 {
				col = styleRed
			}
			return "  " + col.Bold(true).Render(summary)
		})
	}
	return visible, idx
}

// renderResourceLine rend une ressource comme docker compose :
// « ✔ Container uptime-kuma  Started  0.4s ». La durée court pendant
// l'exécution et se fige à l'état terminal.
func (m Model) renderResourceLine(r *resourceProgress, stackDone bool, now time.Time, indent string) string {
	end := r.doneAt
	if end.IsZero() {
		end = now
	}
	dur := styleDim.Render(fmt.Sprintf("%.1fs", end.Sub(r.firstSeen).Seconds()))

	status := r.status
	if r.details != "" {
		status += " " + r.details
	} else if r.state == "working" && r.percent > 0 {
		status += fmt.Sprintf(" %d%%", r.percent)
	}

	switch {
	case r.state == "working" && stackDone:
		// Stuck: never reached a terminal state (stack failed mid-way).
		return indent + styleDim.Render("· "+r.id+"  "+status)
	case r.state == "working":
		spin := styleYellow.Render(m.spinner.View())
		return indent + spin + " " + styleYellow.Render(r.id) + "  " + styleDim.Render(status) + " " + dur
	case r.state == "error":
		return indent + styleRed.Render("✕ "+r.id) + "  " + styleDim.Render(status) + " " + dur
	case r.state == "warning":
		return indent + styleYellow.Render("! "+r.id) + "  " + styleDim.Render(status) + " " + dur
	default:
		return indent + styleGreen.Render("✔") + " " + r.id + "  " + styleDim.Render(status) + " " + dur
	}
}

func renderProgressView(m Model) string {
	header := renderHeader(m.metrics, m.history, m.stackCounts(), m.width)
	bodyH := m.progressBodyHeight()

	scrollHint := ""
	if m.progressManualScroll {
		scrollHint = styleDim.Render("  ↑↓ scroll  g=bas")
	}
	var footer string
	switch {
	case m.progressDone:
		footer = renderFooterKeys([]struct{ key, desc string }{
			{"↩ / q", "fermer"},
		}, m.width) + scrollHint
	case m.progressCancelling:
		footer = styleYellow.PaddingLeft(2).Render("Annulation en cours…") + scrollHint
	case m.progressConfirmCancel:
		footer = styleYellow.Bold(true).PaddingLeft(2).Render("⚠ Annuler l'opération ?") +
			styleDim.Render("  esc = oui  ·  autre touche = non")
	default:
		footer = styleDim.PaddingLeft(2).Render("esc  annuler l'opération") + scrollHint
	}

	// Première passe : comptage seul (aucune ligne construite) pour placer la
	// fenêtre ; seconde passe : seules les lignes visibles sont stylées.
	_, total := m.progressLines(0, 0)

	// Scroll: progressScroll = lines above the bottom; 0 + !manual = auto-scroll to bottom
	scroll := m.progressScroll
	// Clamp: can't scroll more than (total - bodyH) lines up
	maxScroll := total - bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	start := total - bodyH - scroll
	if start < 0 {
		start = 0
	}
	end := start + bodyH
	if end > total {
		end = total
	}
	visible, _ := m.progressLines(start, end)

	// Indicator when content is hidden below the scroll window
	hiddenBelow := total - end
	var belowHint string
	if hiddenBelow > 0 {
		belowHint = "\n" + styleDim.Render(fmt.Sprintf("  ↓ %d ligne(s) cachée(s)", hiddenBelow))
	}

	title := styleBold.Render(m.progressTitle)
	sep := styleDim.Render(strings.Repeat("─", m.width-2))
	body := title + "\n" + sep + "\n" + strings.Join(visible, "\n") + belowHint

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func wordWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var lines []string
	words := strings.Fields(text)
	current := ""
	for _, w := range words {
		if current == "" {
			current = w
		} else if len(current)+1+len(w) <= width {
			current += " " + w
		} else {
			lines = append(lines, current)
			current = w
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{text}
	}
	return lines
}
