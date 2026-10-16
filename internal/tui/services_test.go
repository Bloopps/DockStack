package tui

import (
	"fmt"
	"os"
	"path/filepath"
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
	} else if m.view == viewBackup || m.view == viewRestorePick {
		mm, cmd = m.Update(keyMsg(k)) // vues des captures : routage complet
	} else {
		mm, cmd = m.handleListKey(keyMsg(k))
	}
	m = mm.(Model)
	// Seule la commande de prévision est rejouée (statut d'attente) : les
	// minuteurs du statut dormiraient.
	if cmd != nil && m.statusKind == statusBusy {
		if pm, ok := findPlanMsg(cmd); ok {
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

// t arrête (stop, confirmé) ; d retire (down, confirmé) ; up sur un service
// arrêté et pull sont directs ; annuler garde le mode.
func TestServiceStopDownRules(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right", "down", "down") // curseur sur db (arrêté)
	m = press(m, "s")
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Stop") || len(fake.calls["stop"]) != 0 {
		t.Fatalf("s doit demander confirmation de Stop (vue %v, %q)", m.view, m.confirmLabel)
	}
	m = press(m, "esc")
	if !m.svcFocus {
		t.Error("annuler la confirmation doit conserver le mode services")
	}
	m = press(m, "s", "y")
	if got := onlyOf(fake, "stop"); got != "db" {
		t.Fatalf("stop ciblé : %s", got)
	}
	m = press(m, "d")
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Down") {
		t.Fatalf("d doit demander confirmation de Down (%q)", m.confirmLabel)
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
	if !strings.Contains(out, "Services (1 sur 4)") || !strings.Contains(out, "service 1/1") || strings.Contains(out, "worker") {
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
		m2 := press(m, "s", "y")
		if got := fake.calls["stop"]; len(got) != 1 || got[0].WithDeps {
			t.Errorf("stop ne doit pas inclure les dépendances : %+v (%v)", got, m2.view)
		}
	}
}

// Le titre compte les services ; la position du curseur est à droite du
// pied de page.
func TestServiceModeRenderAndEscape(t *testing.T) {
	m := press(svcTestModel(&fakeClient{}), "right", "space")
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "web › services") || !strings.Contains(out, "Services (4) · 1 coché") ||
		!strings.Contains(out, "cocher") || !strings.Contains(out, "service 2/4") {
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

// Le libellé du bloc d'actions annonce exactement ce que visent les lettres :
// les cochés visibles, ou le service du curseur quand le filtre masque tous
// les cochés. Il comptait tous les cochés, masqués compris.
func TestServiceActionLabelMatchesTargets(t *testing.T) {
	fake := &fakeClient{}
	m := press(svcTestModel(fake), "right", "e") // coche db et worker
	m = press(m, "/", "d", "b", "enter")         // filtre « db » : worker masqué
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "Actions sur 1 coché visible (1 masqué)") {
		t.Errorf("libellé attendu pour db seul :\n%s", out)
	}
	m = press(m, "esc", "/", "c", "a", "enter") // filtre « ca » : aucun coché visible
	out = ansi.Strip(m.viewContent())
	if !strings.Contains(out, "Actions sur cache") {
		t.Errorf("sans coché visible, le libellé doit nommer le service du curseur :\n%s", out)
	}
	press(m, "p")
	if got := onlyOf(fake, "pull"); got != "cache" {
		t.Errorf("pull : Only = %s, want cache (le libellé l'annonçait)", got)
	}
}

// Le cache des services déclarés suit aussi le .env : y activer un profil
// (COMPOSE_PROFILES) fait apparaître ses services sans relancer dockstack.
func TestDeclaredServicesFollowDotEnv(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, []byte("services:\n  app:\n    image: nginx\n  debug:\n    image: busybox\n    profiles: [debug]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := compose.Stack{Name: "p", Dir: dir, ComposeFile: file}
	c := &svcCacheBox{}
	if n := len(c.declared(s)); n != 1 {
		t.Fatalf("sans profil : %d service(s), want 1", n)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("COMPOSE_PROFILES=debug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := len(c.declared(s)); n != 2 {
		t.Errorf("profil activé dans le .env : %d service(s), want 2", n)
	}
}

// Sous 100 colonnes, → entre quand même dans les services : le volet prend
// toute la largeur (il était refusé). Un terminal rétréci en mode services
// garde les services à l'écran : rendre la seule liste laissait le mode actif
// mais invisible, et ↓ ne bougeait rien de visible.
func TestServicesNarrowFullWidth(t *testing.T) {
	m := svcTestModel(&fakeClient{})
	m.width, m.height = 80, 24
	m = press(m, "right")
	out := ansi.Strip(m.viewContent())
	if !m.svcFocus || !strings.Contains(out, "web › services") || !strings.Contains(out, "▶ ● app") {
		t.Fatalf("80 colonnes : services attendus en pleine largeur (focus %v) :\n%s", m.svcFocus, out)
	}

	m = press(svcTestModel(&fakeClient{}), "right")
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = press(m, "down")
	out = ansi.Strip(m.viewContent())
	if !strings.Contains(out, "▶ ● cache") || !strings.Contains(out, "Actions sur cache") {
		t.Fatalf("120 → 80 colonnes : le service sous le curseur doit rester visible :\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("ligne de %d colonnes : %q", w, l)
		}
	}
	if m = press(m, "left"); m.svcFocus || !strings.Contains(ansi.Strip(m.viewContent()), "web") {
		t.Error("← doit ramener à la liste")
	}
}

// En mode services, seul le service visé garde le fond du curseur : la
// ligne de la stack, à gauche, perd le sien et prend un ▷ (les deux ▶
// avaient le même style, rien ne disait laquelle visaient les lettres).
func TestServiceModeListCursorAway(t *testing.T) {
	m := press(svcTestModel(&fakeClient{}), "right")
	list := strings.Split(renderStackList(m, 20, 70), "\n")
	var row string
	for _, l := range list {
		if strings.Contains(ansi.Strip(l), "web") {
			row = l
		}
	}
	if got := ansi.Strip(row); !strings.Contains(got, "▷") || strings.Contains(got, "▶") {
		t.Fatalf("ligne de la stack en mode services : %q, want ▷ sans ▶", got)
	}
	plain := renderStackLine(m.stacks[0], false, false, false, 70, false, m.rowsCache.nameW, m.rowUsageFor(m.stacks[0]))
	if strings.Replace(row, styleDim.Render("▷"), " ", 1) != plain {
		t.Errorf("hors repère, la ligne doit garder son rendu ordinaire (couleurs, sans fond) :\n%q\n%q", row, plain)
	}
	if out := ansi.Strip(m.viewContent()); strings.Count(out, "▶") != 1 || !strings.Contains(out, "▶ ● app") {
		t.Errorf("un seul ▶, celui du service :\n%s", out)
	}
}

// Le détail du service suit une zone de la hauteur de tous les services,
// plus une ligne vide : il ne bouge ni en naviguant ni en filtrant (collé en
// bas du volet, il était loin du service décrit).
func TestServiceDetailStaysUnderList(t *testing.T) {
	m := press(svcTestModel(&fakeClient{}), "right")
	rule := func(m Model) int {
		for i, l := range previewServices(m, m.stacks[0], 48, 34) {
			if strings.HasPrefix(ansi.Strip(l), "──") {
				return i
			}
		}
		return -1
	}
	want := rule(m)
	// Titre, état, « Services (4) », ligne du filtre, 4 services, ligne vide.
	if want != 9 {
		t.Fatalf("détail à la ligne %d, want 9 (juste sous les 4 services)", want)
	}
	for _, k := range []string{"down", "down", "down"} {
		if m = press(m, k); rule(m) != want {
			t.Errorf("curseur %d : détail à la ligne %d, want %d", m.svcCursor, rule(m), want)
		}
	}
	if m = press(m, "/", "d", "b"); rule(m) != want {
		t.Errorf("filtre « db » : détail à la ligne %d, want %d", rule(m), want)
	}
	// Place comptée : les services passent avant la ligne vide, et le volet
	// garde sa hauteur.
	m = press(svcTestModel(&fakeClient{}), "right")
	lines := previewServices(m, m.stacks[0], 48, 20)
	if len(lines) != 20 {
		t.Fatalf("%d lignes pour une hauteur de 20", len(lines))
	}
	for i, l := range lines {
		if strings.HasPrefix(ansi.Strip(l), "──") && (i < 1 || strings.TrimSpace(ansi.Strip(lines[i-1])) == "") {
			t.Errorf("volet bas : ligne vide avant le détail au lieu d'un service :\n%s", ansi.Strip(strings.Join(lines, "\n")))
		}
	}
}

// manySvcModel : une stack de n services (le quatrième arrêté, dépendant du
// premier), 120x40, hors chargement.
func manySvcModel(n int) Model {
	st := stack("big", "", n-1, n, 0)
	for i := range n {
		sv := compose.ServiceStatus{Name: fmt.Sprintf("svc%02d", i), Running: 1, Total: 1, Image: "ghcr.io/org/some-very-long-image-name:release-2026", Status: "Up 2 hours"}
		if i == 3 {
			sv.Running, sv.Status = 0, "Exited (137) 2 hours ago"
			sv.DependsOn = []string{"svc00"}
		}
		st.Services = append(st.Services, sv)
	}
	m := newTestModel([]compose.Stack{st})
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 120, 40, false
	return m
}

// En 80 ou 100 colonnes, la fenêtre des services ne montre pas tout : des
// repères « ··· N » doivent dire où l'on est et ce qui est caché.
func TestServicesHiddenOnesAreMarkedWhenNarrow(t *testing.T) {
	for _, w := range []int{80, 100} {
		m := manySvcModel(12)
		m, _ = update(m, tea.WindowSizeMsg{Width: w, Height: 24})
		m = press(m, "right")
		for range 6 {
			m = press(m, "down")
		}
		out := ansi.Strip(m.viewContent())
		shown := 0
		for i := range 12 {
			if strings.Contains(out, fmt.Sprintf("svc%02d", i)) {
				shown++
			}
		}
		if shown < 12 && !strings.Contains(out, "···") {
			t.Errorf("%d colonnes : %d services sur 12 à l'écran, aucun repère\n%s", w, shown, out)
		}
	}
}

// Écran étroit (60x20) : mêmes repères, et le service sous le curseur reste
// affiché.
func TestServicesNarrowScreenKeepsCursorAndMarkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stacks := []compose.Stack{capStack("immich", svc("database", 1), svc("immich-machine-learning", 1), svc("immich-server", 1), svc("redis", 1))}
	m := newTestModel(stacks)
	m.client = &fakeClient{stacks: stacks, autoDone: true}
	m.loading = false
	m.width, m.height = 120, 36
	m.invalidateRows()
	m = press(m, "right")
	m.width, m.height = 60, 20
	s := ansi.Strip(m.viewContent())
	n := 0
	for _, name := range []string{"database", "immich-machine-learning", "immich-server", "redis"} {
		if strings.Contains(s, "● "+name) {
			n++
		}
	}
	if n < 4 && !strings.Contains(s, "···") {
		t.Errorf("%d/4 services visibles, aucun repère :\n%s", n, s)
	}
	if !strings.Contains(s, "▶ ● database") {
		t.Errorf("le service sous le curseur n'est pas affiché :\n%s", s)
	}
}

// Le trait du détail ne bouge pas en naviguant, même quand les services ne
// tiennent pas tous : la zone suit le détail le plus haut, pas celui du
// service visé.
func TestServiceDetailRuleStaysPutWhenCrowded(t *testing.T) {
	st := stack("app", "", 4, 7, 0)
	for i := range 7 {
		sv := compose.ServiceStatus{Name: fmt.Sprintf("svc%d", i), Running: 1, Total: 1, Image: "nginx:alpine", Status: "Up 2 hours"}
		if i%2 == 1 {
			sv.Running, sv.Status = 0, "Exited (0) 2 hours ago"
		}
		st.Services = append(st.Services, sv)
	}
	m := newTestModel([]compose.Stack{st})
	m.client = &fakeClient{}
	m.loading = false
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = press(m, "right")
	rule := func(m Model) int {
		for i, l := range strings.Split(ansi.Strip(m.viewContent()), "\n") {
			if strings.HasPrefix(strings.TrimLeft(l, "│ "), "────") && strings.HasPrefix(l, "│") {
				return i
			}
		}
		return -1
	}
	want := rule(m)
	for c := 1; c < 7; c++ {
		m = press(m, "down")
		if got := rule(m); got != want {
			t.Fatalf("curseur sur svc%d : détail à la ligne %d, puis %d\n%s", c, want, got, ansi.Strip(m.viewContent()))
		}
	}
}

// Terminal bas : le service visé par les lettres reste à l'écran (le détail
// et l'en-tête cèdent la place), sinon p partirait sur un service invisible.
func TestServiceActionTargetStaysVisibleOnShortTerminal(t *testing.T) {
	st := stack("web", "", 2, 3, 0)
	st.Services = []compose.ServiceStatus{
		{Name: "app", Running: 1, Total: 1, Status: "Up 2 hours"},
		{Name: "cache", Running: 1, Total: 1, Status: "Up 2 hours"},
		{Name: "db", Total: 1, Status: "Exited (137) 3 hours ago"},
	}
	fake := &fakeClient{}
	m := newTestModel([]compose.Stack{st})
	m.client = fake
	m.loading = false
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 12})
	m = press(m, "right")
	out := ansi.Strip(m.viewContent())
	if !strings.Contains(out, "▶ ● app") {
		t.Fatalf("le service visé est absent de l'écran :\n%s", out)
	}
}
