package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
	"github.com/charmbracelet/x/ansi"
)

// TestSwapStyle : Swap neutre tant que la RAM n'est pas sous pression.
func TestSwapStyle(t *testing.T) {
	calm := monitor.Metrics{MemPercent: 48, SwapPercent: 84}
	if swapStyle(calm).GetForeground() == colorForPct(84).GetForeground() {
		t.Error("RAM à 48 % : le Swap ne doit pas être coloré en alerte")
	}
	tight := monitor.Metrics{MemPercent: 90, SwapPercent: 84}
	if swapStyle(tight).GetForeground() != colorForPct(84).GetForeground() {
		t.Error("RAM à 90 % : le Swap doit reprendre sa couleur d'alerte")
	}
}

func TestDockerLine(t *testing.T) {
	sc := StackCounts{Total: 62, Running: 14, Stopped: 1, NotDep: 47}
	if got := ansi.Strip(dockerLine(nil, sc)); !strings.Contains(got, "Conteneurs …") || !strings.Contains(got, "14/15") {
		t.Errorf("sans mesure : %q", got)
	}
	got := ansi.Strip(dockerLine(&compose.DockerUsage{Running: 39, Containers: 40,
		ImagesSize: 85 << 30, ImagesReclaimable: 24 << 30, ImagesExact: true, ImagesOK: true, VolumesSize: 512 << 20, VolumesOK: true}, sc))
	for _, want := range []string{"Stacks ⚠ 14/15 (■ 1 arrêtée)", "Conteneurs 39/40", "Images 85.0 Go (24.0 Go libérables)", "Volumes 512 Mo"} {
		if !strings.Contains(got, want) {
			t.Errorf("ligne Docker %q : %q absent", got, want)
		}
	} // Libérable estimé (image non détaillée couche par couche) : « ≈ ».
	got = ansi.Strip(dockerLine(&compose.DockerUsage{ImagesSize: 85 << 30, ImagesReclaimable: 24 << 30, ImagesOK: true}, sc))
	if !strings.Contains(got, "(≈ 24.0 Go libérables)") {
		t.Errorf("estimation non signalée : %q", got)
	}
}

// TestUsageKeepsVolumes : une mesure sans volumes (tick ordinaire) conserve
// la dernière taille connue des volumes au lieu de l'effacer.
func TestUsageKeepsVolumes(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	out, _ := m.Update(usageMsg{usage: compose.DockerUsage{Containers: 2, VolumesSize: 7, VolumesOK: true}})
	out, _ = out.Update(usageMsg{usage: compose.DockerUsage{Containers: 3}})
	got := out.(Model).usage
	if got == nil || got.Containers != 3 || !got.VolumesOK || got.VolumesSize != 7 {
		t.Errorf("usage = %+v, want Containers 3 et volumes conservés (7)", got)
	}
}

// TestUsageTickNoOverlap : pas de nouvelle requête tant qu'une mesure court.
func TestUsageTickNoOverlap(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	m.client = &fakeClient{}
	m.usageBusy = true
	out, _ := m.Update(usageTickMsg{})
	if !out.(Model).usageBusy {
		t.Error("usageBusy ne doit pas retomber sur un tick")
	}
}

func TestStacksSummary(t *testing.T) {
	for _, tc := range []struct {
		sc   StackCounts
		want string
	}{
		{StackCounts{Total: 63, Running: 15, NotDep: 48}, "✔ 15/15"},
		{StackCounts{Total: 63, Running: 12, Unhealthy: 1, Partial: 1, Stopped: 1, NotDep: 48},
			"⚠ 12/15 (▲ 1 unhealthy · ◐ 1 partielle · ■ 1 arrêtée)"},
		{StackCounts{Total: 5, NotDep: 5}, "aucune déployée"},
	} {
		if got := ansi.Strip(stacksSummary(tc.sc)); got != tc.want {
			t.Errorf("%+v : %q, want %q", tc.sc, got, tc.want)
		}
	}
}

// Le compteur du démon inclut les conteneurs hors compose : l'en-tête doit
// expliquer l'écart avec les stacks et signaler les arrêtés parmi eux.
func TestDockerItemsOutsideStacks(t *testing.T) {
	du := &compose.DockerUsage{Running: 39, Containers: 40}
	sc := StackCounts{Total: 14, CtrRunning: 28, CtrTotal: 28}
	got := ansi.Strip(dockerItems(du, sc, true)[1])
	if !strings.Contains(got, "39/40") || !strings.Contains(got, "12 hors stack (1 arrêté)") {
		t.Fatalf("écart non expliqué : %q", got)
	}
	// Tous les conteneurs sont dans des stacks : aucune note.
	got = ansi.Strip(dockerItems(du, StackCounts{Total: 14, CtrRunning: 39, CtrTotal: 40}, true)[1])
	if strings.Contains(got, "hors stack") {
		t.Fatalf("note superflue : %q", got)
	}
}

// TestImagesRefreshCycle : la taille des images (coûteuse pour le démon)
// n'est relue qu'au premier passage, à la cadence des volumes, ou après un
// événement qui peut la changer ; un tick ordinaire ne lit que les
// compteurs et conserve les images connues.
func TestImagesRefreshCycle(t *testing.T) {
	m := New(&config.Config{StackDir: "/tmp"})
	m.client = &fakeClient{}
	opts := func(m *Model) compose.UsageOptions {
		t.Helper()
		cmd := m.startUsage()
		if !m.usageBusy {
			t.Fatal("usageBusy doit être levé")
		}
		m.usageBusy = false
		return cmd().(usageMsg).opts
	}
	if o := opts(&m); !o.Images || !o.Volumes {
		t.Fatalf("premier passage : %+v, attendu images et volumes", o)
	}
	out, _ := m.Update(usageMsg{opts: compose.UsageOptions{Images: true, Volumes: true},
		usage: compose.DockerUsage{Containers: 2, ImagesSize: 9, ImagesOK: true, VolumesOK: true}})
	m = out.(Model)
	if o := opts(&m); o.Images || o.Volumes {
		t.Fatalf("tick ordinaire : %+v, attendu compteurs seuls", o)
	}
	out, _ = m.Update(usageMsg{usage: compose.DockerUsage{Containers: 3}})
	m = out.(Model)
	if u := m.usage; !u.ImagesOK || u.ImagesSize != 9 || u.Containers != 3 {
		t.Fatalf("images non conservées : %+v", u)
	}

	// Un exec de contrôle de santé ne touche pas aux images ; un pull, si.
	out, _ = m.Update(dockerEventMsg{Type: "container", Action: "exec_start"})
	if m = out.(Model); m.imagesStale {
		t.Fatal("exec_start ne doit pas invalider les images")
	}
	out, cmd := m.Update(dockerEventMsg{Type: "image", Action: "pull"})
	if m = out.(Model); !m.imagesStale || cmd == nil {
		t.Fatal("pull doit invalider les images et programmer leur relecture")
	}
	out, cmd = m.Update(imagesRefreshMsg{})
	m = out.(Model)
	if cmd == nil || !m.usageBusy || m.imagesStale {
		t.Fatalf("relecture non lancée (busy=%v stale=%v)", m.usageBusy, m.imagesStale)
	}
	msg := cmd().(usageMsg)
	if !msg.opts.Images || msg.opts.Volumes {
		t.Fatalf("relecture : %+v, attendu images seules", msg.opts)
	}
	// Échec : les images restent à relire au prochain passage.
	out, _ = m.Update(usageMsg{opts: msg.opts, err: context.DeadlineExceeded})
	if m = out.(Model); !m.imagesStale {
		t.Fatal("après un échec, les images doivent rester à relire")
	}
	if o := opts(&m); !o.Images {
		t.Fatal("le passage suivant doit relire les images")
	}
}

// narrowMetrics : mesures d'une machine ordinaire, pour l'en-tête étroit.
func narrowMetrics(cores int) monitor.Metrics {
	return monitor.Metrics{CPUPercent: 23, CPUCores: make([]int, cores), NCores: cores,
		MemUsedGB: 14.6, MemTotalGB: 31.2, MemPercent: 46, SwapUsedGB: 0.4, SwapTotalGB: 4, SwapPercent: 10,
		DiskUsedGB: 162, DiskTotalGB: 194, DiskPercent: 88, DiskReadKBs: 320, DiskWriteKBs: 2200,
		Load1: "1.12", Load5: "0.98", Load15: "0.87", Uptime: "12j 3h"}
}

// Sous 120 colonnes, l'en-tête tient en 4 lignes quel que soit le nombre de
// cœurs (10 en 80x24 avec 8 cœurs, 16 avec 32), en français, sans « Ko /s »
// ni charge tronquée ; IO, charge et images s'ajoutent quand la place le
// permet.
func TestNarrowHeaderFourLines(t *testing.T) {
	sc := StackCounts{Dir: "/srv/stacks", Total: 12, Running: 8, Unhealthy: 1, Stopped: 3, CtrRunning: 21, CtrTotal: 27}
	du := &compose.DockerUsage{Running: 21, Containers: 27, ImagesSize: 88 << 30, ImagesOK: true}
	for _, cores := range []int{8, 16, 32} {
		for _, w := range []int{60, 80, 100, 110, 119} {
			h := renderHeader(narrowMetrics(cores), metricsHistory{}, sc, du, w, 100, true)
			lines := strings.Split(ansi.Strip(h), "\n")
			if len(lines) != 4 || headerHeight(cores, w, 100) != 4 {
				t.Fatalf("%d cœurs, w=%d : %d lignes (headerHeight %d), want 4\n%s", cores, w, len(lines), headerHeight(cores, w, 100), ansi.Strip(h))
			}
			for _, bad := range []string{"cores", "avg", "Disk ", "Ko /s", "1m:"} {
				if strings.Contains(ansi.Strip(h), bad) {
					t.Errorf("%d cœurs, w=%d : %q dans l'en-tête\n%s", cores, w, bad, ansi.Strip(h))
				}
			}
			if w < 80 {
				continue
			}
			for _, want := range []string{"CPU", "RAM", "Swap", "Disque"} {
				if !strings.Contains(lines[1], want) {
					t.Errorf("w=%d : %q absent de la ligne des ressources %q", w, want, lines[1])
				}
			}
			if !strings.Contains(lines[2], "Stacks ⚠ 8/12") || !strings.Contains(lines[2], "Conteneurs 21/27") {
				t.Errorf("w=%d : ligne Docker %q", w, lines[2])
			}
			wide := w >= 100
			if got := strings.Contains(lines[1], "IO ↓320Ko/s ↑2.1Mo/s"); got != wide {
				t.Errorf("w=%d : IO affiché = %v, want %v : %q", w, got, wide, lines[1])
			}
			if got := strings.Contains(lines[2], "Images 88.0 Go"); got != wide {
				t.Errorf("w=%d : Images affiché = %v, want %v : %q", w, got, wide, lines[2])
			}
		}
	}
	if l := strings.Split(ansi.Strip(renderHeader(narrowMetrics(8), metricsHistory{}, sc, du, 119, 100, true)), "\n")[1]; !strings.Contains(l, "Charge 1.12 0.98 0.87") {
		t.Errorf("w=119 : charge absente ou tronquée : %q", l)
	}
}

// L'IO de l'en-tête étroit apparaît à une largeur donnée quel que soit le
// débit : décidée sur la largeur réelle, elle clignoterait au gré des
// mesures.
func TestNarrowHeaderIOStable(t *testing.T) {
	sc := StackCounts{Dir: "/srv", Total: 1, Running: 1}
	for w := 80; w < compactHeaderMinWidth; w++ {
		var shown []bool
		for _, kbs := range []int{5, 1000, 5000, 900000} {
			mt := narrowMetrics(8)
			mt.DiskReadKBs, mt.DiskWriteKBs = kbs, kbs
			l := strings.Split(ansi.Strip(renderHeader(mt, metricsHistory{}, sc, nil, w, 100, true)), "\n")[1]
			shown = append(shown, strings.Contains(l, "IO ↓"))
		}
		for _, s := range shown[1:] {
			if s != shown[0] {
				t.Fatalf("w=%d : IO affiché selon le débit %v", w, shown)
			}
		}
	}
}
