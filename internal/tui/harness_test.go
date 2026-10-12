package tui

// Harnais commun : le Model dans un vrai tea.Program (rendu compris, sortie
// jetée), piloté par messages, avec lecture de son état à tout moment.

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
)

// queryMsg demande au programme son état et son écran. L'écran est rendu
// dans la boucle du programme : le rendre depuis le test ferait courir les
// caches partagés du Model (aperçu, lignes) avec le rendu du programme.
type queryMsg struct{ reply chan snap }

type snap struct {
	m      Model
	screen string // sans séquences ANSI
}
type mutateMsg func(*Model)

// probe enveloppe le Model dans un vrai tea.Program (rendu compris, sortie
// jetée) et compte ce qui passe.
type probe struct {
	Model
	stats *probeStats
	swap  func(dockerService) dockerService // remplace le client à l'init
}

type probeStats struct {
	logLines   int64
	lastLogSeq int64 // dernier compteur « zzd <i> » vu
	progEvents int64
	progDone   chan struct{}
}

func (p probe) Init() tea.Cmd { return p.Model.Init() }
func (p probe) View() tea.View {
	return p.Model.View()
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case queryMsg:
		msg.reply <- snap{p.Model, ansi.Strip(p.Model.viewContent())}
		return p, nil
	case mutateMsg:
		msg(&p.Model)
		return p, nil
	case clientReadyMsg:
		if p.swap != nil && msg.client != nil {
			msg.client = p.swap(msg.client)
		}
		mm, cmd := p.Model.Update(msg)
		p.Model = mm.(Model)
		return p, cmd
	case logLinesMsg:
		if msg.seq == p.Model.logSeq {
			p.stats.logLines += int64(len(msg.entries))
			last := msg.entries[len(msg.entries)-1].Line
			if f := strings.Fields(last); len(f) > 1 && f[0] == "zzd" {
				if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					p.stats.lastLogSeq = n
				}
			}
		}
	case progressEventMsg:
		if msg.seq == p.Model.progressSeq {
			p.stats.progEvents += int64(len(msg.evs))
		}
	}
	mm, cmd := p.Model.Update(msg)
	p.Model = mm.(Model)
	if p.Model.view == viewProgress && p.Model.progressDone && p.stats.progDone != nil {
		select {
		case p.stats.progDone <- struct{}{}:
		default:
		}
	}
	return p, cmd
}

type harness struct {
	t     *testing.T
	p     *tea.Program
	stats *probeStats
	done  chan struct{}
}

func startHarness(t *testing.T, dir string, swap func(dockerService) dockerService) *harness {
	cfg := &config.Config{StackDir: dir, MaxParallel: 4}
	st := &probeStats{progDone: make(chan struct{}, 1)}
	p := tea.NewProgram(probe{Model: New(cfg), stats: st, swap: swap},
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(190, 50),
		tea.WithFPS(renderFPSForTests), tea.WithoutSignalHandler())
	h := &harness{t: t, p: p, stats: st, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		if _, err := p.Run(); err != nil {
			t.Errorf("programme : %v", err)
		}
	}()
	t.Cleanup(h.stop)
	return h
}

// renderFPSForTests : même cadence que cmd/dockstack.
const renderFPSForTests = 20

func (h *harness) stop() {
	select {
	case <-h.done:
		return
	default:
	}
	h.p.Quit()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		h.p.Kill()
	}
}

func (h *harness) snapshot() snap {
	r := make(chan snap, 1)
	h.p.Send(queryMsg{reply: r})
	select {
	case s := <-r:
		return s
	case <-time.After(10 * time.Second):
		h.t.Fatal("le programme ne répond plus")
	}
	return snap{}
}

func (h *harness) model() Model   { return h.snapshot().m }
func (h *harness) screen() string { return h.snapshot().screen }

// waitScreen attend que l'état et l'écran vérifient ok.
func (h *harness) waitScreen(what string, ok func(Model, string) bool, d time.Duration) snap {
	deadline := time.Now().Add(d)
	for {
		s := h.snapshot()
		if ok(s.m, s.screen) {
			return s
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("délai dépassé : %s\n%s", what, s.screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) key(k string) { h.p.Send(keyMsg(k)) }
func (h *harness) waitFor(what string, ok func(Model) bool, d time.Duration) Model {
	deadline := time.Now().Add(d)
	for {
		m := h.model()
		if ok(m) {
			return m
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("délai dépassé : %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// focusStack place le curseur sur la stack nommée (via le Model, sans
// dépendre de l'ordre des lignes).
func (h *harness) focusStack(name string) {
	h.p.Send(mutateMsg(func(m *Model) {
		m.cfg.ShowAll = true
		m.invalidateRows()
		for i, r := range m.listRows() {
			if r.isStack() && r.stack.Name == name {
				m.cursor = i
			}
		}
	}))
	h.waitFor("curseur sur "+name, func(m Model) bool {
		s, ok := m.cursorStack()
		return ok && s.Name == name
	}, 5*time.Second)
}

func writeStack(t *testing.T, root, name, yaml string) compose.Stack {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(f, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return compose.Stack{Name: name, Dir: dir, ComposeFile: f}
}
