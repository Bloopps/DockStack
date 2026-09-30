package tui

import (
	"strings"
	"testing"

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
