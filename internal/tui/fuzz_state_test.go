package tui

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
)

// stacksFromBytes : 4 octets par stack (nom, groupe, en marche/total,
// malsains), valeurs incohérentes comprises (en marche > total, négatifs).
func stacksFromBytes(data []byte) []compose.Stack {
	var out []compose.Stack
	for i := 0; i+3 < len(data) && len(out) < 64; i += 4 {
		b := data[i : i+4]
		out = append(out, compose.Stack{
			Name:      fmt.Sprintf("s%d", b[0]%12), // doublons possibles
			Group:     []string{"", "media", "infra"}[int(b[1])%3],
			Running:   int(int8(b[2])) % 5,
			Total:     int(b[2]>>4) % 5,
			Unhealthy: int(int8(b[3])) % 3,
		})
	}
	return out
}

// FuzzSortByState : rien de perdu ni dupliqué, groupes contigus et triés
// par meilleur état puis nom, membres triés par état puis nom, et ordre
// d'entrée conservé à clé égale (tri stable).
func FuzzSortByState(f *testing.F) {
	f.Add([]byte{1, 0, 0x11, 0, 2, 1, 0x20, 0, 1, 0, 0x11, 1, 3, 2, 0, 0})
	f.Add([]byte{0, 0, 0xff, 0xff, 0, 0, 0x80, 0x7f})
	f.Fuzz(func(t *testing.T, data []byte) {
		in := stacksFromBytes(data)
		for i := range in {
			in[i].Dir = fmt.Sprint(i) // identité d'entrée, pour la stabilité
		}
		out := sortByState(slices.Clone(in))
		if len(out) != len(in) {
			t.Fatalf("%d stacks en entrée, %d en sortie", len(in), len(out))
		}
		key := func(s compose.Stack) string { return s.Dir }
		a, b := make([]string, len(in)), make([]string, len(out))
		for i := range in {
			a[i], b[i] = key(in[i]), key(out[i])
		}
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			t.Fatal("stacks perdues ou dupliquées")
		}
		seen := map[string]bool{}
		best := map[string]int{}
		for _, s := range in {
			p := statePriority(s.State())
			if v, ok := best[s.Group]; !ok || p < v {
				best[s.Group] = p
			}
		}
		for i, s := range out {
			if i > 0 && out[i-1].Group != s.Group {
				if seen[s.Group] {
					t.Fatalf("groupe %q non contigu", s.Group)
				}
				pg, g := out[i-1].Group, s.Group
				if best[pg] > best[g] || (best[pg] == best[g] && pg > g) {
					t.Fatalf("groupes mal ordonnés : %q avant %q", pg, g)
				}
			}
			seen[s.Group] = true
			if i == 0 || out[i-1].Group != s.Group {
				continue
			}
			p, q := out[i-1], s
			pp, qp := statePriority(p.State()), statePriority(q.State())
			switch {
			case pp > qp, pp == qp && p.Name > q.Name:
				t.Fatalf("membres mal ordonnés : %+v avant %+v", p, q)
			case pp == qp && p.Name == q.Name:
				var ip, iq int
				fmt.Sscan(p.Dir, &ip)
				fmt.Sscan(q.Dir, &iq)
				if ip > iq {
					t.Fatalf("tri instable : %s avant %s", p.Dir, q.Dir)
				}
			}
		}
		// L'état agrégé est toujours une valeur connue.
		for _, s := range in {
			switch s.State() {
			case compose.StateUnknown, compose.StateUnhealthy, compose.StateRunning, compose.StatePartial, compose.StateStopped:
			default:
				t.Fatalf("état inconnu pour %+v", s)
			}
		}
	})
}

// FuzzDiffCapture : chaque entrée de la capture ressort une fois, classée
// (à relancer, introuvable, en marche) dans l'ordre de la capture à statut
// égal ; introuvable exactement quand la stack n'existe pas.
func FuzzDiffCapture(f *testing.F) {
	f.Add([]byte{1, 0, 0x11, 0, 2, 1, 0x20, 0}, []byte{1, 3, 2, 0, 5, 1})
	f.Add([]byte{}, []byte{7, 7, 7})
	f.Fuzz(func(t *testing.T, sdata, cdata []byte) {
		stacks := stacksFromBytes(sdata)
		for i := range stacks {
			stacks[i].Services = []compose.ServiceStatus{
				{Name: "web", Running: stacks[i].Running, Total: 1},
				{Name: "db", Running: 0, Total: 1},
			}
		}
		var items []backup.Entry
		for i := 0; i+1 < len(cdata) && len(items) < 64; i += 2 {
			e := backup.Entry{Name: fmt.Sprintf("s%d", cdata[i]%14)}
			switch cdata[i+1] % 4 {
			case 1:
				e.Services = []string{"web"}
			case 2:
				e.Services = []string{"db", "web"}
			case 3:
				e.Services = []string{"absent"}
			}
			items = append(items, e)
		}
		out := diffCapture(items, stacks)
		if len(out) != len(items) {
			t.Fatalf("%d entrées, %d en sortie", len(items), len(out))
		}
		exists := map[string]bool{}
		for _, s := range stacks {
			exists[s.Name] = true
		}
		// Position d'origine de chaque sortie (les noms peuvent se répéter :
		// on consomme les entrées dans l'ordre).
		used := make([]bool, len(items))
		lastKind, lastPos := captureEntryKind(-1), -1
		for _, e := range out {
			pos := -1
			for i, it := range items {
				if !used[i] && it.Name == e.name && slices.Equal(it.Services, e.services) {
					pos = i
					break
				}
			}
			if pos < 0 {
				t.Fatalf("entrée %q inventée ou dupliquée", e.name)
			}
			used[pos] = true
			if e.kind < lastKind {
				t.Fatalf("statuts mal ordonnés")
			}
			if e.kind == lastKind && pos < lastPos {
				t.Fatalf("ordre de la capture perdu pour %q", e.name)
			}
			lastKind, lastPos = e.kind, pos
			if (e.kind == entryMissing) != !exists[e.name] {
				t.Fatalf("%q : introuvable=%v, existe=%v", e.name, e.kind == entryMissing, exists[e.name])
			}
		}
	})
}
