package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

func svcTestModel(fake *fakeClient) Model {
	st := stack("web", "", 2, 3, 0)
	st.Services = []compose.ServiceStatus{
		{Name: "app", Running: 1, Total: 1}, {Name: "cache", Running: 1, Total: 1}, {Name: "db", Total: 1},
	}
	m := newTestModel([]compose.Stack{st})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	return m
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "right":
			msg = tea.KeyPressMsg{Code: tea.KeyRight}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		var mm tea.Model
		if m.view == viewConfirm {
			mm, _ = m.handleConfirmKey(msg)
		} else {
			mm, _ = m.handleListKey(msg)
		}
		m = mm.(Model)
	}
	return m
}

// Cocher un service et lancer une action ne cible QUE lui (Stack.Only).
func TestServiceActionTargetsOnlyChecked(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right")
	if !m.svcFocus {
		t.Fatal("→ sur une stack doit donner le focus aux services")
	}
	// Curseur sur « app » : space coche et descend sur « cache » ; on coche aussi db.
	m = press(m, "space", "down", "space")
	if !m.svcSel["app"] || !m.svcSel["db"] || m.svcSel["cache"] {
		t.Fatalf("sélection inattendue : %v", m.svcSel)
	}
	// Up sur app (qui tourne) + db : confirmé (un service ciblé tourne déjà).
	m = press(m, "u")
	if m.view != viewConfirm {
		t.Fatalf("up sur un service en marche : confirmation attendue, vue %v", m.view)
	}
	m = press(m, "enter")
	if got := fake.calls["up"]; len(got) != 1 || strings.Join(got[0].Only, ",") != "app,db" {
		t.Fatalf("up ciblé : Only = %+v", got)
	}
	if m.svcFocus {
		t.Error("le focus services doit être rendu après l'action")
	}
}

// Sans coche, l'action vise le service sous le curseur ; un service arrêté
// se relance sans confirmation ; pull est direct ; down se confirme.
func TestServiceActionCursorAndConfirmRules(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right", "down", "down") // curseur sur db (arrêté)
	m2 := press(m, "u")
	if got := fake.calls["up"]; len(got) != 1 || strings.Join(got[0].Only, ",") != "db" {
		t.Fatalf("up sans coche : Only = %+v (vue %v)", got, m2.view)
	}
	fake.calls = nil
	m = press(m, "d")
	if m.view != viewConfirm || len(fake.calls["down"]) != 0 {
		t.Fatalf("down doit être confirmé avant exécution (vue %v)", m.view)
	}
	m = press(m, "esc") // annule : on retrouve le focus et la cible
	if !m.svcFocus {
		t.Error("annuler la confirmation doit conserver le mode services")
	}
	press(m, "p")
	if got := fake.calls["pull"]; len(got) != 1 || strings.Join(got[0].Only, ",") != "db" {
		t.Fatalf("pull direct : %+v", got)
	}
}

func TestServiceModeRenderAndEscape(t *testing.T) {
	m := press(svcTestModel(&fakeClient{}), "right", "space")
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "Services · 2/3 · 1 coché") || !strings.Contains(out, "cocher") {
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
