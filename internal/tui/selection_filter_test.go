package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// typeKeys tape des touches dans la liste par la boucle complète.
func typeKeys(m Model, keys ...string) Model {
	for _, k := range keys {
		m, _ = update(m, keyMsg(k))
	}
	return m
}

// selectionModel : trois stacks qui tournent, jellyfin et sonarr cochées.
// Chacune est son propre dossier, comme une stack à la racine.
func selectionModel() (Model, *fakeClient) {
	fake := &fakeClient{autoDone: true}
	m := newTestModel([]compose.Stack{
		stack("jellyfin", "jellyfin", 1, 1, 0), stack("sonarr", "sonarr", 1, 1, 0), stack("traefik", "traefik", 1, 1, 0),
	})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	m.selected["jellyfin"] = true
	m.selected["sonarr"] = true
	return m, fake
}

// Une stack cochée puis masquée par le filtre n'est plus visée : p et u (sans
// confirmation) ne la touchent pas, et ne retombent pas non plus sur la stack
// sous le curseur. Avant, « /traef ↩ » puis p lançait le pull des deux
// stacks cochées, invisibles.
func TestHiddenSelectionNotTargeted(t *testing.T) {
	m, fake := selectionModel()
	m = typeKeys(m, "/", "t", "r", "a", "e", "f", "enter")
	if got := m.selectedStacks(); len(got) != 0 {
		t.Fatalf("selectedStacks = %v, want aucune (toutes masquées)", got)
	}
	if sel := m.selection(); sel.shown != 0 || sel.hidden != 2 || !sel.byFilter {
		t.Fatalf("selection = %+v", sel)
	}
	for _, k := range []string{"p", "u", "d"} {
		m = typeKeys(m, k)
		if m.view != viewList || m.statusKind != statusInfo || !strings.Contains(m.status, "esc pour l'effacer") {
			t.Fatalf("%s : vue %v, statut %q (%v) ; want un refus", k, m.view, m.status, m.statusKind)
		}
	}
	if len(fake.called("pull")) != 0 || len(fake.called("up")) != 0 {
		t.Fatalf("une action est partie : pull %v, up %v", fake.called("pull"), fake.called("up"))
	}

	// Filtre qui n'en montre qu'une : p ne vise qu'elle, l'autre reste cochée.
	m.setStatus("", false)
	m = typeKeys(m, "esc", "/", "j", "e", "l", "enter", "p")
	if got := fake.called("pull"); len(got) != 1 || got[0].Name != "jellyfin" {
		t.Fatalf("pull = %v, want [jellyfin]", got)
	}
	if !m.selected["sonarr"] || m.selected["jellyfin"] {
		t.Errorf("sélection après l'action = %v, want sonarr seule (masquée, non visée)", m.selected)
	}
}

// Masquées par a (stacks arrêtées) : même règle, le refus renvoie vers a.
func TestHiddenSelectionByShowAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeClient{autoDone: true}
	m := newTestModel([]compose.Stack{stack("web", "web", 1, 1, 0), stack("old", "old", 0, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	m.cfg.ShowAll = true
	m.invalidateRows()
	m.selected["old"] = true
	m = typeKeys(m, "a", "u")
	if len(fake.called("up")) != 0 {
		t.Fatalf("up lancé sur une stack masquée : %v", fake.called("up"))
	}
	if sel := m.selection(); sel.hidden != 1 || sel.byFilter || !strings.Contains(m.status, "a pour les afficher") {
		t.Errorf("selection %+v, statut %q", sel, m.status)
	}
}

// Le pied de page et le volet comptent à part les cochées masquées, et
// disent qu'esc efface d'abord le filtre.
func TestHiddenSelectionShown(t *testing.T) {
	m, _ := selectionModel()
	m = typeKeys(m, "/", "s", "o", "n", "enter")
	f := ansi.Strip(m.renderFooter())
	for _, want := range []string{"1  cochée", "1  masquée", "esc  effacer le filtre"} {
		if !strings.Contains(f, want) {
			t.Errorf("pied de page sans %q : %q", want, f)
		}
	}
	screen := ansi.Strip(m.viewContent())
	if !strings.Contains(screen, "Actions sur la stack cochée") || !strings.Contains(screen, "(1 autre masquée par le filtre)") {
		t.Errorf("volet :\n%s", screen)
	}

	// Toutes masquées : ni actions annoncées, ni « 0 cochée ».
	m = typeKeys(m, "esc", "/", "t", "r", "a", "e", "f", "enter")
	f = ansi.Strip(m.renderFooter())
	if !strings.Contains(f, "2  cochées masquées") || strings.Contains(f, "up") || strings.Contains(f, "0  cochée") {
		t.Errorf("pied de page, toutes masquées : %q", f)
	}
	screen = ansi.Strip(m.viewContent())
	if !strings.Contains(screen, "2 stacks cochées, masquées") || strings.Contains(screen, "r restart") {
		t.Errorf("volet, toutes masquées : actions de la stack affichées alors qu'aucune lettre n'agit :\n%s", screen)
	}
}

// Saisie et filtre figé ne se ressemblent plus : la saisie rappelle ↩ et esc,
// le filtre figé est grisé et compte les stacks retenues. Pendant la saisie,
// le pied de page n'annonce que ce qui marche (↩ et ␣ y sont du texte ou
// valident).
func TestFilterTypingVsFrozen(t *testing.T) {
	m, _ := selectionModel()
	m.selected = map[string]bool{}
	m = typeKeys(m, "/", "s", "o", "n")
	typing := ansi.Strip(m.filterBar(80))
	if !strings.Contains(typing, "↩ valider · esc effacer") {
		t.Errorf("barre en saisie = %q", typing)
	}
	f := ansi.Strip(m.renderFooter())
	if !strings.Contains(f, "valider") || !strings.Contains(f, "choisir") || !strings.Contains(f, "effacer") ||
		strings.Contains(f, "cocher") || strings.Contains(f, "aide") {
		t.Errorf("pied de page en saisie = %q", f)
	}

	m = typeKeys(m, "enter")
	frozen := ansi.Strip(m.filterBar(80))
	if frozen == typing || !strings.Contains(frozen, "/ son  (1 stack · / modifier · esc effacer)") {
		t.Errorf("barre figée = %q (saisie : %q)", frozen, typing)
	}
	if f := ansi.Strip(m.renderFooter()); strings.Contains(f, "valider") {
		t.Errorf("pied de page du filtre figé = %q", f)
	}
}

// ↑↓ pendant la saisie choisissent parmi les résultats, sans toucher au
// texte ni ramener le curseur en tête.
func TestFilterTypingArrowsMoveCursor(t *testing.T) {
	m, _ := selectionModel()
	m = typeKeys(m, "/", "r") // sonarr et traefik
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.filtering || m.filter.Value() != "r" || m.cursor != 1 {
		t.Fatalf("↓ : saisie %v, filtre %q, curseur %d ; want saisie, « r », 1", m.filtering, m.filter.Value(), m.cursor)
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.cursor != 0 || m.filter.Value() != "r" {
		t.Errorf("↑ : curseur %d, filtre %q", m.cursor, m.filter.Value())
	}
}

// La colonne des noms ne bouge pas avec le filtre ni avec a : calculée sur
// les seules lignes affichées, elle rétrécissait dès la première lettre et
// les colonnes d'état et de mesures sautaient.
func TestNameColumnStableWithFilter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel([]compose.Stack{
		stack("web", "web", 1, 1, 0),
		stack("monitoring/uptime-kuma", "monitoring", 1, 1, 0), stack("monitoring/grafana", "monitoring", 1, 1, 0),
		stack("une-stack-arretee-au-nom-tres-long", "une-stack-arretee-au-nom-tres-long", 0, 1, 0),
	})
	m.width, m.height, m.loading = 120, 40, false
	m.listRows()
	want := m.rowsCache.nameW
	if want != len("une-stack-arretee-au-nom-tres-long") {
		t.Fatalf("colonne = %d, want la stack masquée la plus longue (%d)", want, len("une-stack-arretee-au-nom-tres-long"))
	}
	m = typeKeys(m, "/", "w", "e", "b")
	m.listRows()
	if got := m.rowsCache.nameW; got != want {
		t.Errorf("filtre : colonne = %d, want %d", got, want)
	}
	m = typeKeys(m, "esc", "a")
	m.listRows()
	if got := m.rowsCache.nameW; got != want {
		t.Errorf("a : colonne = %d, want %d", got, want)
	}
}

// La fenêtre de la liste n'affiche un repère que s'il sert : en tête de
// liste, pas de « au-dessus » ni de ligne vide à sa place, la hauteur est
// remplie de stacks jusqu'au repère du bas.
func TestStackListMarksOnlyWhenUseful(t *testing.T) {
	var stacks []compose.Stack
	for _, n := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "b1", "b2", "b3"} {
		stacks = append(stacks, stack(n, n, 1, 1, 0))
	}
	m := newTestModel(stacks)
	m.width, m.height, m.loading = 80, 30, false
	const h = 6
	lines := strings.Split(ansi.Strip(renderStackList(m, h+1, 80)), "\n") // +1 : en-tête des colonnes
	lines = lines[1:]
	if len(lines) != h || strings.Contains(lines[0], "au-dessus") || strings.TrimSpace(lines[h-2]) == "" ||
		!strings.Contains(lines[h-1], "en-dessous") {
		t.Fatalf("en tête : %d lignes, want %d stacks puis le repère du bas :\n%s", len(lines), h-1, strings.Join(lines, "\n"))
	}
	m.cursor = len(stacks) - 1
	lines = strings.Split(ansi.Strip(renderStackList(m, h+1, 80)), "\n")[1:]
	if len(lines) != h || !strings.Contains(lines[0], "au-dessus") || strings.Contains(lines[h-1], "dessous") ||
		!strings.Contains(lines[h-1], "b3") {
		t.Errorf("en fin : repère du haut puis les dernières stacks attendus :\n%s", strings.Join(lines, "\n"))
	}
}

// Liste vide : un filtre sans résultat ne propose qu'esc (o et R y seraient
// des lettres du filtre) ; un répertoire sans stack dit ce qu'est une stack
// et jusqu'où on la cherche.
func TestEmptyListMessages(t *testing.T) {
	m, _ := selectionModel()
	m = typeKeys(m, "/", "z", "z", "z")
	out := ansi.Strip(renderStackList(m, 20, 100))
	if !strings.Contains(out, "Aucune stack ne contient « zzz »") || !strings.Contains(out, "esc efface le filtre") ||
		strings.Contains(out, "répertoire") {
		t.Errorf("filtre sans résultat :\n%s", out)
	}

	m = newTestModel(nil)
	m.width, m.height, m.loading = 100, 30, false
	out = ansi.Strip(renderStackList(m, 20, 100))
	for _, want := range []string{"Aucune stack dans /tmp", "compose.yaml", "jusqu'à 3 niveaux", "o choisir un autre répertoire", "R relire"} {
		if !strings.Contains(out, want) {
			t.Errorf("répertoire vide : %q absent :\n%s", want, out)
		}
	}
}
