package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// Non-régressions de la chasse aux bugs du 2026-09-29.

var (
	keyShiftR = tea.KeyPressMsg{Code: 'r', ShiftedCode: 'R', Mod: tea.ModShift, Text: "R"}
	keyShiftQ = tea.KeyPressMsg{Code: 'q', ShiftedCode: 'Q', Mod: tea.ModShift, Text: "Q"}
)

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}

// R et Q sont des lettres pendant la saisie du nom d'une capture et du filtre
// des services (avant : R rafraîchissait et disparaissait, Q devenait q).
func TestTypingKeepsShortcutLetters(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.client = &fakeClient{}
	m.view = viewBackup
	m.backups = []backup.Snapshot{{Date: time.Now(), Stacks: []string{"a"}}}
	m, _ = update(m, keyMsg("n"))
	m, _ = update(m, keyShiftR)
	m, _ = update(m, keyMsg("a"))
	m, _ = update(m, keyShiftQ)
	if m.captureLabel != "RaQ" || m.refreshing {
		t.Errorf("nom de capture = %q (rafraîchi=%v), want \"RaQ\"", m.captureLabel, m.refreshing)
	}

	m = newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.client = &fakeClient{}
	m.width, m.height = 140, 40
	m, _ = update(m, keyMsg("s"))
	m, _ = update(m, keyMsg("/"))
	m, _ = update(m, keyShiftR)
	if m.svcFilter != "R" || m.refreshing {
		t.Errorf("filtre services = %q (rafraîchi=%v), want \"R\"", m.svcFilter, m.refreshing)
	}
}

// L'avertissement de sortie s'affiche aussi dans les vues à pied de page
// propre, et q puis esc puis q ne quitte plus depuis la progression.
func TestQuitWarningVisibleEverywhere(t *testing.T) {
	for _, v := range []view{viewLogs, viewCompose, viewProgress, viewDirPicker} {
		m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
		m.width, m.height = 120, 40
		m.view, m.progressDone, m.progress = v, true, newProgressState()
		m.cfg.StackDir = "/tmp"
		m, _ = update(m, keyMsg("q"))
		if !m.quitArmed || !strings.Contains(ansi.Strip(m.viewContent()), "Appuyer à nouveau pour quitter") {
			t.Errorf("vue %v : sortie armée=%v sans avertissement visible", v, m.quitArmed)
		}
	}

	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.width, m.height = 120, 40
	m.view, m.progressDone, m.progress = viewProgress, true, newProgressState()
	m, _ = update(m, keyMsg("q"))
	m, _ = update(m, keyMsg("esc"))
	if m.quitArmed {
		t.Fatal("esc doit désarmer la sortie dans la vue de progression")
	}
	// Le 3e q doit ARMER (nouveau quitSeq) et non quitter.
	if after, _ := update(m, keyMsg("q")); after.quitSeq == m.quitSeq {
		t.Fatal("q, esc, q ne doit pas quitter")
	}
}

// Le panneau d'actions tient à l'écran : toutes les actions restent
// visibles, les services sont plafonnés (avant : 2 services sur 24 lignes
// cachaient Compose, Remove et Retour).
func TestActionPanelFitsScreen(t *testing.T) {
	for _, h := range []int{24, 30, 40} {
		for _, n := range []int{0, 2, 8, 20} {
			s := stack("big", "", max(n, 1), max(n, 1), 0) // déployée : une stack à 0 conteneur est masquée
			for i := 0; i < n; i++ {
				s.Services = append(s.Services, compose.ServiceStatus{Name: string(rune('a'+i)) + "svc", Running: 1, Total: 1})
			}
			m := newTestModel([]compose.Stack{s})
			m.width, m.height, m.loading = 100, h, false
			m.metrics.NCores = 8
			m, _ = update(m, keyMsg("enter"))
			m.actionCursor = len(stackActions) - 1
			out := ansi.Strip(m.viewContent())
			for _, a := range stackActions {
				if !strings.Contains(out, a.icon+"  "+a.label) {
					t.Errorf("h=%d, %d services : action %q invisible", h, n, a.label)
				}
			}
			if lines := strings.Count(out, "\n") + 1; lines != h {
				t.Errorf("h=%d, %d services : %d lignes rendues", h, n, lines)
			}
		}
	}
	// Sélection nombreuse : les actions groupées restent visibles.
	var many []compose.Stack
	m := newTestModel(nil)
	for i := 0; i < 30; i++ {
		st := stack(string(rune('a'+i%26))+string(rune('a'+i/26)), "", 1, 1, 0)
		many = append(many, st)
		m.selected[st.Name] = true
	}
	m.stacks = many
	m.invalidateRows()
	m.width, m.height, m.loading, m.view = 100, 24, false, viewAction
	m.metrics.NCores = 8
	out := ansi.Strip(m.viewContent())
	for _, a := range groupActions {
		if !strings.Contains(out, a.icon+"  "+a.label) {
			t.Errorf("groupe de 30 : action %q invisible", a.label)
		}
	}
}

// L'aide défile quand elle dépasse l'écran : la fin (Général) est atteignable
// et la hauteur rendue reste celle du terminal.
func TestHelpScrollsToEnd(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.width, m.height, m.loading = 100, 24, false
	m.metrics.NCores = 8
	m, _ = update(m, keyMsg("?"))
	if m.helpMaxScroll() == 0 {
		t.Fatal("à 100×24 l'aide devrait dépasser et défiler")
	}
	for i := 0; i < 100; i++ {
		m, _ = update(m, keyMsg("j"))
	}
	out := ansi.Strip(m.viewContent())
	if m.view != viewHelp || !strings.Contains(out, "Général") {
		t.Fatalf("fin de l'aide inatteignable (vue %v)", m.view)
	}
	if lines := strings.Count(out, "\n") + 1; lines != m.height {
		t.Errorf("%d lignes rendues, want %d", lines, m.height)
	}
	m, _ = update(m, keyMsg("x"))
	if m.view != viewList {
		t.Error("une autre touche doit fermer l'aide")
	}
}

// Exclure une stack liste aussi ses sous-stacks, que l'exclusion retire.
func TestExcludeListsChildren(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel([]compose.Stack{stack("immich", "immich", 0, 0, 0), stack("immich/immich", "immich", 1, 1, 0), stack("zz", "zz", 1, 1, 0)})
	m.cfg.ShowInactive = true
	m.invalidateRows()
	for i, r := range m.listRows() {
		if r.isStack() && r.stack.Name == "immich" {
			m.cursor = i
		}
	}
	m, _ = update(m, keyMsg("x"))
	if strings.Join(m.confirmNames, ",") != "immich,immich/immich" {
		t.Fatalf("confirmation = %v, want [immich immich/immich]", m.confirmNames)
	}
}

// Relancer une capture dont une stack est partielle passe par la prévision
// (ses conteneurs en marche peuvent être recréés).
func TestRestorePartialStackIsPlanned(t *testing.T) {
	root := t.TempDir()
	writeTestCompose(t, root, "p")
	fake := &fakeClient{autoDone: true, plan: []compose.Recreation{{Service: "web", Reason: "config modifiée"}}}
	m := backupModel(fake, []compose.Stack{stack("p", "p", 1, 2, 0)},
		backup.Snapshot{Date: time.Now(), Stacks: []string{"p"}, Legacy: true})
	m.cfg.StackDir = root
	m.view = viewBackup
	m = press(m, "enter", "enter")
	if m.view != viewConfirm || len(fake.called("up")) != 0 {
		t.Fatalf("vue %v, up appelé %d fois : la recréation doit être confirmée", m.view, len(fake.called("up")))
	}
}

// Pas de capture tant que l'état des stacks est inconnu.
func TestCaptureRefusedBeforeLoad(t *testing.T) {
	m := backupModel(&fakeClient{}, nil)
	m.view = viewBackup
	m, cmd := update(m, keyMsg("c"))
	if m.spinning || !m.statusErr {
		t.Fatalf("capture lancée sans liste chargée (spinning=%v, statut %q)", m.spinning, m.status)
	}
	_ = cmd
}

// Une capture prise depuis un autre répertoire n'est pas relancée ici.
func TestRestoreOtherDirRefused(t *testing.T) {
	m := backupModel(&fakeClient{}, []compose.Stack{stack("a", "a", 0, 1, 0)},
		backup.Snapshot{Date: time.Now(), StackDir: "/ailleurs", Stacks: []string{"a"}})
	m.view = viewBackup
	m = press(m, "enter")
	if m.view != viewBackup || !m.statusErr {
		t.Fatalf("vue %v : relance depuis un autre répertoire non refusée", m.view)
	}
}

// Retour arrière (touche « delete » des Mac) supprime aussi une capture.
func TestBackspaceDeletesCapture(t *testing.T) {
	m := backupModel(&fakeClient{}, nil, backup.Snapshot{Date: time.Now(), Stacks: []string{"a"}})
	m.view = viewBackup
	m = press(m, "backspace")
	if m.view != viewConfirm {
		t.Fatalf("vue %v, want viewConfirm", m.view)
	}
}

// Un second client prêt (deux R pendant l'init) est ignoré : pas de second
// abonnement aux événements.
func TestDuplicateClientReadyIgnored(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	m.client = &fakeClient{}
	if _, cmd := update(m, clientReadyMsg{client: &fakeClient{}}); cmd != nil {
		t.Fatal("client en double : aucune commande attendue")
	}
}

// Les tabulations des logs deviennent des espaces (le rendu les ignorait).
func TestSanitizeExpandsTabs(t *testing.T) {
	if got := sanitizeLogLine("ab\tc\td"); got != "ab      c       d" {
		t.Errorf("sanitizeLogLine = %q", got)
	}
}
