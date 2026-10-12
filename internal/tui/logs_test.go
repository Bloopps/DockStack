package tui

import (
	"testing"

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
	} {
		if got := sanitizeLogLine(in); got != want {
			t.Errorf("sanitizeLogLine(%q) = %q, want %q", in, got, want)
		}
	}
}
