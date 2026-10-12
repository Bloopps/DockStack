package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// longConfirm : confirmation de 30 lignes ouverte en 80x24, où le cadre n'en
// montre qu'une partie (l'en-tête réduit lui laisse 21 lignes).
func longConfirm(t *testing.T) Model {
	t.Helper()
	m := newTestModel([]compose.Stack{stack("app", "", 1, 1, 0)})
	m.width, m.height, m.loading = 80, 24, false
	names := make([]string, 30)
	for i := range names {
		names[i] = fmt.Sprintf("■ arrêter stack-%02d", i)
	}
	next, _ := m.askConfirm("Mettre à l'identique", "", names, nil)
	m = next.(Model)
	if m.confirmMaxScroll() == 0 {
		t.Fatalf("la liste de 30 lignes devait déborder en 80x24")
	}
	return m
}

// La liste d'une confirmation se relit avant de valider : ↓/j, pgdown et G
// la font défiler, bornée à sa fin ; les repères disent ce qui est caché, et
// le pied de page commence par « ↑↓ lire ». Avant, elle était coupée en
// « … et N autres » sans moyen de lire la suite.
func TestConfirmListScrolls(t *testing.T) {
	m := longConfirm(t)
	screen := ansi.Strip(m.viewContent())
	if !strings.Contains(screen, "stack-00") || strings.Contains(screen, "stack-29") || strings.Contains(screen, "au-dessus") {
		t.Fatalf("ouverture : début de liste attendu, sans repère du haut :\n%s", screen)
	}
	if !strings.Contains(screen, "en dessous (↓ pour lire)") {
		t.Errorf("repère du bas absent :\n%s", screen)
	}
	if f := ansi.Strip(m.renderFooter()); !strings.HasPrefix(strings.TrimSpace(f), "↑↓") || !strings.Contains(f, "lire") {
		t.Errorf("pied de page = %q, want « ↑↓ lire » en tête", f)
	}

	m, _ = update(m, keyMsg("j"))
	if m.confirmScroll != 1 {
		t.Fatalf("j : défilement = %d, want 1", m.confirmScroll)
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.confirmScroll <= 1 {
		t.Errorf("pgdown : défilement = %d, want plus d'une ligne", m.confirmScroll)
	}
	m, _ = update(m, keyMsg("G"))
	end := m.confirmMaxScroll()
	if m.confirmScroll != end {
		t.Fatalf("G : défilement = %d, want %d", m.confirmScroll, end)
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.confirmScroll != end {
		t.Errorf("↓ en fin de liste : défilement = %d, want borné à %d", m.confirmScroll, end)
	}
	screen = ansi.Strip(m.viewContent())
	if !strings.Contains(screen, "stack-29") || strings.Contains(screen, "en dessous") || !strings.Contains(screen, "au-dessus") {
		t.Errorf("fin de liste : dernière ligne et repère du haut attendus :\n%s", screen)
	}
	// La ligne des touches reste dans le cadre, à l'écran.
	if !strings.Contains(screen, "n / esc  annuler") || strings.Count(screen, "\n")+1 > m.height {
		t.Errorf("cadre trop haut ou touches perdues (%d lignes) :\n%s", strings.Count(screen, "\n")+1, screen)
	}

	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.confirmScroll != end-1 {
		t.Errorf("↑ : défilement = %d, want %d", m.confirmScroll, end-1)
	}
	m, _ = update(m, keyMsg("g"))
	if m.confirmScroll != 0 {
		t.Errorf("g : défilement = %d, want 0", m.confirmScroll)
	}
	// Une nouvelle confirmation repart du haut.
	m.confirmScroll = 3
	next, _ := m.askConfirm("Autre", "", m.confirmNames, nil)
	if next.(Model).confirmScroll != 0 {
		t.Error("askConfirm doit remettre le défilement à zéro")
	}
}

// Une liste qui tient dans le cadre ne défile pas et le pied de page ne
// propose pas de la lire.
func TestConfirmShortListNoScroll(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("app", "", 1, 1, 0)})
	m.width, m.height, m.loading = 120, 40, false
	next, _ := m.askConfirm("Exclure du scan — app", "", []string{"app", "app/web"}, nil)
	m = next.(Model)
	m, _ = update(m, keyMsg("j"))
	if m.confirmScroll != 0 {
		t.Errorf("défilement = %d sur une liste qui tient", m.confirmScroll)
	}
	if f := ansi.Strip(m.renderFooter()); strings.Contains(f, "lire") {
		t.Errorf("pied de page = %q, sans « lire » attendu", f)
	}
}

// Une stack unique : sous le séparateur, la ligne d'effet (ce que fait
// l'action) remplace la répétition du nom, déjà dans le titre. Down dit que
// les volumes sont conservés, restart qu'il ne relit pas le compose ; en mode
// services, down garde le réseau.
func TestConfirmShowsEffect(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("immich", "", 1, 1, 0)})
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 120, 40, false

	m, _ = update(m, keyMsg("d"))
	if m.view != viewConfirm || len(m.confirmNames) != 0 || m.confirmEffect != actionEffect("down", false) {
		t.Fatalf("Down : vue %v, noms %v, effet %q", m.view, m.confirmNames, m.confirmEffect)
	}
	screen := ansi.Strip(m.viewContent())
	if !strings.Contains(screen, "volumes et images conservés") {
		t.Errorf("effet de Down absent :\n%s", screen)
	}
	if n := strings.Count(screen, "immich"); n != 1 {
		t.Errorf("nom affiché %d fois, want 1 (titre seul) :\n%s", n, screen)
	}

	m, _ = update(m, keyMsg("esc"))
	m, _ = update(m, keyMsg("r"))
	if !strings.Contains(m.confirmEffect, "sans relire le compose") || !strings.Contains(m.confirmEffect, "recreate") {
		t.Errorf("Restart : effet %q, want le rappel de recreate", m.confirmEffect)
	}
	if !strings.Contains(actionEffect("down", true), "réseau, volumes et images conservés") {
		t.Errorf("Down de services : effet %q", actionEffect("down", true))
	}

	// Mode services : la confirmation liste les services et porte l'effet
	// restreint.
	m, _ = update(m, keyMsg("esc"))
	m.stacks[0].Services = []compose.ServiceStatus{{Name: "web", Running: 1, Total: 1}, {Name: "db", Running: 1, Total: 1}}
	m.invalidateRows()
	m, _ = update(m, keyMsg("right"))
	m, _ = update(m, keyMsg("d"))
	if m.view != viewConfirm || m.confirmEffect != actionEffect("down", true) {
		t.Errorf("Down de service : vue %v, effet %q", m.view, m.confirmEffect)
	}
}

// Up sur une stack qui tourne, prévision en échec : la confirmation
// générique ne répète pas non plus le nom de la stack.
func TestConfirmUpSingleNoRepeatedName(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("immich", "", 1, 1, 0)})
	m.client = &fakeClient{planErr: fmt.Errorf("compose illisible")}
	m.width, m.height, m.loading = 120, 40, false
	m = send(m, "u")
	if m.view != viewConfirm || len(m.confirmNames) != 0 || m.confirmWarning != upWarning {
		t.Fatalf("Up : vue %v, noms %v, avertissement %q", m.view, m.confirmNames, m.confirmWarning)
	}
}

// Remove efface des données : cadre et titre de suppression définitive, et
// l'effet chiffre les volumes effacés (taille inconnue tant qu'ils ne sont
// pas mesurés). Down garde le titre ordinaire.
func TestConfirmRemoveDangerAndSize(t *testing.T) {
	m := newTestModel([]compose.Stack{{Name: "immich", Project: "immich", Running: 1, Total: 1}})
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 120, 40, false
	openRemove := func(m Model) Model {
		m.view = viewAction
		for i, a := range stackActions {
			if a.id == "remove" {
				m.actionCursor = i
			}
		}
		m, _ = update(m, keyMsg("enter"))
		return m
	}

	m = openRemove(m)
	if !m.confirmDanger || m.confirmEffect != "Volumes effacés : taille inconnue (dossiers montés conservés)" {
		t.Fatalf("Remove sans mesure : danger %v, effet %q", m.confirmDanger, m.confirmEffect)
	}
	screen := ansi.Strip(m.viewContent())
	if !strings.Contains(screen, "✕ Suppression définitive : Remove — immich") || strings.Contains(screen, "⚠ Confirmer") {
		t.Errorf("titre de Remove :\n%s", screen)
	}

	m, _ = update(m, keyMsg("esc"))
	m.usage = &compose.DockerUsage{VolumesOK: true, VolumesByProject: map[string]int64{"immich": 18 << 30}}
	m = openRemove(m)
	if !strings.Contains(m.confirmEffect, "Volumes effacés : 18.0 Go") {
		t.Errorf("Remove mesuré : effet %q", m.confirmEffect)
	}

	m, _ = update(m, keyMsg("esc"))
	m, _ = update(m, keyMsg("d"))
	if screen := ansi.Strip(m.viewContent()); !strings.Contains(screen, "⚠ Confirmer : Down") || strings.Contains(screen, "Suppression définitive") {
		t.Errorf("Down : titre ordinaire attendu :\n%s", screen)
	}
}

// Le cadre est centré comme le panneau d'actions, et son séparateur va
// d'un bord à l'autre (largeur fixe avant : il s'arrêtait au milieu des
// cadres larges).
func TestConfirmBoxCenteredFullRule(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("app", "", 1, 1, 0)})
	m.width, m.height, m.loading = 120, 40, false
	long := "Supprime aussi le réseau externe traefik_lan de traefik : son up échouera tant qu'il n'est pas recréé."
	next, _ := m.askConfirm("docker network prune", long, []string{"Réseaux inutilisés : 2"}, nil)
	m = next.(Model)
	lines := strings.Split(ansi.Strip(renderConfirmView(m, 30)), "\n")

	var top, rule string
	for _, l := range lines {
		switch {
		case top == "" && strings.Contains(l, "╭"):
			top = l
		case rule == "" && strings.Contains(l, "──") && strings.Contains(l, "│"):
			rule = l
		}
	}
	if top == "" || rule == "" {
		t.Fatalf("cadre ou séparateur introuvable :\n%s", strings.Join(lines, "\n"))
	}
	left := len(top) - len(strings.TrimLeft(top, " "))
	right := m.width - lipgloss.Width(strings.TrimRight(top, " ")) // colonnes libres à droite
	if d := left - right; d < -1 || d > 1 {
		t.Errorf("cadre non centré : %d colonnes à gauche, %d à droite", left, right)
	}
	inner := strings.TrimSpace(strings.Trim(strings.TrimSpace(rule), "│"))
	if got, want := lipgloss.Width(inner), lipgloss.Width(long); got != want {
		t.Errorf("séparateur de %d colonnes, want %d (la plus longue ligne)", got, want)
	}
}

// markedWindow : un repère n'apparaît que du côté où des lignes sont cachées,
// et chaque pas montre une nouvelle ligne en bas.
func TestMarkedWindow(t *testing.T) {
	for _, tc := range []struct {
		n, s, h                int
		from, to, above, below int
	}{
		{5, 0, 10, 0, 5, 0, 0},     // tout tient
		{20, 0, 5, 0, 4, 0, 16},    // en tête : pas de repère du haut
		{20, 1, 5, 2, 5, 2, 15},    // un pas : une ligne de plus en bas
		{20, 15, 5, 16, 20, 16, 0}, // en fin : pas de repère du bas
		{20, 99, 5, 16, 20, 16, 0}, // borné à la fin
		{20, -3, 5, 0, 4, 0, 16},   // borné au début
		{20, 7, 2, 7, 9, 0, 0},     // trop bas pour des repères
	} {
		from, to, above, below := markedWindow(tc.n, tc.s, tc.h)
		if from != tc.from || to != tc.to || above != tc.above || below != tc.below {
			t.Errorf("markedWindow(%d, %d, %d) = %d, %d, %d, %d ; want %d, %d, %d, %d",
				tc.n, tc.s, tc.h, from, to, above, below, tc.from, tc.to, tc.above, tc.below)
		}
	}
}
