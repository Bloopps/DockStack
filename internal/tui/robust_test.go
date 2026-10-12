package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
)

// viewStates : chaque vue de l'application, peuplée.
func viewStates(t *testing.T) map[string]Model {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services:\n  web:\n    image: nginx\n\tports: [\"80:80\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := func() Model {
		m := screenModel(40)
		m.stacks[0].Dir, m.stacks[0].ComposeFile = dir, file
		m.invalidateRows()
		return m
	}
	out := map[string]Model{}
	out["liste"] = base()
	m := base()
	m.stacks = nil
	m.invalidateRows()
	out["liste vide"] = m
	m = base()
	m.filter.SetValue("zzzz-aucune")
	m.invalidateRows()
	out["filtre sans résultat"] = m
	m = base()
	m.svcFocus, m.svcStack = true, m.stacks[0].Name
	out["services"] = m
	m = base()
	m.previewCompose = true
	out["volet compose"] = m
	for _, v := range []view{viewHelp, viewBackup, viewAction} {
		m = base()
		m.view = v
		out[fmt.Sprintf("vue %d", v)] = m
	}
	m = base()
	m.backups = []backup.Snapshot{{Date: time.Now(), StackDir: "/srv", Stacks: []string{m.stacks[0].Name, "disparue"}}}
	m.view = viewBackup
	out["captures"] = m
	mm, _ := m.Update(keyMsg("enter"))
	out["relance"] = mm.(Model)
	m = base()
	m.client = &fakeClient{cleanup: sampleCleanup()}
	mm, _ = m.openCleanup()
	m = mm.(Model)
	m.cleanupRep = func() *compose.CleanupReport { r := sampleCleanup(); return &r }()
	m.cleanupLoading = false
	m.cleanupSel = map[string]bool{}
	for k := compose.CleanBuildCache; k <= compose.CleanAnonymousVolume; k++ {
		m.cleanupOpen[k] = true
	}
	m.cleanupCursor = 6
	out["nettoyage"] = m
	m = base()
	mm, _ = m.askConfirm("Down — x", "attention", []string{"a", strings.Repeat("très-long-", 30)}, nil)
	out["confirmation"] = mm.(Model)
	m = base()
	mm, _ = m.Update(keyMsg("t"))
	out["thèmes"] = mm.(Model)
	m = base()
	m.view, m.logStack = viewLogs, "stack"
	for i := range 50 {
		m.appendLogs([]compose.LogEntry{{Name: "web-1", Line: fmt.Sprintf("ligne %d 日本語 🎉", i)}})
	}
	out["logs"] = m
	m = base()
	mm, _ = m.openComposeView(m.stacks[0])
	out["compose"] = mm.(Model)
	m = progressModel(3, 3, 5)
	m.progress.apply(compose.ProgressEvent{StackName: "stack-00", Container: "Container x", Status: strings.Repeat("erreur très longue ", 20), State: "error"})
	out["progression"] = m
	m.progressDone = true
	out["progression finie"] = m
	m = base()
	m.view, m.dirPath, m.dirEntries = viewDirPicker, "/", []string{"[*] Choisir ce dossier", "etc/", strings.Repeat("x", 300) + "/"}
	out["dossiers"] = m
	m = base()
	m.view, m.dirPath, m.dirEntries = viewDirPicker, "/home", []string{"[*] Choisir ce dossier", "../"}
	for i := range 200 {
		m.dirEntries = append(m.dirEntries, fmt.Sprintf("dossier-%03d/", i))
	}
	m.dirCursor = 150
	out["dossiers nombreux"] = m
	return out
}

// TestViewsSurviveAnySize : aucune vue ne panique, quelle que soit la taille
// du terminal (même 1 × 1), et aucune ligne ne dépasse la largeur.
func TestViewsSurviveAnySize(t *testing.T) {
	sizes := [][2]int{{1, 1}, {2, 2}, {5, 3}, {20, 5}, {40, 10}, {80, 24}, {99, 30}, {100, 30}, {300, 100}}
	for name, m := range viewStates(t) {
		for _, sz := range sizes {
			m.width, m.height = sz[0], sz[1]
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s %dx%d : panique %v", name, sz[0], sz[1], r)
					}
				}()
				out := m.viewContent()
				if n := strings.Count(out, "\n") + 1; sz[1] >= 24 && n > sz[1] {
					t.Errorf("%s %dx%d : %d lignes, plus que l'écran", name, sz[0], sz[1], n)
				}
				if sz[0] < 60 {
					return // en dessous, l'en-tête et le pied sont tronqués par le terminal
				}
				for i, l := range strings.Split(out, "\n") {
					if w := ansi.StringWidth(l); w > sz[0] {
						t.Errorf("%s %dx%d : ligne %d de %d colonnes", name, sz[0], sz[1], i, w)
						break
					}
				}
			}()
		}
	}
}

// Démon injoignable : le message d'erreur est replié à la largeur de
// l'écran (il était coupé en bout de ligne par le terminal).
func TestErrorWrapsToWidth(t *testing.T) {
	err := fmt.Errorf("failed to connect to the docker API at unix:///tmp/zz.sock; check if the path is correct and if the daemon is running: dial unix /tmp/zz.sock: connect: no such file or directory")
	out := renderError(err, 80, "")
	for _, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("ligne de %d colonnes : %q", w, ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(out), "no such file or directory") {
		t.Fatal("la fin du message doit rester visible")
	}
}

// Tant que la liste n'a pas pu être lue, l'en-tête n'affirme pas « aucune
// déployée ».
func TestHeaderUnknownWhileLoading(t *testing.T) {
	m := scaleModel(0)
	m.stacks, m.loading = nil, true
	m.invalidateRows()
	if got := ansi.Strip(stacksSummary(m.headerCounts())); got != "…" {
		t.Fatalf("chargement : %q", got)
	}
	m.loading, m.err = false, fmt.Errorf("démon injoignable")
	if got := ansi.Strip(stacksSummary(m.headerCounts())); got != "…" {
		t.Fatalf("erreur : %q", got)
	}
	m.err = nil
	if got := ansi.Strip(stacksSummary(m.headerCounts())); got != "aucune déployée" {
		t.Fatalf("liste lue et vide : %q", got)
	}
}

// truncate borne la largeur affichée, pas le nombre de caractères : un nom
// en caractères larges (CJK, emoji) sortait deux fois trop large.
func TestTruncateDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"abcdef", 4, "abc…"},
		{"abc", 3, "abc"},
		{"abc", 0, ""},
		{"abcdef", 1, "…"},
		{"服务服务服务", 5, "服务…"},
		{"服务", 4, "服务"},
		{"📦📦📦", 4, "📦…"},
	} {
		got := truncate(tc.in, tc.max)
		if got != tc.want || ansi.StringWidth(got) > max(tc.max, 0) {
			t.Errorf("truncate(%q, %d) = %q (%d col), want %q", tc.in, tc.max, got, ansi.StringWidth(got), tc.want)
		}
	}
}

// Le filtre du mode services, en cours de saisie, reste dans le volet : on
// voit la fin de ce qu'on tape.
func TestServiceFilterFitsPane(t *testing.T) {
	m := svcTestModel(&fakeClient{})
	m.width = 104
	m = press(m, "right", "/")
	for _, r := range strings.Repeat("abcdefghij", 8) + "fin" {
		m = press(m, string(r))
	}
	out := m.viewContent()
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Fatalf("ligne %d de %d colonnes pour %d : %q", i, w, m.width, ansi.Strip(l))
		}
	}
	if !strings.Contains(ansi.Strip(out), "fin▏") {
		t.Fatal("la fin de la saisie doit rester visible")
	}
}

// ~/.dockstack en lecture seule (ou à root après un lancement en sudo) :
// enregistrer la config ou une capture échoue avec un message, sans perdre
// le réglage pour la session ni planter.
func TestReadOnlyConfigDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root écrit partout")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".dockstack")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)

	m := screenModel(5)
	m.client = &fakeClient{}
	showAll := m.cfg.ShowAll
	out, _ := m.Update(keyMsg("a"))
	m = out.(Model)
	if m.cfg.ShowAll == showAll || m.statusKind != statusErr || !strings.Contains(m.status, "non sauvegardée") {
		t.Fatalf("a : ShowAll=%v, statut %q (%v)", m.cfg.ShowAll, m.status, m.statusKind)
	}
	msg := saveBackup(m.cfg.ConfigDir(), m.cfg.StackDir, m.stacks)()
	out, _ = m.Update(msg)
	if m = out.(Model); m.statusKind != statusErr || m.status == "" {
		t.Fatalf("capture : statut %q (%v)", m.status, m.statusKind)
	}
	t.Logf("statut : %s", m.status)
}
