package tui

import (
	"errors"
	"github.com/Bloopps/dockstack/internal/backup"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
)

// TestHeaderFooterFitWidth : aucune ligne d'en-tête ni de footer ne dépasse la
// largeur du terminal (le moteur de rendu couperait en silence), et les
// raccourcis épinglés (aide, quitter) restent visibles même en 60 colonnes.
func TestHeaderFooterFitWidth(t *testing.T) {
	metrics := monitor.Metrics{CPUPercent: 45, CPUCores: make([]int, 16), NCores: 16,
		MemUsedGB: 12.3, MemTotalGB: 31.2, MemPercent: 40, SwapUsedGB: 1.2, SwapTotalGB: 4,
		SwapPercent: 30, DiskUsedGB: 149, DiskTotalGB: 194, DiskPercent: 82,
		DiskReadKBs: 12345, DiskWriteKBs: 2345, Load1: "10.52", Load5: "10.61", Load15: "10.70", Uptime: "112d 13h"}

	for _, w := range []int{60, 80, 100, 110, 120, 140, 200} {
		m := New(&config.Config{StackDir: "/home/ubuntu/un/chemin/de/stacks/assez/long"})
		m.width, m.height, m.loading = w, 40, false
		m.metrics = metrics
		m.stacks = []compose.Stack{{Name: "a", Running: 1, Total: 1}}
		m.invalidateRows()

		h := renderHeader(m.metrics, m.history, m.stackCounts(), &compose.DockerUsage{Running: 39, Containers: 40, ImagesSize: 85 << 30, ImagesReclaimable: 24 << 30, ImagesOK: true, VolumesSize: 5 << 30, VolumesOK: true}, w, 100, true)
		for _, l := range strings.Split(h, "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("w=%d : ligne d'en-tête de %d colonnes", w, lw)
			}
		}
		if got, want := lipgloss.Height(h), headerHeight(m.metrics.NCores, w, 100); got != want {
			t.Errorf("w=%d : en-tête de %d lignes, headerHeight=%d", w, got, want)
		}

		for _, sel := range []bool{false, true} {
			m.selected = map[string]bool{"a": sel}
			f := m.renderFooter()
			if lw := lipgloss.Width(f); lw > w {
				t.Errorf("w=%d sélection=%v : footer de %d colonnes", w, sel, lw)
			}
			// Le bloc de droite (sortie, aide) ne cède jamais la place.
			exit := "quitter"
			if sel {
				exit = "décocher"
			}
			if !strings.Contains(f, "aide") || !strings.Contains(f, exit) {
				t.Errorf("w=%d sélection=%v : aide/%s absents du footer %q", w, sel, exit, f)
			}
		}
	}
}

// TestListFooterContext : le pied de page de la liste sert à naviguer. Les
// actions sur la stack sont dans le volet de droite ; elles ne reviennent dans
// le pied de page que si le volet est masqué faute de largeur, après
// « ↩ actions », qui reste visible en 80 colonnes. Il ne change qu'avec le
// mode (sélection).
func TestListFooterContext(t *testing.T) {
	m := newTestModel([]compose.Stack{
		stack("solo", "solo", 1, 1, 0),
		stack("g/a", "g", 1, 1, 0), stack("g/b", "g", 0, 1, 0),
	})
	m.cfg.ShowAll = true // g/b est arrêtée : l'afficher pour avoir un dossier à 2 stacks
	m.loading = false    // liste lue : pendant le chargement, le pied se réduit à q
	m.invalidateRows()
	has := func(f, s string) bool { return strings.Contains(ansi.Strip(f), s) }

	for _, w := range []int{400, 90, 80} { // volet visible, puis masqué
		m.width = w
		for i, r := range m.listRows() {
			m.cursor = i
			f := m.renderFooter()
			switch {
			case r.header:
				if !has(f, "replier") {
					t.Errorf("w=%d en-tête : footer %q", w, ansi.Strip(f))
				}
			case r.isStack():
				if !has(f, "actions") {
					t.Errorf("w=%d stack : « ↩ actions » absent : %q", w, ansi.Strip(f))
				}
				if lw := lipgloss.Width(f); lw > w {
					t.Errorf("w=%d stack : pied de %d colonnes", w, lw)
				}
				if got := has(f, "restart"); got != (w < minPreviewTermWidth) {
					t.Errorf("w=%d stack : actions dans le pied de page = %v, want %v", w, got, w < minPreviewTermWidth)
				}
			}
		}
	}
	m.width = 400
	m.selected["solo"] = true
	if f := m.renderFooter(); !has(f, "cochée") || !has(f, "toutes les actions") || !has(f, "décocher") {
		t.Errorf("sélection : footer %q", ansi.Strip(f))
	}
}

// TestInactiveHiddenByDefault : les stacks jamais déployées sont masquées et
// résumées en une ligne informative que le curseur saute ; a les affiche ;
// un filtre les inclut.
func TestInactiveHiddenByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel([]compose.Stack{stack("up", "up", 1, 1, 0), stack("dev", "dev", 0, 0, 0)})
	rows := m.listRows()
	if len(rows) != 2 || !rows[1].more || !strings.Contains(rows[1].note, "1 non déployée") {
		t.Fatalf("lignes = %+v, want [up, ligne « 1 non déployée… »]", rows)
	}
	m.moveCursor(1)
	if m.cursor != 0 {
		t.Fatal("la ligne « non déployées » doit être sautée par le curseur")
	}

	out, _ := m.handleListKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = out.(Model)
	if !m.cfg.ShowAll || len(m.visibleStacks()) != 2 {
		t.Fatal("a doit afficher les non déployées")
	}

	m.cfg.ShowAll = false
	m.filter.SetValue("dev")
	m.invalidateRows()
	if s := m.shownStacks(); len(s) != 1 || s[0].Name != "dev" {
		t.Errorf("filtre « dev » : %v, want la stack inactive trouvée", s)
	}
}

// TestActionPanelMatchesExec : chaque entrée affichée du panneau d'actions
// exécute l'action qu'elle annonce (régression : « Compose » manquait à
// l'affichage, et « Retour » lançait Remove).
func TestActionPanelMatchesExec(t *testing.T) {
	s := stack("web", "web", 1, 1, 0)
	s.ComposeFile = ""
	m := newTestModel([]compose.Stack{s})
	m.client = &fakeClient{}
	panel := ansi.Strip(buildActionPanel(s, -1, 100, 120))
	for _, a := range stackActions {
		if !strings.Contains(panel, a.label) {
			t.Errorf("entrée %q absente du panneau", a.label)
		}
	}
	for i, a := range stackActions {
		m = withPanel(m)
		m.actionCursor = i
		out, _ := m.execStackAction(s)
		got := out.(Model).view
		want := map[string]view{"compose": viewCompose, "back": viewList, "remove": viewConfirm,
			"down": viewConfirm, "restart": viewConfirm, "recreate": viewConfirm}[a.id]
		if want != 0 && got != want {
			t.Errorf("entrée %d « %s » : vue %v, want %v", i, a.label, got, want)
		}
	}
}

// Le panneau d'actions dit l'état comme le volet (« ● en marche  4/4
// conteneurs », plus de « Running (4/4) »), s'élargit pour ne pas couper un
// long nom de service sans dépasser l'écran, et place les stacks cochées
// sous le titre, là où sont les services d'une stack seule.
func TestActionPanelStateWidthAndOrder(t *testing.T) {
	s := capStack("immich", svc("immich-server", 1), svc("immich-machine-learning", 1), svc("redis", 1), svc("database", 1))
	panel := ansi.Strip(buildActionPanel(s, 0, 40, 120))
	if !strings.Contains(panel, "● en marche  4/4 conteneurs") || strings.Contains(panel, "Running") {
		t.Errorf("état du panneau :\n%s", panel)
	}
	if !strings.Contains(panel, "immich-machine-learning ") {
		t.Errorf("nom de service coupé :\n%s", panel)
	}
	if got := ansi.Strip(buildActionPanel(stack("dev", "", 0, 0, 0), 0, 40, 120)); !strings.Contains(got, "○ non déployée") || strings.Contains(got, "conteneurs") {
		t.Errorf("stack non déployée : pas de compteur :\n%s", got)
	}
	for _, termW := range []int{36, 50, 80} {
		long := capStack("x", svc(strings.Repeat("nom-de-service-", 5), 1), svc("b", 1))
		if w := lipgloss.Width(buildActionPanel(long, 0, 40, termW)); w > max(termW-4, panelMinWidth) || w > panelMaxWidth {
			t.Errorf("écran de %d colonnes : panneau de %d colonnes", termW, w)
		}
	}

	sel := []compose.Stack{stack("media/jellyfin", "media", 1, 1, 0), stack("media/prowlarr", "media", 1, 1, 0)}
	group := ansi.Strip(buildGroupPanel(sel, 0, 40, 120))
	if i, j := strings.Index(group, "media/prowlarr"), strings.Index(group, "Up"); i < 0 || j < 0 || i > j {
		t.Errorf("stacks cochées attendues sous le titre, avant les actions :\n%s", group)
	}
}

// TestExcludeFlow : x puis confirmation retire la stack (et, sur un en-tête,
// tout le dossier) de la liste et l'enregistre dans la config.
func TestExcludeFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel([]compose.Stack{
		stack("keep", "keep", 1, 1, 0),
		stack("dev/a", "dev", 1, 1, 0), stack("dev/b", "dev", 1, 1, 0),
	})
	for i, r := range m.listRows() {
		if r.header && r.group == "dev" {
			m.cursor = i
		}
	}
	out, _ := m.handleListKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = out.(Model)
	if m.view != viewConfirm {
		t.Fatalf("x : view = %v, want viewConfirm", m.view)
	}
	out, _ = m.handleConfirmKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = out.(Model)
	if len(m.stacks) != 1 || m.stacks[0].Name != "keep" {
		t.Errorf("après exclusion : %v, want [keep]", m.stacks)
	}
	if len(m.cfg.Exclude) != 1 || m.cfg.Exclude[0] != "dev" || !m.cfg.IsExcluded("dev/a") {
		t.Errorf("Exclude = %v", m.cfg.Exclude)
	}
	if _, ok := m.cursorStack(); !ok {
		t.Error("le curseur doit retomber sur une stack")
	}
}

// TestViewFillsScreen : quelle que soit la longueur du contenu, chaque vue
// occupe exactement la hauteur de l'écran, footer sur la dernière ligne.
func TestViewFillsScreen(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "a", 1, 1, 0)})
	m.width, m.height, m.loading = 160, 40, false
	for _, v := range []view{viewList, viewHelp, viewBackup, viewProgress} {
		m.view = v
		if v == viewProgress {
			m.progress = newProgressState()
		}
		out := m.viewContent()
		if n := strings.Count(out, "\n") + 1; n != m.height {
			t.Errorf("vue %v : %d lignes, want %d", v, n, m.height)
		}
	}
}

// TestUppercaseQQuits : Q (verrouillage majuscules) quitte comme q, en deux
// frappes ; dans le filtre, Q reste une lettre.
func TestUppercaseQQuits(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "a", 1, 1, 0)})
	keyQ := tea.KeyPressMsg{Code: 'Q', Text: "Q"}
	out, _ := m.Update(keyQ)
	if !out.(Model).quitArmed {
		t.Fatal("premier Q : la sortie doit être armée")
	}
	if _, cmd := out.Update(keyQ); cmd == nil {
		t.Fatal("second Q : tea.Quit attendu")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("second Q : tea.Quit attendu")
	}

	m.filtering = true
	m.filter.Focus()
	out, _ = m.Update(keyQ)
	if got := out.(Model); got.quitArmed || got.filter.Value() != "Q" {
		t.Errorf("dans le filtre : quitArmed=%v valeur=%q, want lettre Q saisie", got.quitArmed, got.filter.Value())
	}
}

// TestHelpFits : sur un terminal large de hauteur modeste, l'aide tient
// entière (la fin — captures, quitter — était coupée sur une colonne).
func TestHelpFits(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height, m.loading, m.view = 200, 30, false, viewHelp
	out := ansi.Strip(m.viewContent())
	for _, want := range []string{"Captures d'état", "quitter", "et non déployées", "╰"} {
		if !strings.Contains(out, want) {
			t.Errorf("aide coupée : %q absent", want)
		}
	}
}

// Le footer du mode services garde toujours « retour » et « aide », même étroit.
func TestServiceFooterPinsExit(t *testing.T) {
	for _, w := range []int{100, 120, 160} {
		m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
		m.width, m.svcFocus, m.loading = w, true, false
		f := ansi.Strip(m.renderFooter())
		if !strings.Contains(f, "retour") || !strings.Contains(f, "aide") {
			t.Errorf("w=%d : retour/aide absents du footer %q", w, f)
		}
		if lipgloss.Width(f) > w {
			t.Errorf("w=%d : footer de %d colonnes", w, lipgloss.Width(f))
		}
	}
}

// Hors de la liste et du sélecteur de thème (dont l'en-tête est l'aperçu),
// l'en-tête se réduit au titre, au dossier et au nom de la vue : l'aide, le
// nettoyage ou le panneau d'actions récupèrent les 8 lignes et plus du
// tableau de bord. La hauteur annoncée (bodyHeight, progressBodyHeight)
// reste celle de l'en-tête rendu.
func TestReducedHeaderOutsideList(t *testing.T) {
	s := capStack("immich", svc("immich-server", 1), svc("machine-learning", 1), svc("redis", 1), svc("database", 1))
	for _, sz := range [][2]int{{80, 24}, {120, 36}} {
		m := newTestModel([]compose.Stack{s})
		m.cfg.StackDir = "/srv/stacks"
		m.width, m.height, m.loading = sz[0], sz[1], false
		m.metrics.NCores = 8
		m.client = &fakeClient{cleanup: sampleCleanup()}
		views := map[string]func(Model) Model{
			"Aide":            func(m Model) Model { m.view = viewHelp; return m },
			"Captures d'état": func(m Model) Model { m.view = viewBackup; return m },
			"Actions":         withPanel,
			"Nettoyage":       func(m Model) Model { return drive(t, m, keyMsg("n"), 0) },
			"Confirmation": func(m Model) Model {
				out, _ := m.askConfirm("Down — immich", "", []string{"immich"}, nil)
				return out.(Model)
			},
			"Opération": func(m Model) Model {
				m.view, m.progress, m.progressTitle = viewProgress, newProgressState(), "Up: immich"
				return m
			},
		}
		for title, set := range views {
			m := set(m)
			out := ansi.Strip(m.viewContent())
			lines := strings.Split(out, "\n")
			if len(lines) != m.height {
				t.Errorf("%dx%d %s : %d lignes, want %d", sz[0], sz[1], title, len(lines), m.height)
			}
			if !strings.Contains(lines[0], "/srv/stacks") || !strings.HasSuffix(strings.TrimSpace(lines[0]), title) || strings.Contains(lines[0], "o changer") {
				t.Errorf("%dx%d %s : ligne de titre %q", sz[0], sz[1], title, lines[0])
			}
			if !strings.HasPrefix(lines[1], "───") || strings.Contains(out, "Swap") {
				t.Errorf("%dx%d %s : tableau de bord encore affiché :\n%s", sz[0], sz[1], title, out)
			}
			if m.headerLines() != 2 || m.bodyHeight() != m.height-3 {
				t.Errorf("%s : headerLines %d, bodyHeight %d, want 2 et %d", title, m.headerLines(), m.bodyHeight(), m.height-3)
			}
		}
		// Le panneau d'actions retrouve l'état et les services en 80x24.
		m = withPanel(m)
		if out := ansi.Strip(m.viewContent()); !strings.Contains(out, "machine-learning") || !strings.Contains(out, "Retour") {
			t.Errorf("%dx%d : panneau d'actions sans les services :\n%s", sz[0], sz[1], out)
		}
		m.view = viewProgress
		if got, want := m.progressBodyHeight(), m.height-2-1-3; got != want {
			t.Errorf("progressBodyHeight = %d, want %d", got, want)
		}
		// La liste et le thème gardent le tableau de bord complet.
		for _, v := range []view{viewList, viewTheme} {
			m.view = v
			if m.headerLines() != headerHeight(m.metrics.NCores, m.width, m.height) || !strings.Contains(ansi.Strip(m.headerBlock()), "CPU") {
				t.Errorf("vue %v : en-tête complet attendu", v)
			}
		}
	}
}

// TestEveryViewFitsEveryScreenSize : chaque vue, de 40x12 à 200x60 (y compris
// sur un hôte à 128 cœurs), tient exactement dans l'écran : pas de panique,
// la hauteur du terminal et aucune ligne plus large que lui. Un en-tête qui
// grossit avec les cœurs, une ligne d'avertissement trop longue ou un cadre
// plus haut que l'écran poussaient jusqu'ici le pied de page dehors.
func TestEveryViewFitsEveryScreenSize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stacks := []compose.Stack{
		capStack("immich", svc("database", 1), svc("immich-server", 1), svc("redis", 1)),
		capStack("authelia", svc("authelia", 0)),
	}
	stacks[0].Group, stacks[1].Group = "media", "media"
	for _, cores := range []int{8, 128} {
		for _, sz := range [][2]int{{40, 12}, {60, 20}, {80, 24}, {120, 30}, {200, 60}} {
			base := newTestModel(stacks)
			base.stacks = sortByState(stacks)
			base.client = &fakeClient{stacks: stacks, autoDone: true, cleanup: sampleCleanup()}
			base.loading, base.lastRefresh = false, time.Now()
			base.width, base.height = sz[0], sz[1]
			base.metrics.NCores, base.metrics.CPUCores = cores, make([]int, cores)
			base.backups = []backup.Snapshot{{Date: time.Now().Add(-26 * time.Hour), StackDir: base.cfg.StackDir,
				Entries: []backup.Entry{{Name: "authelia"}}, Stacks: []string{"authelia"}}}
			base.invalidateRows()

			views := map[string]Model{
				"liste":      base,
				"bandeau":    base,
				"services":   press(base, "right"),
				"filtre svc": press(base, "right", "/", "r"),
				"thème":      press(base, "t"),
				"aide":       press(base, "?"),
				"confirm":    press(base, "d"),
				"panneau":    press(base, "enter"),
				"captures":   press(base, "b"),
				"nettoyage":  press(base, "n"),
				"dossier":    press(base, "o"),
				"sortie":     press(base, "q"),
				"coches":     press(base, "space"),
				"erreur": func() Model {
					m := base
					m.err = errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock")
					return m
				}(),
				"progression": progressSample(base),
			}
			for name, m := range views {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Errorf("%s %dx%d, %d cœurs : panique %v", name, sz[0], sz[1], cores, r)
						}
					}()
					lines := strings.Split(m.viewContent(), "\n")
					if len(lines) != sz[1] {
						t.Errorf("%s %dx%d, %d cœurs : %d lignes rendues, %d attendues", name, sz[0], sz[1], cores, len(lines), sz[1])
					}
					for i, l := range lines {
						if w := ansi.StringWidth(strings.TrimRight(ansi.Strip(l), " ")); w > sz[0] {
							t.Errorf("%s %dx%d, %d cœurs : ligne %d de %d colonnes : %q", name, sz[0], sz[1], cores, i+1, w, ansi.Strip(l))
							break
						}
					}
				}()
			}
		}
	}
}

// progressSample : la vue de progression d'une opération en cours.
func progressSample(m Model) Model {
	m.view, m.progress, m.progressTitle = viewProgress, newProgressState(), "Pull — 3 stacks"
	m.progressStart = time.Now().Add(-51 * time.Second)
	for _, ev := range []compose.ProgressEvent{
		{StackName: "immich", StackStarted: true},
		{StackName: "immich", Container: "Image lscr.io/linuxserver/sonarr:latest", Status: "Pulling", State: "working"},
	} {
		m.progress.apply(ev)
	}
	return m
}

// En-tête large : à 64 ou 128 cœurs, la grille des cœurs mangeait l'écran
// (31 lignes à 128 cœurs). Il retombe sur l'en-tête étroit, et la liste et le
// pied de page restent visibles.
func TestWideHeaderCappedWithManyCores(t *testing.T) {
	stacks := []compose.Stack{capStack("immich", svc("redis", 1))}
	m := newTestModel(stacks)
	m.loading, m.lastRefresh = false, time.Now()
	m.metrics.NCores, m.metrics.CPUCores = 128, make([]int, 128)
	for _, h := range []int{24, 30, 36} {
		m.width, m.height = 120, h
		if n := m.headerLines(); n != narrowHeaderLines {
			t.Errorf("120x%d, 128 cœurs : en-tête de %d lignes, %d attendues", h, n, narrowHeaderLines)
		}
		if s := ansi.Strip(m.viewContent()); !strings.Contains(s, "immich") || m.bodyHeight() < 8 {
			t.Errorf("120x%d : liste absente ou corps de %d lignes", h, m.bodyHeight())
		}
	}
	// Peu de cœurs : le gabarit large reste.
	m.metrics.NCores, m.metrics.CPUCores, m.width, m.height = 8, make([]int, 8), 120, 36
	if m.headerLines() == narrowHeaderLines {
		t.Error("8 cœurs en 120x36 : gabarit large attendu")
	}
}

// Pendant la saisie du filtre des services, ␣, e, + et ? sont des lettres :
// le pied n'annonce que ↩, ↑↓ et esc, comme pour le filtre de la liste.
func TestServiceFilterFooterOnlyTypingKeys(t *testing.T) {
	st := stack("a", "", 2, 2, 0)
	st.Services = []compose.ServiceStatus{{Name: "redis", Running: 1, Total: 1}, {Name: "web", Running: 1, Total: 1}}
	m := newTestModel([]compose.Stack{st})
	m.width, m.height, m.loading = 120, 36, false
	m = press(m, "right", "/", "r")
	f := strings.Join(strings.Fields(ansi.Strip(m.renderFooter())), " ")
	for _, bad := range []string{"cocher", "en défaut", "dépendances", "retour", "aide"} {
		if strings.Contains(f, bad) {
			t.Errorf("saisie du filtre des services : le pied annonce %q (%q)", bad, f)
		}
	}
	if !strings.Contains(f, "valider") || !strings.Contains(f, "effacer") {
		t.Errorf("pied de saisie incomplet : %q", f)
	}
}

// « o changer » ne figure que dans la liste : le sélecteur de thème et le
// mode services ignorent o.
func TestHeaderChangeHintOnlyInList(t *testing.T) {
	st := stack("a", "", 1, 1, 0)
	st.Services = []compose.ServiceStatus{{Name: "web", Running: 1, Total: 1}}
	base := newTestModel([]compose.Stack{st})
	base.width, base.height, base.loading = 120, 36, false
	first := func(m Model) string { return ansi.Strip(strings.Split(m.headerBlock(), "\n")[0]) }
	if !strings.Contains(first(base), "o changer") {
		t.Errorf("liste : « o changer » attendu : %q", first(base))
	}
	if h := first(press(base, "right")); strings.Contains(h, "o changer") {
		t.Errorf("services : « o changer » affiché, o sans effet : %q", h)
	}
	out, _ := base.openThemePicker()
	if h := first(out.(Model)); strings.Contains(h, "o changer") {
		t.Errorf("thèmes : « o changer » affiché, o sans effet : %q", h)
	}
}

// ↩ reste annoncé sous 120 colonnes : sur une sélection, c'est la seule porte
// vers Pull, Recreate et le panneau groupé ; sur un dossier, il replie.
func TestListFooterKeepsEnterWhenNarrow(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0), stack("b", "", 1, 1, 0)})
	m.width, m.height, m.loading = 80, 24, false
	m.selected["a"] = true
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, "↩") {
		t.Errorf("80 colonnes, sélection : ↩ absent du pied %q", f)
	}
	g := newTestModel([]compose.Stack{stack("a", "dir", 1, 1, 0), stack("b", "dir", 1, 1, 0)})
	g.width, g.height, g.loading = 60, 24, false
	g.cursor = 0
	if row, ok := g.cursorRow(); !ok || !row.header {
		t.Skip("pas d'en-tête de dossier sous le curseur")
	}
	if f := ansi.Strip(g.renderFooter()); !strings.Contains(f, "↩") {
		t.Errorf("60 colonnes, dossier : ↩ absent du pied %q", f)
	}
}

// Le pied d'erreur (R réessayer, o répertoire) est celui de la liste : l'aide
// et le nettoyage gardent le leur, où R et o n'agissent pas.
func TestErrorFooterOnlyOnList(t *testing.T) {
	m := newTestModel([]compose.Stack{stack("a", "", 1, 1, 0)})
	m.width, m.height, m.loading = 120, 36, false
	m.err = errors.New("Cannot connect to the Docker daemon")
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, "réessayer") {
		t.Errorf("liste en erreur : « réessayer » attendu : %q", f)
	}
	m.view, m.helpReturn = viewHelp, viewList
	if f := ansi.Strip(m.renderFooter()); strings.Contains(f, "réessayer") || !strings.Contains(f, "fermer") {
		t.Errorf("aide : pied de la liste en erreur au lieu du sien : %q", f)
	}
	m.view = viewCleanup
	if f := ansi.Strip(m.renderFooter()); strings.Contains(f, "réessayer") {
		t.Errorf("nettoyage : pied de la liste en erreur : %q", f)
	}
}

// Captures en 120 colonnes : « ⌫ supprimer » reste annoncé ; c'est « * épingler »
// qui cède la place à « ? aide ».
func TestCapturesFooterKeepsDelete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel([]compose.Stack{capStack("immich", svc("redis", 1))})
	m.loading, m.lastRefresh = false, time.Now()
	m.width, m.height, m.view = 120, 36, viewBackup
	m.backups = []backup.Snapshot{{Date: time.Now(), StackDir: m.cfg.StackDir, Stacks: []string{"immich"}}}
	if f := ansi.Strip(m.renderFooter()); !strings.Contains(f, "supprimer") {
		t.Errorf("120 colonnes : %q", f)
	}
}

// Le bandeau de capture ne dépasse jamais la largeur de l'écran, texte tronqué
// ou non.
func TestCaptureBannerFitsWidth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel([]compose.Stack{capStack("immich", svc("redis", 1)), capStack("authelia", svc("authelia", 0))})
	m.loading, m.lastRefresh = false, time.Now()
	m.backups = []backup.Snapshot{{Date: time.Now().Add(-26 * time.Hour), StackDir: m.cfg.StackDir,
		Entries: []backup.Entry{{Name: "authelia"}}, Stacks: []string{"authelia"}}}
	for _, w := range []int{40, 60, 80, 100, 200} {
		m.width = w
		if bw := ansi.StringWidth(m.captureBanner()); bw > w {
			t.Errorf("w=%d : bandeau de %d colonnes : %q", w, bw, ansi.Strip(m.captureBanner()))
		}
	}
}
