package theme

import (
	"image/color"
	"math"

	"charm.land/lipgloss/v2"
)

// DefaultName : nom du thème d'origine de dockstack, qui garde les couleurs
// du terminal pour les états (vert, jaune, rouge…).
const DefaultName = "dockstack"

// Scheme : les couleurs de l'interface par rôle. Les vues n'utilisent que
// ces rôles, jamais une couleur écrite en dur.
type Scheme struct {
	Name string
	// Bg/Fg : fond et texte par défaut imposés au terminal pendant
	// l'exécution (restaurés en sortie). nil = ceux du terminal.
	Bg, Fg color.Color

	// Couleurs d'état, reprises des emplacements ANSI du thème.
	OK       color.Color // vert : en marche, succès, jauge basse
	Warn     color.Color // jaune : partiel, attention, jauge moyenne
	Danger   color.Color // rouge : arrêté, échec, jauge haute
	Degraded color.Color // orange : unhealthy
	Accent   color.Color // cyan : libellés, titres, clés YAML
	Group    color.Color // magenta : dossiers, séparateurs, booléens YAML

	// Muted : texte secondaire. nil = attribut « faint » du terminal.
	Muted    color.Color
	Inactive color.Color // stacks jamais déployées, plus discret que Muted
	Strong   color.Color // titres de panneau

	Key     color.Color // touches (pied de page, volet), flèche du curseur
	KeyBg   color.Color // fond des touches du pied de page
	KeyText color.Color // libellé des touches du pied de page
	SelBg   color.Color // bande de la ligne sous le curseur
	SelFg   color.Color

	Track       color.Color // sillon des jauges
	TrackText   color.Color // texte écrit sur le sillon
	BarText     color.Color // texte sur la partie remplie ; nil = calculé (OnFill)
	NeutralFill color.Color // remplissage d'une jauge sans alerte (Swap)

	Border      color.Color // cadres des panneaux
	BorderMuted color.Color // cadres secondaires
	Rule        color.Color // séparateur « · » du pied de page
	BlockSep    color.Color // séparateur « │ » du pied de page

	pal *Palette
}

// Palette retourne le thème Ghostty d'origine (nil pour le thème par défaut).
func (s Scheme) Palette() *Palette { return s.pal }

// Default : les couleurs historiques de dockstack. Couleurs d'état prises
// dans les 16 couleurs du terminal (elles suivent donc son thème), mises en
// évidence dans la palette 256 fixe (cf. commentaires de stacklist.go).
func Default() Scheme {
	c := lipgloss.Color
	return Scheme{
		Name:        DefaultName,
		OK:          c("2"),
		Warn:        c("3"),
		Danger:      c("1"),
		Degraded:    c("208"),
		Accent:      c("6"),
		Group:       c("5"),
		Inactive:    c("240"),
		Strong:      c("15"),
		Key:         c("117"),
		KeyBg:       c("237"),
		KeyText:     c("250"),
		SelBg:       c("24"),
		SelFg:       c("231"),
		Track:       c("238"),
		TrackText:   c("252"),
		BarText:     c("0"),
		NeutralFill: c("244"),
		Border:      c("6"),
		BorderMuted: c("8"),
		Rule:        c("238"),
		BlockSep:    c("240"),
	}
}

// Seuils de contraste (WCAG) garantis sur le fond du thème.
const (
	minText   = 4.5 // texte courant
	minColor  = 3.0 // couleurs d'état, libellés
	minSubtle = 2.0 // éléments volontairement discrets
)

// FromPalette déduit les rôles d'un thème Ghostty. Chaque couleur trop proche
// du fond est rapprochée du texte jusqu'au contraste voulu : beaucoup de
// thèmes ont un jaune illisible sur fond clair ou un bleu invisible sur fond
// sombre.
func FromPalette(p Palette) Scheme {
	bg, fg, a := p.Bg, p.Fg, p.ANSI
	on := func(c color.RGBA, min float64) color.RGBA { return ensure(c, bg, fg, min) }

	s := Scheme{
		Name:     p.Name,
		Bg:       bg,
		Fg:       fg,
		OK:       on(a[2], minColor),
		Warn:     on(a[3], minColor),
		Danger:   on(a[1], minColor),
		Degraded: on(mix(a[1], a[3], 0.5), minColor),
		Accent:   on(a[6], minColor),
		Group:    on(a[5], minColor),
		Muted:    on(mix(fg, bg, 0.45), minColor),
		Inactive: on(mix(fg, bg, 0.6), minSubtle),
		Strong:   fg,

		Track:       mix(bg, fg, 0.16),
		TrackText:   fg,
		NeutralFill: mix(fg, bg, 0.5),
		BorderMuted: on(mix(fg, bg, 0.65), 1.8),
		Rule:        mix(bg, fg, 0.28),
		BlockSep:    mix(bg, fg, 0.4),
	}
	s.Border = s.Accent

	// Sélection : celle du thème, sauf si elle se confond avec le fond.
	selBg := p.SelBg
	if !p.HasSel || contrast(selBg, bg) < 1.3 {
		selBg = mix(bg, on(a[4], minColor), 0.4)
	}
	selFg := p.SelFg
	if !p.HasSel || contrast(selFg, selBg) < minText {
		selFg = best(selBg, fg, bg, color.RGBA{255, 255, 255, 255}, color.RGBA{0, 0, 0, 255})
	}
	s.SelBg, s.SelFg = selBg, selFg

	// Touches : un bleu vif sur un fond légèrement décalé du fond du thème.
	keyBg := mix(bg, fg, 0.14)
	key := ensure(a[12], keyBg, fg, minText)
	for _, c := range []color.RGBA{a[12], a[4], a[14], a[6]} {
		if contrast(c, keyBg) >= minText {
			key = c
			break
		}
	}
	s.KeyBg, s.Key = keyBg, key
	s.KeyText = ensure(mix(fg, bg, 0.2), keyBg, fg, minText)

	pp := p
	s.pal = &pp
	return s
}

// Soften : version atténuée d'une couleur d'état (compteurs de conteneurs).
// ok=false pour le thème par défaut, qui garde l'attribut « faint ».
func (s Scheme) Soften(c color.Color) (color.Color, bool) {
	if s.pal == nil {
		return nil, false
	}
	return ensure(mix(toRGBA(c), s.pal.Bg, 0.35), s.pal.Bg, s.pal.Fg, minSubtle), true
}

// OnFill : couleur du texte écrit sur une jauge remplie de fill.
func (s Scheme) OnFill(fill color.Color) color.Color {
	if s.BarText != nil || s.pal == nil {
		return s.BarText
	}
	return best(toRGBA(fill), s.pal.Bg, s.pal.Fg)
}

// ---- calculs de couleur ----

func toRGBA(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 0xff}
}

// mix : a déplacé d'une fraction t vers b.
func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), 0xff}
}

// luminance relative (WCAG 2).
func luminance(c color.RGBA) float64 {
	ch := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// contrast : rapport de contraste WCAG, de 1 (identiques) à 21.
func contrast(a, b color.RGBA) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// ensure rapproche c de toward par pas de 10 % jusqu'à atteindre le contraste
// min sur bg. Si toward lui-même n'y suffit pas, c'est lui qui est retourné.
func ensure(c, bg, toward color.RGBA, min float64) color.RGBA {
	for i := 0; i <= 10; i++ {
		m := mix(c, toward, float64(i)/10)
		if contrast(m, bg) >= min {
			return m
		}
	}
	return toward
}

// best : parmi les candidats, le plus contrasté sur bg.
func best(bg color.RGBA, cands ...color.RGBA) color.RGBA {
	out, max := cands[0], 0.0
	for _, c := range cands {
		if r := contrast(c, bg); r > max {
			out, max = c, r
		}
	}
	return out
}
