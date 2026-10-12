package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Mise en page de l'écran principal sans les assemblages lipgloss.
//
// Style.Width/Height, JoinHorizontal et JoinVertical mesurent chacun la
// largeur de toutes les lignes, souvent deux fois : une ligne de la liste
// était mesurée six fois par rendu, et la mise en page coûtait plus que le
// dessin des lignes. Ici chaque ligne est mesurée une fois, puis complétée
// d'espaces à une largeur connue. Le résultat est identique octet pour octet
// à l'assemblage lipgloss (vérifié par TestScreenMatchesLipgloss).

// block : lignes d'un bloc de texte et largeur affichée de chacune.
type block struct {
	lines  []string
	widths []int
	maxW   int
}

// newBlock découpe s en lignes et les mesure, avec les mêmes conversions que
// lipgloss (tabulation = 4 espaces, fins de ligne CRLF).
func newBlock(s string) block {
	if strings.Contains(s, "\t") {
		s = strings.ReplaceAll(s, "\t", "    ")
	}
	if strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	b := block{lines: strings.Split(s, "\n")}
	b.widths = make([]int, len(b.lines))
	for i, l := range b.lines {
		b.widths[i] = ansi.StringWidth(l)
		b.maxW = max(b.maxW, b.widths[i])
	}
	return b
}

// fit complète (lignes vides) ou coupe le bloc à exactement h lignes, comme
// fitHeight. Pour h ≤ 0, fitHeight rend "", qui compte ensuite pour une
// ligne vide : écran plus bas que l'en-tête, rendu gardé tel quel.
func (b *block) fit(h int) {
	if h <= 0 {
		*b = block{lines: []string{""}, widths: []int{0}}
		return
	}
	if len(b.lines) > h {
		b.lines, b.widths = b.lines[:h], b.widths[:h]
		b.maxW = 0
		for _, w := range b.widths {
			b.maxW = max(b.maxW, w)
		}
		return
	}
	for len(b.lines) < h {
		b.lines = append(b.lines, "")
		b.widths = append(b.widths, 0)
	}
}

// prepend ajoute les lignes de top au-dessus du bloc.
func (b *block) prepend(top block) {
	b.lines = append(top.lines, b.lines...)
	b.widths = append(top.widths, b.widths...)
	b.maxW = max(b.maxW, top.maxW)
}

// spaces sert de réserve pour compléter les lignes sans allouer.
var spaces = strings.Repeat(" ", 512)

// writePad écrit n espaces.
func writePad(sb *strings.Builder, n int) {
	for n > 0 {
		k := min(n, len(spaces))
		sb.WriteString(spaces[:k])
		n -= k
	}
}

// columns place right à droite de left, lignes alignées en haut. Chaque
// colonne est complétée à sa largeur, left à au moins leftW : c'est ce que
// donnaient Style.Width(leftW).Height(h) puis JoinHorizontal(Top).
func columns(left block, leftW, h int, right block) block {
	lw := max(leftW, left.maxW)
	n := max(len(left.lines), len(right.lines), h)
	out := block{lines: make([]string, n), widths: make([]int, n), maxW: lw + right.maxW}
	var sb strings.Builder
	for i := range n {
		sb.Reset()
		sb.Grow(lw + right.maxW + 64)
		lline, lwid := "", 0
		if i < len(left.lines) {
			lline, lwid = left.lines[i], left.widths[i]
		}
		rline, rwid := "", 0
		if i < len(right.lines) {
			rline, rwid = right.lines[i], right.widths[i]
		}
		sb.WriteString(lline)
		writePad(&sb, lw-lwid)
		sb.WriteString(rline)
		writePad(&sb, right.maxW-rwid)
		out.lines[i] = sb.String()
		out.widths[i] = out.maxW
	}
	return out
}

// stackBlocks empile les blocs et complète chaque ligne à la largeur de la
// plus large, comme JoinVertical(Left).
func stackBlocks(blocks ...block) string {
	maxW, size := 0, 0
	for _, b := range blocks {
		maxW = max(maxW, b.maxW)
		for _, l := range b.lines {
			size += len(l) + 1
		}
	}
	var sb strings.Builder
	sb.Grow(size + maxW*8)
	first := true
	for _, b := range blocks {
		for i, l := range b.lines {
			if !first {
				sb.WriteByte('\n')
			}
			first = false
			sb.WriteString(l)
			writePad(&sb, maxW-b.widths[i])
		}
	}
	return sb.String()
}

// listBlock rend la liste des stacks (avec le volet de détail si visible) sur
// bodyH lignes.
func (m Model) listBlock(bodyH int) block {
	if !m.previewVisible() {
		return newBlock(renderStackList(m, bodyH, m.width))
	}
	// Liste à gauche (largeur réduite), aperçu à droite. La liste est
	// complétée à sa largeur pour que le volet s'aligne quelle que soit la
	// longueur des lignes affichées.
	paneW := m.previewWidth()
	listW := m.width - paneW
	raw := renderStackList(m, bodyH, listW)
	list := newBlock(raw)
	if list.maxW > listW {
		// Ligne trop longue : lipgloss la replie sur plusieurs lignes, ce que
		// l'assemblage direct ne sait pas faire. Cas anormal, on garde son
		// rendu exact.
		list = newBlock(lipgloss.NewStyle().Width(listW).Height(bodyH).Render(raw))
	}
	pane := newBlock(renderPreviewPane(m, bodyH, paneW))
	return columns(list, listW, bodyH, pane)
}
