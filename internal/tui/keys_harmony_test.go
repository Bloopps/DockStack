package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
)

// q arme la sortie depuis toute sous-vue (elle ne revient plus en arrière) ;
// esc revient à la liste.
func TestQArmsQuitAndEscGoesBack(t *testing.T) {
	for _, v := range []view{viewAction, viewBackup, viewLogs, viewCompose} {
		m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
		m.view = v
		out, _ := m.Update(keyMsg("q"))
		if got := out.(Model); !got.quitArmed || got.view != v {
			t.Errorf("vue %v : q doit armer la sortie sans changer de vue (armé=%v, vue=%v)", v, got.quitArmed, got.view)
		}
		out, _ = m.Update(keyMsg("esc"))
		if got := out.(Model); got.view != viewList {
			t.Errorf("vue %v : esc doit revenir à la liste (vue=%v)", v, got.view)
		}
	}
}

// s = Stop dans la liste, confirmé comme Down.
func TestListStopKey(t *testing.T) {
	m := press(newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)}), "down", "s")
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Stop") {
		t.Fatalf("s doit demander confirmation de Stop (vue %v, %q)", m.view, m.confirmLabel)
	}
}

// keyViews : une vue de chaque sorte, prête à recevoir des touches (80x24).
func keyViews(t *testing.T) map[string]Model {
	t.Helper()
	base := func() Model {
		var stacks []compose.Stack
		for i := range 60 {
			stacks = append(stacks, stack(fmt.Sprintf("stack-%02d", i), "", 1, 1, 0))
		}
		m := newTestModel(stacks)
		m.width, m.height, m.loading = 80, 24, false
		return m
	}
	views := map[string]Model{"liste": base()}
	m := base()
	m.view = viewAction
	views["panneau"] = m
	m = base()
	m.view = viewBackup
	for i := range 50 {
		m.backups = append(m.backups, backup.Snapshot{Date: time.Now().Add(-time.Duration(i) * time.Hour)})
	}
	views["captures"] = m
	m = base()
	m.view, m.restoreCursor = viewRestorePick, 0
	for i := range 50 {
		m.restoreEntries = append(m.restoreEntries, captureEntry{name: fmt.Sprintf("s%02d", i)})
	}
	m.restoreSel = map[string]bool{}
	views["relance"] = m
	m = base()
	m.view, m.dirPath, m.dirEntries = viewDirPicker, "/srv", []string{dirChooseEntry, "../"}
	for i := range 100 {
		m.dirEntries = append(m.dirEntries, fmt.Sprintf("d%03d/", i))
	}
	views["dossiers"] = m
	m = base()
	out, _ := m.openComposeView(m.stacks[0])
	m = out.(Model)
	m.composeLines = strings.Split(strings.Repeat("ligne\n", 200), "\n")
	views["compose"] = m
	m = base()
	out, _ = m.startStackAction("logs", m.stacks[0])
	views["logs"] = logBatch(out.(Model), webLogs(300))
	m = base()
	m.client = &fakeClient{cleanup: sampleCleanup()}
	m = drive(t, m, keyMsg("n"), 0)
	for k := compose.CleanBuildCache; k <= compose.CleanAnonymousVolume; k++ {
		m.cleanupOpen[k] = true
	}
	views["nettoyage"] = m
	m = progressModel(10, 3, 0)
	m.width, m.height = 80, 24
	views["progression"] = m
	return views
}

// ? ouvre l'aide de toutes les vues (seules la liste et les services la
// proposaient, pas la page Nettoyage) et la fermer ramène à la vue d'où on
// l'a ouverte, état compris (mode services).
func TestHelpFromEveryView(t *testing.T) {
	views := keyViews(t)
	delete(views, "progression") // garde la main : esc y annule l'opération
	m := views["liste"]
	m.svcFocus, m.svcStack = true, m.stacks[0].Name
	views["services"] = m
	for name, m := range views {
		from := m.view
		m, _ = update(m, keyMsg("?"))
		if m.view != viewHelp {
			t.Errorf("%s : ? n'ouvre pas l'aide (vue %v)", name, m.view)
			continue
		}
		m, _ = update(m, keyMsg("esc"))
		if m.view != from {
			t.Errorf("%s : fermer l'aide ramène à la vue %v, want %v", name, m.view, from)
		}
		if name == "services" && !m.svcFocus {
			t.Error("services : le mode services est perdu en fermant l'aide")
		}
	}
}

// ? reste une lettre pendant une saisie, et n'interrompt pas une
// confirmation, qui attend sa réponse.
func TestHelpKeyIgnoredWhileTypingOrConfirming(t *testing.T) {
	views := keyViews(t)
	m := logKeys(views["logs"], "/", "?")
	if m.view != viewLogs || m.logFilter != "?" {
		t.Errorf("filtre des logs : vue %v, filtre %q, want « ? » saisi", m.view, m.logFilter)
	}
	m = views["captures"]
	m.captureLabeling = true
	if m, _ = update(m, keyMsg("?")); m.view != viewBackup || m.captureLabel != "?" {
		t.Errorf("nom de capture : vue %v, libellé %q", m.view, m.captureLabel)
	}
	out, _ := views["liste"].askConfirm("Down — x", "", []string{"x"}, nil)
	if m, _ = update(out.(Model), keyMsg("?")); m.view != viewConfirm {
		t.Errorf("confirmation : ? a ouvert la vue %v", m.view)
	}
}

// Dans l'aide, q arme la sortie comme partout (en la fermant, il fallait
// trois frappes), g/G et pgup/pgdn défilent au lieu de la fermer, et t n'y
// ouvre les thèmes que si elle a été ouverte sur la liste.
func TestHelpKeys(t *testing.T) {
	m := keyViews(t)["liste"]
	m, _ = update(m, keyMsg("?"))
	if m.helpMaxScroll() == 0 {
		t.Fatal("à 80x24 l'aide devrait défiler")
	}
	m, _ = update(m, keyMsg("q"))
	if m.view != viewHelp || !m.quitArmed {
		t.Fatalf("q : vue %v, armé %v, want aide et sortie armée", m.view, m.quitArmed)
	}
	if _, cmd := update(m, keyMsg("q")); cmd == nil {
		t.Fatal("second q : tea.Quit attendu")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("second q : tea.Quit attendu")
	}
	m, _ = update(m, keyMsg("G"))
	if m.view != viewHelp || m.quitArmed || m.helpScroll != m.helpMaxScroll() {
		t.Fatalf("G : vue %v, armé %v, défilement %d/%d", m.view, m.quitArmed, m.helpScroll, m.helpMaxScroll())
	}
	m, _ = update(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if want := max(m.helpMaxScroll()-page(m.bodyHeight()-1), 0); m.helpScroll != want {
		t.Errorf("pgup : défilement %d, want %d", m.helpScroll, want)
	}
	m, _ = update(m, keyMsg("g"))
	if m.view != viewHelp || m.helpScroll != 0 {
		t.Errorf("g : vue %v, défilement %d", m.view, m.helpScroll)
	}
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, "pgup/pgdn") || !strings.Contains(f, "quitter") {
		t.Errorf("pied de l'aide : %q", f)
	}

	m, _ = update(keyViews(t)["logs"], keyMsg("?"))
	if m, _ = update(m, keyMsg("t")); m.view != viewLogs {
		t.Errorf("t dans l'aide des logs : vue %v, want retour aux logs", m.view)
	}
}

// L'aide décrit la page Nettoyage et le défilement par page.
func TestHelpDescribesCleanupAndPaging(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 200, 50
	help := ansi.Strip(helpBox(m))
	for _, want := range []string{"Nettoyage  (n)", "déplier / replier", "réanalyser", "nettoyer la sélection", "pgup pgdn  page"} {
		if !strings.Contains(help, want) {
			t.Errorf("aide sans %q", want)
		}
	}
}

// pgup et pgdown avancent d'une page dans toutes les vues à liste ou à
// lire, et reviennent à leur point de départ.
func TestPageKeys(t *testing.T) {
	pos := map[string]func(Model) int{
		"liste":       func(m Model) int { return m.cursor },
		"captures":    func(m Model) int { return m.actionCursor },
		"relance":     func(m Model) int { return m.restoreCursor },
		"dossiers":    func(m Model) int { return m.dirCursor },
		"compose":     func(m Model) int { return m.composeScroll },
		"nettoyage":   func(m Model) int { return m.cleanupCursor },
		"progression": func(m Model) int { return m.progressMaxScroll() - m.progressScroll }, // 0 en haut
		"logs":        func(m Model) int { return m.logScroll },
	}
	for name, m := range keyViews(t) {
		get, ok := pos[name]
		if !ok {
			continue
		}
		down, up := tea.KeyPressMsg{Code: tea.KeyPgDown}, tea.KeyPressMsg{Code: tea.KeyPgUp}
		if name == "logs" || name == "progression" {
			down, up = up, down // vues ouvertes en bas : on remonte d'abord
		}
		start := get(m)
		m1, _ := update(m, down)
		moved := get(m1) - start
		if name == "logs" || name == "progression" {
			moved = -moved
		}
		if moved < 2 {
			t.Errorf("%s : une page déplace de %d", name, moved)
		}
		if m2, _ := update(m1, up); get(m2) != start {
			t.Errorf("%s : aller-retour d'une page : %d, want %d", name, get(m2), start)
		}
	}
}

// L'analyse du nettoyage anime son spinner sous l'aide : la chaîne de
// ticks rompue, il restait figé au retour sur la page.
func TestCleanupSpinnerUnderHelp(t *testing.T) {
	m := keyViews(t)["nettoyage"]
	m.cleanupLoading = true
	m, _ = update(m, keyMsg("?"))
	if _, cmd := update(m, m.spinner.Tick()); cmd == nil {
		t.Error("spinner arrêté sous l'aide pendant l'analyse")
	}
}

// Pieds de page : « ? aide » proposé dans les logs, le compose, le
// nettoyage et les captures ; en 80 colonnes, les logs gardent w et le
// compose tab.
func TestFootersOfferHelp(t *testing.T) {
	views := keyViews(t)
	feet := map[string]string{
		"logs":      views["logs"].logsFooter(),
		"compose":   strings.Split(renderComposeView(views["compose"]), "\n")[views["compose"].height-1],
		"nettoyage": views["nettoyage"].renderFooter(),
		"captures":  views["captures"].renderFooter(),
	}
	for name, f := range feet {
		f = strings.Join(strings.Fields(ansi.Strip(f)), " ")
		if !strings.Contains(f, "? aide") {
			t.Errorf("%s : « ? aide » absent du pied %q", name, f)
		}
	}
	if f := ansi.Strip(views["logs"].logsFooter()); !strings.Contains(f, "replier") || !strings.Contains(f, "pgup/pgdn") {
		t.Errorf("logs en 80 colonnes : w ou pgup/pgdn perdus : %q", f)
	}
	if f := ansi.Strip(feet["compose"]); !strings.Contains(f, "brut/résolu") {
		t.Errorf("compose en 80 colonnes : tab perdu : %q", f)
	}
	for _, name := range []string{"logs", "compose"} {
		m := views[name]
		m.width = 160
		f := m.logsFooter()
		if name == "compose" {
			f = strings.Split(renderComposeView(m), "\n")[m.height-1]
		}
		if f = strings.Join(strings.Fields(ansi.Strip(f)), " "); !strings.Contains(f, "↑↓ pgup/pgdn défiler · g/G haut/bas") {
			t.Errorf("%s en 160 colonnes : %q", name, f)
		}
	}
}
