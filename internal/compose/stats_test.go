package compose

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
)

func TestCPUPercent(t *testing.T) {
	prev := usageSample{total: 1_000, system: 10_000}
	cur := usageSample{total: 1_050, system: 11_000}
	// 50/1000 du temps système sur 4 cœurs = 20 % d'un cœur, comme docker stats.
	if got, ok := cpuPercent(prev, cur, 4); !ok || got < 19.99 || got > 20.01 {
		t.Errorf("cpuPercent = %v, %v ; veut 20, true", got, ok)
	}
	// Conteneur inactif : 0 valide, pas « inconnu ».
	if got, ok := cpuPercent(prev, usageSample{total: 1_000, system: 11_000}, 4); !ok || got != 0 {
		t.Errorf("inactif : %v, %v", got, ok)
	}
	// Compteur en recul (redémarrage) ou temps système figé : inconnu, jamais négatif.
	if _, ok := cpuPercent(prev, usageSample{total: 500, system: 11_000}, 4); ok {
		t.Error("compteur en recul : devrait être inconnu")
	}
	if _, ok := cpuPercent(prev, usageSample{total: 1_050, system: 10_000}, 4); ok {
		t.Error("temps système figé : devrait être inconnu")
	}
}

func TestCPUCores(t *testing.T) {
	var st container.StatsResponse
	if got := cpuCores(st); got != 1 {
		t.Errorf("sans info : %v, veut 1", got)
	}
	st.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2}
	if got := cpuCores(st); got != 2 {
		t.Errorf("percpu : %v, veut 2", got)
	}
	st.CPUStats.OnlineCPUs = 8
	if got := cpuCores(st); got != 8 {
		t.Errorf("online_cpus prioritaire : %v, veut 8", got)
	}
}

func TestMemUsageExcludesCache(t *testing.T) {
	var st container.StatsResponse
	st.MemoryStats.Usage = 500
	st.MemoryStats.Stats = map[string]uint64{"inactive_file": 200}
	if got := memUsage(st); got != 300 {
		t.Errorf("cgroup v2 : %d, veut 300", got)
	}
	st.MemoryStats.Stats = map[string]uint64{"total_inactive_file": 100}
	if got := memUsage(st); got != 400 {
		t.Errorf("cgroup v1 : %d, veut 400", got)
	}
	// Cache supérieur à l'usage (mesures décalées) : on garde l'usage brut.
	st.MemoryStats.Stats = map[string]uint64{"inactive_file": 900}
	if got := memUsage(st); got != 500 {
		t.Errorf("cache > usage : %d, veut 500", got)
	}
}

func TestBlkioBytes(t *testing.T) {
	var st container.StatsResponse
	st.BlkioStats.IoServiceBytesRecursive = []container.BlkioStatEntry{
		{Major: 8, Minor: 0, Op: "read", Value: 100},
		{Major: 8, Minor: 0, Op: "write", Value: 40},
		{Major: 259, Minor: 0, Op: "Read", Value: 50}, // cgroup v1 : casse différente
		{Major: 259, Minor: 0, Op: "Total", Value: 999},
		{Major: 259, Minor: 0, Op: "discard", Value: 7},
	}
	if rd, wr := blkioBytes(st); rd != 150 || wr != 40 {
		t.Errorf("lu/écrit = %d/%d, veut 150/40", rd, wr)
	}
}

func TestIORates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	prev := usageSample{read: 1_000, write: 5_000, at: t0}
	cur := usageSample{read: 3_000, write: 5_000, at: t0.Add(2 * time.Second)}
	if rd, wr, ok := ioRates(prev, cur); !ok || rd != 1000 || wr != 0 {
		t.Errorf("débits = %v %v %v, veut 1000 0 true", rd, wr, ok)
	}
	// Compteur en recul (redémarrage) ou intervalle nul : inconnu, jamais négatif.
	if _, _, ok := ioRates(prev, usageSample{read: 10, write: 5_000, at: t0.Add(time.Second)}); ok {
		t.Error("compteur en recul : devrait être inconnu")
	}
	if _, _, ok := ioRates(prev, usageSample{read: 2_000, write: 5_000, at: t0}); ok {
		t.Error("intervalle nul : devrait être inconnu")
	}
}
