package tui

import (
	"errors"
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
	m, _ = update(m, keyMsg("right"))
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
	m.cfg.ShowAll = true
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
	if m.spinning || m.statusKind != statusErr {
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
	if m.view != viewBackup || m.statusKind != statusErr {
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

// Changer de dossier doit se découvrir sans connaître l'outil : l'en-tête
// rappelle « o changer » à côté du dossier, à toutes les largeurs, et le pied
// de page garde « o dossier » dès 120 colonnes (il tombait sous ~220).
func TestChangeDirDiscoverable(t *testing.T) {
	for _, w := range []int{80, 100, 120, 140, 160, 200} {
		m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
		m.width, m.height, m.loading = w, 40, false
		m.metrics.NCores = 8
		lines := strings.Split(ansi.Strip(m.viewContent()), "\n")
		if !strings.Contains(lines[0], "o changer") {
			t.Errorf("w=%d : en-tête sans rappel de o : %q", w, lines[0])
		}
		if foot := lines[len(lines)-1]; w >= 120 && !strings.Contains(foot, "o  dossier") {
			t.Errorf("w=%d : « o dossier » absent du pied de page : %q", w, foot)
		}
	}
}

// Par défaut la liste ne montre que les stacks qui tournent (partielles et
// unhealthy comprises) ; les arrêtées et non déployées sont résumées sur une
// ligne informative (sautée par le curseur, ↩ n'y fait rien), a les affiche,
// et le filtre les retrouve.
func TestOnlyRunningByDefault(t *testing.T) {
	m := newTestModel([]compose.Stack{
		stack("run", "run", 1, 1, 0), stack("part", "part", 1, 2, 0), stack("sick", "sick", 1, 1, 1),
		stack("stop", "stop", 0, 1, 0), stack("new", "new", 0, 0, 0),
	})
	var names []string
	for _, s := range m.visibleStacks() {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "run,part,sick" {
		t.Fatalf("stacks visibles = %s, want run,part,sick", got)
	}
	rows := m.listRows()
	last := rows[len(rows)-1]
	if !last.more || !strings.Contains(last.note, "1 arrêtée et 1 non déployée masquées") {
		t.Fatalf("ligne récapitulative = %+v", last)
	}

	for i := 0; i < len(rows); i++ {
		m.moveCursor(1)
	}
	if row, _ := m.cursorRow(); row.more {
		t.Fatal("le curseur ne doit pas se poser sur la ligne récapitulative")
	}
	t.Setenv("HOME", t.TempDir())
	m, _ = update(m, keyMsg("a"))
	if !m.cfg.ShowAll || len(m.visibleStacks()) != 5 {
		t.Fatalf("a doit tout afficher (ShowAll=%v, %d visibles)", m.cfg.ShowAll, len(m.visibleStacks()))
	}

	m.cfg.ShowAll = false
	m.filter.SetValue("sto")
	m.invalidateRows()
	if v := m.visibleStacks(); len(v) != 1 || v[0].Name != "stop" {
		t.Errorf("le filtre doit retrouver une stack arrêtée : %+v", v)
	}
}

// Grille de raccourcis cohérente : une lettre garde son sens partout.
func TestKeymapCoherent(t *testing.T) {
	// Panneau d'actions : les lettres de la liste y marchent, et y sont affichées.
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 120, 40, false
	m, _ = update(m, keyMsg("enter"))
	if out := ansi.Strip(m.viewContent()); !strings.Contains(out, "Down") || !strings.Contains(out, "d") {
		t.Fatalf("panneau sans lettres :\n%s", out)
	}
	m, _ = update(m, keyMsg("d"))
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Down") {
		t.Fatalf("d dans le panneau doit demander confirmation de Down (vue %v, %q)", m.view, m.confirmLabel)
	}

	// s = Stop dans la liste ; → entre dans les services, ← en sort, h et tab non.
	m = newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.width, m.height = 140, 40
	if got, _ := update(m, keyMsg("s")); got.view != viewConfirm || !strings.HasPrefix(got.confirmLabel, "Stop") {
		t.Fatalf("s doit demander confirmation de Stop (vue %v)", got.view)
	}
	m, _ = update(m, keyMsg("right"))
	for _, k := range []string{"h", "tab"} {
		if got, _ := update(m, keyMsg(k)); !got.svcFocus {
			t.Errorf("%s ne doit plus quitter les services", k)
		}
	}
	if got, _ := update(m, keyMsg("left")); got.svcFocus {
		t.Error("← doit quitter les services")
	}

	// g / G : haut / bas dans la liste, les logs et la progression.
	m = newTestModel([]compose.Stack{stack("a", "", 1, 1, 0), stack("b", "", 1, 1, 0), stack("c", "", 1, 1, 0)})
	m, _ = update(m, keyMsg("G"))
	if st, _ := m.cursorStack(); st.Name != "c" {
		t.Errorf("G dans la liste : curseur sur %q, want c", st.Name)
	}
	m, _ = update(m, keyMsg("g"))
	if st, _ := m.cursorStack(); st.Name != "a" {
		t.Errorf("g dans la liste : curseur sur %q, want a", st.Name)
	}

	m.view, m.height = viewLogs, 10
	m.logLines = make([]logLine, 50)
	m.logScroll = 20
	m, _ = update(m, keyMsg("g"))
	if m.logScroll != 0 {
		t.Errorf("g dans les logs : défilement %d, want 0", m.logScroll)
	}

	m = newTestModel(nil)
	m.width, m.height = 120, 20
	m.view, m.progressDone, m.progress = viewProgress, true, newProgressState()
	for i := 0; i < 40; i++ {
		m.progress.apply(compose.ProgressEvent{StackName: "s", Container: string(rune('a' + i)), State: "done"})
	}
	m, _ = update(m, keyMsg("g"))
	if m.progressScroll == 0 || m.progressScroll != m.progressMaxScroll() {
		t.Errorf("g dans la progression doit remonter en haut (défilement %d, max %d)", m.progressScroll, m.progressMaxScroll())
	}
	m, _ = update(m, keyMsg("G"))
	if m.progressScroll != 0 {
		t.Errorf("G dans la progression doit revenir en bas (défilement %d)", m.progressScroll)
	}
}

// Avec des stacks cochées, le volet annonce que les actions visent la
// sélection (et non la stack affichée sous le curseur).
func TestPaneActionsFollowSelection(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0), stack("b", "", 1, 1, 0)})
	m.width, m.height, m.loading = 140, 40, false
	if out := ansi.Strip(m.viewContent()); !strings.Contains(out, "r restart") || strings.Contains(out, "cochée") {
		t.Fatalf("sans sélection : actions de la stack attendues")
	}
	m.selected["b"] = true
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "Actions sur la stack cochée") {
		t.Fatalf("avec sélection : le volet doit viser la stack cochée :\n%s", out)
	}
}

// Le volet n'affiche pas deux fois le nom de la stack (la ligne du dossier le
// répétait, le nom étant le chemin relatif au dossier scanné).
func TestPaneNameNotRepeated(t *testing.T) {
	st := stack("wg-easy", "", 1, 1, 0)
	st.Dir = "/srv/wg-easy"
	out := ansi.Strip(strings.Join(previewDetail("/srv", st, nil, 60, 30, selState{}, previewExtra{}), "\n"))
	if n := strings.Count(out, "wg-easy"); n != 1 {
		t.Fatalf("nom affiché %d fois :\n%s", n, out)
	}
}

// Un chargement de la liste parti avant un changement de dossier, ou doublé
// par un chargement plus récent, ne doit pas remplacer la liste affichée.
func TestStaleStacksLoadIgnored(t *testing.T) {
	m := New(&config.Config{StackDir: "/nouveau"})
	now := time.Now()
	out, _ := m.Update(stacksLoadedMsg{stacks: []compose.Stack{{Name: "neuve"}}, dir: "/nouveau", at: now})
	m = out.(Model)

	out, _ = m.Update(stacksLoadedMsg{stacks: []compose.Stack{{Name: "ancienne"}}, dir: "/ancien", at: now.Add(time.Second)})
	if m2 := out.(Model); len(m2.stacks) != 1 || m2.stacks[0].Name != "neuve" {
		t.Fatalf("chargement d'un autre dossier appliqué : %v", m2.stacks)
	}
	out, _ = m.Update(stacksLoadedMsg{stacks: []compose.Stack{{Name: "vieille"}}, dir: "/nouveau", at: now.Add(-time.Second)})
	if m2 := out.(Model); m2.stacks[0].Name != "neuve" {
		t.Fatalf("chargement plus ancien appliqué : %v", m2.stacks)
	}
	// Une erreur périmée n'affiche pas l'écran d'erreur.
	out, _ = m.Update(stacksLoadedMsg{err: errors.New("démon injoignable"), dir: "/ancien", at: now.Add(time.Second)})
	if out.(Model).err != nil {
		t.Fatal("erreur d'un autre dossier affichée")
	}
}

// Une erreur de chargement remet l'indicateur « actualisation… » à zéro : il
// restait affiché jusqu'au chargement réussi suivant.
func TestStacksLoadErrorClearsRefreshing(t *testing.T) {
	m := New(&config.Config{StackDir: "/srv"})
	m.refreshing, m.manualR = true, true
	out, _ := m.Update(stacksLoadedMsg{err: errors.New("timeout"), dir: "/srv", at: time.Now()})
	got := out.(Model)
	if got.err == nil || got.refreshing || got.manualR || got.loading {
		t.Fatalf("err=%v refreshing=%v manualR=%v loading=%v", got.err, got.refreshing, got.manualR, got.loading)
	}
}

// Changer de dossier efface la sélection : elle est indexée par nom relatif,
// et une stack de même nom dans le nouveau dossier en aurait hérité.
func TestDirChangeClearsSelection(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("media/app", "media", 1, 1, 0)})
	m.client = &fakeClient{}
	m.selected["media/app"] = true
	m.view, m.dirPath = viewDirPicker, "/autre"
	m.dirEntries = []string{"[*] Choisir ce dossier"}
	t.Setenv("HOME", t.TempDir()) // cfg.Save
	out, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)
	if got.cfg.StackDir != "/autre" || len(got.selected) != 0 || len(got.stacks) != 0 {
		t.Fatalf("dossier %q, sélection %v, stacks %d", got.cfg.StackDir, got.selected, len(got.stacks))
	}
}
