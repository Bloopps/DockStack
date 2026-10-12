package theme

import (
	"image/color"
	"strings"
	"testing"
)

// FuzzParse : un fichier de thème quelconque (utilisateur, copié d'ailleurs)
// ne fait jamais paniquer la lecture ni la construction des couleurs.
func FuzzParse(f *testing.F) {
	f.Add("background = #1e1e2e\nforeground = #cdd6f4\npalette = 1=#f38ba8\n")
	f.Add("background=fff\nforeground=000\npalette = 15 = #ffffff\nselection-background = #333\nselection-foreground=#eee")
	f.Add("# commentaire\npalette = 300=#000000\n")
	f.Add("palette=-1=#000\nbackground = #é\n")
	f.Fuzz(func(t *testing.T, data string) {
		p, err := Parse("fuzz", strings.NewReader(data))
		if err != nil {
			return
		}
		_ = FromPalette(p)
		_, _ = parseBundle("[fuzz]\n" + data)
	})
}

// FuzzSchemeReadable : pour tout thème dont le texte se lit sur le fond
// (contraste ≥ 4,5, le minimum WCAG), les couleurs de l'interface restent
// lisibles, comme pour les 658 thèmes du catalogue.
func FuzzSchemeReadable(f *testing.F) {
	f.Add(uint32(0x1e1e2e), uint32(0xcdd6f4), uint32(0xf38ba8), uint32(0xa6e3a1), uint32(0xf9e2af), uint32(0x89b4fa))
	f.Add(uint32(0xffffff), uint32(0x000000), uint32(0xffff00), uint32(0xffffff), uint32(0xeeeeee), uint32(0xdddddd))
	f.Add(uint32(0x808080), uint32(0x000000), uint32(0x808080), uint32(0x808080), uint32(0x808080), uint32(0x808080))
	f.Fuzz(func(t *testing.T, bg, fg, red, green, yellow, blue uint32) {
		rgb := func(v uint32) color.RGBA {
			return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}
		}
		p := Palette{Name: "fuzz", Bg: rgb(bg), Fg: rgb(fg)}
		for i := range p.ANSI {
			p.ANSI[i] = rgb(uint32(i) * 0x0f0f0f)
		}
		p.ANSI[1], p.ANSI[2], p.ANSI[3], p.ANSI[4] = rgb(red), rgb(green), rgb(yellow), rgb(blue)
		p.ANSI[9], p.ANSI[10], p.ANSI[11], p.ANSI[12] = rgb(red), rgb(green), rgb(yellow), rgb(blue)
		if contrast(p.Fg, p.Bg) < 4.5 {
			return
		}
		s := FromPalette(p)
		for role, c := range map[string]color.Color{"OK": s.OK, "Warn": s.Warn, "Danger": s.Danger,
			"Degraded": s.Degraded, "Accent": s.Accent, "Group": s.Group, "Muted": s.Muted} {
			if got := toRGBA(c); contrast(got, p.Bg) < minColor && got != p.Fg {
				t.Errorf("%s : contraste %.2f sur le fond (bg %06x fg %06x)", role, contrast(got, p.Bg), bg&0xffffff, fg&0xffffff)
			}
		}
		if r := contrast(toRGBA(s.SelFg), toRGBA(s.SelBg)); r < 3 {
			t.Errorf("sélection : contraste %.2f (bg %06x fg %06x)", r, bg&0xffffff, fg&0xffffff)
		}
	})
}
