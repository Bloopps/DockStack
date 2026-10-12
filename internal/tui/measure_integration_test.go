//go:build integration

package tui

// Mesures de la passe complémentaire (2026-10-01). Ignorées sauf avec
// DOCKSTACK_MEASURE=1 : elles durent plusieurs minutes et chargent le démon.
// Seuls des projets jetables zzdsprobe* sont déployés ; HOME est isolé par
// TestMain.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Bloopps/dockstack/internal/compose"
)

func measureGate(t *testing.T) {
	if os.Getenv("DOCKSTACK_MEASURE") == "" {
		t.Skip("mesure longue : DOCKSTACK_MEASURE=1 pour la lancer")
	}
}

func cpuSelf() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// cpuOfPid : temps CPU cumulé d'un processus (utime + stime de /proc/pid/stat).
func cpuOfPid(pid int) time.Duration {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	ut, _ := strconv.ParseInt(f[11], 10, 64)
	st, _ := strconv.ParseInt(f[12], 10, 64)
	return time.Duration(ut+st) * (time.Second / 100)
}

func dockerdPid() int {
	out, _ := exec.Command("pidof", "dockerd").Output()
	pid, _ := strconv.Atoi(strings.Fields(string(out) + " 0")[0])
	return pid
}

func fdCount() int {
	e, _ := os.ReadDir("/proc/self/fd")
	return len(e)
}

type memSnap struct {
	goroutines int
	heap       uint64
	fds        int
}

func (s memSnap) String() string {
	return fmt.Sprintf("goroutines %d, tas %.1f Mo, descripteurs %d", s.goroutines, float64(s.heap)/1e6, s.fds)
}

func snapshot() memSnap {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return memSnap{runtime.NumGoroutine(), ms.HeapAlloc, fdCount()}
}

func dockerOut(args ...string) string {
	out, _ := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func cleanupProbeContainers() {
	if ids := dockerOut("ps", "-aq", "--filter", "name=zzdsprobe"); ids != "" {
		exec.Command("docker", append([]string{"rm", "-f"}, strings.Fields(ids)...)...).Run()
	}
	if nets := dockerOut("network", "ls", "-q", "--filter", "name=zzdsprobe"); nets != "" {
		exec.Command("docker", append([]string{"network", "rm"}, strings.Fields(nets)...)...).Run()
	}
}

func drainOp(ch <-chan compose.ProgressEvent) (n int, errs []error) {
	for ev := range ch {
		if ev.AllDone {
			return
		}
		n++
		if ev.StackDone && ev.Err != nil {
			errs = append(errs, ev.Err)
		}
	}
	return
}

// ---- 5. logs en suivi ----

func TestMeasureLogsFollow(t *testing.T) {
	measureGate(t)
	c, err := compose.New()
	if err != nil {
		t.Skip(err)
	}
	cleanupProbeContainers()
	t.Cleanup(cleanupProbeContainers)
	window := 20 * time.Second

	for _, tc := range []struct{ name, loop string }{
		{"max", `i=0; while :; do echo "zzd $$i GET /api/v1/items?id=42 200 12ms lorem ipsum dolor sit amet"; i=$$((i+1)); done`},
		{"1000/s", `i=0; while :; do echo "zzd $$i GET /api/v1/items?id=42 200 12ms lorem ipsum dolor sit amet"; i=$$((i+1)); usleep 1000; done`},
	} {
		root := t.TempDir()
		name := "zzdsprobelogs"
		st := writeStack(t, root, name, `services:
  chat:
    image: busybox:latest
    command: ["sh", "-c", '`+tc.loop+`']
    logging:
      driver: json-file
      options: {max-size: "20m", max-file: "2"}
`)
		if _, errs := drainOp(c.UpManyLive(context.Background(), []compose.Stack{st}, 1)); len(errs) > 0 {
			t.Fatal(errs)
		}
		h := startHarness(t, root, nil)
		h.waitFor("liste chargée", func(m Model) bool { return !m.loading && len(m.stacks) == 1 && m.client != nil }, 30*time.Second)
		base := snapshot()
		h.focusStack(name)
		h.key("l")
		h.waitFor("vue logs", func(m Model) bool { return m.view == viewLogs && len(m.logLines) > 0 }, 10*time.Second)
		time.Sleep(3 * time.Second) // historique (tail 500) avalé

		ctr := name + "-chat-1"
		prodSeq := func() int64 {
			f := strings.Fields(dockerOut("logs", "--tail", "1", ctr))
			if len(f) > 1 {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				return n
			}
			return 0
		}
		dpid := dockerdPid()
		lines0, p0, cpu0, d0, t0 := h.stats.logLines, prodSeq(), cpuSelf(), cpuOfPid(dpid), time.Now()
		// Profil CPU de la fenêtre si DOCKSTACK_MEASURE_PROF désigne un dossier.
		if dir := os.Getenv("DOCKSTACK_MEASURE_PROF"); dir != "" {
			f, err := os.Create(filepath.Join(dir, "logs-"+strings.TrimSuffix(tc.name, "/s")+".prof"))
			if err == nil {
				pprof.StartCPUProfile(f)
				defer f.Close()
			}
		}
		time.Sleep(window)
		pprof.StopCPUProfile()
		m := h.model()
		lines1, cons1, p1, cpu1, d1, el := h.stats.logLines, h.stats.lastLogSeq, prodSeq(), cpuSelf(), cpuOfPid(dpid), time.Since(t0)
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("[%s] produit %.0f l/s, affiché %.0f l/s, retard en fin %d lignes, CPU dockstack %.0f %% d'un cœur, dockerd %.0f %%, tas %.1f Mo, %d lignes gardées",
			tc.name, float64(p1-p0)/el.Seconds(), float64(lines1-lines0)/el.Seconds(), p1-cons1,
			100*(cpu1-cpu0).Seconds()/el.Seconds(), 100*(d1-d0).Seconds()/el.Seconds(), float64(ms.HeapAlloc)/1e6, len(m.logLines))

		if tc.name == "max" {
			// 10 ouvertures/fermetures, puis bilan mémoire.
			for range 10 {
				h.key("esc")
				h.waitFor("liste", func(m Model) bool { return m.view == viewList }, 5*time.Second)
				time.Sleep(200 * time.Millisecond)
				h.key("l")
				h.waitFor("logs", func(m Model) bool { return m.view == viewLogs }, 5*time.Second)
				time.Sleep(700 * time.Millisecond)
			}
			h.key("esc")
			h.waitFor("liste", func(m Model) bool { return m.view == viewList }, 5*time.Second)
			time.Sleep(2 * time.Second)
			after := snapshot()
			t.Logf("[%s] avant la 1re ouverture : %v ; après 10 cycles : %v", tc.name, base, after)
		}
		h.stop()
		drainOp(c.DownManyLive(context.Background(), []compose.Stack{st}, 1))
	}
}

// ---- 6. contre-pression de la progression ----

// Images jetables, absentes de la machine avant la mesure, supprimées après.
var pullImages = []string{"httpd:2.4-alpine", "memcached:1.6-alpine", "haproxy:3.0-alpine", "rabbitmq:4.0-alpine"}

func TestMeasurePullBackpressure(t *testing.T) {
	measureGate(t)
	for _, img := range pullImages {
		if dockerOut("image", "ls", "-q", img) != "" {
			t.Fatalf("%s déjà présente : mesure refusée (on ne supprime que ce qu'on a tiré)", img)
		}
	}
	rmi := func() { exec.Command("docker", append([]string{"rmi"}, pullImages...)...).Run() }
	t.Cleanup(rmi)
	c, err := compose.New()
	if err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	var y strings.Builder
	y.WriteString("services:\n")
	for i, img := range pullImages {
		fmt.Fprintf(&y, "  s%d:\n    image: %s\n", i, img)
	}
	st := writeStack(t, root, "zzdsprobepull", y.String())

	for serie := 1; serie <= 2; serie++ {
		// Sans TUI : le canal lu à vide.
		rmi()
		d0, t0 := compose.DroppedWorking(), time.Now()
		n, errs := drainOp(c.PullManyLive(context.Background(), []compose.Stack{st}, 4))
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		t.Logf("[série %d] sans TUI : %v, %d événements reçus, %d « working » abandonnés",
			serie, time.Since(t0).Round(10*time.Millisecond), n, compose.DroppedWorking()-d0)

		// Avec la TUI : vrai programme, rendu à 20 images/s.
		rmi()
		h := startHarness(t, root, nil)
		h.waitFor("liste chargée", func(m Model) bool { return !m.loading && len(m.stacks) == 1 && m.client != nil }, 30*time.Second)
		h.focusStack("zzdsprobepull")
		d0, t0 = compose.DroppedWorking(), time.Now()
		cpu0 := cpuSelf()
		h.key("p")
		select {
		case <-h.stats.progDone:
		case <-time.After(10 * time.Minute):
			t.Fatal("pull trop long")
		}
		el := time.Since(t0)
		m := h.model()
		if m.progressOk != 1 {
			t.Fatalf("pull en échec : %d/%d", m.progressOk, m.progressTotal)
		}
		t.Logf("[série %d] avec TUI : %v, %d événements reçus, %d « working » abandonnés, CPU %.0f %% d'un cœur",
			serie, el.Round(10*time.Millisecond), h.stats.progEvents, compose.DroppedWorking()-d0, 100*(cpuSelf()-cpu0).Seconds()/el.Seconds())
		h.stop()
	}
}

// ---- 11. goroutines, tas et descripteurs dans la durée ----

// slowOpClient : lectures réelles (stacks, occupation, prévision, logs),
// actions simulées par un flux « working » qui ne finit qu'à l'annulation.
// Aucune action compose réelle.
type slowOpClient struct{ *compose.Client }

func (slowOpClient) slow(ctx context.Context, st []compose.Stack) <-chan compose.ProgressEvent {
	ch := make(chan compose.ProgressEvent, 16)
	go func() {
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				for _, s := range st {
					ch <- compose.ProgressEvent{StackName: s.Name, StackDone: true, Err: ctx.Err()}
				}
				ch <- compose.ProgressEvent{AllDone: true}
				return
			case <-tk.C:
				for _, s := range st {
					select {
					case ch <- compose.ProgressEvent{StackName: s.Name, Container: "Image x", Status: "Pulling", State: "working"}:
					default:
					}
				}
			}
		}
	}()
	return ch
}
func (c slowOpClient) UpManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}
func (c slowOpClient) DownManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}
func (c slowOpClient) StopManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}
func (c slowOpClient) RestartManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}
func (c slowOpClient) RecreateManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}
func (c slowOpClient) PullManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}
func (c slowOpClient) RemoveManyLive(ctx context.Context, s []compose.Stack, _ int) <-chan compose.ProgressEvent {
	return c.slow(ctx, s)
}

// Commandes prune simulées : jamais de prune réel depuis la mesure.
func (c slowOpClient) PruneLive(ctx context.Context, _ compose.PruneCommand) <-chan compose.ProgressEvent {
	return c.slow(ctx, []compose.Stack{{Name: "prune"}})
}

// Nettoyage simulé : jamais de suppression réelle depuis la mesure.
func (c slowOpClient) CleanupLive(ctx context.Context, _ []compose.CleanupItem) <-chan compose.ProgressEvent {
	return c.slow(ctx, []compose.Stack{{Name: "nettoyage"}})
}

func TestMeasureLongIdle(t *testing.T) {
	measureGate(t)
	idle := 30 * time.Minute
	if v, err := time.ParseDuration(os.Getenv("DOCKSTACK_MEASURE_IDLE")); err == nil {
		idle = v
	}
	h := startHarness(t, "/home/ubuntu", func(d dockerService) dockerService {
		return slowOpClient{d.(*compose.Client)}
	})
	m := h.waitFor("liste chargée", func(m Model) bool { return !m.loading && len(m.stacks) > 0 && m.client != nil }, 60*time.Second)
	target := ""
	for _, s := range m.stacks {
		if s.State() == compose.StateRunning {
			target = s.Name
			break
		}
	}
	if target == "" {
		t.Skip("aucune stack en marche")
	}
	time.Sleep(10 * time.Second)
	start := snapshot()
	t.Logf("départ : %v", start)
	for el := time.Duration(0); el < idle; el += 5 * time.Minute {
		time.Sleep(min(5*time.Minute, idle-el))
		t.Logf("repos %v : %v", min(el+5*time.Minute, idle), snapshot())
	}
	afterIdle := snapshot()

	for i := range 20 {
		// Logs ouverts puis fermés (flux réel, lecture seule).
		h.focusStack(target)
		h.key("l")
		h.waitFor("logs", func(m Model) bool { return m.view == viewLogs && len(m.logLines) > 0 }, 15*time.Second)
		time.Sleep(500 * time.Millisecond)
		h.key("esc")
		h.waitFor("liste", func(m Model) bool { return m.view == viewList }, 5*time.Second)
		// Opération (simulée) lancée puis annulée.
		h.focusStack(target)
		h.key("p")
		h.waitFor("opération", func(m Model) bool { return m.view == viewProgress }, 5*time.Second)
		time.Sleep(300 * time.Millisecond)
		h.key("esc")
		h.key("esc")
		h.waitFor("annulée", progressFinished, 10*time.Second)
		h.key("esc")
		h.waitFor("liste", func(m Model) bool { return m.view == viewList }, 5*time.Second)
		// R puis o (même dossier).
		h.p.Send(keyShiftR)
		h.waitFor("rechargée", func(m Model) bool { return !m.refreshing && !m.loading }, 30*time.Second)
		h.key("o")
		h.waitFor("dossiers", func(m Model) bool { return m.view == viewDirPicker && len(m.dirEntries) > 0 }, 10*time.Second)
		h.key("enter") // « Choisir ce dossier » (config écrite dans le HOME isolé)
		h.waitFor("liste rechargée", func(m Model) bool { return m.view == viewList && !m.loading && len(m.stacks) > 0 }, 60*time.Second)
		if i%5 == 4 {
			t.Logf("cycle %d : %v", i+1, snapshot())
		}
	}
	time.Sleep(5 * time.Second)
	end := snapshot()
	t.Logf("départ %v\naprès %v de repos %v\naprès 20 cycles %v", start, idle, afterIdle, end)
	if end.goroutines > start.goroutines+3 || end.fds > start.fds+3 {
		t.Errorf("fuite probable : %v → %v", start, end)
	}
}
