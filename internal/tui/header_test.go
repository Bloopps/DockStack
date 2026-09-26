package tui

import (
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
	if got := ansi.Strip(dockerLine(nil, sc)); !strings.Contains(got, "mesure en cours") || !strings.Contains(got, "14/15") {
		t.Errorf("sans mesure : %q", got)
	}
	got := ansi.Strip(dockerLine(&compose.DockerUsage{Running: 39, Containers: 40,
		ImagesSize: 85 << 30, ImagesReclaimable: 24 << 30, VolumesSize: 512 << 20, VolumesOK: true}, sc))
	for _, want := range []string{"⚠ 14/15 en marche (● 1 arrêtée)", "conteneurs 39/40", "images 85.0 Go", "dont 24.0 Go inutilisées", "volumes 512 Mo"} {
		if !strings.Contains(got, want) {
			t.Errorf("ligne Docker %q : %q absent", got, want)
		}
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
		{StackCounts{Total: 63, Running: 15, NotDep: 48}, "✔ 15/15 en marche"},
		{StackCounts{Total: 63, Running: 12, Unhealthy: 1, Partial: 1, Stopped: 1, NotDep: 48},
			"⚠ 12/15 en marche (● 1 unhealthy · ● 1 partielle · ● 1 arrêtée)"},
		{StackCounts{Total: 5, NotDep: 5}, "aucune déployée"},
	} {
		if got := ansi.Strip(stacksSummary(tc.sc)); got != tc.want {
			t.Errorf("%+v : %q, want %q", tc.sc, got, tc.want)
		}
	}
}
