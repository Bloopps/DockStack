package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// fakeClient implémente dockerService en mémoire : aucun démon Docker requis.
// C'est précisément ce que l'interface dockerService rend possible.
type fakeClient struct {
	stacks  []compose.Stack
	listErr error
	logsErr error
	logsCh  <-chan compose.LogEntry
}

func (f *fakeClient) ListStacks(context.Context, string) ([]compose.Stack, error) {
	return f.stacks, f.listErr
}
func (f *fakeClient) SubscribeEvents(context.Context) <-chan compose.DockerEvent { return nil }
func (f *fakeClient) LogsAsync(context.Context, compose.Stack) (<-chan compose.LogEntry, error) {
	return f.logsCh, f.logsErr
}
func (f *fakeClient) UpManyLive(context.Context, []compose.Stack, int) <-chan compose.ProgressEvent {
	return nil
}
func (f *fakeClient) DownManyLive(context.Context, []compose.Stack, int) <-chan compose.ProgressEvent {
	return nil
}
func (f *fakeClient) RestartManyLive(context.Context, []compose.Stack, int) <-chan compose.ProgressEvent {
	return nil
}
func (f *fakeClient) RecreateManyLive(context.Context, []compose.Stack, int) <-chan compose.ProgressEvent {
	return nil
}
func (f *fakeClient) PullManyLive(context.Context, []compose.Stack, int) <-chan compose.ProgressEvent {
	return nil
}
func (f *fakeClient) RemoveManyLive(context.Context, []compose.Stack, int) <-chan compose.ProgressEvent {
	return nil
}

// Garde-fou : fakeClient doit satisfaire l'interface comme le vrai client.
var _ dockerService = (*fakeClient)(nil)

// ---- commandes ----

func TestLoadStacksCmd(t *testing.T) {
	fake := &fakeClient{stacks: []compose.Stack{{Name: "a"}}}

	// Succès -> stacksLoadedMsg.
	switch msg := loadStacks(fake, "/x")().(type) {
	case stacksLoadedMsg:
		if len(msg) != 1 || msg[0].Name != "a" {
			t.Errorf("stacks = %v, want [a]", []compose.Stack(msg))
		}
	default:
		t.Fatalf("type de message = %T, want stacksLoadedMsg", msg)
	}

	// Erreur de listing -> fatalErrMsg (écran d'erreur, R pour réessayer).
	fake.listErr = errors.New("daemon HS")
	if _, ok := loadStacks(fake, "/x")().(fatalErrMsg); !ok {
		t.Errorf("erreur de listing : type = %T, want fatalErrMsg", loadStacks(fake, "/x")())
	}

	// Client nil -> fatalErrMsg (garde anti-panic).
	if _, ok := loadStacks(nil, "/x")().(fatalErrMsg); !ok {
		t.Error("client nil devrait produire un fatalErrMsg")
	}
}

func TestStartLogsErrorIsOpErr(t *testing.T) {
	fake := &fakeClient{logsErr: errors.New("pas de conteneur")}
	msg := startLogs(context.Background(), fake, compose.Stack{Name: "a"}, 1)()
	if _, ok := msg.(opErrMsg); !ok {
		t.Fatalf("échec LogsAsync : type = %T, want opErrMsg", msg)
	}
}

// ---- Update : routage des erreurs typées ----

func TestUpdateFatalErr(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	out, _ := m.Update(fatalErrMsg(errors.New("boom")))
	got := out.(Model)
	if got.err == nil {
		t.Error("fatalErrMsg devrait poser m.err (écran d'erreur)")
	}
	if got.loading {
		t.Error("loading devrait repasser à false")
	}
}

// TestUpdateOpErr : une erreur d'op récupérable ne doit PAS laisser l'utilisateur
// sur un écran vide — retour à la liste, erreur en barre de statut, et le
// contexte des logs (s'il avait été ouvert) doit être annulé.
func TestUpdateOpErr(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	m.view = viewLogs
	cancelled := false
	m.logCancel = func() { cancelled = true }

	out, _ := m.Update(opErrMsg{err: errors.New("logs HS")})
	got := out.(Model)

	if got.view != viewList {
		t.Errorf("view = %v, want viewList", got.view)
	}
	if got.err != nil {
		t.Error("opErrMsg ne doit pas poser m.err (pas d'écran fatal)")
	}
	if got.status == "" || !got.statusErr {
		t.Error("l'erreur d'op devrait s'afficher en barre de statut (rouge)")
	}
	if !cancelled {
		t.Error("le contexte des logs aurait dû être annulé")
	}
	if got.logCancel != nil {
		t.Error("logCancel devrait être remis à nil")
	}
}

func TestUpdateStacksLoadedClearsErr(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	m.err = errors.New("ancienne erreur transitoire")
	out, _ := m.Update(stacksLoadedMsg([]compose.Stack{{Name: "a"}}))
	got := out.(Model)
	if got.err != nil {
		t.Error("un chargement réussi doit effacer l'erreur précédente")
	}
	if got.loading || got.refreshing {
		t.Error("loading/refreshing devraient retomber à false")
	}
	if len(got.stacks) != 1 {
		t.Errorf("stacks = %d, want 1", len(got.stacks))
	}
}

// TestUpdateOpDone : le résultat d'une capture/restauration s'affiche en statut
// (rouge si erreur, vert sinon) sans éjecter l'utilisateur de sa vue.
func TestUpdateOpDone(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	m.opLabel = "Capture"

	out, _ := m.Update(opDoneMsg{err: errors.New("disque plein")})
	if got := out.(Model); !got.statusErr || got.status == "" {
		t.Errorf("opDone en erreur : statusErr=%v status=%q, want erreur affichée", got.statusErr, got.status)
	}

	out, _ = m.Update(opDoneMsg{})
	if got := out.(Model); got.statusErr {
		t.Error("opDone réussi ne devrait pas marquer statusErr")
	}
}
