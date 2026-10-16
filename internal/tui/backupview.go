package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
)

// captureListMinW : largeur hors-tout minimale du volet des captures (date,
// nombre de stacks et ancienneté).
const captureListMinW = 38

// captureListW : largeur hors-tout du volet des captures. Elle s'élargit pour
// montrer le plus long libellé en entier (bordure et marges 4, date et nombre
// 19, une espace), sans prendre plus des 2/5 de l'écran : à largeur fixe, il
// ne restait que 15 colonnes au libellé choisi par l'utilisateur, « avant
// mise à … » même en 180 colonnes.
func (m Model) captureListW() int {
	longest := 0
	for _, s := range m.backups {
		longest = max(longest, lipgloss.Width(s.Label))
	}
	return max(captureListMinW, min(4+19+1+longest, m.width*2/5))
}

// Statut d'une stack de la capture, comparée à l'état actuel.
type captureEntryKind int

const (
	entryRelaunch captureEntryKind = iota // existe, pas (entièrement) en marche
	entryMissing                          // absente du répertoire des stacks
	entryRunning                          // déjà en marche : Up n'y changera rien
)

type captureEntry struct {
	name     string
	kind     captureEntryKind
	stack    compose.Stack
	services []string // services capturés ; vide = toute la stack (capture ancienne)
	down     []string // parmi eux, ceux qui ne tournent pas actuellement
}

// serviceRunning dit si le service name de la stack tourne (unhealthy compris :
// il tourne, un up n'y changerait rien).
func serviceRunning(s compose.Stack, name string) bool {
	for _, sv := range s.Services {
		if sv.Name == name {
			return sv.Running > 0
		}
	}
	return false
}

// diffCapture compare une capture à l'état actuel, triée par statut (à
// relancer, introuvables, en marche) puis dans l'ordre de la capture. Une
// capture avec détail de services est jugée service par service : une stack
// dont un service capturé est arrêté est « à relancer ».
func diffCapture(items []backup.Entry, stacks []compose.Stack) []captureEntry {
	byName := make(map[string]compose.Stack, len(stacks))
	for _, s := range stacks {
		byName[s.Name] = s
	}
	entries := make([]captureEntry, 0, len(items))
	for _, it := range items {
		s, ok := byName[it.Name]
		e := captureEntry{name: it.Name, stack: s, services: it.Services, kind: entryRelaunch}
		switch {
		case !ok:
			e.kind = entryMissing
		case len(it.Services) > 0:
			for _, sv := range it.Services {
				if !serviceRunning(s, sv) {
					e.down = append(e.down, sv)
				}
			}
			if len(e.down) == 0 {
				e.kind = entryRunning
			}
		case s.State() == compose.StateRunning || s.State() == compose.StateUnhealthy:
			e.kind = entryRunning
		}
		entries = append(entries, e)
	}
	// Tri stable par statut : l'ordre de la capture est conservé à statut égal.
	out := entries[:0:0]
	for _, k := range []captureEntryKind{entryRelaunch, entryMissing, entryRunning} {
		for _, e := range entries {
			if e.kind == k {
				out = append(out, e)
			}
		}
	}
	return out
}

// alignPlan : ce qu'il faut faire pour que l'état actuel colle à une capture.
type alignPlan struct {
	relaunch     []captureEntry  // à démarrer (up)
	stopStacks   []compose.Stack // stacks en marche absentes de la capture
	stopServices []compose.Stack // stacks capturées dont des services tournent hors capture (Only posé)
}

func (p alignPlan) empty() bool {
	return len(p.relaunch) == 0 && len(p.stopStacks) == 0 && len(p.stopServices) == 0
}

// planAlign calcule l'alignement sur une capture. Les stacks introuvables ne
// sont pas relancées (rien à démarrer) ; une capture ancienne, sans détail de
// services, n'arrête aucun service isolé.
func planAlign(items []backup.Entry, stacks []compose.Stack) alignPlan {
	var p alignPlan
	inCapture := make(map[string]backup.Entry, len(items))
	for _, it := range items {
		inCapture[it.Name] = it
	}
	for _, e := range diffCapture(items, stacks) {
		if e.kind == entryRelaunch {
			p.relaunch = append(p.relaunch, e)
		}
	}
	for _, s := range stacks {
		it, captured := inCapture[s.Name]
		if !captured {
			if s.Running > 0 {
				p.stopStacks = append(p.stopStacks, s)
			}
			continue
		}
		if len(it.Services) == 0 {
			continue
		}
		want := make(map[string]bool, len(it.Services))
		for _, n := range it.Services {
			want[n] = true
		}
		var extra []string
		for _, sv := range s.Services {
			if sv.Running > 0 && !want[sv.Name] {
				extra = append(extra, sv.Name)
			}
		}
		if len(extra) > 0 {
			s.Only = extra
			p.stopServices = append(p.stopServices, s)
		}
	}
	return p
}

func renderBackupView(m Model, height int) string {
	running := 0
	for _, s := range m.stacks {
		if st := s.State(); st == compose.StateRunning || st == compose.StatePartial || st == compose.StateUnhealthy {
			running++
		}
	}

	// La feature capture la LISTE des stacks en marche, pas leurs données —
	// l'expliquer d'emblée évite le contresens « sauvegarde de volumes ».
	title := "  " + styleBold.Render("💾 Captures d'état")
	// Le rappel ne s'affiche que s'il tient : en 80 colonnes la ligne en
	// faisait 90 et le terminal la coupait.
	if note := "  ·  mémorise quelles stacks (et services) tournent, pas leurs données"; lipgloss.Width(title)+lipgloss.Width(note) <= m.width {
		title += styleDim.Render(note)
	}
	if m.captureLabeling {
		title = "  " + styleBold.Render("✎ Nom de la capture : ") + m.captureLabel + styleCyan.Render("▏") +
			styleDim.Render("   ↩ valider · esc annuler")
	}

	if len(m.backups) == 0 {
		k := styleCyan.Bold(true).Render
		return strings.Join([]string{
			title, "", "",
			"  Aucune capture pour l'instant.",
			"",
			"  " + k("c") + "  capturer l'état actuel" + styleDim.Render(fmt.Sprintf("  (%d stack%s en marche)", running, plural(running))),
			"",
			styleDim.Render("  Relancer une capture lance (Up) les stacks qu'elle contient."),
		}, "\n")
	}

	panelH := max(height-2, 5)
	listW := m.captureListW()
	left := renderCaptureList(m, listW, panelH)
	body := left
	if rightW := m.width - 2 - listW - 1; rightW >= 30 {
		snap := m.backups[min(m.actionCursor, len(m.backups)-1)]
		right := renderCaptureDetail(snap, m.stacks, m.cfg.StackDir, rightW, panelH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}
	return title + "\n\n" + lipgloss.NewStyle().PaddingLeft(2).Render(body)
}

// renderCaptureList : une ligne par capture, curseur en vidéo inversée, dans
// un volet de w colonnes hors-tout.
func renderCaptureList(m Model, w, h int) string {
	inner := w - 4
	rowsH := h - 2
	// Fenêtre glissante qui garde le curseur visible.
	off := max(0, m.actionCursor-rowsH+1)

	now := time.Now()
	var rows []string
	for i := off; i < len(m.backups) && len(rows) < rowsH; i++ {
		s := m.backups[i]
		date := s.Date.Format("02/01 15:04")
		if s.Date.Year() != now.Year() {
			date = s.Date.Format("02/01/2006")
		}
		count := fmt.Sprintf("%3d", len(s.Stacks))
		pin := " "
		if s.Pinned {
			pin = "★"
		}
		// Ligne brute d'abord : sous le curseur elle est rendue dans UN seul
		// style (des spans imbriqués casseraient le fond inversé).
		plain := fmt.Sprintf("%s%s  %s  ", pin, date, count)
		// À droite : le libellé s'il y en a un, sinon l'ancienneté.
		right, labeled := relativeTime(s.Date, now), false
		if s.Label != "" {
			right, labeled = truncate(s.Label, max(inner-lipgloss.Width(plain)-1, 4)), true
		}
		pad := max(1, inner-lipgloss.Width(plain)-lipgloss.Width(right))
		if i == m.actionCursor {
			rows = append(rows, styleSelected.Render(plain+strings.Repeat(" ", pad)+right))
			continue
		}
		rightStyle := styleDim
		if labeled {
			rightStyle = styleCyan
		}
		pinStyled := pin
		if s.Pinned {
			pinStyled = styleYellow.Render(pin)
		}
		rows = append(rows, fmt.Sprintf("%s%s  %s  ", pinStyled, date, styleBold.Render(count))+
			strings.Repeat(" ", pad)+rightStyle.Render(right))
	}
	for len(rows) < rowsH {
		rows = append(rows, "")
	}
	return styleCapturePanel.Width(w).Render(strings.Join(rows, "\n"))
}

// renderCaptureDetail : contenu complet de la capture, comparé à l'état actuel.
func renderCaptureDetail(snap backup.Snapshot, stacks []compose.Stack, stackDir string, w, h int) string {
	inner := w - 4
	rowsH := h - 2
	entries := diffCapture(snap.Items(), stacks)
	plan := planAlign(snap.Items(), stacks)
	nExtra := len(plan.stopStacks) + len(plan.stopServices)

	var nRelaunch, nMissing, nRunning int
	for _, e := range entries {
		switch e.kind {
		case entryRelaunch:
			nRelaunch++
		case entryMissing:
			nMissing++
		case entryRunning:
			nRunning++
		}
	}

	rows := []string{stylePanelTitle.Render(truncate(fmt.Sprintf("Capture du %s · %d stack%s",
		snap.Date.Format("02/01/2006 15:04"), len(snap.Stacks), plural(len(snap.Stacks))), inner))}
	if snap.Label != "" {
		rows = append(rows, styleCyan.Render(truncate("★ "+snap.Label, inner)))
	}

	var parts []string
	if nRelaunch > 0 {
		parts = append(parts, styleYellow.Render(fmt.Sprintf("%d à relancer", nRelaunch)))
	}
	if nMissing > 0 {
		parts = append(parts, styleRed.Render(fmt.Sprintf("%d introuvable%s", nMissing, plural(nMissing))))
	}
	if nRunning > 0 {
		parts = append(parts, styleGreen.Render(fmt.Sprintf("%d en marche", nRunning)))
	}
	if nExtra > 0 {
		parts = append(parts, styleOrange.Render(fmt.Sprintf("%d hors capture", nExtra)))
	}
	switch {
	case len(entries) == 0:
		rows = append(rows, styleDim.Render("(capture vide)"))
	case nRelaunch == 0 && nMissing == 0 && nExtra == 0:
		rows = append(rows, styleGreen.Render("✓ tout est déjà en marche"))
	default:
		// Replié ici, entre deux comptes, et non par le cadre : replié par
		// lipgloss, le bilan prenait une ligne que avail ignorait, et le cadre
		// trop haut perdait sa bordure basse en 80 colonnes.
		rows = append(rows, joinWrap(parts, styleDim.Render(" · "), inner)...)
	}
	// Capture prise depuis un autre répertoire : ses noms peuvent ne plus
	// correspondre aux stacks actuelles.
	if snap.StackDir != "" && snap.StackDir != stackDir {
		rows = append(rows, styleYellow.Render(truncate("⚠ capturée depuis "+snap.StackDir, inner)))
	}
	rows = append(rows, styleDim.Render(strings.Repeat("─", inner)))

	// Statut juste après le plus long nom (+2), et non au bord droit : en
	// 180 colonnes, il finissait à 120 colonnes du nom qu'il qualifie. La
	// colonne des noms cède ce qu'il faut au plus long statut.
	labels := make([]string, len(entries))
	nameW, labelW := 0, 0
	for i, e := range entries {
		switch e.kind {
		case entryMissing:
			labels[i] = "introuvable"
		case entryRunning:
			labels[i] = "en marche"
		default:
			labels[i] = "à relancer"
			if len(e.down) > 0 {
				labels[i] += " · " + truncate(strings.Join(e.down, ", "), max(inner/2-12, 6))
			}
		}
		nameW = max(nameW, lipgloss.Width(e.name))
		// Seul le mot d'état compte : la suite « · services » est coupée au
		// rendu et ne doit pas rogner les noms (deux stacks « monitoring/p… »
		// devenaient identiques).
		labelW = max(labelW, lipgloss.Width(strings.SplitN(labels[i], " · ", 2)[0]))
	}
	nameCol := max(min(nameW+2, inner-2-labelW), 3)

	avail := rowsH - len(rows)
	for i, e := range entries {
		if avail < 1 {
			break
		}
		if i == avail-1 && len(entries) > avail {
			rows = append(rows, styleDim.Render(fmt.Sprintf("… +%d autres", len(entries)-i)))
			break
		}
		dot, st := stateStyle(e.stack.State())
		if e.kind == entryMissing {
			dot, st = "✕", styleRed
		}
		name := truncate(e.name, nameCol-1)
		pad := strings.Repeat(" ", max(nameCol-lipgloss.Width(name), 1))
		rows = append(rows, st.Render(dot)+" "+name+pad+styleDim.Render(truncate(labels[i], max(inner-2-nameCol, 1))))
	}
	for len(rows) < rowsH {
		rows = append(rows, "")
	}
	return styleDetailPanel.Width(w).Render(strings.Join(rows, "\n"))
}

// joinWrap assemble des morceaux déjà stylés, séparés par sep, en lignes
// d'au plus w colonnes : on ne passe à la ligne qu'entre deux morceaux, et un
// morceau trop long à lui seul est coupé.
func joinWrap(parts []string, sep string, w int) []string {
	var lines []string
	cur := ""
	for _, p := range parts {
		switch {
		case cur == "":
			cur = p
		case lipgloss.Width(cur)+lipgloss.Width(sep)+lipgloss.Width(p) <= w:
			cur += sep + p
		default:
			lines = append(lines, ansiTruncate(cur, w))
			cur = p
		}
	}
	if cur != "" {
		lines = append(lines, ansiTruncate(cur, w))
	}
	return lines
}

func relativeTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "à l'instant"
	case d < time.Hour:
		return fmt.Sprintf("il y a %d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("il y a %d h", int(d.Hours()))
	case d < 48*time.Hour:
		return "hier"
	default:
		return fmt.Sprintf("il y a %d j", int(d.Hours()/24))
	}
}
