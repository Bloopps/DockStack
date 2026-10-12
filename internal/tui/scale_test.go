package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Benchmarks à l'échelle visée (600 stacks) : coût d'un rafraîchissement de
// la liste, d'une frappe de navigation et d'un rendu.

func scaleModel(n int) Model {
	m := screenModel(n)
	m.width, m.height = 190, 50
	return m
}

// Rafraîchissement : réception d'une nouvelle liste puis premier rendu.
func BenchmarkStacksLoaded600(b *testing.B) {
	m := scaleModel(600)
	fresh := append([]compose.Stack(nil), m.stacks...)
	b.ReportAllocs()
	for b.Loop() {
		out, _ := m.Update(stacksLoadedMsg{stacks: append([]compose.Stack(nil), fresh...), dir: m.cfg.StackDir})
		_ = out.(Model).viewContent()
	}
}

// Une frappe ↓ puis le rendu qui suit.
func BenchmarkCursorDown600(b *testing.B) {
	m := scaleModel(600)
	_ = m.viewContent()
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	b.ReportAllocs()
	for b.Loop() {
		out, _ := m.Update(down)
		m = out.(Model)
		if m.cursor >= len(m.listRows())-2 {
			m.cursor = 0
		}
		_ = m.viewContent()
	}
}

// Rendu seul, liste de 600 stacks.
func BenchmarkView600(b *testing.B) {
	m := scaleModel(600)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.viewContent()
	}
}

// Saisie d'un caractère dans le filtre, puis rendu.
func BenchmarkFilterType600(b *testing.B) {
	m := scaleModel(600)
	m = press(m, "/")
	b.ReportAllocs()
	for b.Loop() {
		m2 := press(m, "s")
		_ = m2.viewContent()
	}
}
