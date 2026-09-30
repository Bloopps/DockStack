// Package theme lit les thèmes au format Ghostty (palette de 16 couleurs,
// fond, texte, sélection) et en déduit les couleurs de l'interface de
// dockstack. Le catalogue de Ghostty est embarqué ; des thèmes personnels
// peuvent s'y ajouter (cf. Catalog).
package theme

import (
	"bufio"
	"fmt"
	"image/color"
	"io"
	"strconv"
	"strings"
)

// Palette : un thème tel que décrit par un fichier Ghostty.
type Palette struct {
	Name string
	// Path : fichier d'origine pour un thème personnel, vide pour un thème
	// du catalogue embarqué.
	Path   string
	Bg, Fg color.RGBA
	// SelBg/SelFg : couleurs de sélection ; HasSel faux si le fichier ne les
	// définit pas (elles sont alors déduites).
	SelBg, SelFg color.RGBA
	HasSel       bool
	ANSI         [16]color.RGBA
}

// Light dit si le thème est clair (fond lumineux).
func (p Palette) Light() bool { return luminance(p.Bg) > 0.4 }

// xtermANSI : palette par défaut d'xterm, pour les entrées qu'un fichier
// personnel ne définirait pas.
var xtermANSI = [16]string{
	"#000000", "#cd0000", "#00cd00", "#cdcd00", "#0000ee", "#cd00cd", "#00cdcd", "#e5e5e5",
	"#7f7f7f", "#ff0000", "#00ff00", "#ffff00", "#5c5cff", "#ff00ff", "#00ffff", "#ffffff",
}

// parseHex lit « #rrggbb », « rrggbb » ou « #rgb ».
func parseHex(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return color.RGBA{}, fmt.Errorf("couleur invalide %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("couleur invalide %q", s)
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, nil
}

// builder accumule les clés d'un thème en cours de lecture.
type builder struct {
	p            Palette
	hasBg, hasFg bool
	hasSelBg     bool
	hasSelFg     bool
	hasANSI      [16]bool
}

// set applique une ligne « clé = valeur ». Les clés inconnues (police,
// curseur, options de fenêtre d'une config Ghostty complète) sont ignorées.
func (b *builder) set(key, val string) error {
	switch key {
	case "palette":
		idx, hex, ok := strings.Cut(val, "=")
		if !ok {
			return fmt.Errorf("palette invalide %q", val)
		}
		i, err := strconv.Atoi(strings.TrimSpace(idx))
		if err != nil || i < 0 || i > 255 {
			return fmt.Errorf("palette invalide %q", val)
		}
		if i > 15 {
			return nil // couleurs 16-255 : non utilisées
		}
		c, err := parseHex(hex)
		if err != nil {
			return err
		}
		b.p.ANSI[i], b.hasANSI[i] = c, true
	case "background", "foreground", "selection-background", "selection-foreground":
		c, err := parseHex(val)
		if err != nil {
			return err
		}
		switch key {
		case "background":
			b.p.Bg, b.hasBg = c, true
		case "foreground":
			b.p.Fg, b.hasFg = c, true
		case "selection-background":
			b.p.SelBg, b.hasSelBg = c, true
		default:
			b.p.SelFg, b.hasSelFg = c, true
		}
	}
	return nil
}

// finish complète les valeurs absentes. Fond et texte sont exigés : sans eux,
// impossible de garantir la lisibilité.
func (b *builder) finish() (Palette, error) {
	if !b.hasBg || !b.hasFg {
		return Palette{}, fmt.Errorf("thème %q : background et foreground sont requis", b.p.Name)
	}
	for i, ok := range b.hasANSI {
		if !ok {
			b.p.ANSI[i], _ = parseHex(xtermANSI[i])
		}
	}
	b.p.HasSel = b.hasSelBg && b.hasSelFg
	return b.p, nil
}

// splitLine découpe « clé = valeur » (espaces facultatifs autour de « = »).
// Retourne ok=false pour une ligne vide ou un commentaire.
func splitLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, val, ok = strings.Cut(line, "=")
	return strings.TrimSpace(key), strings.TrimSpace(val), ok
}

// Parse lit un fichier de thème Ghostty.
func Parse(name string, r io.Reader) (Palette, error) {
	b := builder{p: Palette{Name: name}}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		key, val, ok := splitLine(sc.Text())
		if !ok {
			continue
		}
		if err := b.set(key, val); err != nil {
			return Palette{}, fmt.Errorf("thème %q, ligne %d : %w", name, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Palette{}, err
	}
	return b.finish()
}

// parseBundle lit le catalogue embarqué : des blocs Ghostty précédés chacun
// d'une ligne « [Nom] ».
func parseBundle(data string) ([]Palette, error) {
	var out []Palette
	var cur *builder
	flush := func() error {
		if cur == nil {
			return nil
		}
		p, err := cur.finish()
		if err != nil {
			return err
		}
		out = append(out, p)
		return nil
	}
	for n, line := range strings.Split(data, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &builder{p: Palette{Name: t[1 : len(t)-1]}}
			continue
		}
		key, val, ok := splitLine(line)
		if !ok {
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("ligne %d : clé hors d'un bloc [Nom]", n+1)
		}
		if err := cur.set(key, val); err != nil {
			return nil, fmt.Errorf("thème %q : %w", cur.p.Name, err)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}
