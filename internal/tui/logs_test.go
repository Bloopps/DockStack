package tui

import (
	"strings"
	"testing"

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
