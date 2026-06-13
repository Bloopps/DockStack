package tui

import (
	"errors"
	"testing"

	"github.com/Bloopps/dockstack/internal/compose"
)

// TestProgressApplyResources : les ressources de premier niveau sont créées
// dans l'ordre premier-vu, les sous-ressources (layers de pull) rangées sous
// leur parent, et les MAJ successives écrasent le même enregistrement.
func TestProgressApplyResources(t *testing.T) {
	ps := newProgressState()
	ps.apply(compose.ProgressEvent{StackName: "immich", Container: "db", Status: "Creating", State: "working"})
	ps.apply(compose.ProgressEvent{StackName: "immich", Container: "web", Status: "Creating", State: "working"})
	// Un layer de pull rattaché à "web".
	ps.apply(compose.ProgressEvent{StackName: "immich", Container: "layer1", ParentID: "web", Status: "Downloading", State: "working"})
	// MAJ de "db" : même ressource, nouveau statut.
	ps.apply(compose.ProgressEvent{StackName: "immich", Container: "db", Status: "Started", State: "done", Percent: 100})

	st := ps.byName["immich"]
	if st == nil {
		t.Fatal("stack immich absente de l'état")
	}
	// Deux racines, ordre premier-vu : db puis web.
	if len(st.roots) != 2 || st.roots[0].id != "db" || st.roots[1].id != "web" {
		t.Fatalf("racines = %v, want [db web]", st.roots)
	}
	// layer1 est un enfant de web, pas une racine.
	if got := st.children["web"]; len(got) != 1 || got[0].id != "layer1" {
		t.Fatalf("enfants de web = %v, want [layer1]", got)
	}
	// La MAJ de db a écrasé le même enregistrement (pas de doublon).
	db := st.byID["db"]
	if db.status != "Started" || db.state != "done" || db.percent != 100 {
		t.Errorf("db après MAJ = {%q %q %d}, want {Started done 100}", db.status, db.state, db.percent)
	}
	if db.doneAt.IsZero() {
		t.Error("doneAt devrait être posé quand l'état quitte 'working'")
	}
}

// TestProgressStackDone : StackDone marque la stack terminée et la première
// erreur l'emporte (les suivantes ne l'écrasent pas).
func TestProgressStackDone(t *testing.T) {
	ps := newProgressState()
	first := errors.New("première erreur")
	ps.apply(compose.ProgressEvent{StackName: "x", StackDone: true, Err: first})
	ps.apply(compose.ProgressEvent{StackName: "x", StackDone: true, Err: errors.New("seconde")})

	st := ps.byName["x"]
	if !st.done {
		t.Error("la stack devrait être marquée done")
	}
	if st.err != first {
		t.Errorf("err = %v, want la première (%v)", st.err, first)
	}
}
