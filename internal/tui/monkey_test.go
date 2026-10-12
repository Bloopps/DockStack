package tui

import (
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// monkeyKeys : toutes les touches que l'application traite, plus quelques
// autres (lettres sans rôle, majuscules du verrouillage).
var monkeyKeys = func() []tea.KeyPressMsg {
	keys := []tea.KeyPressMsg{
		{Code: tea.KeyUp}, {Code: tea.KeyDown}, {Code: tea.KeyLeft}, {Code: tea.KeyRight},
		{Code: tea.KeyEnter}, {Code: tea.KeyEscape}, {Code: tea.KeyTab}, {Code: tea.KeySpace, Text: " "},
		{Code: tea.KeyBackspace}, {Code: tea.KeyDelete}, {Code: tea.KeyHome}, {Code: tea.KeyEnd},
		{Code: tea.KeyPgUp}, {Code: tea.KeyPgDown}, {Code: 'a', Mod: tea.ModCtrl},
	}
	for _, r := range "abcdefghijklmnopqrstuvwxyzGQRT?/+*-.é0123 " {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return keys
}()

// drive applique msg puis rejoue les commandes qui répondent vite (résultats
// asynchrones : prévision d'un up, flux de progression déjà terminés,
// rechargements) ; les minuteurs, eux, sont abandonnés.
func drive(t *testing.T, m Model, msg tea.Msg, depth int) Model {
	out, cmd := m.Update(msg)
	m = out.(Model)
	if depth > 3 || cmd == nil {
		return m
	}
	for _, next := range runQuick(cmd) {
		if _, quit := next.(tea.QuitMsg); quit {
			continue
		}
		m = drive(t, m, next, depth+1)
	}
	return m
}

// quickWait : délai au-delà duquel runQuick abandonne un message (minuteurs).
// Un test qui attend un lot de logs (livré après logFrame) l'allonge.
var quickWait = 20 * time.Millisecond

// runQuick exécute cmd (et les lots qu'il produit) et garde les messages
// obtenus en moins de quickWait.
func runQuick(cmd tea.Cmd) []tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				if c != nil {
					out = append(out, runQuick(c)...)
				}
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(quickWait):
		return nil
	}
}

// TestMonkeyKeys : des milliers de frappes au hasard dans toutes les vues,
// y compris pendant une opération et pendant une saisie, avec des
// redimensionnements : jamais de panique, et l'écran garde sa taille.
func TestMonkeyKeys(t *testing.T) {
	// 12 séquences par défaut (≈ 10 s) ; DOCKSTACK_MONKEY=200 pour une longue
	// passe.
	seeds := 12
	if n, err := strconv.Atoi(os.Getenv("DOCKSTACK_MONKEY")); err == nil && n > 0 {
		seeds = n
	}
	for seed := range seeds {
		rng := rand.New(rand.NewSource(int64(seed)))
		m := screenModel(40)
		m.client = &fakeClient{stacks: m.stacks, autoDone: true}
		m.width, m.height = 160, 45
		var trail []string
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("graine %d : panique %v après %v", seed, r, trail[max(0, len(trail)-15):])
				}
			}()
			for range 400 {
				if rng.Intn(40) == 0 {
					w, h := 20+rng.Intn(260), 5+rng.Intn(80)
					trail = append(trail, "resize")
					m = drive(t, m, tea.WindowSizeMsg{Width: w, Height: h}, 0)
					continue
				}
				k := monkeyKeys[rng.Intn(len(monkeyKeys))]
				trail = append(trail, k.String())
				m = drive(t, m, k, 0)
				out := m.viewContent()
				if m.width >= 100 && m.height >= 30 && (m.view == viewList || m.view == viewHelp || m.view == viewConfirm || m.view == viewAction || m.view == viewTheme) {
					if n := strings.Count(out, "\n") + 1; n != m.height {
						t.Fatalf("graine %d, vue %d : %d lignes pour %d après %v", seed, m.view, n, m.height, trail[max(0, len(trail)-15):])
					}
					for _, l := range strings.Split(out, "\n") {
						if w := ansi.StringWidth(l); w > m.width {
							t.Fatalf("graine %d, vue %d : ligne de %d colonnes pour %d après %v\n%q", seed, m.view, w, m.width, trail[max(0, len(trail)-15):], ansi.Strip(l))
						}
					}
				}
			}
		}()
	}
}

// TestMonkeyAsync : comme TestMonkeyKeys, avec en plus toutes les majuscules
// (verrouillage), la molette, des collages (courts, longs, multilignes) et
// des messages asynchrones livrés en retard et dans le désordre : frappes
// pendant une capture en cours, une prévision, un chargement ou une
// opération. Jamais de panique, et l'écran garde sa taille.
func TestMonkeyAsync(t *testing.T) {
	seeds := 12
	if n, err := strconv.Atoi(os.Getenv("DOCKSTACK_MONKEY")); err == nil && n > 0 {
		seeds = n
	}
	keys := append([]tea.KeyPressMsg(nil), monkeyKeys...)
	for r := 'A'; r <= 'Z'; r++ {
		keys = append(keys, tea.KeyPressMsg{Code: r + 'a' - 'A', ShiftedCode: r, Mod: tea.ModShift, Text: string(r)})
	}
	pastes := []string{"web", "imm\nich", strings.Repeat("ab\tc ", 3000), "udrcp", "\x1b[2J日本語🎉", ""}
	for seed := range seeds {
		rng := rand.New(rand.NewSource(int64(1000 + seed)))
		m := screenModel(40)
		m.client = &fakeClient{stacks: m.stacks, autoDone: true}
		m.width, m.height = 160, 45
		var pending []tea.Msg
		var trail []string
		var deliver func(msg tea.Msg, depth int)
		deliver = func(msg tea.Msg, depth int) {
			out, cmd := m.Update(msg)
			m = out.(Model)
			if cmd == nil || depth > 3 {
				return
			}
			for _, next := range runQuick(cmd) {
				if _, quit := next.(tea.QuitMsg); quit {
					continue
				}
				// Un message sur trois arrive plus tard, après d'autres frappes.
				if rng.Intn(3) == 0 {
					pending = append(pending, next)
					continue
				}
				deliver(next, depth+1)
			}
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("graine %d : panique %v après %v", seed, r, trail[max(0, len(trail)-15):])
				}
			}()
			for range 400 {
				switch x := rng.Intn(100); {
				case x < 2:
					trail = append(trail, "resize")
					deliver(tea.WindowSizeMsg{Width: 20 + rng.Intn(260), Height: 5 + rng.Intn(80)}, 0)
				case x < 8:
					b := tea.MouseWheelUp
					if rng.Intn(2) == 0 {
						b = tea.MouseWheelDown
					}
					trail = append(trail, "molette")
					deliver(tea.MouseWheelMsg{Button: b}, 0)
				case x < 11:
					trail = append(trail, "collage")
					deliver(tea.PasteMsg{Content: pastes[rng.Intn(len(pastes))]}, 0)
				case x < 25 && len(pending) > 0:
					i := rng.Intn(len(pending))
					msg := pending[i]
					pending = append(pending[:i], pending[i+1:]...)
					trail = append(trail, "tardif")
					deliver(msg, 1)
				default:
					k := keys[rng.Intn(len(keys))]
					trail = append(trail, k.String())
					deliver(k, 0)
				}
				out := m.viewContent()
				if m.width >= 100 && m.height >= 30 && (m.view == viewList || m.view == viewHelp || m.view == viewConfirm || m.view == viewAction || m.view == viewTheme) {
					if n := strings.Count(out, "\n") + 1; n != m.height {
						t.Fatalf("graine %d, vue %d : %d lignes pour %d après %v", seed, m.view, n, m.height, trail[max(0, len(trail)-15):])
					}
					for _, l := range strings.Split(out, "\n") {
						if w := ansi.StringWidth(l); w > m.width {
							t.Fatalf("graine %d, vue %d : ligne de %d colonnes pour %d après %v\n%q", seed, m.view, w, m.width, trail[max(0, len(trail)-15):], ansi.Strip(l))
						}
					}
				}
			}
		}()
	}
}
