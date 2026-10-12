package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/charmbracelet/x/ansi"
)

// Consommation (CPU/RAM) des stacks affichée sur leur ligne.
//
// Sur l'hôte du démon, les compteurs sont lus dans les cgroups (≈8 ms pour
// 58 conteneurs, rien côté démon) ; sinon via l'API (≈0,6 ms par conteneur
// et ≈1,3 ms de CPU pour le démon). Dans les deux cas on ne lit que les
// conteneurs des lignes à l'écran (et d'un écran de marge de chaque côté),
// pas ceux des centaines de stacks de la liste. Le CPU est un écart
// entre deux lectures : il apparaît donc au second passage, après statsInterval.
const (
	statsFirstDelay = time.Second
	statsInterval   = 3 * time.Second
	statsTimeout    = 10 * time.Second
	// statsMaxIDs borne une lecture, quelle que soit la taille de l'écran.
	statsMaxIDs = 600
)

type statsTickMsg struct{}

type statsMsg struct {
	usage map[string]compose.ContainerUsage
	err   error
}

func tickStats(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return statsTickMsg{} })
}

func fetchUsages(client dockerService, ids []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
		defer cancel()
		u, err := client.ContainerUsages(ctx, ids)
		return statsMsg{usage: u, err: err}
	}
}

// visibleContainerIDs : conteneurs en marche des stacks proches de l'écran.
func (m Model) visibleContainerIDs() []string {
	if m.client == nil || m.view != viewList {
		return nil
	}
	rows := m.listRows()
	cursor := max(min(m.cursor, len(rows)-1), 0)
	margin := max(m.height, 10)
	from, to := max(cursor-margin, 0), min(cursor+margin+1, len(rows))
	var ids []string
	for _, r := range rows[from:to] {
		if !r.isStack() || r.stack.Running == 0 {
			continue
		}
		for _, sv := range r.stack.Services {
			ids = append(ids, sv.IDs...)
		}
		if len(ids) >= statsMaxIDs {
			return ids[:statsMaxIDs]
		}
	}
	return ids
}

// onStatsTick lance une lecture, sauf si la précédente n'est pas finie ou s'il
// n'y a rien à lire.
func (m Model) onStatsTick() (Model, tea.Cmd) {
	next := tickStats(statsInterval)
	if m.statsBusy {
		return m, next
	}
	ids := m.visibleContainerIDs()
	if len(ids) == 0 {
		return m, next
	}
	m.statsBusy = true
	return m, tea.Batch(next, fetchUsages(m.client, ids))
}

// onStatsResult remplace les mesures : celles des conteneurs qui ne sont plus
// à l'écran n'ont plus lieu d'être. Une erreur garde les dernières valeurs.
func (m Model) onStatsResult(msg statsMsg) Model {
	m.statsBusy = false
	if msg.err == nil {
		m.ctrUsage = msg.usage
	}
	return m
}

func fmtCPU(p float64) string {
	if p >= 100 {
		return fmt.Sprintf("%.0f%%", p)
	}
	return fmt.Sprintf("%.1f%%", p)
}

func fmtMem(b uint64) string { return fmtBytes(int64(b)) }

// usageSum : consommation cumulée des conteneurs d'un service ou d'une stack.
// found : au moins un conteneur mesuré ; cpuOK / ioOK : tous ont la valeur.
type usageSum struct {
	cpu    float64
	mem    uint64
	rd, wr float64 // octets/s
	found  bool
	cpuOK  bool
	ioOK   bool
}

func sumUsage(ids []string, usage map[string]compose.ContainerUsage) usageSum {
	s := usageSum{cpuOK: true, ioOK: true}
	for _, id := range ids {
		u, ok := usage[id]
		if !ok {
			continue
		}
		s.found = true
		s.mem += u.MemBytes
		s.cpu += u.CPU
		s.rd += u.ReadBps
		s.wr += u.WriteBps
		s.cpuOK = s.cpuOK && u.CPUOK
		s.ioOK = s.ioOK && u.IOOK
	}
	return s
}

// usageCells : texte des colonnes CPU et RAM d'un ensemble de conteneurs.
// « … » tant qu'une valeur manque.
func usageCells(ids []string, usage map[string]compose.ContainerUsage) (cpu, mem string) {
	s := sumUsage(ids, usage)
	switch {
	case !s.found:
		return "…", "…"
	case !s.cpuOK:
		return "…", fmtMem(s.mem)
	}
	return fmtCPU(s.cpu), fmtMem(s.mem)
}

// ioCells : débits de lecture et d'écriture disque (par seconde).
func ioCells(ids []string, usage map[string]compose.ContainerUsage) (rd, wr string) {
	s := sumUsage(ids, usage)
	if !s.found || !s.ioOK {
		return "…", "…"
	}
	return fmtIO(s.rd), fmtIO(s.wr)
}

// fmtIO : débit en octets/s. « 0 » quand rien n'a bougé, « <1 Ko » sous la
// résolution : un conteneur au repos écrit toujours un peu (journaux).
func fmtIO(bps float64) string {
	switch {
	case bps <= 0:
		return "0"
	case bps < 1<<10:
		return "<1 Ko"
	case bps < 1<<20:
		return fmt.Sprintf("%.0f Ko", bps/(1<<10))
	case bps < 1<<30:
		return fmt.Sprintf("%.1f Mo", bps/(1<<20))
	}
	return fmt.Sprintf("%.1f Go", bps/(1<<30))
}

func stackContainerIDs(s compose.Stack) []string {
	var ids []string
	for _, sv := range s.Services {
		ids = append(ids, sv.IDs...)
	}
	return ids
}

// rowUsage : colonnes CPU/RAM d'une ligne de stack. show=false pour une stack
// sans conteneur en marche : rien à mesurer.
type rowUsage struct {
	cpu, mem string
	show     bool   // la stack tourne : CPU, RAM et débits disque
	disk     string // vide : pas de colonne disque sur cette ligne
	rd, wr   string // débits de lecture / écriture disque par seconde
}

func (m Model) rowUsageFor(s compose.Stack) rowUsage {
	var ru rowUsage
	if s.Running > 0 {
		ids := stackContainerIDs(s)
		ru.cpu, ru.mem = usageCells(ids, m.ctrUsage)
		ru.rd, ru.wr = ioCells(ids, m.ctrUsage)
		ru.show = true
	}
	// Le disque se lit aussi pour une stack arrêtée : ses volumes restent.
	if s.Total > 0 {
		ru.disk = m.diskCell(s)
	}
	return ru
}

// stackDisk : espace disque de la stack vu par le démon — volumes nommés et
// couches d'écriture des conteneurs. known=false tant que la mesure (lente,
// espacée) n'a pas eu lieu. Les bind mounts ne sont pas comptés : ils vivent
// dans des dossiers du disque que le démon ne mesure pas.
func (m Model) stackDisk(s compose.Stack) (volumes, layers int64, known bool) {
	if m.usage == nil || !m.usage.VolumesOK {
		return 0, 0, false
	}
	p := strings.ToLower(s.Project)
	return m.usage.VolumesByProject[p], m.usage.LayersByProject[p], true
}

// fmtDisk : taille pour la colonne disque ; « <1 Mo » plutôt qu'un « 0 Mo »
// trompeur.
func fmtDisk(b int64) string {
	if b > 0 && b < 1<<20 {
		return "<1 Mo"
	}
	return fmtBytes(b)
}

func (m Model) diskCell(s compose.Stack) string {
	v, l, known := m.stackDisk(s)
	switch {
	case !known:
		return "…"
	case v+l == 0:
		return "—"
	}
	return fmtDisk(v + l)
}

// Géométrie des colonnes d'une ligne de stack, partagée par renderStackLine et
// listColumnHeader pour qu'elles ne se décalent jamais.
const (
	colCount = 5 // compteur « 2/2 »
	colWord  = 9 // état : « en marche », « partielle », « unhealthy »
	colCPU   = 6
	colMem   = 7
	colDisk  = 7
	colIO    = 7 // chacune des deux colonnes lecture / écriture
	colLead  = 5 // retrait, marque, pastille et espaces avant le nom
)

// listColumnHeader : en-tête des colonnes CPU / RAM / Disque, sinon on ne sait
// pas lire « 31 Mo » et « 5.0 Go » côte à côte. Vide quand les lignes ne
// portent pas ces colonnes (écran étroit, aucune stack avec conteneurs).
func (m Model) listColumnHeader(width int) string {
	has := false
	for _, s := range m.filteredStacks() {
		if s.Total > 0 {
			has = true
			break
		}
	}
	if !has {
		return ""
	}
	_ = m.listRows() // garantit nameW à jour
	base := colLead + m.rowsCache.nameW + 2 + colCount + 2 + colWord + 2
	w := base + colCPU + 1 + colMem
	if w > width {
		return ""
	}
	h := strings.Repeat(" ", base) + fmt.Sprintf("%*s %*s", colCPU, "CPU", colMem, "RAM")
	if w+1+colDisk <= width {
		w += 1 + colDisk
		h += fmt.Sprintf(" %*s", colDisk, "Disque")
		if w+2*(1+colIO) <= width {
			h += fmt.Sprintf(" %*s %*s", colIO, "Lect/s", colIO, "Écr/s")
		}
	}
	return styleDim.Render(h)
}

// previewExtra : données du volet qui ne viennent pas de la stack elle-même.
type previewExtra struct {
	volumes int64                             // octets de volumes nommés (0 : aucun ou inconnu)
	layers  int64                             // octets de couches d'écriture des conteneurs
	usage   map[string]compose.ContainerUsage // mesures par conteneur
	fail    *stackFailure                     // dernier échec de la stack (cf. lastFail)
}

func (m Model) previewExtraFor(s compose.Stack) previewExtra {
	ex := previewExtra{usage: m.ctrUsage}
	ex.volumes, ex.layers, _ = m.stackDisk(s)
	if f, ok := m.lastFail[s.Name]; ok {
		ex.fail = &f
	}
	return ex
}

// stackUsageLines : l'espace disque de la stack sous l'en-tête du volet, avec
// sa décomposition. CPU et RAM sont sur la ligne de la stack dans la liste.
// Une ligne si elle tient, sinon deux (total, puis décomposition en grisé) :
// coupée par la fin, la ligne perdait les couches de 100 à 139 colonnes.
func stackUsageLines(ex previewExtra, inner int) []string {
	total := ex.volumes + ex.layers
	if total <= 0 {
		return nil
	}
	head := styleCyan.Render("Disque") + " " + fmtDisk(total)
	var one, two string
	switch {
	case ex.volumes > 0 && ex.layers > 0:
		one = "volumes " + fmtDisk(ex.volumes) + " + couches " + fmtDisk(ex.layers)
		two = "volumes " + fmtDisk(ex.volumes) + " · couches " + fmtDisk(ex.layers)
	case ex.volumes > 0:
		one, two = "volumes", "volumes"
	default:
		one, two = "couches d'écriture", "couches d'écriture"
	}
	if line := head + styleDim.Render(" ("+one+")"); lipgloss.Width(line) <= inner {
		return []string{line}
	}
	return []string{ansi.Truncate(head, inner, "…"), styleDim.Render(truncate("  "+two, inner))}
}

// truncImage coupe une référence d'image au milieu : le nom court et le tag,
// à la fin, sont ce qu'on cherche (« ghcr.io/…/immich-server:release »), à
// défaut on garde le dernier segment (« …/immich-machine-learning:release »),
// puis la fin (« …-learning:release »). Coupée par la fin comme le reste, la
// référence perdait justement son tag.
func truncImage(ref string, w int) string {
	if w <= 0 {
		return ""
	}
	width := ansi.StringWidth(ref)
	if width <= w {
		return ref
	}
	i, j := strings.Index(ref, "/"), strings.LastIndex(ref, "/")
	if i >= 0 && j > i {
		if short := ref[:i] + "/…" + ref[j:]; ansi.StringWidth(short) <= w {
			return short
		}
	}
	if j >= 0 {
		if short := "…" + ref[j:]; ansi.StringWidth(short) <= w {
			return short
		}
	}
	if w == 1 {
		return "…"
	}
	return "…" + ansi.TruncateLeft(ref, width-(w-1), "")
}

// renderServiceBlock : les services de la stack, avec autant de détail (image,
// ports, état Docker d'un service qui ne tourne pas) que la hauteur le
// permet. Trois niveaux, du plus riche au plus sobre : une ligne par
// information, image et ports sur une seule ligne, puis rien. Si même une
// ligne par service ne tient pas, les derniers sont résumés (« … +N autres »).
func renderServiceBlock(svcs []compose.ServiceStatus, inner, avail int) []string {
	stW, nameW := 0, 0
	for _, sv := range svcs {
		stW = max(stW, lipgloss.Width(serviceStateWord(sv.State(), 1)))
		nameW = max(nameW, lipgloss.Width(sv.Name))
	}
	nameW = min(nameW, max(inner-(4+2+stW), 8))

	detail := func(sv compose.ServiceStatus, level int) []string {
		var out []string
		const ind = "    "
		switch level {
		case 3:
			if sv.Image != "" {
				out = append(out, ind+styleDim.Render("image ")+truncImage(sv.Image, max(inner-len(ind)-6, 1)))
			}
			if sv.Ports != "" {
				out = append(out, ind+styleDim.Render("ports ")+truncate(sv.Ports, max(inner-len(ind)-6, 1)))
			}
			// Code de sortie et ancienneté : « arrêté » seul ne dit pas si
			// le service a fini normalement (0), planté (1) ou été tué (137).
			// Le statut est déjà dans la liste : rien de plus à demander au
			// démon.
			if st := dockerStatusFR(sv.Status); st != "" && sv.State() != compose.StateRunning {
				out = append(out, ind+styleDim.Render("état  ")+truncate(st, max(inner-len(ind)-6, 1)))
			}
		case 2:
			text := sv.Image
			switch {
			case sv.Ports != "" && sv.Image == "":
				text = sv.Ports
			case sv.Ports != "":
				room := inner - len(ind) - lipgloss.Width(sv.Ports) - 3
				text = truncImage(sv.Image, max(room, 8)) + " · " + sv.Ports
			default:
				text = truncImage(sv.Image, max(inner-len(ind), 1))
			}
			if text != "" {
				out = append(out, ind+styleDim.Render(truncate(text, max(inner-len(ind), 1))))
			}
		}
		return out
	}
	lineCount := func(level int) int {
		n := 0
		for _, sv := range svcs {
			n += 1 + len(detail(sv, level))
		}
		return n
	}

	// Le niveau le plus riche qui tient (le titre « Services » compte 1 ligne).
	level := 1
	for _, l := range []int{3, 2, 1} {
		if lineCount(l)+1 <= avail {
			level = l
			break
		}
	}

	out := []string{styleDim.Render("Services")}
	budget := avail - 1
	hidden, hiddenWhat := 0, ""
	shown := svcs
	if level == 1 && len(svcs) > budget {
		shown = svcs[:max(budget-1, 1)]
		rest := svcs[len(shown):]
		hidden = len(rest)
		hiddenWhat = "service" + plural(hidden)
		same := true
		for _, r := range rest {
			same = same && r.State() == rest[0].State()
		}
		if same {
			hiddenWhat += " " + serviceStateWord(rest[0].State(), hidden)
		}
	}
	for _, sv := range shown {
		d, st := stateStyle(sv.State())
		name := truncate(sv.Name, nameW)
		word := serviceStateWord(sv.State(), 1)
		out = append(out, "  "+st.Render(d)+" "+name+strings.Repeat(" ", max(nameW-lipgloss.Width(name), 0))+
			"  "+st.Render(word))
		out = append(out, detail(sv, level)...)
	}
	if hidden > 0 {
		out = append(out, styleDim.Render(fmt.Sprintf("  … +%d autre%s %s", hidden, plural(hidden), hiddenWhat)))
	}
	return out
}
