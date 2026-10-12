package tui

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
)

// Non-régressions de la passe complémentaire du 2026-10-01.

// findPlanMsg exécute la commande renvoyée par une touche et en extrait la
// prévision d'up, en descendant dans les lots (tea.Batch).
func findPlanMsg(c tea.Cmd) (planUpMsg, bool) {
	if c == nil {
		return planUpMsg{}, false
	}
	switch msg := c().(type) {
	case planUpMsg:
		return msg, true
	case tea.BatchMsg:
		for _, sub := range msg {
			if pm, ok := findPlanMsg(sub); ok {
				return pm, true
			}
		}
	}
	return planUpMsg{}, false
}

// planMsgOf : findPlanMsg, prévision obligatoire.
func planMsgOf(t *testing.T, cmd tea.Cmd) planUpMsg {
	t.Helper()
	pm, ok := findPlanMsg(cmd)
	if !ok {
		t.Fatal("aucune prévision d'up dans la commande")
	}
	return pm
}

// Une prévision d'up dont la réponse arrive après un aller-retour dans une
// autre vue ne lance plus l'up : l'utilisateur est passé à autre chose, et
// l'action partirait sans nouvelle frappe (avant : seule la vue d'origine
// était comparée, et elle était redevenue la liste).
func TestStalePlanUpAfterViewRoundTrip(t *testing.T) {
	fake := &fakeClient{} // plan vide : l'up partirait sans confirmation
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false

	m, cmd := update(m, keyMsg("u"))
	pm := planMsgOf(t, cmd)
	m, _ = update(m, keyMsg("?")) // aide…
	if m.view != viewHelp {
		t.Fatalf("vue = %v, want aide", m.view)
	}
	m, _ = update(m, keyMsg("x")) // … puis retour à la liste
	if m.view != viewList {
		t.Fatalf("vue = %v, want liste", m.view)
	}
	m, _ = update(m, pm)
	if got := fake.called("up"); got != nil || m.view == viewProgress {
		t.Errorf("up lancé par une prévision périmée (appels %v, vue %v)", got, m.view)
	}
	if m.status != "" {
		t.Errorf("statut « %s » resté affiché", m.status)
	}
}

// Deux u rapprochés : seule la dernière prévision compte, la première est
// écartée (avant : la première lançait l'up, la seconde tombait sur le verrou
// d'opération, sans effet visible mais par hasard).
func TestOnlyLatestPlanUpApplies(t *testing.T) {
	fake := &fakeClient{plan: []compose.Recreation{{Service: "app", Reason: "config modifiée"}}}
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false

	m, cmd1 := update(m, keyMsg("u"))
	first := planMsgOf(t, cmd1)
	m, cmd2 := update(m, keyMsg("u"))
	second := planMsgOf(t, cmd2)

	m, _ = update(m, first)
	if m.view == viewConfirm {
		t.Fatal("la première prévision, périmée, a ouvert la confirmation")
	}
	m, _ = update(m, second)
	if m.view != viewConfirm {
		t.Fatalf("vue = %v, want confirmation pour la dernière prévision", m.view)
	}

	// Sans changement de vue, la prévision reste valable (cas nominal).
	fake.plan = nil
	m = newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	m, cmd := update(m, keyMsg("u"))
	pm := planMsgOf(t, cmd)
	m, _ = update(m, keyMsg("down"))
	m, _ = update(m, pm)
	if got := fake.called("up"); len(got) != 1 || got[0].Name != "web" {
		t.Errorf("up attendu sur web après prévision vide, appels = %v", got)
	}
}

// Messages réels du démon (pull d'images jetables inexistantes) et texte
// accentué ou large.
var wrapSamples = []string{
	`Error response from daemon: pull access denied for zzdsprobe-image-inexistante-4f9a, repository does not exist or may require 'docker login': denied: requested access to the resource is denied`,
	`Error response from daemon: Head "https://ghcr.io/v2/zzdsprobe/organisation-tres-longue/sous-projet/depot-avec-un-nom-interminable-pour-tester/manifests/etiquette-tres-longue-2026-10-01": denied`,
	`Error response from daemon: manifest for busybox@sha256:0000000000000000000000000000000000000000000000000000000000000000 not found: manifest unknown: manifest unknown`,
	upWarning,
	"Déjà démarré : réseau « traefik_lan » introuvable, vérifiez l'éèàùç du nom",
	"容器 名称 冲突 镜像 不存在 请检查 配置 文件 🐳🐳🐳 部署 失败 重试",
	"🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳🐳",
}

// wordWrap mesure en colonnes et coupe un mot plus large que la ligne. Avant :
// mesure en octets (repli prématuré du texte accentué) et mot long laissé
// entier, qui débordait de l'écran (référence d'image, URL du registre).
func TestWordWrapColumns(t *testing.T) {
	for _, s := range wrapSamples {
		for w := 1; w <= 120; w++ {
			lines := wordWrap(s, w)
			for _, l := range lines {
				if lw := ansi.StringWidth(l); lw > max(w, 2) {
					t.Fatalf("largeur %d : ligne de %d colonnes %q", w, lw, l)
				}
			}
			// Rien de perdu : mêmes caractères hors espaces, dans l'ordre.
			if got, want := strings.Join(strings.Fields(strings.Join(lines, "")), ""), strings.Join(strings.Fields(s), ""); got != want {
				t.Fatalf("largeur %d : texte altéré\n got %q\nwant %q", w, got, want)
			}
		}
	}
	// Une phrase accentuée qui tient en colonnes tient sur une ligne.
	if got := wordWrap(upWarning, ansi.StringWidth(upWarning)); len(got) != 1 {
		t.Errorf("repli prématuré du texte accentué : %q", got)
	}
}

// Les trois écrans qui replient (confirmation, erreur, progression) ne
// débordent plus avec ces messages, de 60 à 120 colonnes (en dessous,
// l'en-tête est de toute façon coupé par le terminal).
func TestWrappedViewsFitWidth(t *testing.T) {
	for _, s := range wrapSamples {
		for w := 60; w <= 120; w += 3 {
			m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
			m.width, m.height, m.loading = w, 40, false

			c := m
			c.view = viewConfirm
			c.confirmLabel, c.confirmWarning, c.confirmNames = "Up — web", s, []string{s}
			e := m
			e.err = errors.New(s)
			p := m
			p.view = viewProgress
			p.progress = newProgressState()
			p.progress.apply(compose.ProgressEvent{StackName: "web", Container: "Container web-1", Status: "Error", State: "error", Details: s})
			p.progress.apply(compose.ProgressEvent{StackName: "web", StackDone: true, Err: errors.New(s)})
			p.progressDone = true

			for name, mm := range map[string]Model{"confirmation": c, "erreur": e, "progression": p} {
				for i, l := range strings.Split(mm.View().Content, "\n") {
					if lw := ansi.StringWidth(l); lw > w {
						t.Fatalf("%s, %d colonnes, ligne %d : %d colonnes\n%q", name, w, i, lw, ansi.Strip(l))
					}
				}
			}
		}
	}
}

// truncateRunes : truncate avant le passage à ansi.Truncate (comptage en
// caractères).
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// Le passage de truncate à ansi.Truncate ne change rien, octet pour octet,
// pour du texte à caractères étroits (ASCII, accents, symboles de l'interface).
// Seuls changent les caractères larges (voulu), les caractères de largeur
// nulle et les séquences d'échappement, qu'aucun appelant ne passe.
func TestTruncateSameAsRunesForNarrowText(t *testing.T) {
	alphabet := []rune("abcXYZ019 -_./:@·…éèàçùÉœ«»─│●○✔✕⚠▶")
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		n := rng.IntN(40)
		r := make([]rune, n)
		for i := range r {
			r[i] = alphabet[rng.IntN(len(alphabet))]
		}
		s := string(r)
		for w := 0; w <= n+2; w++ {
			if got, want := truncate(s, w), truncateRunes(s, w); got != want {
				t.Fatalf("truncate(%q, %d) = %q, avant %q", s, w, got, want)
			}
		}
	}
}

// Lignes de logs qui arrivent une à une (≈ 500 lignes/s, conteneur bavard
// ordinaire) : elles sont regroupées sur une image (logFrame) au lieu d'un
// message, donc d'un recalcul complet de la vue, par ligne. Mesuré avant :
// 40 % d'un cœur à 494 lignes/s.
func TestLogLinesCoalescedPerFrame(t *testing.T) {
	ch := make(chan compose.LogEntry, compose.LogBuffer) // comme LogsAsync
	go func() {
		for i := range 20 {
			ch <- compose.LogEntry{Name: "web-1", Line: fmt.Sprint(i)}
			time.Sleep(time.Millisecond)
		}
	}()
	t0 := time.Now()
	msg, ok := readLogCmd(ch, 3)().(logLinesMsg)
	if !ok {
		t.Fatal("lot attendu")
	}
	if len(msg.entries) < 10 {
		t.Errorf("%d ligne(s) par lot, want ≥ 10 (une image)", len(msg.entries))
	}
	if d := time.Since(t0); d > logFrame+30*time.Millisecond {
		t.Errorf("lot livré au bout de %v, plus d'une image", d)
	}
	for i, e := range msg.entries {
		if e.Line != fmt.Sprint(i) {
			t.Fatalf("ordre perdu : %v", msg.entries)
		}
	}

	// Canal fermé pendant l'attente : le lot est livré, puis la fin.
	ch2 := make(chan compose.LogEntry, 2)
	ch2 <- compose.LogEntry{Line: "a"}
	close(ch2)
	if m, ok := readLogCmd(ch2, 1)().(logLinesMsg); !ok || len(m.entries) != 1 {
		t.Errorf("fin du flux : %v", m)
	}
	if _, ok := readLogCmd(ch2, 1)().(logDoneMsg); !ok {
		t.Error("fin du flux non signalée")
	}
}

// Démon arrêté ou tombé en cours de route : messages réels du client Docker.
// Avant, la seule présence de « docker.sock » affichait « Accès Docker
// refusé » et le conseil de rejoindre le groupe docker, sans le message.
func TestRenderErrorDaemonUnreachable(t *testing.T) {
	for _, msg := range []string{
		`failed to connect to the docker API at unix:///var/run/docker.sock; check if the path is correct and if the daemon is running: dial unix /var/run/docker.sock: connect: no such file or directory`,
		`Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?`,
		`error during connect: Get "http://%2Fvar%2Frun%2Fdocker.sock/v1.53/containers/json?all=1": EOF`,
		`Cannot connect to the Docker daemon at tcp://127.0.0.1:2376. Is the docker daemon running?`,
		`le démon Docker ne répond pas (10s sans réponse) : context deadline exceeded`,
	} {
		out := ansi.Strip(renderError(errors.New(msg), 100, ""))
		if strings.Contains(out, "Accès Docker refusé") || strings.Contains(out, "usermod") {
			t.Errorf("%q affiché comme un refus d'accès :\n%s", msg, out)
		}
		if !strings.Contains(out, "Démon Docker injoignable") || !strings.Contains(out, "R pour réessayer") {
			t.Errorf("%q : cause et reprise absentes :\n%s", msg, out)
		}
		// Le message d'origine reste lisible (diagnostic).
		words := strings.Fields(msg)
		if !strings.Contains(strings.Join(strings.Fields(out), " "), words[len(words)-1]) {
			t.Errorf("%q : message d'origine absent :\n%s", msg, out)
		}
		for i, l := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(l); w > 100 {
				t.Errorf("%q : ligne %d de %d colonnes", msg, i, w)
			}
		}
	}
}

// Coller du texte (bracketed paste, actif par défaut dans bubbletea v2) dans
// les champs de saisie. Avant : tea.PasteMsg était ignoré, coller ne faisait
// rien. Hors saisie, un collage ne doit jamais devenir des raccourcis.
func TestPasteIntoInputs(t *testing.T) {
	long := strings.Repeat("immich\tweb\n", 2000) // 22 000 caractères
	base := func() Model {
		m := newTestModel([]compose.Stack{stack("immich", "", 1, 1, 0), stack("web", "", 1, 1, 0)})
		m.client = &fakeClient{}
		m.width, m.height, m.loading = 120, 40, false
		return m
	}

	// Filtre de la liste.
	m, _ := update(base(), keyMsg("/"))
	m, _ = update(m, tea.PasteMsg{Content: "imm\x1b[31mich"})
	if got := m.filter.Value(); got != "imm[31mich" {
		t.Errorf("filtre de liste après collage = %q", got)
	}
	m, _ = update(m, tea.PasteMsg{Content: long})
	if n := len([]rune(m.filter.Value())); n > 64 {
		t.Errorf("filtre de liste : %d caractères, plafond 64", n)
	}

	// Filtre des services, du sélecteur de thèmes, nom de capture.
	m, _ = update(base(), keyMsg("right"))
	m, _ = update(m, keyMsg("/"))
	m, _ = update(m, tea.PasteMsg{Content: "we\nb"})
	if m.svcFilter != "we b" {
		t.Errorf("filtre des services = %q, want %q", m.svcFilter, "we b")
	}
	m, _ = update(base(), keyMsg("t"))
	m, _ = update(m, keyMsg("/"))
	m, _ = update(m, tea.PasteMsg{Content: long})
	if n := len([]rune(m.themeFilter)); n == 0 || n > 64 || strings.ContainsAny(m.themeFilter, "\t\n") {
		t.Errorf("filtre des thèmes = %q", m.themeFilter)
	}
	for i, l := range strings.Split(m.viewContent(), "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Fatalf("thèmes après collage : ligne %d de %d colonnes", i, w)
		}
	}
	m = base()
	m.view = viewBackup
	m.backups = []backup.Snapshot{{Date: time.Now(), Stacks: []string{"web"}}}
	m, _ = update(m, keyMsg("n"))
	m, _ = update(m, tea.PasteMsg{Content: "avant la mise à jour"})
	if m.captureLabel != "avant la mise à jour" {
		t.Errorf("nom de capture = %q", m.captureLabel)
	}

	// Hors saisie : rien ne se passe.
	fake := &fakeClient{}
	m = base()
	m.client = fake
	m, cmd := update(m, tea.PasteMsg{Content: "udrcp q"})
	if m.view != viewList || cmd != nil || fake.called("up") != nil || m.quitArmed {
		t.Errorf("collage hors saisie interprété : vue %v, commande %v", m.view, cmd != nil)
	}
}
