package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// TestReadLogCmdBatches : les lignes déjà disponibles sont regroupées en un seul
// logLinesMsg (un cycle Update au lieu d'un par ligne).
func TestReadLogCmdBatches(t *testing.T) {
	ch := make(chan compose.LogEntry, 3)
	ch <- compose.LogEntry{Name: "a", Line: "1"}
	ch <- compose.LogEntry{Name: "a", Line: "2"}
	ch <- compose.LogEntry{Name: "a", Line: "3"}

	msg := readLogCmd(ch, 7)()
	lm, ok := msg.(logLinesMsg)
	if !ok {
		t.Fatalf("type = %T, want logLinesMsg", msg)
	}
	if lm.seq != 7 {
		t.Errorf("seq = %d, want 7", lm.seq)
	}
	if len(lm.entries) != 3 {
		t.Fatalf("entries = %d, want 3 (lot)", len(lm.entries))
	}
}

// TestReadLogCmdClosed : un canal fermé produit logDoneMsg (fin de session).
func TestReadLogCmdClosed(t *testing.T) {
	ch := make(chan compose.LogEntry)
	close(ch)
	if _, ok := readLogCmd(ch, 1)().(logDoneMsg); !ok {
		t.Errorf("canal fermé : type = %T, want logDoneMsg", readLogCmd(ch, 1)())
	}
}

// sanitizeLogLine retire couleurs et séquences de contrôle, qu'elles soient
// seules ou mêlées au texte ; une ligne sans échappement est rendue telle
// quelle (le raccourci qui évite l'expression régulière).
func TestSanitizeLogLine(t *testing.T) {
	for in, want := range map[string]string{
		"GET /api 200 12ms":                 "GET /api 200 12ms",
		"\x1b[32mINFO\x1b[0m démarré":       "INFO démarré",
		"\x1b[1;31;40mERREUR\x1b[m":         "ERREUR",
		"progression 10%\rprogression 100%": "progression 100%",
		"cloche\a et \x00nul":               "cloche et nul",
		"\x1b[?25lcurseur masqué\x1b[?25h":  "curseur masqué",
		"clé\tvaleur":                       "clé     valeur",
		"日本語 🎉 \x1b[33mjaune\x1b[39m":       "日本語 🎉 jaune",
		// OSC : titre de fenêtre, presse-papiers (OSC 52), lien ; BEL ou ST.
		"\x1b]0;titre\x07après":                     "après",
		"\x1b]52;c;cm0gLXJmIH4=\x07ok":              "ok",
		"\x1b]8;;https://x\x1b\\lien\x1b]8;;\x1b\\": "lien",
		// Contrôles C1 (U+009B : CSI sur un octet pour certains terminaux).
		"a\u009b31mb\u0085c": "a31mbc",
		"\x1bMligne":         "ligne",
	} {
		if got := sanitizeLogLine(in); got != want {
			t.Errorf("sanitizeLogLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// Textes externes affichés sur une ligne (erreurs du démon ou d'un registre,
// noms de dossiers) : fins de ligne en espaces, séquences et contrôles retirés.
func TestSanitizeText(t *testing.T) {
	for in, want := range map[string]string{
		"pull access denied":                  "pull access denied",
		"ligne 1\nligne 2\r\nligne 3":         "ligne 1 ligne 2 ligne 3",
		"\x1b]52;c;ZXZpbA==\x07denied: accès": "denied: accès",
		"dossier\x1b[2Jpiégé":                 "dossierpiégé",
		"é\u009b":                             "é",
	} {
		if got := sanitizeText(in); got != want {
			t.Errorf("sanitizeText(%q) = %q, want %q", in, got, want)
		}
	}
	if s := "texte ascii ordinaire"; testing.AllocsPerRun(10, func() { _ = sanitizeText(s) }) != 0 {
		t.Error("un texte ASCII imprimable ne doit pas être recopié")
	}
}

// Vues logs et compose : le pied de page reste en bas même quand le contenu
// est court (il remontait au milieu de l'écran), et un flux vide le dit.
func TestShortViewsKeepFooterAtBottom(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 80, 20
	m.view, m.logStack = viewLogs, "app"
	lines := strings.Split(ansi.Strip(renderLogsView(m)), "\n")
	if len(lines) != m.height {
		t.Errorf("logs vides : %d lignes, want %d", len(lines), m.height)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "Aucune ligne pour l'instant") {
		t.Error("flux vide non signalé")
	}
	m.view, m.composeLines = viewCompose, []string{"services:", "  web: {}"}
	m.composeStack = compose.Stack{Name: "app", ComposeFile: "/x/compose.yaml"}
	if n := len(strings.Split(renderComposeView(m), "\n")); n != m.height {
		t.Errorf("compose court : %d lignes, want %d", n, m.height)
	}
}

// webLogs : n lignes d'un conteneur « web-1 », comme les livre le flux.
func webLogs(n int) []compose.LogEntry {
	out := make([]compose.LogEntry, n)
	for i := range out {
		out[i] = compose.LogEntry{Name: "web-1", Line: fmt.Sprintf("ligne %d GET /api/v1/items 200", i)}
	}
	return out
}

// openLogs ouvre la vue des logs sur s comme la touche l. Sans client, le
// flux n'est jamais lancé : les lots sont envoyés à la main (logBatch).
func openLogs(t *testing.T, s compose.Stack, w, h int) Model {
	t.Helper()
	m := newTestModel([]compose.Stack{s})
	m.width, m.height = w, h
	out, _ := m.startStackAction("logs", s)
	m = out.(Model)
	if m.view != viewLogs {
		t.Fatalf("vue %v, want viewLogs", m.view)
	}
	return m
}

// logBatch livre un lot à la session courante, comme readLogCmd.
func logBatch(m Model, entries []compose.LogEntry) Model {
	m, _ = update(m, logLinesMsg{entries: entries, seq: m.logSeq})
	return m
}

// logKeys envoie des touches par le routage complet (raccourcis globaux
// compris : q, R, Q).
func logKeys(m Model, keys ...string) Model {
	for _, k := range keys {
		m, _ = update(m, keyMsg(k))
	}
	return m
}

// logScreen : la vue logs sans couleurs, ligne par ligne.
func logScreen(m Model) []string {
	return strings.Split(ansi.Strip(renderLogsView(m)), "\n")
}

// À l'ouverture, l'historique (500 lignes par conteneur, livrées d'un coup)
// laissait l'écran sur la ligne la plus ancienne, et un lot de plus de deux
// lignes faisait décrocher le suivi. En direct, chaque lot recolle la vue en
// bas ; après ↑, elle ne bouge plus et compte les lignes arrivées.
func TestLogsFollowStaysAtBottom(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	h := m.logsHeight()
	m = logBatch(m, webLogs(500))
	if want := 500 - h; m.logScroll != want || m.logPaused {
		t.Fatalf("après l'historique : défilement %d (pause=%v), want %d en direct", m.logScroll, m.logPaused, want)
	}
	for range 3 {
		m = logBatch(m, webLogs(5))
		if want := len(m.logLines) - h; m.logScroll != want {
			t.Fatalf("lot de 5 : défilement %d/%d, want %d", m.logScroll, len(m.logLines), want)
		}
	}
	screen := logScreen(m)
	if last := screen[len(screen)-2]; !strings.Contains(last, "ligne 4 GET") {
		t.Errorf("dernière ligne du corps : %q, want la dernière reçue", last)
	}
	if !strings.Contains(screen[0], "● en direct") {
		t.Errorf("titre : %q, want « ● en direct »", screen[0])
	}

	m = logKeys(m, "up")
	if !m.logPaused {
		t.Fatal("↑ doit mettre le suivi en pause")
	}
	before := m.logScroll
	m = logBatch(m, webLogs(7))
	if m.logScroll != before {
		t.Errorf("en pause : défilement %d, want %d (la vue ne bouge plus)", m.logScroll, before)
	}
	if n := m.logNew(); n != 7 {
		t.Errorf("lignes arrivées pendant la pause : %d, want 7", n)
	}
	if title := logScreen(m)[0]; !strings.Contains(title, "⏸ en pause · +7 lignes · G suivre") {
		t.Errorf("titre en pause : %q", title)
	}

	// ↓ jusqu'en bas relance le suivi, comme G.
	m = logKeys(m, "down")
	if !m.logPaused {
		t.Fatal("↓ sans atteindre le bas : toujours en pause")
	}
	for m.logPaused && m.logScroll < len(m.logLines) {
		m = logKeys(m, "down")
	}
	if m.logPaused || m.logNew() != 0 {
		t.Errorf("↓ jusqu'en bas : pause=%v, +%d lignes, want suivi relancé", m.logPaused, m.logNew())
	}
	m = logKeys(m, "g")
	if !m.logPaused || m.logScroll != 0 {
		t.Errorf("g : défilement %d (pause=%v), want 0 en pause", m.logScroll, m.logPaused)
	}
	m = logKeys(m, "G")
	if m.logPaused || m.logScroll != len(m.logLines)-h {
		t.Errorf("G : défilement %d (pause=%v), want le bas en direct", m.logScroll, m.logPaused)
	}
}

// Un flux court tient à l'écran : ↑ n'a rien à faire défiler et ne met pas
// le suivi en pause (sinon les lignes suivantes seraient comptées « en
// attente » alors qu'elles s'affichent).
func TestLogsShortFlowNeverPauses(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 80, 24)
	m = logBatch(m, webLogs(3))
	m = logKeys(m, "up", "g")
	if m.logPaused {
		t.Error("flux plus court que l'écran : pas de pause")
	}
}

// Pendant la pause, le pied propose G suivre à la place de g/G.
func TestLogsFooterOffersFollowWhenPaused(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	m = logBatch(m, webLogs(100))
	// Espaces resserrés : les touches du pied ont leur propre marge.
	foot := func() string { s := logScreen(m); return strings.Join(strings.Fields(s[len(s)-1]), " ") }
	if f := foot(); !strings.Contains(f, "g/G haut/bas") || strings.Contains(f, "suivre") {
		t.Errorf("pied en direct : %q", f)
	}
	m = logKeys(m, "up")
	if f := foot(); !strings.Contains(f, "G suivre") || strings.Contains(f, "g/G") {
		t.Errorf("pied en pause : %q, want « G suivre »", f)
	}
}

// La fin du flux s'affiche dans le titre (« Logs terminés » partait en barre
// de statut, que la vue ne rend pas), et un flux fermé sans une ligne ne
// promet plus « Aucune ligne pour l'instant ».
func TestLogsEndedShownInView(t *testing.T) {
	m := openLogs(t, stack("vide", "", 0, 0, 0), 120, 36)
	m, _ = update(m, logDoneMsg{seq: m.logSeq})
	if !m.logEnded || m.status != "" {
		t.Fatalf("fin du flux : terminé=%v, statut %q, want terminé sans statut", m.logEnded, m.status)
	}
	screen := strings.Join(logScreen(m), "\n")
	if !strings.Contains(screen, "■ flux terminé") {
		t.Error("titre sans « ■ flux terminé »")
	}
	if !strings.Contains(screen, "la stack n'a aucun conteneur (non déployée ?)") || strings.Contains(screen, "pour l'instant") {
		t.Errorf("stack sans conteneur, flux fermé : %q", screen)
	}

	m = openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	m, _ = update(m, logDoneMsg{seq: m.logSeq})
	if screen := strings.Join(logScreen(m), "\n"); !strings.Contains(screen, "le flux s'est terminé sans rien recevoir") {
		t.Errorf("stack déployée, flux fermé à vide : %q", screen)
	}

	// Une session périmée (vue rouverte) ne marque pas la nouvelle.
	m = openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	m, _ = update(m, logDoneMsg{seq: m.logSeq - 1})
	if m.logEnded {
		t.Error("fin d'une session précédente prise pour la fin de la courante")
	}
}

// Préfixes alignés sur le plus long nom, comme docker compose logs, et
// « -1 » retiré quand le service n'a qu'un conteneur. Une réplique « -2 »
// qui apparaît rend son numéro à « -1 ».
func TestLogsPrefixesAligned(t *testing.T) {
	s := stack("immich", "", 4, 4, 0)
	s.Services = []compose.ServiceStatus{
		{Name: "server", Running: 1, Total: 1},
		{Name: "machine-learning", Running: 1, Total: 1},
		{Name: "redis", Running: 2, Total: 2},
	}
	m := openLogs(t, s, 120, 36)
	m = logBatch(m, []compose.LogEntry{
		{Name: "server-1", Line: "A démarré"},
		{Name: "machine-learning-1", Line: "B prêt"},
		{Name: "redis-1", Line: "C prêt"},
		{Name: "redis-2", Line: "D prêt"},
	})
	body := logScreen(m)[2:6]
	col := -1
	for i, row := range body {
		bar := strings.Index(row, "│ ")
		if bar < 0 {
			t.Fatalf("ligne %d sans séparateur : %q", i, row)
		}
		if c := ansi.StringWidth(row[:bar]); col < 0 {
			col = c
		} else if c != col {
			t.Errorf("ligne %d : message en colonne %d, want %d (préfixes alignés)\n%s", i, c, col, strings.Join(body, "\n"))
		}
	}
	if !strings.HasPrefix(body[0], "  server ") || !strings.Contains(body[1], "machine-learning │") {
		t.Errorf("noms raccourcis : %q / %q", body[0], body[1])
	}
	if !strings.Contains(body[2], "redis-1") || !strings.Contains(body[3], "redis-2") {
		t.Errorf("service scalé : le numéro distingue les répliques (%q, %q)", body[2], body[3])
	}

	m = logBatch(m, []compose.LogEntry{{Name: "server-2", Line: "E démarré"}})
	if body := logScreen(m); !strings.HasPrefix(body[2], "  server-1 ") {
		t.Errorf("réplique server-2 apparue : %q, want « server-1 »", body[2])
	}
}

// Filtre / : sous-chaîne sans casse, appliquée aussi aux lignes qui
// arrivent ; q et R y sont des lettres ; esc efface le filtre, puis quitte.
func TestLogsFilter(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	m = logBatch(m, []compose.LogEntry{
		{Name: "web-1", Line: "ERROR connexion refusée"},
		{Name: "web-1", Line: "info requête servie"},
		{Name: "db-1", Line: "error: disque plein"},
	})
	m = logKeys(m, "/")
	if !m.typing() {
		t.Fatal("saisie du filtre : typing() doit être vrai (q, R, Q sont des lettres)")
	}
	m = logKeys(m, "E", "r", "r")
	if m.logCount() != 2 {
		t.Fatalf("filtre « Err » : %d lignes, want 2 (sans casse)", m.logCount())
	}
	if title := logScreen(m)[0]; !strings.Contains(title, "/ Err▏ · 2 lignes sur 3") {
		t.Errorf("titre pendant la saisie : %q", title)
	}
	m = logKeys(m, "q", "R")
	if m.logFilter != "ErrqR" || m.quitArmed || m.view != viewLogs {
		t.Fatalf("q et R dans le filtre : filtre %q, sortie armée=%v", m.logFilter, m.quitArmed)
	}
	m = logKeys(m, "backspace", "backspace", "enter")
	if m.logFiltering || m.logFilter != "Err" {
		t.Fatalf("↩ fige le filtre : saisie=%v, filtre %q", m.logFiltering, m.logFilter)
	}

	m = logBatch(m, []compose.LogEntry{{Name: "web-1", Line: "debug"}, {Name: "web-1", Line: "nouvelle erreur"}})
	if m.logCount() != 3 {
		t.Errorf("lignes arrivées filtrées : %d visibles, want 3", m.logCount())
	}
	body := strings.Join(logScreen(m)[2:], "\n")
	if strings.Contains(body, "info requête") || strings.Contains(body, "debug") || !strings.Contains(body, "nouvelle erreur") {
		t.Errorf("corps filtré :\n%s", body)
	}

	// Le nom de la source compte aussi : « / db » isole un service.
	m = logKeys(m, "esc", "/", "d", "b", "enter")
	if m.logCount() != 1 {
		t.Errorf("filtre sur le nom « db » : %d lignes, want 1", m.logCount())
	}

	m = logKeys(m, "esc")
	if m.view != viewLogs || m.logFilter != "" || m.logCount() != 5 {
		t.Fatalf("premier esc : vue %v, filtre %q, %d lignes ; want le filtre effacé", m.view, m.logFilter, m.logCount())
	}
	m = logKeys(m, "esc")
	if m.view != viewList {
		t.Errorf("second esc : vue %v, want la liste", m.view)
	}
}

// En pause, le filtre garde la vue sur la même ligne du tampon : on filtre
// « error », on situe l'erreur, et esc rend son contexte au lieu de renvoyer
// en bas, même après une faute de frappe qui ne retenait plus rien.
func TestLogsFilterKeepsPausedPosition(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 120, 6) // corps de 3 lignes
	var entries []compose.LogEntry
	for i := range 100 {
		line := fmt.Sprintf("ligne %d", i)
		if i%10 == 0 {
			line = fmt.Sprintf("error %d", i)
		}
		entries = append(entries, compose.LogEntry{Name: "web-1", Line: line})
	}
	m = logBatch(m, entries)
	m = logKeys(m, "g")
	for range 25 {
		m = logKeys(m, "down")
	}
	m = logKeys(m, "/", "e", "r", "r", "o", "r", "enter")
	if top := logScreen(m)[2]; !strings.Contains(top, "error 30") {
		t.Errorf("filtre en pause : première ligne %q, want « error 30 » (suivante retenue)", top)
	}
	// Une faute de frappe (aucune ligne retenue) puis sa correction ne
	// renvoient pas au début.
	m = logKeys(m, "/", "x")
	if m.logCount() != 0 {
		t.Fatalf("filtre « errorx » : %d lignes, want 0", m.logCount())
	}
	m = logKeys(m, "backspace", "enter")
	if top := logScreen(m)[2]; !strings.Contains(top, "error 30") {
		t.Errorf("faute corrigée : première ligne %q, want « error 30 »", top)
	}
	m = logKeys(m, "esc")
	if top := logScreen(m)[2]; !strings.Contains(top, "error 30") || !m.logPaused {
		t.Errorf("filtre effacé : première ligne %q (pause=%v), want « error 30 », toujours en pause", top, m.logPaused)
	}
}

// w replie les lignes longues : le message, souvent l'erreur, n'était
// jamais visible en 80 colonnes. Chaque ligne d'écran tient dans la largeur
// (une ligne repliée par le terminal décalerait tout), et en direct la fin
// du dernier message reste visible.
func TestLogsWrap(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 60, 12)
	m = logBatch(m, webLogs(20))
	long := "2026-10-12T14:00:27.123Z ERROR [Api:EventRepository] Websocket connect failed: client=1027, ip=172.18.0.27 FIN"
	m = logBatch(m, []compose.LogEntry{{Name: "immich-machine-learning-1", Line: long}})
	if screen := logScreen(m); strings.Contains(strings.Join(screen, "\n"), "FIN") {
		t.Fatal("sans repli, la fin du message ne doit pas tenir en 60 colonnes (test sans objet)")
	}

	m = logKeys(m, "w")
	screen := logScreen(m)
	if len(screen) != m.height {
		t.Fatalf("replié : %d lignes rendues, want %d", len(screen), m.height)
	}
	for i, row := range screen {
		if w := ansi.StringWidth(row); w > m.width {
			t.Errorf("ligne %d : %d colonnes > %d : %q", i, w, m.width, row)
		}
	}
	body := screen[2 : len(screen)-1]
	if last := body[len(body)-1]; !strings.Contains(last, "FIN") {
		t.Errorf("en direct, replié : dernière ligne %q, want la fin du message\n%s", last, strings.Join(body, "\n"))
	}
	// La suite du message s'aligne sous son début, pas sous le préfixe.
	cont := body[len(body)-1]
	if lead := len(cont) - len(strings.TrimLeft(cont, " ")); lead != m.logPrefixW()+5 {
		t.Errorf("suite du message en colonne %d, want %d (sous le début du message)", lead, m.logPrefixW()+5)
	}
	// En 80 colonnes, le pied garde w (« / filtre » cède la place en premier).
	wide := m
	wide.width = 80
	if s := logScreen(wide); !strings.Contains(strings.Join(strings.Fields(s[len(s)-1]), " "), "w tronquer") {
		t.Errorf("pied replié en 80 colonnes : %q, want « w tronquer »", s[len(s)-1])
	}

	m = logKeys(m, "w")
	if strings.Contains(strings.Join(logScreen(m), "\n"), "FIN") {
		t.Error("w de nouveau : lignes tronquées")
	}
}

// Le plafond du tampon décale les indices du filtre : chaque ligne retenue
// doit toujours contenir le filtre, sans en oublier.
func TestLogsTrimKeepsFilter(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	m = logKeys(m, "/", "x", "enter")
	for b := range 6 {
		entries := make([]compose.LogEntry, 2500)
		for i := range entries {
			line := fmt.Sprintf("lot %d ligne %d", b, i)
			if i%3 == 0 {
				line += " x"
			}
			entries[i] = compose.LogEntry{Name: "web-1", Line: line}
		}
		m = logBatch(m, entries)
	}
	if len(m.logLines) > maxLogLines+logTrimChunk {
		t.Fatalf("tampon de %d lignes, plafond non appliqué", len(m.logLines))
	}
	want := 0
	for _, l := range m.logLines {
		if strings.Contains(l.text, "x") {
			want++
		}
	}
	if len(m.logMatch) != want {
		t.Errorf("%d lignes retenues, want %d", len(m.logMatch), want)
	}
	for _, i := range m.logMatch {
		if !strings.Contains(m.logLines[i].text, "x") {
			t.Fatalf("indice %d décalé : %q", i, m.logLines[i].text)
		}
	}
	if m.logScroll != m.logsMaxScroll() {
		t.Errorf("en direct après la taille : défilement %d, want %d", m.logScroll, m.logsMaxScroll())
	}
}

// Un terminal réduit en direct garde la fin du flux visible.
func TestLogsResizeKeepsBottom(t *testing.T) {
	m := openLogs(t, stack("app", "", 1, 1, 0), 120, 36)
	m = logBatch(m, webLogs(100))
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 12})
	if want := 100 - m.logsHeight(); m.logScroll != want {
		t.Errorf("après redimensionnement : défilement %d, want %d", m.logScroll, want)
	}
}
