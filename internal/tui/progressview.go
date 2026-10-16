package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
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
	// queued : annoncée mais pas encore lancée (elle attend sa place). Reste
	// vrai pour une stack terminée sans avoir été lancée (annulation).
	queued bool
	// cancelled : arrêtée par l'annulation demandée (esc), ni réussie ni en
	// échec. Posé par le modèle, qui sait si l'annulation a été confirmée.
	cancelled bool
}

// launched : la stack a commencé (une stack sans annonce StackQueued, comme
// une commande prune, l'est dès son premier événement).
func (st *stackProgress) launched() bool { return !st.queued }

// running : lancée et pas encore terminée.
func (st *stackProgress) running() bool { return !st.done && !st.queued }

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
	switch {
	case ev.StackQueued:
		st.queued = true
		return
	case ev.StackStarted:
		st.queued = false
		return
	case ev.StackDone:
		st.done = true
		if ev.Err != nil && st.err == nil {
			st.err = ev.Err
		}
		return
	}
	st.queued = false // une ressource avance : la stack est lancée

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
	// Textes du démon, parfois relayés d'un registre (message d'erreur d'un
	// pull) : assainis une fois ici plutôt qu'à chaque image.
	r.status = sanitizeText(ev.Status)
	r.details = sanitizeText(ev.Details)
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
	bodyH := m.height - m.headerLines() - 1 - 3 // 1 = footer, 3 = title + blank + separator
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
	visible, total, _ = m.progressLayout(start, end)
	return visible, total
}

// progressLayout : progressLines, plus l'indice de la ligne d'en-tête de
// chaque stack (dans l'ordre de m.progress.order), relevé pendant le
// comptage : le repère du haut nomme les stacks en cours sorties de l'écran.
func (m Model) progressLayout(start, end int) (visible []string, total int, heads []int) {
	ps := m.progress
	if ps == nil {
		return nil, 0, nil
	}
	now := time.Now()

	idx := 0
	add := func(build func() string) {
		if idx >= start && idx < end {
			visible = append(visible, build())
		}
		idx++
	}

	// addResource ajoute une ressource : une seule ligne en régime normal, mais
	// en cas d'erreur le statut (message complet du démon Docker, ex. « Conflict.
	// The container name … is already in use ») est replié sous la ligne au lieu
	// de déborder hors de l'écran et d'être tronqué par le terminal.
	addResource := func(r *resourceProgress, stackDone, stackCancelled bool, indent string) {
		// Un élément arrêté par l'annulation (« context canceled » du démon, ou
		// erreur émise par compose en l'interrompant) n'est pas un échec : une
		// ligne grise « ⊘ … interrompu », sans le texte technique.
		if r.state == "error" && (stackCancelled || canceledText(r.status)) {
			add(func() string {
				return indent + styleDim.Render("⊘ "+truncate(r.id, max(m.width-lipgloss.Width(indent)-14, 8))+"  interrompu")
			})
			return
		}
		if r.state != "error" {
			add(func() string { return m.renderResourceLine(r, stackDone, now, indent) })
			return
		}
		end := r.doneAt
		if end.IsZero() {
			end = now
		}
		dur := fmt.Sprintf("%.1fs", end.Sub(r.firstSeen).Seconds())
		add(func() string {
			return indent + styleRed.Render("✕ "+r.id) + "  " + styleDim.Render(dur)
		})
		status := r.status
		if r.details != "" {
			status += " " + r.details
		}
		if status == "" {
			return
		}
		wrapW := m.width - len(indent) - 4
		if wrapW < 20 {
			wrapW = 20
		}
		for _, l := range wordWrap(status, wrapW) {
			l := l
			add(func() string { return indent + "  " + styleRed.Render(l) })
		}
	}

	for _, st := range ps.order {
		heads = append(heads, idx)
		// Stack header with an aggregate indicator and a docker-style counter.
		add(func() string {
			// En file, puis jamais lancée si l'annulation l'a devancée : en
			// gris, sans compteur (rien n'a commencé).
			switch {
			case st.queued && !st.done:
				return "  " + styleDim.Render("· "+st.name+"  en attente")
			case st.queued && st.cancelled:
				return "  " + styleDim.Render("· "+st.name+"  non lancée")
			}
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
			case st.cancelled:
				hi = styleYellow.Render("⊘")
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
			if st.cancelled {
				header += "  " + styleYellow.Render("interrompue")
			}
			return header
		})

		for _, r := range st.roots {
			addResource(r, st.done, st.cancelled, "      ")
			for _, c := range st.children[r.id] {
				addResource(c, st.done, st.cancelled, "         ")
			}
		}

		// Stack-level error (e.g. image not found), wrapped under the stack.
		// Omise quand elle répète le message d'une de ses ressources (un pull
		// émet l'erreur sur l'image puis la renvoie) : le doublon prenait
		// trois des dix lignes utiles en 80x24. La ligne de la ressource
		// reste, texte brut à copier.
		if err := st.err; err != nil && !st.cancelled && !repeatsResource(st, oneLine(err.Error())) {
			maxErrW := m.width - 8
			if maxErrW < 20 {
				maxErrW = 20
			}
			for _, l := range wordWrap(sanitizeText(err.Error()), maxErrW) {
				add(func() string { return "      " + styleRed.Render(l) })
			}
		}
		// Conseil pour les erreurs du démon les plus courantes (errhint.go).
		if hint := errHint(failureReason(st)); hint != "" {
			for _, l := range wordWrap("→ "+hint, max(m.width-8, 20)) {
				add(func() string { return "      " + styleYellow.Render(l) })
			}
		}
	}

	// Récapitulatif des échecs en fin d'opération groupée : la cause de chaque
	// stack en échec, sans avoir à remonter tout le défilement.
	if m.progressDone && len(ps.order) > 1 {
		var failed []*stackProgress
		for _, st := range ps.order {
			if failureReason(st) != "" {
				failed = append(failed, st)
			}
		}
		if len(failed) > 0 {
			add(func() string { return "" })
			add(func() string {
				return "  " + styleRed.Bold(true).Render(fmt.Sprintf("✕ %d échec%s", len(failed), plural(len(failed))))
			})
			for _, st := range failed {
				st := st
				add(func() string {
					line := "      " + styleRed.Render(st.name) + styleDim.Render("  ")
					room := max(m.width-lipgloss.Width(line)-1, 10)
					return line + styleDim.Render(truncate(daemonCause(failureReason(st)), room))
				})
			}
		}
	}

	if m.progressDone {
		add(func() string { return "" })
		add(func() string {
			summary, col := m.progressSummary(true)
			return "  " + col.Bold(true).Render(summary)
		})
	}
	return visible, idx, heads
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

	// L'identifiant (« Image lscr.io/linuxserver/… ») cède la place au reste
	// de la ligne : trop long, il la faisait dépasser l'écran.
	fixed := lipgloss.Width(indent) + 2 + 2 + lipgloss.Width(status) + 1 + lipgloss.Width(dur)
	id := truncate(r.id, max(m.width-fixed, 8))

	switch {
	case r.state == "working" && stackDone:
		// Stuck: never reached a terminal state (stack failed mid-way).
		return indent + styleDim.Render("· "+id+"  "+status)
	case r.state == "working":
		spin := styleYellow.Render(m.spinner.View())
		return indent + spin + " " + styleYellow.Render(id) + "  " + styleDim.Render(status) + " " + dur
	case r.state == "error":
		return indent + styleRed.Render("✕ "+id) + "  " + styleDim.Render(status) + " " + dur
	case r.state == "warning":
		return indent + styleYellow.Render("! "+id) + "  " + styleDim.Render(status) + " " + dur
	default:
		return indent + styleGreen.Render("✔") + " " + id + "  " + styleDim.Render(status) + " " + dur
	}
}

func renderProgressView(m Model) string {
	header := m.headerBlock()
	bodyH := m.progressBodyHeight()

	var footer string
	switch {
	case m.quitArmed:
		footer = quitWarning(m.width)
	case m.progressDone:
		// l passe avant g/G : sur un terminal étroit, c'est g/G qui cède.
		left := []footerKey{{"↑↓", "défiler"}}
		if s, ok := m.failedLogsTarget(); ok {
			left = append(left, footerKey{"l", "logs de " + truncate(s.Name, 24)})
		}
		left = append(left, footerKey{"g/G", "haut/bas"})
		footer = renderBar(left, []footerKey{{"↩", "fermer"}, {"q", "quitter"}}, m.width, "")
	case m.progressCancelling:
		footer = styleYellow.PaddingLeft(2).Render("Annulation en cours…")
	case m.progressConfirmCancel:
		footer = m.cancelQuestion()
	default:
		footer = renderBar([]footerKey{{"↑↓", "défiler"}}, []footerKey{{"esc", "annuler"}}, m.width, "")
	}

	// Première passe : comptage seul (aucune ligne construite) pour placer la
	// fenêtre ; seconde passe : seules les lignes visibles sont stylées.
	_, total, heads := m.progressLayout(0, 0)

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
	// Du contenu caché au-dessus : la première ligne de la fenêtre devient
	// un repère, symétrique de celui du bas. La fenêtre suit le bas, et une
	// stack encore en cours sortait par le haut sans laisser de trace.
	var above string
	if start > 0 {
		start++
		above = m.progressAboveHint(start, heads)
	}
	visible, _, _ := m.progressLayout(start, end)
	if above != "" {
		visible = append([]string{above}, visible...)
	}

	// Indicator when content is hidden below the scroll window
	hiddenBelow := total - end
	var belowHint string
	if hiddenBelow > 0 {
		belowHint = "\n" + styleDim.Render(fmt.Sprintf("  ↓ %d ligne%s cachée%s", hiddenBelow, plural(hiddenBelow), plural(hiddenBelow)))
	}

	title := m.progressTitleLine()
	// max(…, 0) : un WindowSizeMsg transitoire à 1 colonne rendrait le compte
	// négatif et ferait paniquer strings.Repeat (viewContent ne garde que 0).
	sep := styleDim.Render(strings.Repeat("─", max(m.width-2, 0)))
	body := title + "\n" + sep + "\n" + strings.Join(visible, "\n") + belowHint
	body = fitHeight(body, m.height-m.headerLines()-1)

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// progressAboveHint : repère des lignes cachées au-dessus de la fenêtre
// (start = première ligne affichée), avec les stacks encore en cours dont
// l'en-tête est sorti de l'écran.
func (m Model) progressAboveHint(start int, heads []int) string {
	var running []string
	for i, st := range m.progress.order {
		if i < len(heads) && heads[i] < start && st.running() {
			running = append(running, st.name)
		}
	}
	line := styleDim.Render(fmt.Sprintf("  ↑ %d ligne%s au-dessus", start, plural(start)))
	if len(running) > 0 {
		line += styleDim.Render(" · en cours : ") + styleYellow.Render(strings.Join(running, ", "))
	}
	return ansiTruncate(line, max(m.width-1, 1))
}

// cancelQuestion : la question d'annulation dit ce qu'il advient du travail
// déjà fait, qui n'est pas défait. L'explication cède la place quand la
// largeur manque, jamais la réponse attendue.
func (m Model) cancelQuestion() string {
	ask := styleYellow.Bold(true).PaddingLeft(2).Render("⚠ Annuler ?")
	keys := styleDim.Render("  esc = oui · autre touche = non")
	why := []string{" Les stacks terminées le restent, celles en cours peuvent rester partielles", " Ce qui est fait reste fait"}
	if m.progressReturn == viewCleanup {
		why[0] = " Ce qui est déjà supprimé le reste"
	}
	for _, w := range why {
		if line := ask + styleYellow.Render(w) + keys; lipgloss.Width(line) <= m.width {
			return line
		}
	}
	return ask + keys
}

// progressTitleLine : titre de l'opération suivi de son avancement (stacks
// en cours, réussies, en échec, en attente, temps écoulé), puis du bilan en
// fin d'opération. Version courte si la ligne manque de place.
func (m Model) progressTitleLine() string {
	left := styleBold.Render(m.progressTitle)
	full, short := m.progressStatus()
	room := m.width - 1 // une colonne de marge, comme le titre des logs
	lw := lipgloss.Width(left)
	for _, st := range []string{full, short} {
		if st != "" && lw+3+lipgloss.Width(st) <= room {
			return left + "   " + st
		}
	}
	return truncate(left, max(room, 1))
}

// progressStatus : partie droite du titre, en version complète et courte.
func (m Model) progressStatus() (full, short string) {
	if m.progressDone {
		summary, col := m.progressSummary(true)
		brief, _ := m.progressSummary(false)
		return col.Render(summary), col.Render(brief)
	}
	clock := ""
	if elapsed := m.progressElapsed(); elapsed >= 0 {
		clock = styleDim.Render(fmtClock(elapsed))
	}
	t := m.progress.tally()
	if t.total <= 1 {
		return clock, clock // une seule stack : son bloc dit déjà tout
	}
	var parts []string
	if t.running > 0 {
		// Le spinner porte déjà son espace (« ⣾ »).
		parts = append(parts, styleYellow.Render(fmt.Sprintf("%s%d en cours", m.spinner.View(), t.running)))
	}
	if t.ok > 0 {
		parts = append(parts, styleGreen.Render(fmt.Sprintf("✔ %d", t.ok)))
	}
	if t.failed > 0 {
		parts = append(parts, styleRed.Render(fmt.Sprintf("✕ %d", t.failed)))
	}
	if n := t.interrupted + t.notLaunched; n > 0 {
		parts = append(parts, styleYellow.Render(fmt.Sprintf("⊘ %d", n)))
	}
	if t.queued > 0 {
		parts = append(parts, styleDim.Render(fmt.Sprintf("%d en attente", t.queued)))
	}
	full = strings.Join(parts, styleDim.Render(" · "))
	short = styleDim.Render(fmt.Sprintf("%d/%d", t.total-t.running-t.queued, t.total))
	if clock != "" {
		full += "   " + clock
		short += styleDim.Render(" · ") + clock
	}
	return full, short
}

// progressSummary : bilan de fin, sous la dernière ligne et dans le titre.
// Les stacks arrêtées par l'annulation sont comptées à part : « context
// canceled » n'est pas un échec, et une stack jamais lancée n'a pas été
// touchée. long ajoute le détail (échecs, interruptions) ; la durée y est
// toujours.
func (m Model) progressSummary(long bool) (string, lipgloss.Style) {
	t := m.progress.tally()
	parts := []string{fmt.Sprintf("%d/%d OK", m.progressOk, m.progressTotal)}
	if long {
		if t.failed > 0 {
			parts = append(parts, fmt.Sprintf("%d échec%s", t.failed, plural(t.failed)))
		}
		if t.interrupted > 0 {
			parts = append(parts, fmt.Sprintf("%d interrompue%s", t.interrupted, plural(t.interrupted)))
		}
		if t.notLaunched > 0 {
			parts = append(parts, fmt.Sprintf("%d non lancée%s", t.notLaunched, plural(t.notLaunched)))
		}
	}
	if d := m.progressElapsed(); d >= 0 {
		parts = append(parts, fmtDuration(d))
	}
	text := strings.Join(parts, " · ")
	cancelled := t.interrupted+t.notLaunched > 0
	if cancelled && long {
		text = "⊘ Annulé — " + text
	}
	col := styleGreen
	switch {
	case cancelled:
		col = styleYellow
	case m.progressOk == 0:
		col = styleRed
	case m.progressOk < m.progressTotal:
		col = styleYellow
	}
	return text, col
}

// progressElapsed : durée de l'opération, figée à sa fin ; -1 si inconnue
// (état construit sans startProgressOp, comme dans les tests).
func (m Model) progressElapsed() time.Duration {
	if m.progressStart.IsZero() {
		return -1
	}
	end := m.progressEnd
	if end.IsZero() {
		end = time.Now()
	}
	return max(end.Sub(m.progressStart), 0)
}

// fmtClock : temps écoulé façon chronomètre (0:42, 12:05, 1:02:03).
func fmtClock(d time.Duration) string {
	s := int(d / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// fmtDuration : durée d'un bilan (« 42 s », « 1 min 5 s », « 1 h 3 min »).
func fmtDuration(d time.Duration) string {
	s := int(d / time.Second)
	switch {
	case s < 1:
		return "< 1 s"
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %d s", s/60, s%60)
	}
	return fmt.Sprintf("%d h %d min", s/3600, s/60%60)
}

// progressTally : stacks de l'opération comptées par état.
type progressTally struct {
	total, running, queued, ok, failed, interrupted, notLaunched int
}

func (ps *progressState) tally() progressTally {
	var t progressTally
	if ps == nil {
		return t
	}
	t.total = len(ps.order)
	for _, st := range ps.order {
		switch {
		case st.cancelled && st.launched():
			t.interrupted++
		case st.cancelled:
			t.notLaunched++
		case st.queued && !st.done:
			t.queued++
		case !st.done:
			t.running++
		case failureReason(st) != "":
			t.failed++
		default:
			t.ok++
		}
	}
	return t
}

// failedLogsTarget : la stack dont « l » ouvre les logs en fin d'opération,
// la première en échec. Pas pour un pull (ses logs ne disent rien d'une
// erreur de registre) ni pour le nettoyage (ses catégories ne sont pas des
// stacks).
func (m Model) failedLogsTarget() (compose.Stack, bool) {
	if !m.progressDone || m.progress == nil || m.progressReturn == viewCleanup || opName(m.progressTitle) == actionTitle["pull"] {
		return compose.Stack{}, false
	}
	for _, st := range m.progress.order {
		if failureReason(st) == "" {
			continue
		}
		if s, ok := m.stackNamed(st.name); ok {
			return s, true
		}
	}
	return compose.Stack{}, false
}

// stackNamed retrouve la stack d'un nom de la progression. L'alignement
// suffixe le nom d'une étape (« immich — arrêt », cf. livePhase).
func (m Model) stackNamed(name string) (compose.Stack, bool) {
	try := []string{name}
	if i := strings.LastIndex(name, " — "); i > 0 {
		try = append(try, name[:i])
	}
	for _, n := range try {
		for _, s := range m.stacks {
			if s.Name == n {
				return s, true
			}
		}
	}
	return compose.Stack{}, false
}

// opName : nom court de l'opération, tiré de son titre (« Pull — 3 stacks »,
// « Up: immich », « Relance — 2 stacks »).
func opName(title string) string {
	if i := strings.Index(title, ": "); i > 0 {
		title = title[:i]
	}
	if i := strings.Index(title, " — "); i > 0 {
		title = title[:i]
	}
	return title
}

func wordWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	// Largeurs en colonnes affichées, pas en octets : un texte accentué se
	// repliait trop tôt. Un mot plus large que la ligne (référence d'image,
	// URL d'un registre, chemin) est coupé net au lieu de déborder.
	var lines []string
	current, curW := "", 0
	for _, w := range strings.Fields(text) {
		ww := ansi.StringWidth(w)
		switch {
		case current != "" && curW+1+ww <= width:
			current += " " + w
			curW += 1 + ww
			continue
		case current != "":
			lines = append(lines, current)
		}
		for ww > width {
			head := ansi.Truncate(w, width, "")
			if head == "" { // caractère large sur une colonne : on le garde
				_, size := utf8.DecodeRuneInString(w)
				head = w[:size]
			}
			lines = append(lines, head)
			w = w[len(head):]
			ww = ansi.StringWidth(w)
		}
		current, curW = w, ww
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{text}
	}
	return lines
}

// failureReason renvoie la cause d'échec d'une stack en une ligne : erreur de
// la stack, sinon message de la première ressource en erreur. Vide si tout va
// bien, et pour une stack arrêtée par l'annulation (ce n'est pas un échec).
func failureReason(st *stackProgress) string {
	if st.cancelled {
		return ""
	}
	if st.err != nil {
		return oneLine(st.err.Error())
	}
	for _, r := range st.roots {
		if r.state == "error" {
			return oneLine(r.id + " : " + resourceErrText(r))
		}
	}
	return ""
}

// daemonCause ôte le préfixe commun des erreurs du démon : sur une ligne
// tronquée, il prenait la place de la cause.
func daemonCause(reason string) string {
	return strings.TrimPrefix(reason, "Error response from daemon: ")
}

// oneLine : texte assaini, blancs et retours à la ligne réduits à une espace.
func oneLine(s string) string { return strings.Join(strings.Fields(sanitizeText(s)), " ") }

// resourceErrText : message d'erreur d'une ressource. Un pull en échec émet
// le texte générique « Error » et met le message du démon dans le détail
// (errorEvent de compose) ; un up met le message dans le texte.
func resourceErrText(r *resourceProgress) string {
	if r.status == "Error" && r.details != "" {
		return r.details
	}
	return strings.TrimSpace(r.status + " " + r.details)
}

// repeatsResource dit si l'erreur de la stack (déjà sur une ligne) répète
// celle d'une de ses ressources. Inclusion dans un sens ou dans l'autre :
// selon le chemin, l'un des deux porte en plus le préfixe « Error response
// from daemon: ».
func repeatsResource(st *stackProgress, stackErr string) bool {
	if stackErr == "" {
		return false
	}
	for _, r := range st.byID {
		if r.state != "error" {
			continue
		}
		if t := oneLine(resourceErrText(r)); t != "" && (strings.Contains(stackErr, t) || strings.Contains(t, stackErr)) {
			return true
		}
	}
	return false
}

// isCanceled : erreur due à l'annulation du contexte. Le texte compte aussi :
// une erreur du démon ne l'emballe pas toujours.
func isCanceled(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || canceledText(err.Error()))
}

// canceledText : message d'une erreur d'annulation (statut d'une ressource).
func canceledText(s string) bool { return strings.Contains(s, "context canceled") }
