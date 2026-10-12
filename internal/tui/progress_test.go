package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
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

// Fin d'opération groupée : les échecs sont récapitulés avec leur cause, sans
// récapitulatif quand tout réussit ou qu'il n'y a qu'une stack.
func TestProgressFailureRecap(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height, m.loading = 120, 40, false
	m.progress = newProgressState()
	m.progress.apply(compose.ProgressEvent{StackName: "ok", StackDone: true})
	m.progress.apply(compose.ProgressEvent{StackName: "ko", StackDone: true, Err: errors.New("pull access denied\nfor image x")})
	m.progressDone, m.progressTotal, m.progressOk = true, 2, 1
	lines, _ := m.progressLines(0, 100)
	out := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(out, "1 échec") || !strings.Contains(out, "ko  pull access denied for image x") {
		t.Fatalf("récapitulatif attendu :\n%s", out)
	}

	m.progress = newProgressState()
	m.progress.apply(compose.ProgressEvent{StackName: "seule", StackDone: true, Err: errors.New("boom")})
	lines, _ = m.progressLines(0, 100)
	if strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "échec") {
		t.Error("une seule stack : pas de récapitulatif redondant")
	}
}

// Les événements disponibles sont livrés par lots, dans l'ordre, et la
// sentinelle de fin lue au milieu d'un lot termine l'opération après eux.
func TestProgressBatching(t *testing.T) {
	ch := make(chan compose.ProgressEvent, 10)
	ch <- compose.ProgressEvent{StackName: "a", Container: "Container a-1", Status: "Starting", State: "working"}
	ch <- compose.ProgressEvent{StackName: "a", Container: "Container a-1", Status: "Started", State: "done"}
	ch <- compose.ProgressEvent{StackName: "a", StackDone: true}
	ch <- compose.ProgressEvent{StackName: "b", StackDone: true, Err: errors.New("image introuvable")}
	ch <- compose.ProgressEvent{AllDone: true}

	msg, ok := readProgressCmd(ch, 7)().(progressEventMsg)
	if !ok || len(msg.evs) != 4 || !msg.done || msg.seq != 7 {
		t.Fatalf("lot attendu de 4 événements terminé : %+v", msg)
	}
	m := New(&config.Config{StackDir: "/tmp"})
	m.progressSeq, m.progress, m.view = 7, newProgressState(), viewProgress
	out, _ := m.Update(msg)
	got := out.(Model)
	if !got.progressDone || got.progressTotal != 2 || got.progressOk != 1 {
		t.Fatalf("done=%v total=%d ok=%d", got.progressDone, got.progressTotal, got.progressOk)
	}
	if r := got.progress.byName["a"].byID["Container a-1"]; r == nil || r.status != "Started" || r.state != "done" {
		t.Fatalf("dernier état de la ressource non appliqué : %+v", r)
	}
	// Un lot d'une opération précédente est ignoré.
	m.progressSeq = 8
	if out, _ := m.Update(msg); out.(Model).progressDone {
		t.Fatal("lot périmé appliqué")
	}
	// Canal vide après un premier événement : le lot part sans attendre.
	ch <- compose.ProgressEvent{StackName: "c", Container: "x", State: "working"}
	if msg := readProgressCmd(ch, 1)().(progressEventMsg); len(msg.evs) != 1 || msg.done {
		t.Fatalf("lot d'un événement attendu : %+v", msg)
	}
}
