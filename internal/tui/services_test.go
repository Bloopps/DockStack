package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

func svcTestModel(fake *fakeClient) Model {
	st := stack("web", "", 2, 4, 0)
	st.Services = []compose.ServiceStatus{
		{Name: "app", Running: 1, Total: 1, Image: "nginx:alpine", Ports: "8080→80", Status: "Up 2 hours", DependsOn: []string{"db"}},
		{Name: "cache", Running: 1, Total: 1},
		{Name: "db", Total: 1, Status: "Exited (137) 3 hours ago"},
		{Name: "worker", Total: 1, Status: "Exited (1) 1 hour ago"},
	}
	m := newTestModel([]compose.Stack{st})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	return m
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
}

// send envoie une touche (confirmation ou liste) et rejoue les commandes
// asynchrones de prévision, comme le ferait la boucle bubbletea.
func send(m Model, k string) Model {
	var mm tea.Model
	var cmd tea.Cmd
	if m.view == viewConfirm {
		mm, cmd = m.handleConfirmKey(keyMsg(k))
	} else {
		mm, cmd = m.handleListKey(keyMsg(k))
	}
	m = mm.(Model)
	// Seule la commande de prévision est rejouée : les autres (minuteurs du
	// statut, spinner) dormiraient.
	if cmd != nil && m.status == "Analyse des changements…" {
		if pm, ok := cmd().(planUpMsg); ok {
			mm, _ = m.Update(pm)
			m = mm.(Model)
		}
	}
	return m
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		m = send(m, k)
	}
	return m
}

func onlyOf(fake *fakeClient, action string) string {
	if got := fake.calls[action]; len(got) == 1 {
		return strings.Join(got[0].Only, ",")
	}
	return "<aucun appel>"
}

// Up sur un service en marche : la confirmation liste précisément ce qui sera
// recréé ; le focus et la sélection survivent à l'action.
func TestServiceUpShowsPlanAndKeepsFocus(t *testing.T) {
	fake := &fakeClient{plan: []compose.Recreation{{Service: "app", Reason: "config modifiée"}}}
	m := press(svcTestModel(fake), "right", "space", "space") // coche app et cache (␣ avance)
	m = press(m, "u")
	if m.view != viewConfirm || len(m.confirmNames) != 1 || m.confirmNames[0] != "app · config modifiée" {
		t.Fatalf("confirmation avec prévision attendue (vue %v, noms %v)", m.view, m.confirmNames)
	}
	m = press(m, "enter")
	if got := onlyOf(fake, "up"); got != "app,cache" {
		t.Fatalf("up ciblé : Only = %s", got)
	}
	if !m.svcFocus || !m.svcSel["app"] || !m.svcSel["cache"] {
		t.Error("le focus et la sélection doivent survivre à l'action")
	}
}

// Rien à recréer : up part sans confirmation ; erreur de prévision : confirmation générique.
func TestServiceUpPlanEmptyOrFailed(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right", "u") // app tourne, plan vide
	if got := onlyOf(fake, "up"); got != "app" || m.view == viewConfirm {
		t.Fatalf("plan vide : up direct attendu (Only=%s, vue %v)", got, m.view)
	}
	fake = &fakeClient{planErr: context_deadline{}}
	m = press(svcTestModel(fake), "right", "u")
	if m.view != viewConfirm || m.confirmWarning != upWarning {
		t.Fatalf("prévision en échec : confirmation générique attendue (vue %v)", m.view)
	}
}

type context_deadline struct{}

func (context_deadline) Error() string { return "boom" }

// d arrête (stop, confirmé) ; D retire (down, confirmé) ; up sur un service
// arrêté et pull sont directs ; annuler garde le mode.
func TestServiceStopDownRules(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right", "down", "down") // curseur sur db (arrêté)
	m = press(m, "d")
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Stop") || len(fake.calls["stop"]) != 0 {
		t.Fatalf("d doit demander confirmation de Stop (vue %v, %q)", m.view, m.confirmLabel)
	}
	m = press(m, "esc")
	if !m.svcFocus {
		t.Error("annuler la confirmation doit conserver le mode services")
	}
	m = press(m, "d", "y")
	if got := onlyOf(fake, "stop"); got != "db" {
		t.Fatalf("stop ciblé : %s", got)
	}
	m = press(m, "D")
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Down") {
		t.Fatalf("D doit demander confirmation de Down (%q)", m.confirmLabel)
	}
	m = press(m, "y")
	if got := onlyOf(fake, "down"); got != "db" {
		t.Fatalf("down ciblé : %s", got)
	}
	m.view = viewList // fin de la vue de progression
	m = press(m, "u")
	m.view = viewList
	press(m, "p")
	if onlyOf(fake, "up") != "db" || onlyOf(fake, "pull") != "db" {
		t.Errorf("up (arrêté) et pull doivent être directs : up=%s pull=%s", onlyOf(fake, "up"), onlyOf(fake, "pull"))
	}
}

// e coche les services en défaut (pas les sains) ; le message est clair s'il n'y en a pas.
func TestServiceSelectFailing(t *testing.T) {
	m := press(svcTestModel(&fakeClient{}), "right", "e")
	if len(m.svcSel) != 2 || !m.svcSel["db"] || !m.svcSel["worker"] {
		t.Fatalf("e doit cocher db et worker, eu %v", m.svcSel)
	}
	healthy := svcTestModel(&fakeClient{})
	healthy.stacks[0].Services = healthy.stacks[0].Services[:2]
	healthy.invalidateRows()
	if m := press(healthy, "right", "e"); len(m.svcSel) != 0 || m.status != "Aucun service en défaut" {
		t.Errorf("aucun défaut : sélection %v, statut %q", m.svcSel, m.status)
	}
}

// Filtre : ne montre que les correspondances, et les actions ne visent que
// des services visibles (jamais un service coché mais masqué).
func TestServiceFilterScopesActions(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right", "e") // coche db et worker
	m = press(m, "/", "d", "b")                  // filtre « db »
	if m.svcFilter != "db" || !m.svcFiltering {
		t.Fatalf("filtre attendu, eu %q (saisie=%v)", m.svcFilter, m.svcFiltering)
	}
	m = press(m, "enter")
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "1/1 sur 4") || strings.Contains(out, "worker") {
		t.Fatalf("liste filtrée attendue :\n%s", out)
	}
	m = press(m, "p") // pull : cibles = cochés ET visibles → db seulement
	if got := onlyOf(fake, "pull"); got != "db" {
		t.Fatalf("service masqué ciblé : Only = %s", got)
	}
	m = press(m, "esc") // efface le filtre, garde le mode
	if m.svcFilter != "" || !m.svcFocus {
		t.Error("esc doit d'abord effacer le filtre")
	}
	m = press(m, "/", "x", "backspace", "esc")
	if m.svcFilter != "" || m.svcFiltering {
		t.Error("esc en saisie efface et quitte le filtre")
	}
}

// Dépendances : avertissement sur le service dont une dépendance est arrêtée ;
// + les inclut pour up.
func TestServiceDependencies(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right") // curseur sur app, qui dépend de db (arrêté)
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "dépend de db (arrêté)") || !strings.Contains(out, "image  nginx:alpine") || !strings.Contains(out, "8080→80") {
		t.Fatalf("détail du service incomplet :\n%s", out)
	}
	m = press(m, "+")
	if strings.Contains(ansi.Strip(m.viewContent()), "dépend de db") || !m.svcDeps {
		t.Error("avec +, l'avertissement de dépendance disparaît")
	}
	press(m, "p")
	if got := fake.calls["pull"]; len(got) != 1 || !got[0].WithDeps {
		t.Errorf("pull avec + doit poser WithDeps : %+v", got)
	}
	// Stop/Down ne suivent jamais les dépendances (elles ne sont pas les dépendantes).
	if m.svcDeps {
		m2 := press(m, "d", "y")
		if got := fake.calls["stop"]; len(got) != 1 || got[0].WithDeps {
			t.Errorf("stop ne doit pas inclure les dépendances : %+v (%v)", got, m2.view)
		}
	}
}

func TestServiceModeRenderAndEscape(t *testing.T) {
	m := press(svcTestModel(&fakeClient{}), "right", "space")
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "Services · 2/4 · 1 coché") || !strings.Contains(out, "cocher") {
		t.Fatalf("rendu du mode services incomplet :\n%s", out)
	}
	m = press(m, "esc") // 1er esc : décoche
	if !m.svcFocus || len(m.svcSel) != 0 {
		t.Fatal("1er esc : décocher seulement")
	}
	m = press(m, "esc") // 2e esc : quitte
	if m.svcFocus {
		t.Fatal("2e esc : quitter le mode services")
	}
}
