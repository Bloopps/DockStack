package tui

import (
	"testing"

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

func TestSelectedStacks(t *testing.T) {
	m := newTestModel([]compose.Stack{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
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
