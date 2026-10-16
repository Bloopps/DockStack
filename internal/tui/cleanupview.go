package tui

// Page « Nettoyage » (touche n) : bilan de l'espace utilisé par Docker et
// éléments supprimables, classés par risque. Ce qui est sans risque est coché
// d'office ; ce qui demande un retéléchargement ou efface des données ne
// l'est jamais. Voir docs/PLAN-NETTOYAGE.md.

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// cleanupTimeout borne l'analyse (le démon mesure les volumes).
const cleanupTimeout = 2 * time.Minute

type cleanupLoadedMsg struct {
	seq int
	rep compose.CleanupReport
	err error
}

// cleanupSections : catégories regroupées par risque, dans l'ordre affiché.
var cleanupSections = []struct {
	title string
	kinds []compose.CleanupKind
}{
	{"Sans risque", []compose.CleanupKind{compose.CleanBuildCache, compose.CleanDanglingImage, compose.CleanUnusedNetwork}},
	{"À retélécharger si besoin", []compose.CleanupKind{compose.CleanUnusedImage, compose.CleanStackImage}},
	{"Données", []compose.CleanupKind{compose.CleanOrphanVolume, compose.CleanAnonymousVolume}},
}

// cleanupKey identifie un élément dans la sélection.
func cleanupKey(it compose.CleanupItem) string {
	return fmt.Sprintf("%d:%s:%s", it.Kind, it.ID, it.Name)
}

// openCleanup ouvre la page et lance l'analyse.
func (m Model) openCleanup() (tea.Model, tea.Cmd) {
	m.view = viewCleanup
	m.cleanupCursor = 0
	m.cleanupOpen = map[compose.CleanupKind]bool{}
	return m.startCleanupAnalysis()
}

// startCleanupAnalysis (re)lance l'analyse ; un résultat d'une analyse
// précédente est écarté par cleanupSeq.
func (m Model) startCleanupAnalysis() (tea.Model, tea.Cmd) {
	if m.cleanupCancel != nil {
		m.cleanupCancel()
	}
	m.cleanupSeq++
	m.cleanupLoading, m.cleanupErr = true, nil
	if m.client == nil {
		m.cleanupLoading = false
		m.cleanupErr = fmt.Errorf("pas de connexion à Docker (R depuis la liste pour réessayer)")
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	m.cleanupCancel = cancel
	client, dir, seq := m.client, m.cfg.StackDir, m.cleanupSeq
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		defer cancel()
		rep, err := client.AnalyzeCleanup(ctx, dir)
		return cleanupLoadedMsg{seq: seq, rep: rep, err: err}
	})
}

// handleCleanupLoaded applique le résultat : sélection d'office de ce qui
// est sans risque.
func (m Model) handleCleanupLoaded(msg cleanupLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.cleanupSeq {
		return m, nil
	}
	m.cleanupLoading, m.cleanupCancel = false, nil
	if msg.err != nil {
		m.cleanupErr = msg.err
		return m, nil
	}
	rep := msg.rep
	// Après un nettoyage : espace réellement libéré, mesuré par le démon
	// (tailles totales avant et après), face à l'espace prévu, et ce qui a
	// échoué ou été annulé.
	if before := m.cleanupBefore; before != nil {
		freed := dfTotal(*before) - dfTotal(rep)
		m.cleanupResult, m.cleanupTone = cleanupResultText(m.cleanupOutcome, freed, m.cleanupPlanned, m.cleanupApprox)
		m.cleanupBefore, m.cleanupOutcome = nil, cleanupOutcome{}
	}
	m.cleanupRep = &rep
	m.cleanupSel = map[string]bool{}
	for _, it := range rep.Items {
		if it.Kind.Risk() == compose.RiskNone {
			m.cleanupSel[cleanupKey(it)] = true
		}
	}
	m.cleanupCursor = min(m.cleanupCursor, max(len(m.cleanupNav())-1, 0))
	return m, nil
}

// cleanupOutcome : échecs et annulation d'un nettoyage (cf. Model).
type cleanupOutcome struct {
	fails     int  // éléments en échec (ou catégories, faute de détail)
	cancelled bool // annulé en cours de route
}

// cleanupOutcome relève l'issue d'un nettoyage dans la progression. Un
// élément arrêté par l'annulation (« context canceled ») n'est pas un échec,
// mais dit que l'annulation a pris effet, même dans une catégorie qui a
// aussi un vrai échec (et n'est donc pas marquée annulée).
func (ps *progressState) cleanupOutcome() cleanupOutcome {
	var o cleanupOutcome
	for _, st := range ps.order {
		if st.cancelled {
			o.cancelled = true
		}
		n := 0
		for _, r := range st.byID {
			switch {
			case r.state != "error":
			case canceledText(r.status):
				o.cancelled = true
			default:
				n++
			}
		}
		if n == 0 && failureReason(st) != "" {
			n = 1 // échec de la catégorie entière, sans élément en cause
		}
		o.fails += n
	}
	return o
}

// tone : couleur d'un bilan.
type tone int

const (
	toneOK   tone = iota // vert : réussite
	toneWarn             // jaune : échec partiel ou annulation
	toneErr              // rouge : rien n'a abouti
)

func (t tone) style() lipgloss.Style {
	switch t {
	case toneWarn:
		return styleYellow
	case toneErr:
		return styleRed
	}
	return styleGreen
}

// cleanupResultText : bilan affiché au retour sur la page. Il était toujours
// « ✔ Libéré » en vert, même après un refus du démon ou une annulation.
// « rien » plutôt que « — », qui se lisait « inconnu ».
func cleanupResultText(o cleanupOutcome, freed, planned int64, approx bool) (string, tone) {
	got := "rien"
	if freed > 0 {
		got = fmtCleanupSize(freed, false)
	}
	switch {
	case o.cancelled:
		s := "⊘ Annulé"
		if o.fails > 0 {
			s += fmt.Sprintf(" · %d échec%s", o.fails, plural(o.fails))
		}
		return s + " · libéré " + got, toneWarn
	case o.fails > 0 && freed > 0:
		return fmt.Sprintf("⚠ %d élément%s en échec · libéré %s", o.fails, plural(o.fails), got), toneWarn
	case o.fails > 0:
		return fmt.Sprintf("✕ Rien libéré : %d échec%s", o.fails, plural(o.fails)), toneErr
	}
	s := "✔ Libéré " + got
	if planned > 0 {
		s += " · prévu " + fmtCleanupSize(planned, approx)
	}
	return s, toneOK
}

// dfTotal : espace occupé par Docker (images, conteneurs, volumes, cache).
func dfTotal(r compose.CleanupReport) int64 {
	return r.Images.Size + r.Containers.Size + r.Volumes.Size + r.Builds.Size
}

// confirmCleanup demande confirmation avant de supprimer la sélection : le
// détail par catégorie, l'espace prévu, et un avertissement si des images
// devront être retéléchargées ou si des données seront effacées.
func (m Model) confirmCleanup() (tea.Model, tea.Cmd) {
	if m.cleanupRep == nil || m.cleanupLoading {
		return m, nil
	}
	if m.opInProgress() {
		return m.refuseOp()
	}
	sel := m.cleanupSelected()
	if len(sel) == 0 {
		m.setInfo("Rien de coché : ␣ pour cocher")
		return m, m.clearStatusIn(3 * time.Second)
	}
	planned, approx := m.cleanupSize(sel)
	var items []compose.CleanupItem
	byKind := map[compose.CleanupKind][]int{}
	var kinds []compose.CleanupKind
	var redownload int
	var volumes []string
	for _, i := range sel {
		it := m.cleanupRep.Items[i]
		items = append(items, it)
		if _, ok := byKind[it.Kind]; !ok {
			kinds = append(kinds, it.Kind)
		}
		byKind[it.Kind] = append(byKind[it.Kind], i)
		switch it.Kind.Risk() {
		case compose.RiskRedownload:
			redownload++
		case compose.RiskData:
			volumes = append(volumes, it.Name)
		}
	}
	var names []string
	for _, k := range kinds {
		idx := byKind[k]
		n := len(idx)
		if k == compose.CleanBuildCache {
			n = m.cleanupRep.Items[idx[0]].Count
		}
		size, ap := m.cleanupSize(idx)
		names = append(names, fmt.Sprintf("%s : %d · %s", k.Label(), n, fmtCleanupSize(size, ap)))
		if k.Risk() == compose.RiskData {
			for _, i := range idx {
				names = append(names, "    "+m.cleanupRep.Items[i].Name)
			}
		}
	}
	var warn []string
	if len(volumes) > 0 {
		warn = append(warn, fmt.Sprintf("Efface définitivement les données de %d volume%s.", len(volumes), plural(len(volumes))))
	}
	if redownload > 0 {
		warn = append(warn, fmt.Sprintf("%d image%s %s être retéléchargée%s si elle%s resser%s.", redownload, plural(redownload), devoir(redownload), plural(redownload), plural(redownload), map[bool]string{true: "vent", false: "t"}[redownload > 1]))
	}
	label := "Nettoyage — " + fmtCleanupSize(planned, approx)
	before := *m.cleanupRep
	ask := m.askConfirm
	if len(volumes) > 0 {
		ask = m.askConfirmDanger // des données seront effacées
	}
	return ask(label, strings.Join(warn, " "), names, func(m Model) (tea.Model, tea.Cmd) {
		ctx, cancel := context.WithCancel(context.Background())
		m.cleanupBefore, m.cleanupPlanned, m.cleanupApprox, m.cleanupResult = &before, planned, approx, ""
		next, cmd := m.startProgressOp(label, cancel, m.client.CleanupLive(ctx, items))
		nm := next.(Model)
		nm.progressReturn = viewCleanup
		return nm, cmd
	})
}

// leaveCleanup revient à la liste et abandonne une analyse en cours.
func (m *Model) leaveCleanup() {
	if m.cleanupCancel != nil {
		m.cleanupCancel()
		m.cleanupCancel = nil
	}
	m.cleanupSeq++
	m.cleanupLoading = false
	m.view = viewList
}

// cleanupRow : une ligne de la sélection.
type cleanupRow struct {
	section string              // titre de section (non navigable)
	note    string              // texte (non navigable)
	kind    compose.CleanupKind // catégorie (ligne de catégorie ou d'élément)
	item    int                 // indice dans Items, -1 pour une ligne de catégorie
	nav     bool
	isPrune bool                 // ligne de commande prune
	prune   compose.PruneCommand // commande de la ligne
	kept    bool                 // section « Conservé » (à droite en disposition large)
}

// cleanupItems renvoie les éléments d'une catégorie (indices dans Items).
func (m Model) cleanupItems(k compose.CleanupKind) []int {
	var out []int
	if m.cleanupRep == nil {
		return nil
	}
	for i, it := range m.cleanupRep.Items {
		if it.Kind == k {
			out = append(out, i)
		}
	}
	return out
}

// cleanupRows construit les lignes de la sélection.
func (m Model) cleanupRows() []cleanupRow {
	var rows []cleanupRow
	for _, sec := range cleanupSections {
		rows = append(rows, cleanupRow{section: sec.title})
		for _, k := range sec.kinds {
			items := m.cleanupItems(k)
			if len(items) == 0 {
				rows = append(rows, cleanupRow{note: k.Label() + " : aucun", kind: k, item: -1})
				continue
			}
			rows = append(rows, cleanupRow{kind: k, item: -1, nav: true})
			if m.cleanupOpen[k] && k != compose.CleanBuildCache {
				for _, i := range items {
					rows = append(rows, cleanupRow{kind: k, item: i, nav: true})
				}
			}
		}
	}
	if rep := m.cleanupRep; rep != nil {
		rows = append(rows, cleanupRow{section: "Conservé", kept: true})
		var parts []string
		if n := len(rep.KeptStackVolumes); n > 0 {
			parts = append(parts, fmt.Sprintf("%d volume%s de stacks arrêtées", n, plural(n)))
		}
		if n := len(rep.KeptUnlabeledVolumes); n > 0 {
			names := make([]string, 0, n)
			for _, v := range rep.KeptUnlabeledVolumes {
				names = append(names, v.Name)
			}
			parts = append(parts, strings.Join(names, ", ")+" (sans étiquette)")
		}
		if len(parts) == 0 {
			parts = []string{"rien"}
		}
		for _, p := range parts {
			rows = append(rows, cleanupRow{note: p, kept: true})
		}
		if n := len(rep.LoadErrors); n > 0 {
			rows = append(rows, cleanupRow{note: fmt.Sprintf("%d stack%s illisible%s", n, plural(n), plural(n)), kept: true})
		}
		rows = append(rows, cleanupRow{section: "Commandes prune"})
		for _, p := range compose.PruneCommands {
			rows = append(rows, cleanupRow{nav: true, isPrune: true, prune: p, item: -1})
		}
	}
	return rows
}

// pruneSummary : ce qu'une commande prune supprimerait d'après l'analyse.
// what décrit les objets, size l'espace (approx si estimé, unknown si le
// démon ne permet pas de le prévoir), warn ce qui dépasse la sélection
// prudente de la page.
type pruneSummary struct {
	what    string
	size    int64
	approx  bool
	unknown bool
	warn    []string
	lines   []string // détail pour la confirmation
	count   string   // décompte court, pour la ligne de la page
}

func (m Model) prunePreview(p compose.PruneCommand) pruneSummary {
	rep := m.cleanupRep
	var s pruneSummary
	var counts []string
	countOf := func(n int, one, many string) {
		u := many
		if n <= 1 {
			u = one
		}
		counts = append(counts, fmt.Sprintf("%d %s", n, u))
	}
	idsOf := func(kinds ...compose.CleanupKind) (ids []int) {
		for _, k := range kinds {
			ids = append(ids, m.cleanupItems(k)...)
		}
		return ids
	}
	for _, step := range p.Steps() {
		switch step {
		case compose.PruneImages:
			idx := idsOf(compose.CleanDanglingImage)
			size, ap := m.cleanupSize(idx)
			s.size += size
			s.approx = s.approx || ap
			s.lines = append(s.lines, fmt.Sprintf("Images pendantes : %d · %s", len(idx), fmtCleanupSize(size, ap)))
			countOf(len(idx), "image", "images")
		case compose.PruneImagesAll:
			idx := idsOf(compose.CleanDanglingImage, compose.CleanUnusedImage, compose.CleanStackImage)
			size, ap := m.cleanupSize(idx)
			s.size += size
			s.approx = s.approx || ap
			s.lines = append(s.lines, fmt.Sprintf("Images sans conteneur : %d · %s", len(idx), fmtCleanupSize(size, ap)))
			countOf(len(idx), "image", "images")
			if n := len(m.cleanupItems(compose.CleanStackImage)); n > 0 {
				s.warn = append(s.warn, fmt.Sprintf("%d image%s de stacks arrêtées %s être retéléchargée%s.", n, plural(n), devoir(n), plural(n)))
			}
		case compose.PruneBuilder:
			s.unknown = true
			s.lines = append(s.lines, "Cache de build pendant : taille connue après coup")
			counts = append(counts, "cache pendant")
		case compose.PruneBuilderAll:
			s.size += rep.Builds.Reclaimable
			n := 0
			if idx := m.cleanupItems(compose.CleanBuildCache); len(idx) > 0 {
				n = rep.Items[idx[0]].Count
			}
			s.lines = append(s.lines, fmt.Sprintf("Cache de build inutilisé : %d entrée%s · %s", n, plural(n), fmtCleanupSize(rep.Builds.Reclaimable, false)))
			countOf(n, "entrée", "entrées")
		case compose.PruneNetworks:
			n := len(m.cleanupItems(compose.CleanUnusedNetwork)) + len(rep.KeptExternalNetworks)
			s.lines = append(s.lines, fmt.Sprintf("Réseaux inutilisés : %d", n))
			countOf(n, "réseau", "réseaux")
			for _, kn := range rep.KeptExternalNetworks {
				s.warn = append(s.warn, fmt.Sprintf("Supprime aussi le réseau externe %s de %s : son up échouera tant qu'il n'est pas recréé.", kn.Name, strings.Join(kn.Stacks, ", ")))
			}
		case compose.PruneContainers:
			n := len(rep.StoppedContainers)
			s.lines = append(s.lines, fmt.Sprintf("Conteneurs arrêtés : %d", n))
			countOf(n, "conteneur", "conteneurs")
			var stacks []string
			for _, c := range rep.StoppedContainers {
				if c.Project != "" && !slices.Contains(stacks, c.Project) {
					stacks = append(stacks, c.Project)
				}
			}
			if len(stacks) > 0 {
				s.warn = append(s.warn, fmt.Sprintf("Supprime les conteneurs arrêtés de %s : il faudra les recréer (up).", strings.Join(stacks, ", ")))
			}
		case compose.PruneVolumes:
			idx := idsOf(compose.CleanAnonymousVolume)
			size, _ := m.cleanupSize(idx)
			s.size += size
			s.lines = append(s.lines, fmt.Sprintf("Volumes anonymes : %d · %s", len(idx), fmtCleanupSize(size, false)))
			countOf(len(idx), "volume", "volumes")
			if len(idx) > 0 {
				s.warn = append(s.warn, fmt.Sprintf("Efface les données de %d volume%s anonyme%s.", len(idx), plural(len(idx)), plural(len(idx))))
			}
		}
	}
	parts := make([]string, 0, len(s.lines))
	for _, l := range s.lines {
		parts = append(parts, strings.SplitN(l, " · ", 2)[0])
	}
	s.what = strings.Join(parts, " + ")
	s.count = strings.Join(counts, " + ")
	return s
}

// devoir : « devra » ou « devront ».
func devoir(n int) string {
	if n > 1 {
		return "devront"
	}
	return "devra"
}

// pruneSize : espace prévu, « ≥ » quand une part n'est pas prévisible.
func (s pruneSummary) sizeText() string {
	t := fmtCleanupSize(s.size, s.approx)
	if s.unknown {
		if s.size > 0 {
			return "≥ " + strings.TrimPrefix(t, "≈ ")
		}
		return "?"
	}
	return t
}

// confirmPrune demande confirmation avant une commande prune.
func (m Model) confirmPrune(p compose.PruneCommand) (tea.Model, tea.Cmd) {
	if m.cleanupRep == nil || m.cleanupLoading {
		return m, nil
	}
	if m.opInProgress() {
		return m.refuseOp()
	}
	s := m.prunePreview(p)
	label := p.CLI()
	if t := s.sizeText(); t != "—" {
		label += " — " + t
	}
	names := append([]string{}, s.lines...)
	before := *m.cleanupRep
	ask := m.askConfirm
	if p.Erases() {
		ask = m.askConfirmDanger
	}
	return ask(label, strings.Join(s.warn, " "), names, func(m Model) (tea.Model, tea.Cmd) {
		ctx, cancel := context.WithCancel(context.Background())
		m.cleanupBefore, m.cleanupPlanned, m.cleanupApprox, m.cleanupResult = &before, s.size, s.approx || s.unknown, ""
		next, cmd := m.startProgressOp(label, cancel, m.client.PruneLive(ctx, p))
		nm := next.(Model)
		nm.progressReturn = viewCleanup
		return nm, cmd
	})
}

// cleanupNav : indices des lignes navigables.
func (m Model) cleanupNav() []int {
	var nav []int
	for i, r := range m.cleanupRows() {
		if r.nav {
			nav = append(nav, i)
		}
	}
	return nav
}

// cleanupCurrent : ligne sous le curseur.
func (m Model) cleanupCurrent() (cleanupRow, bool) {
	rows, nav := m.cleanupRows(), m.cleanupNav()
	if m.cleanupCursor < 0 || m.cleanupCursor >= len(nav) {
		return cleanupRow{}, false
	}
	return rows[nav[m.cleanupCursor]], true
}

// cleanupSize : espace libéré par les éléments idx s'ils sont tous
// supprimés. Les images sont comptées couche par couche (exact) ;
// approx=true si une image n'a pas pu être détaillée.
func (m Model) cleanupSize(idx []int) (size int64, approx bool) {
	rep := m.cleanupRep
	var images []string
	var imagesSum int64
	for _, i := range idx {
		it := rep.Items[i]
		switch it.Kind {
		case compose.CleanDanglingImage, compose.CleanUnusedImage, compose.CleanStackImage:
			images = append(images, it.ID)
			imagesSum += max(it.Size, 0)
		default:
			size += max(it.Size, 0)
		}
	}
	if len(images) == 0 {
		return size, false
	}
	if rep.Layers == nil {
		return size + imagesSum, true
	}
	b, exact := rep.Layers.Freed(images)
	return size + b, !exact
}

// cleanupSelected : indices des éléments cochés.
func (m Model) cleanupSelected() []int {
	if m.cleanupRep == nil {
		return nil
	}
	var out []int
	for i, it := range m.cleanupRep.Items {
		if m.cleanupSel[cleanupKey(it)] {
			out = append(out, i)
		}
	}
	return out
}

func (m Model) handleCleanupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	nav := m.cleanupNav()
	switch msg.String() {
	case "esc":
		m.leaveCleanup()
		return m, nil
	case "R":
		m.cleanupResult = ""
		return m.startCleanupAnalysis()
	case "enter":
		if r, ok := m.cleanupCurrent(); ok && r.isPrune {
			return m.confirmPrune(r.prune)
		}
		return m.confirmCleanup()
	case "up", "k":
		m.cleanupCursor = max(m.cleanupCursor-1, 0)
	case "down", "j":
		m.cleanupCursor = min(m.cleanupCursor+1, max(len(nav)-1, 0))
	case "pgup":
		m.cleanupCursor = m.cleanupPage(-1)
	case "pgdown":
		m.cleanupCursor = m.cleanupPage(1)
	case "g", "home":
		m.cleanupCursor = 0
	case "G", "end":
		m.cleanupCursor = max(len(nav)-1, 0)
	case "right", "l":
		if r, ok := m.cleanupCurrent(); ok && !r.isPrune && r.item < 0 && r.kind != compose.CleanBuildCache {
			m.cleanupOpen[r.kind] = true
		}
	case "left", "h":
		if r, ok := m.cleanupCurrent(); ok && !r.isPrune && m.cleanupOpen[r.kind] {
			m.cleanupOpen[r.kind] = false
			// Le curseur remonte sur la catégorie repliée.
			rows := m.cleanupRows()
			for i, ri := range m.cleanupNav() {
				if rr := rows[ri]; rr.kind == r.kind && rr.item < 0 {
					m.cleanupCursor = i
				}
			}
		}
	case " ", "space":
		r, ok := m.cleanupCurrent()
		if !ok || m.cleanupRep == nil || r.isPrune {
			break
		}
		if r.item >= 0 {
			k := cleanupKey(m.cleanupRep.Items[r.item])
			m.cleanupSel[k] = !m.cleanupSel[k]
			break
		}
		items := m.cleanupItems(r.kind)
		all := true
		for _, i := range items {
			if !m.cleanupSel[cleanupKey(m.cleanupRep.Items[i])] {
				all = false
			}
		}
		for _, i := range items {
			m.cleanupSel[cleanupKey(m.cleanupRep.Items[i])] = !all
		}
	}
	return m, nil
}

// checkBox : [x] tout coché, [-] en partie, [ ] rien.
func (m Model) checkBox(items []int) string {
	n := 0
	for _, i := range items {
		if m.cleanupSel[cleanupKey(m.cleanupRep.Items[i])] {
			n++
		}
	}
	switch {
	case n == 0:
		return "[ ]"
	case n == len(items):
		return "[x]"
	}
	return "[-]"
}

func fmtCleanupSize(b int64, approx bool) string {
	if b <= 0 {
		return "—"
	}
	s := fmtBytes(b)
	if approx {
		s = "≈ " + s
	}
	return s
}

var cleanupUnit = map[compose.CleanupKind][2]string{
	compose.CleanBuildCache:      {"entrée", "entrées"},
	compose.CleanDanglingImage:   {"image", "images"},
	compose.CleanUnusedNetwork:   {"réseau", "réseaux"},
	compose.CleanUnusedImage:     {"image", "images"},
	compose.CleanStackImage:      {"image", "images"},
	compose.CleanOrphanVolume:    {"volume", "volumes"},
	compose.CleanAnonymousVolume: {"volume", "volumes"},
}

// renderCleanup : bilan puis sélection.
func renderCleanup(m Model, height int) string {
	w := m.width
	title := "  " + stylePanelTitle.Render("🧹 Nettoyage")
	switch {
	case m.cleanupLoading:
		return title + "\n\n  " + styleYellow.Render(m.spinner.View()) + styleDim.Render(truncate(" Analyse de l'espace utilisé par Docker…", max(w-6, 8)))
	case m.cleanupErr != nil:
		lines := []string{title, ""}
		for _, l := range wordWrap("Analyse impossible : "+m.cleanupErr.Error(), max(w-4, 20)) {
			lines = append(lines, "  "+styleRed.Render(l))
		}
		return strings.Join(append(lines, "", styleDim.Render("  R pour réessayer")), "\n")
	case m.cleanupRep == nil:
		return title
	}
	rep := m.cleanupRep
	titleLine := title + styleDim.Render("   analysé "+relativeTime(rep.At, time.Now()))
	if m.cleanupResult != "" {
		titleLine += "   " + m.cleanupTone.style().Render(m.cleanupResult)
	}
	sel := m.cleanupSelected()
	selSize, selApprox := m.cleanupSize(sel)
	selLine := "  " + styleBold.Render("Sélection") + styleDim.Render(fmt.Sprintf("  %d élément%s", len(sel), plural(len(sel)))) +
		"  " + styleGreen.Render(fmtCleanupSize(selSize, selApprox))

	// Écran large : la sélection à gauche, le bilan et ce qui est conservé
	// à droite. Sinon, le bilan au-dessus de la sélection.
	if w >= cleanupWideMin {
		leftW := w - cleanupRightW - 3
		left := append([]string{titleLine, selLine}, m.cleanupList(leftW, max(height-2, 1), false)...)
		right := append([]string{""}, m.cleanupBilan(cleanupRightW)...)
		right = append(right, "", "  "+styleDim.Render(strings.Repeat("─", cleanupRightW-4)))
		right = append(right, m.cleanupDetail(cleanupRightW)...)
		// « Conservé » en dessous, s'il reste de la place.
		var kept []string
		for _, r := range m.cleanupRows() {
			switch {
			case r.kept && r.section != "":
				kept = append(kept, "", "  "+styleYellow.Render(r.section))
			case r.kept:
				kept = append(kept, styleDim.Render(truncate("    "+r.note, cleanupRightW)))
			}
		}
		if len(right)+len(kept) <= height {
			right = append(right, kept...)
		}
		n := min(max(len(left), len(right)), max(height, 1))
		out := make([]string, n)
		sep := styleDim.Render("│")
		for i := range n {
			l, r := "", ""
			if i < len(left) {
				l = ansiTruncate(left[i], leftW)
			}
			if i < len(right) {
				r = ansiTruncate(right[i], cleanupRightW)
			}
			out[i] = l + strings.Repeat(" ", max(leftW-lipgloss.Width(l), 0)) + " " + sep + " " + r
		}
		return strings.Join(out, "\n")
	}

	// Écran étroit : le bilan, la sélection, puis une bande qui détaille la
	// ligne sous le curseur. Sans elle, sous 140 colonnes, « ! docker
	// network prune » ne disait qu'à la confirmation qu'un réseau externe
	// serait supprimé.
	l := m.cleanupNarrowLayout(height)
	rule := styleDim.Render(strings.Repeat("─", max(min(w-4, 72), 1)))
	head := []string{titleLine}
	if l.compact {
		head = append(head, m.cleanupBilanCompact(w)...)
	} else {
		head = append(head, m.cleanupBilan(w)...)
	}
	if l.sep {
		head = append(head, rule)
	}
	out := append(append(head, selLine), m.cleanupList(w, l.listH, true)...)
	if l.band {
		out = append(append(out, rule), m.cleanupBrief(w, cleanupBandLines)...)
	}
	return strings.Join(out, "\n")
}

// cleanupNarrow : disposition étroite de la page (sous cleanupWideMin
// colonnes).
type cleanupNarrow struct {
	compact bool // bilan en 2 lignes (cleanupBilanCompact)
	sep     bool // séparateur entre le bilan et la ligne Sélection
	band    bool // bande de détail (séparateur et cleanupBandLines lignes)
	listH   int  // lignes de la sélection
}

// cleanupBandLines : lignes de la bande de détail, séparateur non compris.
// Hauteur fixe : la liste ne change pas de taille en suivant le curseur.
const cleanupBandLines = 3

// cleanupNarrowLayout répartit height lignes entre le titre, le bilan, la
// sélection et la bande de détail, pour renderCleanup comme pour
// cleanupListH. Le bilan complet garde la parité avec « docker system df »
// tant que la sélection garde 8 lignes ; sinon (80x24), il passe sur 2
// lignes. Sous 3 lignes, le séparateur puis la bande cèdent leur place :
// rendre 3 lignes là où 2 tenaient faisait couper le repère « ··· N en
// dessous » par le bas de l'écran.
func (m Model) cleanupNarrowLayout(height int) cleanupNarrow {
	l := cleanupNarrow{sep: true, band: true}
	full, compact := len(m.cleanupBilan(m.width)), len(m.cleanupBilanCompact(m.width))
	rest := func() int {
		n := height - 2 // titre et ligne Sélection
		if l.compact {
			n -= compact
		} else {
			n -= full
		}
		if l.sep {
			n--
		}
		if l.band {
			n -= 1 + cleanupBandLines
		}
		return n
	}
	l.compact = rest() < 8
	if rest() < 3 {
		l.sep = false
	}
	if rest() < 3 {
		l.band = false
	}
	l.listH = max(rest(), 1)
	return l
}

// cleanupListH : hauteur de la sélection dans renderCleanup, pour le pas de
// pgup/pgdown (cf. cleanupPage).
func (m Model) cleanupListH() int {
	h := m.bodyHeight()
	switch {
	case m.cleanupRep == nil:
		return h
	case m.width >= cleanupWideMin:
		return max(h-2, 1) // titre et ligne Sélection
	}
	return m.cleanupNarrowLayout(h).listH
}

// Disposition large : à partir de cleanupWideMin colonnes, le bilan
// (cleanupRightW colonnes) passe à droite de la sélection.
const (
	cleanupWideMin = 140
	cleanupRightW  = 64
)

// ansiTruncate coupe une ligne déjà stylée à w colonnes.
func ansiTruncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// cleanupDetail : détail de la ligne sous le curseur, pour la colonne de
// droite (écran large).
func (m Model) cleanupDetail(w int) []string {
	r, ok := m.cleanupCurrent()
	if !ok {
		return nil
	}
	rep := m.cleanupRep
	inner := max(w-4, 20)
	wrap := func(text string, st func(...string) string) []string {
		var out []string
		for _, l := range wordWrap(text, inner) {
			out = append(out, "  "+st(l))
		}
		return out
	}
	dim := styleDim.Render
	row := func(name, value string) string {
		nameW := max(inner-12, 8)
		return "  " + fmt.Sprintf("%-*s", nameW, truncate(name, nameW)) + fmt.Sprintf("%10s", value)
	}
	var out []string
	switch {
	case r.isPrune:
		s := m.prunePreview(r.prune)
		out = append(out, "  "+styleBold.Render(r.prune.CLI()), "")
		for _, l := range s.lines {
			out = append(out, "  "+truncate(l, inner))
		}
		for _, t := range m.pruneTargets(r.prune) {
			out = append(out, dim("    "+truncate(t, inner-2)))
		}
		if len(s.warn) > 0 {
			out = append(out, "")
			for _, wl := range s.warn {
				out = append(out, wrap("! "+wl, styleYellow.Render)...)
			}
		}
	case r.item < 0:
		items := m.cleanupItems(r.kind)
		size, approx := m.cleanupSize(items)
		head := "  " + styleBold.Render(r.kind.Label())
		if size > 0 {
			head += dim("  " + fmtCleanupSize(size, approx))
		}
		out = append(out, head)
		// Aperçu des éléments seulement si la catégorie est repliée : dépliée,
		// la colonne de gauche les montre déjà.
		if r.kind != compose.CleanBuildCache && !m.cleanupOpen[r.kind] {
			out = append(out, "")
			for n, i := range items {
				if n == 8 {
					out = append(out, dim(fmt.Sprintf("  … et %d autre%s", len(items)-n, plural(len(items)-n))))
					break
				}
				it := rep.Items[i]
				if it.Size <= 0 {
					out = append(out, "  "+truncate(it.Name, inner))
					continue
				}
				out = append(out, row(it.Name, fmtCleanupSize(it.Size, it.Approx)))
			}
		}
	default:
		it := rep.Items[r.item]
		out = append(out, "  "+styleBold.Render(truncate(it.Name, inner)), "")
		for _, f := range itemFacts(it) {
			out = append(out, row(f[0], f[1]))
		}
		if len(it.Refs) > 1 {
			out = append(out, wrap("Étiquettes : "+strings.Join(it.Refs, ", "), dim)...)
		}
	}
	return out
}

// pruneTargets : objets nommés qu'une commande prune supprimerait
// (conteneurs arrêtés, réseaux inutilisés ou externes à une stack).
func (m Model) pruneTargets(p compose.PruneCommand) []string {
	rep := m.cleanupRep
	var out []string
	if p == compose.PruneContainers || p == compose.PruneSystem {
		for _, c := range rep.StoppedContainers {
			name := c.Name
			if c.Project != "" {
				name += " (stack " + c.Project + ")"
			}
			out = append(out, name)
		}
	}
	if p == compose.PruneNetworks || p == compose.PruneSystem {
		for _, i := range m.cleanupItems(compose.CleanUnusedNetwork) {
			out = append(out, rep.Items[i].Name)
		}
		for _, kn := range rep.KeptExternalNetworks {
			out = append(out, kn.Name+" (externe de "+strings.Join(kn.Stacks, ", ")+")")
		}
	}
	return out
}

// itemFacts : propriétés d'un élément (libellé, valeur), pour le détail.
func itemFacts(it compose.CleanupItem) [][2]string {
	var out [][2]string
	if it.ID != "" && it.ID != it.Name {
		id := it.ID
		if len(id) > 19 && strings.HasPrefix(id, "sha256:") {
			id = id[7:19]
		}
		out = append(out, [2]string{"Identifiant", id})
	}
	out = append(out, [2]string{"Libéré seul", fmtCleanupSize(it.Size, it.Approx)})
	if !it.Created.IsZero() {
		out = append(out, [2]string{"Créé", relativeTime(it.Created, time.Now())})
	}
	switch it.Kind {
	case compose.CleanStackImage:
		out = append(out, [2]string{"Stack", strings.Join(it.Stacks, ", ")})
	case compose.CleanOrphanVolume:
		out = append(out, [2]string{"Projet (stack absente)", strings.Join(it.Stacks, ", ")})
	}
	return out
}

// cleanupBrief : détail de la ligne sous le curseur en au plus h lignes,
// pour la bande de la disposition étroite. Une ligne de tête (la commande
// et ce qu'elle supprime, la catégorie ou l'élément), puis les
// avertissements d'une commande prune, qui passent avant le reste : c'est
// ce qu'il ne faut pas découvrir seulement à la confirmation.
func (m Model) cleanupBrief(w, h int) []string {
	r, ok := m.cleanupCurrent()
	if !ok || h < 1 {
		return nil
	}
	rep := m.cleanupRep
	inner := max(w-4, 20)
	var head string
	var warn, rest []string // rest : suite grisée, si la place le permet
	switch {
	case r.isPrune:
		s := m.prunePreview(r.prune)
		// Les objets eux-mêmes quand la commande ne supprime qu'eux (réseaux,
		// conteneurs) ; sinon le décompte complet, que la ligne de la liste
		// coupe souvent (docker system prune).
		what := s.what
		if len(r.prune.Steps()) > 1 {
			what = s.count
		} else if t := m.pruneTargets(r.prune); len(t) > 0 {
			what = strings.Join(t, ", ")
		}
		head = styleBold.Render(r.prune.CLI()) + styleDim.Render(" · "+what)
		warn = s.warn
	case r.item < 0:
		items := m.cleanupItems(r.kind)
		head = styleBold.Render(r.kind.Label())
		if size, approx := m.cleanupSize(items); size > 0 {
			head += styleDim.Render(" · " + fmtCleanupSize(size, approx))
		}
		// Les éléments, si la catégorie est repliée (dépliée, la liste les
		// montre déjà).
		if r.kind != compose.CleanBuildCache && !m.cleanupOpen[r.kind] {
			var names []string
			for _, i := range items {
				it := rep.Items[i]
				if it.Size > 0 {
					names = append(names, it.Name+" ("+fmtCleanupSize(it.Size, it.Approx)+")")
				} else {
					names = append(names, it.Name)
				}
			}
			rest = wordWrap(strings.Join(names, " · "), inner)
		}
	default:
		it := rep.Items[r.item]
		head = styleBold.Render(it.Name)
		var facts []string
		for _, f := range itemFacts(it) {
			facts = append(facts, f[0]+" "+f[1])
		}
		rest = wordWrap(strings.Join(facts, " · "), inner)
		if len(it.Refs) > 1 {
			rest = append(rest, wordWrap("Étiquettes : "+strings.Join(it.Refs, ", "), inner)...)
		}
	}
	out := []string{"  " + ansiTruncate(head, inner)}
	for _, l := range briefWarnings(warn, inner, h-1) {
		out = append(out, "  "+styleYellow.Render(l))
	}
	for _, l := range clipLines(rest, h-len(out), inner) {
		out = append(out, "  "+styleDim.Render(l))
	}
	return out
}

// briefWarnings : avertissements en au plus n lignes de w colonnes. Repliés
// s'ils tiennent, sinon un par ligne, coupés ; s'ils sont trop nombreux, la
// dernière ligne renvoie à la confirmation, qui les donne tous.
func briefWarnings(warn []string, w, n int) []string {
	if len(warn) == 0 || n < 1 {
		return nil
	}
	var out []string
	for _, x := range warn {
		out = append(out, wordWrap("! "+x, w)...)
	}
	if len(out) <= n {
		return out
	}
	out = nil
	for _, x := range warn {
		out = append(out, truncate("! "+x, w))
	}
	if len(out) > n {
		more := len(out) - (n - 1)
		out = append(out[:n-1], truncate(fmt.Sprintf("! +%d autres avertissements (↩ pour les lire)", more), w))
	}
	return out
}

// clipLines garde au plus n lignes de lines (de w colonnes au plus) ; si
// elle en retire, la dernière gardée finit par « … ».
func clipLines(lines []string, n, w int) []string {
	if len(lines) <= n {
		return lines
	}
	if n <= 0 {
		return nil
	}
	out := append([]string(nil), lines[:n]...)
	out[n-1] = truncate(out[n-1]+" …", w)
	return out
}

// cleanupDiskLine : occupation du disque des données Docker (rien si elle
// n'est pas mesurée).
func (m Model) cleanupDiskLine() []string {
	mt := m.metrics
	if mt.DiskTotalGB <= 0 {
		return nil
	}
	return []string{"  " + styleCyan.Render("Disque ") + styleDim.Render(truncate(mt.DiskMount, 12)) + " " +
		bar(mt.DiskPercent, 12) + fmt.Sprintf(" %d / %d Go (%d %%)", mt.DiskUsedGB, mt.DiskTotalGB, mt.DiskPercent) +
		styleDim.Render(fmt.Sprintf(" · %d Go libre%s", mt.DiskFreeGB, plural(mt.DiskFreeGB)))}
}

// cleanupImagesFree : espace libérable des images inutilisées, exact
// (couche par couche) si l'analyse a pu détailler toutes les images.
func (m Model) cleanupImagesFree() (int64, bool) {
	rep := m.cleanupRep
	if rep.Layers == nil {
		return rep.Images.Reclaimable, false
	}
	var unusedImages []string
	for _, it := range rep.Items {
		switch it.Kind {
		case compose.CleanDanglingImage, compose.CleanUnusedImage, compose.CleanStackImage:
			unusedImages = append(unusedImages, it.ID)
		}
	}
	return rep.Layers.Freed(unusedImages)
}

// cleanupBilan : disque, colonnes de « docker system df » (images en
// exact), note sur les volumes.
func (m Model) cleanupBilan(w int) []string {
	rep := m.cleanupRep
	out := m.cleanupDiskLine()
	imgFree, imgExact := m.cleanupImagesFree()
	row := func(label string, r compose.DFRow, free string) string {
		return fmt.Sprintf("  %-15s %6d %7d %10s %11s", label, r.Count, r.Active, fmtBytes(r.Size), free)
	}
	total := rep.Images.Size + rep.Containers.Size + rep.Volumes.Size + rep.Builds.Size
	totalFree := imgFree + rep.Containers.Reclaimable + rep.Volumes.Reclaimable + rep.Builds.Reclaimable
	out = append(out,
		styleDim.Render(fmt.Sprintf("  %-15s %6s %7s %10s %11s", "", "Nombre", "Actifs", "Taille", "Libérable")),
		row("Images", rep.Images, fmtCleanupSize(imgFree, !imgExact)),
		row("Conteneurs", rep.Containers, fmtCleanupSize(rep.Containers.Reclaimable, false)),
		row("Volumes", rep.Volumes, fmtCleanupSize(rep.Volumes.Reclaimable, false)+"*"),
		row("Cache de build", rep.Builds, fmtCleanupSize(rep.Builds.Reclaimable, false)),
		styleBold.Render(fmt.Sprintf("  %-15s %6s %7s %10s %11s", "Total", "", "", fmtBytes(total), fmtCleanupSize(totalFree, !imgExact))),
		styleDim.Render(truncate("  * volumes des stacks arrêtées inclus", max(w, 1))),
	)
	return out
}

// cleanupBilanCompact : le bilan en 2 lignes, quand le bilan complet ne
// laisserait pas 8 lignes à la sélection (cf. cleanupNarrowLayout) : le
// disque, puis la colonne « Libérable » de docker system df, total et par
// type, du plus gros au plus petit pour que la largeur coupe le moins utile.
// Sans la note « * » (une ligne de plus) : la section « Conservé » de la
// liste signale déjà les volumes des stacks arrêtées.
func (m Model) cleanupBilanCompact(w int) []string {
	rep := m.cleanupRep
	imgFree, imgExact := m.cleanupImagesFree()
	type part struct {
		label  string
		size   int64
		approx bool
	}
	parts := []part{
		{"images", imgFree, !imgExact},
		{"conteneurs", rep.Containers.Reclaimable, false},
		{"volumes", rep.Volumes.Reclaimable, false},
		{"cache", rep.Builds.Reclaimable, false},
	}
	slices.SortStableFunc(parts, func(a, b part) int { return cmp.Compare(b.size, a.size) })
	var total int64
	var txt []string
	for _, p := range parts {
		if p.size > 0 {
			total += p.size
			txt = append(txt, p.label+" "+fmtCleanupSize(p.size, p.approx))
		}
	}
	line := "  " + styleCyan.Render("Libérable ") + styleBold.Render(fmtCleanupSize(total, !imgExact))
	if len(txt) > 0 {
		line += styleDim.Render(" : " + strings.Join(txt, " · "))
	}
	return append(m.cleanupDiskLine(), ansiTruncate(line, max(w, 1)))
}

// Colonnes de la sélection : préfixe, libellé, décompte, taille. Toutes les
// lignes (catégories, éléments, commandes prune) finissent leur taille sur
// la même colonne.
const (
	cleanupCountW = 13
	cleanupSizeW  = 10
)

// cleanupList : lignes de la sélection pour une colonne de largeur colW,
// fenêtrées sur h lignes autour du curseur, avec « ··· N au-dessus / en
// dessous » quand tout ne tient pas. withKept : inclure la section
// « Conservé » (disposition étroite). Le titre de la section du curseur
// reste en tête quand la fenêtre l'a fait sortir par le haut : la page est
// classée par risque, et « Volumes orphelins » sans « Données » au-dessus ne
// disait plus qu'il efface des données.
func (m Model) cleanupList(colW, h int, withKept bool) []string {
	rep := m.cleanupRep
	rows := m.cleanupRows()
	nav := m.cleanupNav()
	cur := -1
	if m.cleanupCursor < len(nav) {
		cur = nav[m.cleanupCursor]
	}
	// Libellé : ce qui reste après préfixe (10), décompte, espace, taille et
	// la date d'un élément déplié (« il y a 137 j », 14 avec la marge).
	labelW := min(max(colW-10-cleanupCountW-1-cleanupSizeW-2-14, 12), 56)
	var lines []string
	curLine, secLine := -1, -1
	if cur >= 0 {
		lineOf, secOf, _ := m.cleanupLines(withKept)
		curLine, secLine = lineOf[cur], secOf[cur]
	}
	for i, r := range rows {
		if r.kept && !withKept {
			continue
		}
		switch {
		case r.section != "":
			lines = append(lines, "  "+styleYellow.Render(r.section))
		case !r.nav:
			lines = append(lines, styleDim.Render(truncate("      "+r.note, max(colW, 1))))
		case r.isPrune:
			s := m.prunePreview(r.prune)
			flag := " "
			if len(s.warn) > 0 {
				flag = "!"
			}
			// La commande et son décompte occupent libellé et décompte.
			mid := fmt.Sprintf("%-24s %s", r.prune.CLI(), s.count)
			span := labelW + 4 + cleanupCountW
			plain := fmt.Sprintf("    %s %-*s %*s", flag, span, truncate(mid, span), cleanupSizeW, s.sizeText())
			lines = append(lines, cleanupLine(plain, i == cur, colW))
		case r.item < 0:
			items := m.cleanupItems(r.kind)
			size, approx := m.cleanupSize(items)
			n := len(items)
			if r.kind == compose.CleanBuildCache {
				n = rep.Items[items[0]].Count
			}
			unit := cleanupUnit[r.kind][1]
			if n == 1 {
				unit = cleanupUnit[r.kind][0]
			}
			arrow := " "
			if r.kind != compose.CleanBuildCache {
				arrow = "▸"
				if m.cleanupOpen[r.kind] {
					arrow = "▾"
				}
			}
			plain := fmt.Sprintf("    %s %s %-*s%*s %*s", m.checkBox(items), arrow, labelW, truncate(r.kind.Label(), labelW),
				cleanupCountW, fmt.Sprintf("%d %s", n, unit), cleanupSizeW, fmtCleanupSize(size, approx))
			lines = append(lines, cleanupLine(plain, i == cur, colW))
		default:
			it := rep.Items[r.item]
			box := "[ ]"
			if m.cleanupSel[cleanupKey(it)] {
				box = "[x]"
			}
			extra := ""
			switch {
			case it.Kind == compose.CleanStackImage:
				extra = "stack " + strings.Join(it.Stacks, ", ")
			case it.Kind == compose.CleanOrphanVolume:
				extra = "projet " + strings.Join(it.Stacks, ", ")
			case !it.Created.IsZero():
				extra = relativeTime(it.Created, time.Now())
			}
			plain := fmt.Sprintf("        %s %-*s%*s %*s  %s", box, labelW-2, truncate(it.Name, labelW-2),
				cleanupCountW, "", cleanupSizeW, fmtCleanupSize(it.Size, it.Approx), extra)
			lines = append(lines, cleanupLine(plain, i == cur, colW))
		}
	}
	wh := cleanupWinH(len(lines), curLine, secLine, h)
	win := windowWithMarks(lines, curLine, wh)
	// Rétrécie, la fenêtre peut remonter d'une ligne en fin de liste et
	// montrer le titre à sa place : pas de doublon alors.
	if s, _ := windowSpan(len(lines), curLine, wh); wh == h || secLine >= s {
		return win
	}
	return append([]string{lines[secLine]}, win...)
}

// cleanupLines : disposition de cleanupList sans le rendu. Pour chaque
// ligne de cleanupRows, sa ligne dans la sélection (-1 : absente, section
// « Conservé » de la disposition large) et celle du titre de sa section
// (-1 : aucun), puis le nombre de lignes.
func (m Model) cleanupLines(withKept bool) (lineOf, secOf []int, n int) {
	rows := m.cleanupRows()
	lineOf, secOf = make([]int, len(rows)), make([]int, len(rows))
	sec := -1
	for i, r := range rows {
		lineOf[i], secOf[i] = -1, sec
		if r.kept && !withKept {
			continue
		}
		lineOf[i] = n
		if r.section != "" {
			sec = n
		}
		n++
	}
	return lineOf, secOf, n
}

// cleanupWinH : hauteur de la fenêtre de la sélection autour de la ligne
// cur, sur h lignes : une de moins quand le titre de sa section (ligne sec)
// est sorti par le haut et reste épinglé en tête (cf. cleanupList).
func cleanupWinH(n, cur, sec, h int) int {
	if start, _ := windowSpan(n, cur, h); sec < 0 || sec >= start || h < 4 {
		return h
	}
	return h - 1
}

// cleanupPage : position du curseur après pgup (dir -1) ou pgdown (dir 1).
// La fenêtre suit le curseur, avec ses repères, le titre de section
// épinglé, et des notes et titres intercalés : un pas fixe d'éléments
// faisait passer des éléments à supprimer entre deux écrans sans les
// montrer. Le curseur va donc aussi loin que possible tant que la nouvelle
// fenêtre reprend où l'ancienne s'arrêtait (cf. page), d'un élément au moins.
func (m Model) cleanupPage(dir int) int {
	nav := m.cleanupNav()
	if len(nav) == 0 {
		return 0
	}
	lineOf, secOf, n := m.cleanupLines(m.width < cleanupWideMin)
	h := m.cleanupListH()
	span := func(c int) (int, int) {
		cur := lineOf[nav[c]]
		return windowSpan(n, cur, cleanupWinH(n, cur, secOf[nav[c]], h))
	}
	cur := min(max(m.cleanupCursor, 0), len(nav)-1)
	start, end := span(cur)
	next := min(max(cur+dir, 0), len(nav)-1)
	for c := next + dir; c >= 0 && c < len(nav); c += dir {
		if s, e := span(c); (dir > 0 && s > end) || (dir < 0 && e < start) {
			break
		}
		next = c
	}
	return next
}

// windowWithMarks garde h lignes autour de cur et signale ce qui est
// caché (« ··· N au-dessus », « ··· N en dessous »), comme les autres listes.
func windowWithMarks(lines []string, cur, h int) []string {
	n := len(lines)
	start, end := windowSpan(n, cur, h)
	if start == 0 && end == n {
		return lines
	}
	var out []string
	if start > 0 && h >= 3 {
		out = append(out, styleDim.Render(fmt.Sprintf("    ··· %d au-dessus", start)))
	}
	out = append(out, lines[start:end]...)
	if end < n && h >= 3 {
		out = append(out, styleDim.Render(fmt.Sprintf("    ··· %d en dessous", n-end)))
	}
	return out
}

// windowSpan : lignes [start, end) d'une liste de n lignes que
// windowWithMarks montre dans h lignes autour de cur, repères compris. Sous
// 3 lignes, pas de repère (cf. markedWindow) : h lignes autour du curseur.
// Un plancher de 3 rendait 3 lignes là où 2 tenaient, et le bas de l'écran
// coupait justement le repère « ··· N en dessous ».
func windowSpan(n, cur, h int) (start, end int) {
	h = max(h, 1)
	if n <= h {
		return 0, n
	}
	cur = min(max(cur, 0), n-1)
	if h < 3 {
		start = min(max(cur-(h-1)/2, 0), n-h)
		return start, start + h
	}
	switch {
	case cur < h-1: // en tête : h-1 lignes puis le repère du bas
		return 0, h - 1
	case cur >= n-(h-1): // en fin : le repère du haut puis h-1 lignes
		return n - (h - 1), n
	}
	inner := h - 2
	start = min(max(cur-inner/2, 1), n-1-inner)
	return start, start + inner
}

// markedWindow : fenêtre de h lignes d'une liste de n lignes, à partir de la
// ligne s (bornée pour rester dans la liste). Une ligne du bord cède la place
// au repère « ··· N au-dessus » ou « ··· N en dessous » seulement s'il reste
// des lignes cachées de ce côté : réserver toujours deux lignes aux repères
// laissait une ligne vide en tête ou en fin de liste. Renvoie les lignes à
// afficher [from, to) et les nombres cachés de chaque côté (0 : pas de
// repère). Sous 3 lignes, pas de repère : il ne resterait rien à lire.
//
// Contrairement à windowWithMarks, la fenêtre suit s ligne à ligne : chaque
// pas vers le bas montre une ligne de plus en bas (défilement d'une
// confirmation, curseur centré de la liste des stacks).
func markedWindow(n, s, h int) (from, to, above, below int) {
	if n <= h {
		return 0, n, 0, 0
	}
	h = max(h, 1)
	from = min(max(s, 0), n-h)
	to = from + h
	if h < 3 {
		return from, to, 0, 0
	}
	if from > 0 {
		from++
		above = from
	}
	if to < n {
		to--
		below = n - to
	}
	return from, to, above, below
}

// cleanupLine : ligne d'élément, en texte brut sous le curseur (un seul
// style : des spans imbriqués casseraient le fond inversé).
func cleanupLine(plain string, selected bool, w int) string {
	plain = truncate(plain, max(w, 1))
	if selected {
		return styleSelected.Render(plain)
	}
	return plain
}

// cleanupFooter : pied de page de la page.
func (m Model) cleanupFooter() string {
	// Pendant l'analyse, rien à faire qu'attendre ou sortir (R relance
	// encore l'analyse, sans qu'il faille le proposer). Ensuite, ? rappelle
	// les touches de la page, plus nombreuses que le pied.
	if m.cleanupLoading {
		return renderBar(nil, []footerKey{{"esc", "retour"}}, m.width, "")
	}
	back := []footerKey{{"esc", "retour"}, {"?", "aide"}}
	if m.cleanupRep == nil {
		return renderBar([]footerKey{{"R", "réanalyser"}}, back, m.width, "")
	}
	if r, ok := m.cleanupCurrent(); ok && r.isPrune {
		return renderBar([]footerKey{{"↩", "lancer " + r.prune.CLI()}, {"R", "réanalyser"}}, back, m.width, "")
	}
	left := []footerKey{{"␣", "cocher"}}
	if n := len(m.cleanupSelected()); n > 0 {
		left = append([]footerKey{{"↩", fmt.Sprintf("nettoyer (%d)", n)}}, left...)
	}
	if r, ok := m.cleanupCurrent(); ok && r.kind != compose.CleanBuildCache {
		if m.cleanupOpen[r.kind] {
			left = append(left, footerKey{"←", "replier"})
		} else if r.item < 0 {
			left = append(left, footerKey{"→", "détail"})
		}
	}
	left = append(left, footerKey{"R", "réanalyser"})
	return renderBar(left, back, m.width, "")
}
