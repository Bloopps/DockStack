package theme

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinCatalog(t *testing.T) {
	themes, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(themes) < 600 {
		t.Fatalf("catalogue embarqué : %d thèmes, attendu plus de 600", len(themes))
	}
	for _, name := range []string{"Dracula", "Nord", "Gruvbox Dark", "Catppuccin Mocha", "TokyoNight", "iTerm2 Solarized Light", "Madman's Diary"} {
		if _, ok := Find(themes, name); !ok {
			t.Errorf("thème %q absent du catalogue", name)
		}
	}
	d, _ := Find(themes, "dracula")
	if d.Bg != (color.RGBA{0x28, 0x2a, 0x36, 0xff}) || d.ANSI[5] != (color.RGBA{0xff, 0x79, 0xc6, 0xff}) || !d.HasSel {
		t.Errorf("Dracula mal lu : %+v", d)
	}
	if d.Light() {
		t.Error("Dracula classé clair")
	}
	if l, _ := Find(themes, "iTerm2 Solarized Light"); !l.Light() {
		t.Error("Solarized Light classé sombre")
	}
}

// Tous les thèmes du catalogue doivent donner une interface lisible : c'est
// la garantie de FromPalette, quels que soient les choix du thème.
func TestSchemesReadable(t *testing.T) {
	themes, _ := Builtin()
	for _, p := range themes {
		s := FromPalette(p)
		check := func(role string, c color.Color, on color.RGBA, min float64) {
			got := toRGBA(c)
			if contrast(got, on) < min && got != p.Fg {
				t.Errorf("%s : %s contraste %.2f < %.1f", p.Name, role, contrast(got, on), min)
			}
		}
		for role, c := range map[string]color.Color{"OK": s.OK, "Warn": s.Warn, "Danger": s.Danger,
			"Degraded": s.Degraded, "Accent": s.Accent, "Group": s.Group, "Muted": s.Muted} {
			check(role, c, p.Bg, minColor)
		}
		check("Key", s.Key, toRGBA(s.KeyBg), minText)
		check("KeyText", s.KeyText, toRGBA(s.KeyBg), minText)
		if r := contrast(toRGBA(s.SelFg), toRGBA(s.SelBg)); r < 3 {
			t.Errorf("%s : sélection contraste %.2f", p.Name, r)
		}
		if contrast(toRGBA(s.SelBg), p.Bg) < 1.2 {
			t.Errorf("%s : bande de sélection invisible", p.Name)
		}
	}
}

func TestParseTolerant(t *testing.T) {
	src := "# commentaire\nbackground= #171219\nforeground =eee4ff\npalette = 1=#f00\npalette = 200=#123456\nfont-size = 12\n"
	p, err := Parse("perso", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if p.Bg != (color.RGBA{0x17, 0x12, 0x19, 0xff}) || p.ANSI[1] != (color.RGBA{0xff, 0, 0, 0xff}) {
		t.Errorf("lecture : %+v", p)
	}
	if p.ANSI[2] != (color.RGBA{0, 0xcd, 0, 0xff}) {
		t.Errorf("couleur absente non complétée par xterm : %v", p.ANSI[2])
	}
	if p.HasSel {
		t.Error("sélection absente mais HasSel vrai")
	}
	if _, err := Parse("x", strings.NewReader("palette = 1=#ff0000\n")); err == nil {
		t.Error("thème sans fond accepté")
	}
	if _, err := Parse("x", strings.NewReader("background = #zzzzzz\nforeground = #fff\n")); err == nil {
		t.Error("couleur invalide acceptée")
	}
}

func TestCatalogUserThemes(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(cfg, "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Mon thème", "background = #101010\nforeground = #f0f0f0\n")
	write("Dracula", "background = #000000\nforeground = #ffffff\n") // remplace l'embarqué
	write("cassé", "n'importe quoi\n")

	themes, errs := Catalog(cfg)
	if len(errs) != 1 {
		t.Errorf("erreurs : %v, attendu 1 (thème cassé)", errs)
	}
	mine, ok := Find(themes, "mon thème")
	if !ok || mine.Path == "" {
		t.Fatalf("thème personnel absent : %+v", mine)
	}
	d, _ := Find(themes, "Dracula")
	if d.Bg != (color.RGBA{0, 0, 0, 0xff}) || d.Path == "" {
		t.Error("le thème personnel ne remplace pas l'embarqué de même nom")
	}
	for i := 1; i < len(themes); i++ {
		if strings.ToLower(themes[i-1].Name) > strings.ToLower(themes[i].Name) {
			t.Fatalf("catalogue non trié : %q avant %q", themes[i-1].Name, themes[i].Name)
		}
	}
}

func TestDefaultScheme(t *testing.T) {
	s := Default()
	if s.Bg != nil || s.Fg != nil || s.Muted != nil || s.Palette() != nil {
		t.Error("le thème par défaut ne doit rien imposer au terminal")
	}
	if _, ok := s.Soften(s.OK); ok {
		t.Error("le thème par défaut garde faint pour les compteurs")
	}
	if s.OnFill(s.OK) == nil {
		t.Error("texte des jauges absent")
	}
}

// Chaque thème de la sélection doit exister : un renommage en amont (mise à
// jour du catalogue) le ferait disparaître sans bruit du sélecteur.
func TestPopularExist(t *testing.T) {
	themes, _ := Builtin()
	light := 0
	for _, n := range Popular {
		p, ok := Find(themes, n)
		if !ok {
			t.Errorf("thème populaire %q absent du catalogue", n)
			continue
		}
		if p.Light() {
			light++
		}
	}
	if light < 10 {
		t.Errorf("seulement %d thèmes clairs dans la sélection", light)
	}
	if !IsPopular("dracula") || IsPopular("Dracula+") {
		t.Error("IsPopular : casse ou correspondance exacte")
	}
}
