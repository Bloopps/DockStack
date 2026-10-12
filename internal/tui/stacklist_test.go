package tui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// newTestModel construit un Model prêt pour tester les dérivés de la liste
// (filtre, tri, lignes) sans terminal ni client Docker.
func newTestModel(stacks []compose.Stack) Model {
	m := New(&config.Config{StackDir: "/tmp"})
	m.stacks = stacks
	m.invalidateRows()
	return m
}

// stack fabrique une Stack avec un état déterminé par (running/total/unhealthy).
func stack(name, group string, running, total, unhealthy int) compose.Stack {
	return compose.Stack{
		Name:      name,
		Group:     group,
		Running:   running,
		Total:     total,
		Unhealthy: unhealthy,
	}
}

func TestStatePriority(t *testing.T) {
	// Ordre attendu : malade < en marche < partielle < arrêtée < inconnue.
	order := []compose.State{
		compose.StateUnhealthy,
		compose.StateRunning,
		compose.StatePartial,
		compose.StateStopped,
		compose.StateUnknown,
	}
	for i := 1; i < len(order); i++ {
		if statePriority(order[i-1]) >= statePriority(order[i]) {
			t.Errorf("priorité non strictement croissante entre %v et %v", order[i-1], order[i])
		}
	}
}

// TestSortByState : les groupes sont ordonnés par leur meilleur état (un groupe
// contenant une stack malade passe avant un groupe tout-arrêté) ; à l'intérieur
// d'un groupe, tri par état puis par nom.
func TestSortByState(t *testing.T) {
	stacks := []compose.Stack{
		stack("z-stopped", "media", 0, 1, 0),   // groupe media : arrêté
		stack("a-unhealthy", "media", 1, 1, 1), // groupe media : malade -> meilleur = malade
		stack("b-running", "infra", 1, 1, 0),   // groupe infra : en marche
	}
	got := sortByState(stacks)

	// Le groupe "media" (malade) doit précéder "infra" (en marche).
	if got[0].Group != "media" {
		t.Fatalf("premier groupe = %q, want media", got[0].Group)
	}
	// Dans media : la malade avant l'arrêtée.
	if got[0].Name != "a-unhealthy" || got[1].Name != "z-stopped" {
		t.Errorf("ordre intra-groupe = %q,%q want a-unhealthy,z-stopped", got[0].Name, got[1].Name)
	}
	if got[2].Group != "infra" {
		t.Errorf("dernier groupe = %q, want infra", got[2].Group)
	}
}

func TestFilteredStacks(t *testing.T) {
	m := newTestModel([]compose.Stack{
		{Name: "immich", NameLC: "immich"},
		{Name: "Traefik", NameLC: "traefik"},
		{Name: "jellyfin", NameLC: "jellyfin"},
	})
	m.filter.SetValue("trae")
	m.invalidateRows()

	got := m.filteredStacks()
	if len(got) != 1 || got[0].Name != "Traefik" {
		t.Fatalf("filteredStacks = %v, want [Traefik]", got)
	}

	// Sans filtre : toutes les stacks.
	m.filter.SetValue("")
	m.invalidateRows()
	if len(m.filteredStacks()) != 3 {
		t.Errorf("sans filtre = %d stacks, want 3", len(m.filteredStacks()))
	}
}

// Les stacks cochées sont rendues dans l'ordre de la liste. Elles tournent :
// une stack arrêtée est masquée par défaut, et une stack masquée n'est plus
// visée (cf. TestHiddenSelectionNotTargeted).
func TestSelectedStacks(t *testing.T) {
	m := newTestModel([]compose.Stack{
		stack("a", "", 1, 1, 0), stack("b", "", 1, 1, 0), stack("c", "", 1, 1, 0),
	})
	m.selected["a"] = true
	m.selected["c"] = true

	got := m.selectedStacks()
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("selectedStacks = %v, want [a c]", got)
	}
}

// TestListRowsHeaderForMultiStackGroup : un groupe de >1 stack reçoit un en-tête
// navigable ; un groupe d'une seule stack n'en a pas.
func TestListRowsHeaderForMultiStackGroup(t *testing.T) {
	m := newTestModel([]compose.Stack{
		stack("a", "media", 1, 1, 0),
		stack("b", "media", 1, 1, 0),
		stack("solo", "infra", 1, 1, 0),
	})

	var headers []string
	for _, r := range m.listRows() {
		if r.header {
			headers = append(headers, r.group)
		}
	}
	if len(headers) != 1 || headers[0] != "media" {
		t.Fatalf("en-têtes = %v, want [media] (infra n'a qu'une stack)", headers)
	}
}

// TestFoldHidesMembers : replier un groupe masque ses stacks mais conserve
// l'en-tête ; visibleStacks ne renvoie alors que les stacks restées visibles.
func TestFoldHidesMembers(t *testing.T) {
	m := newTestModel([]compose.Stack{
		stack("a", "media", 1, 1, 0),
		stack("b", "media", 1, 1, 0),
		stack("solo", "infra", 1, 1, 0),
	})
	m.foldedGroups["media"] = true
	m.invalidateRows()

	vis := m.visibleStacks()
	if len(vis) != 1 || vis[0].Name != "solo" {
		t.Fatalf("visibleStacks (media replié) = %v, want [solo]", vis)
	}
	// L'en-tête media reste présent même replié.
	var hasHeader bool
	for _, r := range m.listRows() {
		if r.header && r.group == "media" {
			hasHeader = true
		}
	}
	if !hasHeader {
		t.Error("l'en-tête du groupe replié devrait rester affiché")
	}
}

// TestRenderStackLineFits : un nom trop long est tronqué pour que la ligne
// tienne dans la colonne (sinon elle wrappe et décale la liste).
func TestRenderStackLineFits(t *testing.T) {
	s := compose.Stack{Name: strings.Repeat("tres-long-nom/", 10), Running: 1, Total: 1, Orphaned: true}
	for _, cursor := range []bool{false, true} {
		for _, indented := range []bool{false, true} {
			if w := lipgloss.Width(renderStackLine(s, cursor, false, true, 40, indented, 200, rowUsage{})); w > 40 {
				t.Errorf("cursor=%v indented=%v : largeur %d > 40", cursor, indented, w)
			}
		}
	}
}

// TestRenderStackLineCursorShowsSelection : sous le curseur, une stack
// sélectionnée garde son marqueur ┃.
func TestRenderStackLineCursorShowsSelection(t *testing.T) {
	s := compose.Stack{Name: "a", Running: 1, Total: 1}
	if !strings.Contains(renderStackLine(s, true, false, true, 40, false, 1, rowUsage{}), "┃") {
		t.Error("marqueur de sélection absent sous le curseur")
	}
	if strings.Contains(renderStackLine(s, true, false, false, 40, false, 1, rowUsage{}), "┃") {
		t.Error("marqueur affiché sur une stack non sélectionnée")
	}
}

// Un glyphe par état, et pas seulement une couleur : sous le curseur, la
// ligne est en texte brut, et l'en-tête d'un dossier replié ou le résumé du
// volet n'ont pas de mot d'état. Le pire état du dossier se lit donc aussi
// sur son en-tête surligné.
func TestStateGlyphsWithoutColor(t *testing.T) {
	seen := map[string]compose.State{}
	for _, st := range []compose.State{compose.StateRunning, compose.StatePartial, compose.StateUnhealthy, compose.StateStopped, compose.StateUnknown} {
		g, _ := stateStyle(st)
		if prev, dup := seen[g]; dup {
			t.Errorf("%v et %v partagent le glyphe %q", prev, st, g)
		}
		seen[g] = st
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("%q : %d colonnes, want 1", g, w)
		}
	}
	media := []compose.Stack{stack("media/a", "media", 1, 1, 0), stack("media/b", "media", 0, 1, 0), stack("media/c", "media", 1, 2, 0)}
	row := listRow{header: true, group: "media", size: 3, gc: groupCountsOf(media)["media"]}
	if got := renderGroupHeader(row, true, 40); !strings.Contains(got, "■") {
		t.Errorf("en-tête surligné d'un dossier avec une arrêtée : %q, want ■", got)
	}
	if got := ansi.Strip(groupRollup(media)); got != "● 1  ◐ 1  ■ 1" {
		t.Errorf("résumé du dossier : %q", got)
	}
}

// TestRenderErrorNoSudo : l'écran d'accès refusé ne conseille pas sudo (il
// créerait ~/.dockstack en root), et un « permission denied » hors Docker
// n'est pas présenté comme un refus d'accès Docker.
func TestRenderErrorNoSudo(t *testing.T) {
	out := renderError(errors.New("permission denied while trying to connect to the docker API at unix:///var/run/docker.sock"), 120, "")
	if !strings.Contains(out, "Accès Docker refusé") || strings.Contains(out, "sudo dockstack") {
		t.Errorf("écran d'accès Docker inattendu :\n%s", out)
	}
	for _, msg := range []string{"open /srv/stacks: permission denied", "open /srv/docker: permission denied", "open /opt/docker-stacks/app: permission denied"} {
		if strings.Contains(renderError(errors.New(msg), 120, ""), "Accès Docker refusé") {
			t.Errorf("%q : un dossier illisible ne doit pas s'afficher comme un refus d'accès Docker", msg)
		}
	}
	// Démon distant (tcp) : pas de docker.sock dans le message.
	if !strings.Contains(renderError(errors.New("permission denied while trying to connect to the Docker daemon socket at tcp://h:2376"), 120, ""), "Accès Docker refusé") {
		t.Error("refus d'accès au démon non reconnu")
	}
}
