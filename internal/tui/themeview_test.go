package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/theme"
)

// themeKeys envoie des touches par le routage complet (Update). Chaque mot
// est une touche nommée ; « tape:xyz » saisit du texte lettre par lettre.
func themeKeys(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var msgs []tea.KeyPressMsg
		switch {
		case k == "tab":
			msgs = []tea.KeyPressMsg{{Code: tea.KeyTab}}
		case k == "up":
			msgs = []tea.KeyPressMsg{{Code: tea.KeyUp}}
		case strings.HasPrefix(k, "tape:"):
			for _, r := range strings.TrimPrefix(k, "tape:") {
				msgs = append(msgs, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		default:
			msgs = []tea.KeyPressMsg{keyMsg(k)}
		}
		for _, msg := range msgs {
			mm, _ := m.Update(msg)
			m = mm.(Model)
		}
	}
	return m
}

// resetTheme : les styles sont globaux, chaque test repart du défaut.
func resetTheme(t *testing.T) {
	t.Cleanup(func() { applyScheme(theme.Default()) })
}

func pickerModel(t *testing.T) Model {
	t.Helper()
	resetTheme(t)
	m := newTestModel([]compose.Stack{stack("web", "web", 1, 1, 0), stack("db", "db", 0, 1, 0)})
	m.cfg.StackDir = t.TempDir()
	m.width, m.height, m.loading = 160, 40, false
	return m
}

func TestThemePickerPreviewAndCancel(t *testing.T) {
	m := pickerModel(t)
	m = themeKeys(t, m, "t")
	if m.view != viewTheme {
		t.Fatalf("t doit ouvrir le sélecteur, vue = %v", m.view)
	}
	rows := m.themeRows()
	if m.themeKind != themePopular || len(rows) < 40 || len(rows) > 100 || rows[m.themeCursor].pal != nil {
		t.Fatalf("ouverture attendue sur « populaires », curseur sur le défaut (onglet %d, %d lignes)", m.themeKind, len(rows))
	}
	if m.View().BackgroundColor != nil {
		t.Error("thème par défaut : le fond du terminal ne doit pas être imposé")
	}

	m = themeKeys(t, m, "down")
	if m.themeName == theme.DefaultName || activeScheme.Bg == nil {
		t.Fatal("↓ doit appliquer en direct le thème suivant")
	}
	if v := m.View(); v.BackgroundColor == nil || v.ForegroundColor == nil {
		t.Error("aperçu : fond et texte du thème non transmis au terminal")
	}
	if !strings.Contains(ansi.Strip(m.viewContent()), "Vos stacks") {
		t.Error("l'aperçu doit montrer la liste des stacks")
	}

	m = themeKeys(t, m, "esc")
	if m.view != viewList || m.themeName != theme.DefaultName || activeScheme.Bg != nil {
		t.Errorf("esc doit revenir à la liste et au thème d'avant (vue %v, thème %q)", m.view, m.themeName)
	}
	if m.cfg.Theme != "" {
		t.Errorf("esc ne doit rien enregistrer, config = %q", m.cfg.Theme)
	}
}

func TestThemePickerFilterAndSave(t *testing.T) {
	m := pickerModel(t)
	m = themeKeys(t, m, "t", "/", "tape:dracula")
	if m.themeFilter != "dracula" || m.view != viewTheme {
		t.Fatalf("filtre = %q, vue = %v", m.themeFilter, m.view)
	}
	if r := m.themeRows()[m.themeCursor]; r.name != "Dracula" || m.themeName != "Dracula" {
		t.Fatalf("premier résultat attendu Dracula, obtenu %q (actif %q)", r.name, m.themeName)
	}
	// q pendant la saisie est une lettre, pas une sortie.
	m = themeKeys(t, m, "q")
	if m.quitArmed || m.themeFilter != "draculaq" {
		t.Errorf("q pendant le filtre : quitArmed=%v filtre=%q", m.quitArmed, m.themeFilter)
	}
	m = themeKeys(t, m, "backspace", "enter", "enter")
	if m.view != viewList || m.cfg.Theme != "Dracula" {
		t.Fatalf("↩ doit enregistrer Dracula (vue %v, config %q)", m.view, m.cfg.Theme)
	}
	saved, err := config.Load()
	if err != nil || saved.Theme != "Dracula" {
		t.Fatalf("config relue : %+v, %v", saved, err)
	}

	// Relancement : le thème enregistré est appliqué dès New.
	applyScheme(theme.Default())
	m2 := New(saved)
	if m2.themeName != "Dracula" || activeScheme.Bg == nil || m2.status != "" {
		t.Errorf("au démarrage : thème %q, statut %q", m2.themeName, m2.status)
	}

	// Retour au défaut : la clé disparaît de la config.
	m = themeKeys(t, m, "t", "g", "enter")
	if m.cfg.Theme != "" || m.themeName != theme.DefaultName {
		t.Errorf("retour au défaut : config %q, actif %q", m.cfg.Theme, m.themeName)
	}
}

func TestThemePickerKinds(t *testing.T) {
	m := pickerModel(t)
	m = themeKeys(t, m, "t", "tab")
	if m.themeKind != themeAll || len(m.themeRows()) < 600 {
		t.Fatalf("premier tab : « tous » attendu (onglet %d, %d thèmes)", m.themeKind, len(m.themeRows()))
	}
	m = themeKeys(t, m, "tab")
	for _, r := range m.themeRows() {
		if r.pal == nil || r.pal.Light() {
			t.Fatalf("« sombres » contient %q", r.name)
		}
	}
	m = themeKeys(t, m, "tab")
	rows := m.themeRows()
	if len(rows) < 50 {
		t.Fatalf("« clairs » : seulement %d thèmes", len(rows))
	}
	for _, r := range rows {
		if r.pal == nil || !r.pal.Light() {
			t.Fatalf("« clairs » contient %q", r.name)
		}
	}
	if m.themeName == theme.DefaultName {
		t.Error("changer de catégorie doit prévisualiser un thème de la catégorie")
	}
	m = themeKeys(t, m, "/", "tape:zzzzzz")
	if len(m.themeRows()) != 0 || !strings.Contains(ansi.Strip(m.viewContent()), "aucun thème") {
		t.Error("filtre sans résultat : message attendu")
	}
	m = themeKeys(t, m, "enter", "enter")
	if m.view != viewTheme {
		t.Error("↩ sans thème affiché ne doit rien enregistrer ni fermer")
	}
}

func TestThemeUnknownAtStartup(t *testing.T) {
	resetTheme(t)
	m := New(&config.Config{StackDir: "/tmp", Theme: "N'existe pas"})
	if m.themeName != theme.DefaultName || activeScheme.Bg != nil {
		t.Errorf("thème inconnu : défaut attendu, obtenu %q", m.themeName)
	}
	if !strings.Contains(m.status, "introuvable") || m.startupStatusClear() == nil {
		t.Errorf("avertissement attendu (et effacé ensuite), statut = %q", m.status)
	}
}

// Un thème personnel (format Ghostty) déposé dans ~/.dockstack/themes est
// proposé et marqué comme tel.
func TestThemePersonal(t *testing.T) {
	m := pickerModel(t)
	dir := filepath.Join(m.cfg.ConfigDir(), "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "Maison"), []byte("background = #102030\nforeground = #e0e0e0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = themeKeys(t, m, "t", "/", "tape:maison")
	if m.themeName != "Maison" || !strings.Contains(ansi.Strip(m.viewContent()), "Maison (perso)") {
		t.Errorf("thème personnel non proposé (actif %q)", m.themeName)
	}
}

// Toutes les vues se rendent sans déborder avec un thème clair et un sombre.
func TestThemeViewsRender(t *testing.T) {
	m := pickerModel(t)
	for _, name := range []string{"Catppuccin Latte", "Gruvbox Dark"} {
		themes, _ := theme.Builtin()
		p, ok := theme.Find(themes, name)
		if !ok {
			t.Fatalf("%s absent", name)
		}
		applyScheme(theme.FromPalette(p))
		m.restyleWidgets()
		for _, v := range []view{viewList, viewHelp, viewTheme} {
			m.view = v
			if v == viewTheme {
				m.themes = themes
			}
			for i, l := range strings.Split(m.viewContent(), "\n") {
				if w := ansi.StringWidth(l); w > m.width {
					t.Errorf("%s, vue %v, ligne %d : largeur %d > %d", name, v, i, w, m.width)
				}
			}
		}
	}
}

// t ouvre le sélecteur depuis la liste, le mode services et l'aide ; T aussi
// (verrouillage des majuscules). Pendant une saisie, c'est une lettre.
func TestThemeKeyEverywhere(t *testing.T) {
	cases := []struct {
		name  string
		setup func(Model) Model
		key   string
	}{
		{"liste", func(m Model) Model { return m }, "t"},
		{"majuscule", func(m Model) Model { return m }, "T"},
		{"services", func(m Model) Model { return themeKeys(t, m, "right") }, "t"},
		{"aide", func(m Model) Model { m.view = viewHelp; return m }, "t"},
	}
	for _, c := range cases {
		m := c.setup(pickerModel(t))
		if c.name == "services" && !m.svcFocus {
			t.Fatal("→ doit entrer dans les services")
		}
		m = themeKeys(t, m, c.key)
		if m.view != viewTheme {
			t.Errorf("%s : %s n'ouvre pas le sélecteur (vue %v)", c.name, c.key, m.view)
		}
		if c.name == "services" {
			if m = themeKeys(t, m, "esc"); !m.svcFocus || m.view != viewList {
				t.Error("esc doit revenir au mode services")
			}
		}
	}

	m := pickerModel(t)
	m.filtering = true
	m.filter.Focus()
	if m = themeKeys(t, m, "t"); m.view != viewList || m.filter.Value() != "t" {
		t.Errorf("filtre de la liste : t doit être saisi (vue %v, filtre %q)", m.view, m.filter.Value())
	}
}

// Depuis « populaires », la recherche porte sur tout le catalogue ; un thème
// actif hors sélection ouvre le sélecteur sur « tous », curseur dessus.
func TestThemePopularTab(t *testing.T) {
	m := pickerModel(t)
	m = themeKeys(t, m, "t")
	for _, r := range m.themeRows() {
		if r.pal != nil && !theme.IsPopular(r.name) {
			t.Fatalf("« populaires » contient %q", r.name)
		}
	}
	m = themeKeys(t, m, "/", "tape:dracula+")
	if rows := m.themeRows(); len(rows) != 1 || rows[0].name != "Dracula+" {
		t.Fatalf("recherche depuis populaires : %v", rows)
	}
	m = themeKeys(t, m, "enter", "enter")
	if m.cfg.Theme != "Dracula+" {
		t.Fatalf("config %q", m.cfg.Theme)
	}
	m = themeKeys(t, m, "t")
	if m.themeKind != themeAll || m.themeRows()[m.themeCursor].name != "Dracula+" {
		t.Errorf("réouverture : onglet %d, curseur sur %q", m.themeKind, m.themeRows()[m.themeCursor].name)
	}
}

// Colonne des thèmes bornée à sa largeur, même sur un terminal de 80 colonnes.
func TestThemePickerNarrow(t *testing.T) {
	m := pickerModel(t)
	m.width = 80
	m = themeKeys(t, m, "t")
	for i, l := range strings.Split(m.viewContent(), "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("ligne %d : largeur %d > %d", i, w, m.width)
		}
	}
}
