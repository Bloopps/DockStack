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
