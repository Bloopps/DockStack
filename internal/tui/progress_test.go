package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	m.progressTotal, m.progressOk = 1, 0
	lines, _ = m.progressLines(0, 100)
	// Le bilan compte l'échec (« 0/1 OK · 1 échec »), mais sans le bloc
	// récapitulatif « ✕ 1 échec » qui répéterait la cause.
	if strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "✕ 1 échec") {
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

// feedProgress livre des événements comme readProgressCmd ; done termine
// l'opération.
func feedProgress(m Model, done bool, evs ...compose.ProgressEvent) Model {
	m, _ = update(m, progressEventMsg{evs: evs, done: done, seq: m.progressSeq})
	return m
}

// opProgress : vue de progression vide d'une opération, sur la liste stacks.
func opProgress(title string, stacks ...compose.Stack) Model {
	m := newTestModel(stacks)
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 120, 40, false
	m.view, m.progressTitle, m.progress = viewProgress, title, newProgressState()
	return m
}

// Un pull en échec : compose émet l'erreur sur l'image (texte « Error »,
// message dans le détail) puis la renvoie, préfixée par le démon. Le message
// ne s'affiche qu'une fois dans le bloc de la stack ; une erreur de stack
// sans rapport avec ses ressources reste affichée.
func TestProgressStackErrorNotRepeated(t *testing.T) {
	msg := "manifest for app:404 not found: manifest unknown: manifest unknown"
	m := opProgress("Pull: app")
	m.width = 200
	m.progress.apply(compose.ProgressEvent{StackName: "app", Container: "Image app:404", Status: "Error", Details: msg, State: "error"})
	m.progress.apply(compose.ProgressEvent{StackName: "app", StackDone: true, Err: errors.New("Error response from daemon: " + msg)})
	lines, _ := m.progressLines(0, 100)
	if out := ansi.Strip(strings.Join(lines, "\n")); strings.Count(out, "not found") != 1 {
		t.Errorf("message répété :\n%s", out)
	}

	m.progress.apply(compose.ProgressEvent{StackName: "b", Container: "Container b", Status: "Error", Details: "conflit de nom", State: "error"})
	m.progress.apply(compose.ProgressEvent{StackName: "b", StackDone: true, Err: errors.New("réseau introuvable")})
	lines, _ = m.progressLines(0, 100)
	if out := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(out, "réseau introuvable") || !strings.Contains(out, "conflit de nom") {
		t.Errorf("erreur de stack distincte masquée :\n%s", out)
	}
}

// Sans erreur de stack, la cause est celle de la ressource : pour un pull,
// le message est dans le détail, le texte ne vaut que « Error ».
func TestFailureReasonPullDetails(t *testing.T) {
	ps := newProgressState()
	ps.apply(compose.ProgressEvent{StackName: "x", Container: "Image x:1", Status: "Error", Details: "manifest unknown", State: "error"})
	if got := failureReason(ps.byName["x"]); got != "Image x:1 : manifest unknown" {
		t.Errorf("failureReason = %q", got)
	}
	ps.apply(compose.ProgressEvent{StackName: "y", Container: "Container y", Status: "Conflict. name in use", State: "error"})
	if got := failureReason(ps.byName["y"]); got != "Container y : Conflict. name in use" {
		t.Errorf("failureReason (up) = %q", got)
	}
}

// Un échec reste lisible après la fermeture de la progression : le volet de
// la stack le date et en donne la cause (et le conseil) ; une réussite
// l'efface. Ni les catégories du nettoyage ni les stacks annulées ne sont
// retenues.
func TestLastFailKeptInPreview(t *testing.T) {
	web := stack("web", "", 0, 1, 0)
	m := opProgress("Pull — 2 stacks", web)
	m = feedProgress(m, true,
		compose.ProgressEvent{StackName: "web", Container: "Image web:404", Status: "Error", Details: "manifest unknown", State: "error"},
		compose.ProgressEvent{StackName: "web", StackDone: true, Err: errors.New("Error response from daemon: manifest unknown")},
		compose.ProgressEvent{StackName: "inconnue", StackDone: true, Err: errors.New("boom")},
	)
	f, ok := m.lastFail["web"]
	if !ok || f.op != "Pull" || f.reason != "Error response from daemon: manifest unknown" {
		t.Fatalf("échec retenu : %+v (%v)", f, ok)
	}
	if _, ok := m.lastFail["inconnue"]; ok {
		t.Error("échec retenu pour un nom absent de la liste")
	}
	m, _ = update(m, keyMsg("enter"))
	if m.view != viewList {
		t.Fatalf("↩ : vue %v", m.view)
	}
	out := ansi.Strip(strings.Join(previewDetail("/tmp", web, nil, 60, 30, selState{}, m.previewExtraFor(web)), "\n"))
	if !strings.Contains(out, "✕ Pull en échec à "+f.at.Format("15:04")+" · manifest unknown") ||
		!strings.Contains(out, "→ étiquette absente du registre") {
		t.Errorf("volet sans l'échec :\n%s", out)
	}

	// Réussite suivante : l'échec s'efface.
	m.view, m.progressTitle, m.progress, m.progressDone = viewProgress, "Up: web", newProgressState(), false
	m = feedProgress(m, true, compose.ProgressEvent{StackName: "web", StackDone: true})
	if _, ok := m.lastFail["web"]; ok {
		t.Error("échec gardé après une réussite")
	}

	// Nettoyage (catégories) et annulation : rien n'est retenu.
	m.progress, m.progressDone, m.progressReturn = newProgressState(), false, viewCleanup
	m = feedProgress(m, false, compose.ProgressEvent{StackName: "web", StackDone: true, Err: errors.New("refusé")})
	m.progressReturn, m.progressCancelling = viewList, true
	m = feedProgress(m, true, compose.ProgressEvent{StackName: "web", StackDone: true, Err: context.Canceled})
	if _, ok := m.lastFail["web"]; ok {
		t.Error("échec retenu pour une catégorie du nettoyage ou une annulation")
	}
}

// En fin d'opération (hors pull), l ouvre les logs de la première stack en
// échec, annoncée au pied, et y pose le curseur de la liste.
func TestProgressLogsOfFailedStack(t *testing.T) {
	// ko partielle : une stack arrêtée est masquée par défaut (a), le
	// curseur ne pourrait pas s'y poser.
	m := opProgress("Up — 2 stacks", stack("ok", "", 1, 1, 0), stack("ko", "", 1, 2, 0))
	m = feedProgress(m, true,
		compose.ProgressEvent{StackName: "ok", StackDone: true},
		compose.ProgressEvent{StackName: "ko", StackDone: true, Err: errors.New("port is already allocated")},
	)
	if s := ansi.Strip(m.viewContent()); !strings.Contains(s, "logs de ko") {
		t.Fatalf("pied sans l :\n%s", s)
	}
	m, _ = update(m, keyMsg("l"))
	if m.view != viewLogs || m.logStack != "ko" {
		t.Fatalf("l : vue %v, logs de %q", m.view, m.logStack)
	}
	if row, ok := m.cursorRow(); !ok || !row.isStack() || row.stack.Name != "ko" {
		t.Errorf("curseur pas sur la stack en échec : %+v", row)
	}

	// Pull : des logs n'aident pas pour une erreur de registre.
	m = opProgress("Pull — 2 stacks", stack("ok", "", 1, 1, 0), stack("ko", "", 0, 1, 0))
	m = feedProgress(m, true, compose.ProgressEvent{StackName: "ko", StackDone: true, Err: errors.New("manifest unknown")})
	if s := ansi.Strip(m.viewContent()); strings.Contains(s, "logs de") {
		t.Errorf("l proposé après un pull :\n%s", s)
	}
	if m, _ = update(m, keyMsg("l")); m.view != viewProgress {
		t.Errorf("l après un pull : vue %v", m.view)
	}
}

// Annulation confirmée : les stacks arrêtées ne sont pas des échecs. Celle
// qui avait commencé est « interrompue », celle encore en file « non
// lancée », et le bilan les compte à part.
func TestProgressCancelledNotFailed(t *testing.T) {
	m := opProgress("Pull — 3 stacks")
	m = feedProgress(m, false,
		compose.ProgressEvent{StackName: "a", StackQueued: true},
		compose.ProgressEvent{StackName: "b", StackQueued: true},
		compose.ProgressEvent{StackName: "c", StackQueued: true},
		compose.ProgressEvent{StackName: "a", StackStarted: true},
		compose.ProgressEvent{StackName: "b", StackStarted: true},
		compose.ProgressEvent{StackName: "a", StackDone: true},
		compose.ProgressEvent{StackName: "b", Container: "Image b", Status: "Pulling", State: "working"},
	)
	m.progressCancelling = true
	m = feedProgress(m, true,
		compose.ProgressEvent{StackName: "b", StackDone: true, Err: context.Canceled},
		compose.ProgressEvent{StackName: "c", StackDone: true, Err: errors.New("Error response from daemon: context canceled")},
	)
	lines, _ := m.progressLines(0, 100)
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"⊘ b  0/1  interrompue", "· c  non lancée", "⊘ Annulé — 1/3 OK · 1 interrompue · 1 non lancée"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q absent :\n%s", want, out)
		}
	}
	if strings.Contains(out, "échec") || strings.Contains(out, "context canceled") {
		t.Errorf("annulation présentée comme un échec :\n%s", out)
	}
}

// La question d'annulation dit ce qui reste fait ; la réponse attendue
// (esc = oui) tient toujours sur la ligne.
func TestProgressCancelQuestion(t *testing.T) {
	m := opProgress("Pull — 3 stacks")
	m.progressConfirmCancel = true
	for _, tc := range []struct {
		width  int
		back   view
		reason string
	}{
		{120, viewList, "Les stacks terminées le restent, celles en cours peuvent rester partielles"},
		{80, viewList, "Ce qui est fait reste fait"},
		{120, viewCleanup, "Ce qui est déjà supprimé le reste"},
	} {
		m.width, m.progressReturn = tc.width, tc.back
		lines := strings.Split(ansi.Strip(m.viewContent()), "\n")
		last := lines[len(lines)-1]
		if !strings.Contains(last, tc.reason) || !strings.Contains(last, "esc = oui") {
			t.Errorf("%d colonnes : %q", tc.width, last)
		}
	}
}

// « autre touche = non » : toute touche autre que esc désarme la question
// d'annulation. ↑↓, pgup/pgdn, g/G, ↩ et q la laissaient armée, et l'esc
// suivant annulait l'opération.
func TestProgressCancelQuestionOtherKeys(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyUp}, {Code: tea.KeyPgDown}, {Code: tea.KeyPgUp},
		{Code: 'g', Text: "g"}, {Code: 'G', Text: "G"}, {Code: tea.KeyEnter}, {Code: 'q', Text: "q"}} {
		m := opProgress("Pull — 2 stacks")
		cancelled := false
		m.progressCancel = func() { cancelled = true }
		m, _ = update(m, keyMsg("esc"))
		if !strings.Contains(ansi.Strip(m.viewContent()), "autre touche = non") {
			t.Fatal("question absente")
		}
		m, _ = update(m, k)
		m, _ = update(m, keyMsg("esc"))
		if cancelled || !m.progressConfirmCancel {
			t.Errorf("esc, %s, esc : annulée %v, question réarmée %v", k, cancelled, m.progressConfirmCancel)
		}
	}
}

// Le titre suit l'avancement d'une opération groupée (en cours, réussies,
// en échec, en attente, temps écoulé) ; une stack qui attend sa place est
// listée en gris. En fin d'opération, titre et bilan donnent la durée.
func TestProgressTitleShowsProgress(t *testing.T) {
	m := opProgress("Pull — 5 stacks")
	m.progressStart = time.Now().Add(-42 * time.Second)
	var evs []compose.ProgressEvent
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		evs = append(evs, compose.ProgressEvent{StackName: n, StackQueued: true})
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		evs = append(evs, compose.ProgressEvent{StackName: n, StackStarted: true})
	}
	evs = append(evs,
		compose.ProgressEvent{StackName: "a", StackDone: true},
		compose.ProgressEvent{StackName: "b", StackDone: true, Err: errors.New("boom")},
		compose.ProgressEvent{StackName: "c", Container: "Image c", Status: "Pulling", State: "working"},
	)
	m = feedProgress(m, false, evs...)
	if title := ansi.Strip(m.progressTitleLine()); !strings.Contains(title, "2 en cours · ✔ 1 · ✕ 1 · 1 en attente   0:42") {
		t.Errorf("titre : %q", title)
	}
	lines, _ := m.progressLines(0, 100)
	if out := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(out, "· e  en attente") {
		t.Errorf("stack en file absente :\n%s", out)
	}

	m = feedProgress(m, true,
		compose.ProgressEvent{StackName: "c", StackDone: true},
		compose.ProgressEvent{StackName: "d", StackDone: true},
		compose.ProgressEvent{StackName: "e", StackStarted: true},
		compose.ProgressEvent{StackName: "e", StackDone: true},
	)
	m.progressEnd = m.progressStart.Add(72 * time.Second)
	want := "4/5 OK · 1 échec · 1 min 12 s"
	if title := ansi.Strip(m.progressTitleLine()); !strings.Contains(title, want) {
		t.Errorf("titre final : %q", title)
	}
	lines, _ = m.progressLines(0, 100)
	if out := ansi.Strip(strings.Join(lines, "\n")); !strings.Contains(out, "  "+want) {
		t.Errorf("bilan sans durée :\n%s", out)
	}
}

// La fenêtre suit le bas : ce qui sort par le haut est signalé, avec les
// stacks encore en cours dont l'en-tête n'est plus visible. Tout en haut
// (g), plus de repère.
func TestProgressAboveHint(t *testing.T) {
	m := opProgress("Up — 2 stacks")
	m.height = 30
	var evs []compose.ProgressEvent
	for i := range 30 {
		evs = append(evs, compose.ProgressEvent{StackName: "longue", Container: fmt.Sprintf("Container longue-%d", i), Status: "Starting", State: "working"})
	}
	evs = append(evs, compose.ProgressEvent{StackName: "courte", Container: "Container courte", Status: "Started", State: "done"},
		compose.ProgressEvent{StackName: "courte", StackDone: true})
	m = feedProgress(m, false, evs...)
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "lignes au-dessus · en cours : longue") {
		t.Fatalf("repère du haut absent :\n%s", out)
	}
	if n := strings.Count(out, "\n") + 1; n != m.height {
		t.Errorf("%d lignes, want %d", n, m.height)
	}
	m, _ = update(m, keyMsg("g"))
	if out := ansi.Strip(m.viewContent()); strings.Contains(out, "au-dessus") || !strings.Contains(out, "longue  0/30") {
		t.Errorf("en haut : repère ou en-tête de stack incorrect :\n%s", out)
	}
}

// Durées : chronomètre dans le titre, en toutes lettres au bilan.
func TestProgressDurations(t *testing.T) {
	for _, tc := range []struct {
		d            time.Duration
		clock, human string
	}{
		{300 * time.Millisecond, "0:00", "< 1 s"},
		{42 * time.Second, "0:42", "42 s"},
		{65 * time.Second, "1:05", "1 min 5 s"},
		{3*time.Hour + 4*time.Minute + 5*time.Second, "3:04:05", "3 h 4 min"},
	} {
		if got := fmtClock(tc.d); got != tc.clock {
			t.Errorf("fmtClock(%v) = %q, want %q", tc.d, got, tc.clock)
		}
		if got := fmtDuration(tc.d); got != tc.human {
			t.Errorf("fmtDuration(%v) = %q, want %q", tc.d, got, tc.human)
		}
	}
}
