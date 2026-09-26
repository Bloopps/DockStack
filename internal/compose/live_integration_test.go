//go:build integration

// Tests contre un vrai démon Docker (go test -tags integration ./internal/compose/).
// Tout est créé sous des noms « zzdsprobe* » dans un dossier temporaire et
// supprimé à la fin : aucune stack existante n'est touchée.
package compose

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const probeImage = "busybox:latest"

// probeStack écrit un compose.yaml (et un .env optionnel) dans root/name.
func probeStack(t *testing.T, root, name, yaml, dotenv string) Stack {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "compose.yaml")
	writeFile(t, f, yaml)
	if dotenv != "" {
		writeFile(t, filepath.Join(dir, ".env"), dotenv)
	}
	return Stack{Name: name, Dir: dir, ComposeFile: f}
}

// runOp consomme un flux jusqu'à AllDone et renvoie l'erreur par stack et les
// événements reçus.
func runOp(t *testing.T, ch <-chan ProgressEvent) (map[string]error, []ProgressEvent) {
	t.Helper()
	errs := map[string]error{}
	var evs []ProgressEvent
	timeout := time.After(3 * time.Minute)
	for {
		select {
		case ev := <-ch:
			if ev.AllDone {
				return errs, evs
			}
			if ev.StackDone {
				errs[ev.StackName] = ev.Err
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("opération bloquée (> 3 min)")
		}
	}
}

func mustOK(t *testing.T, what string, errs map[string]error) {
	t.Helper()
	for name, err := range errs {
		if err != nil {
			t.Fatalf("%s %s : %v", what, name, err)
		}
	}
}

// stackState relit l'état d'une stack via ListStacks (chemin réel de la TUI).
func stackState(t *testing.T, c *Client, root, name string) Stack {
	t.Helper()
	InvalidateComposeCache()
	stacks, err := c.ListStacks(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stacks {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("stack %s absente de ListStacks", name)
	return Stack{}
}

func dockerCLI(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func cleanupProbes(t *testing.T) {
	ids, _ := dockerCLI("ps", "-aq", "--filter", "name=zzdsprobe")
	if ids != "" {
		dockerCLI(append([]string{"rm", "-f"}, strings.Fields(ids)...)...)
	}
	vols, _ := dockerCLI("volume", "ls", "-q", "--filter", "name=zzdsprobe")
	if vols != "" {
		dockerCLI(append([]string{"volume", "rm", "-f"}, strings.Fields(vols)...)...)
	}
	nets, _ := dockerCLI("network", "ls", "-q", "--filter", "name=zzdsprobe")
	if nets != "" {
		dockerCLI(append([]string{"network", "rm"}, strings.Fields(nets)...)...)
	}
}

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	c, err := New()
	if err != nil {
		t.Skipf("Docker indisponible : %v", err)
	}
	cleanupProbes(t)
	t.Cleanup(func() { cleanupProbes(t) })
	return c
}

// TestLiveLifecycle : Pull, Up, Logs, Restart, Recreate, Down, Remove sur une
// stack jetable, en vérifiant l'état réel après chaque étape.
func TestLiveLifecycle(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobe", `services:
  web:
    image: `+probeImage+`
    command: ["sh", "-c", "echo zzdsprobe-hello; sleep 3600"]
    volumes: [data:/data]
volumes:
  data: {}
`, "")
	one := []Stack{st}

	errs, _ := runOp(t, c.PullManyLive(ctx, one, 1))
	mustOK(t, "Pull", errs)

	errs, _ = runOp(t, c.UpManyLive(ctx, one, 1))
	mustOK(t, "Up", errs)
	if s := stackState(t, c, root, "zzdsprobe"); s.State() != StateRunning || s.Total != 1 {
		t.Fatalf("après Up : %d/%d", s.Running, s.Total)
	}

	// Logs : la ligne d'amorce arrive, et annuler ferme le flux.
	lctx, cancel := context.WithCancel(ctx)
	lch, err := c.LogsAsync(lctx, st)
	if err != nil {
		t.Fatalf("LogsAsync : %v", err)
	}
	got := false
	deadline := time.After(20 * time.Second)
	for !got {
		select {
		case e, ok := <-lch:
			if !ok {
				t.Fatal("flux de logs fermé sans la ligne attendue")
			}
			got = strings.Contains(e.Line, "zzdsprobe-hello")
		case <-deadline:
			t.Fatal("ligne de log non reçue en 20 s")
		}
	}
	cancel()
	for range lch { // doit se fermer après annulation
	}

	errs, _ = runOp(t, c.RestartManyLive(ctx, one, 1))
	mustOK(t, "Restart", errs)
	if s := stackState(t, c, root, "zzdsprobe"); s.State() != StateRunning {
		t.Fatalf("après Restart : %d/%d", s.Running, s.Total)
	}

	before, _ := dockerCLI("ps", "-q", "--filter", "name=zzdsprobe-web-1")
	errs, _ = runOp(t, c.RecreateManyLive(ctx, one, 1))
	mustOK(t, "Recreate", errs)
	after, _ := dockerCLI("ps", "-q", "--filter", "name=zzdsprobe-web-1")
	if before == "" || after == "" || before == after {
		t.Fatalf("Recreate : conteneur %q → %q, want un nouveau conteneur en marche", before, after)
	}

	errs, _ = runOp(t, c.DownManyLive(ctx, one, 1))
	mustOK(t, "Down", errs)
	if s := stackState(t, c, root, "zzdsprobe"); s.Total != 0 {
		t.Fatalf("après Down : %d conteneur(s)", s.Total)
	}
	if v, _ := dockerCLI("volume", "ls", "-q", "--filter", "name=zzdsprobe_data"); v == "" {
		t.Fatal("Down ne doit pas supprimer les volumes")
	}

	errs, _ = runOp(t, c.UpManyLive(ctx, one, 1))
	mustOK(t, "Up (avant Remove)", errs)
	errs, _ = runOp(t, c.RemoveManyLive(ctx, one, 1))
	mustOK(t, "Remove", errs)
	if v, _ := dockerCLI("volume", "ls", "-q", "--filter", "name=zzdsprobe_data"); v != "" {
		t.Fatal("Remove doit supprimer les volumes")
	}
}

// TestLivePullFailure : une image introuvable fait échouer la stack (plus de
// « OK » vert sur un pull raté).
func TestLivePullFailure(t *testing.T) {
	c := newLiveClient(t)
	st := probeStack(t, t.TempDir(), "zzdsprobebad",
		"services:\n  web:\n    image: zzdsprobe-image-inexistante-4f9a:latest\n", "")
	errs, _ := runOp(t, c.PullManyLive(context.Background(), []Stack{st}, 1))
	if errs["zzdsprobebad"] == nil {
		t.Fatal("Pull d'une image inexistante : erreur attendue")
	}
}

// TestLiveProjectNameFromDotEnv : COMPOSE_PROJECT_NAME du .env nomme le
// projet (label), et Down le retrouve.
func TestLiveProjectNameFromDotEnv(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobedir",
		"services:\n  web:\n    image: "+probeImage+"\n    command: [\"sleep\", \"3600\"]\n",
		"COMPOSE_PROJECT_NAME=zzdsprobeenv\n")
	errs, _ := runOp(t, c.UpManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Up", errs)
	if p, _ := dockerCLI("ps", "--filter", "label=com.docker.compose.project=zzdsprobeenv", "-q"); p == "" {
		t.Fatal("aucun conteneur étiqueté projet zzdsprobeenv")
	}
	if s := stackState(t, c, root, "zzdsprobedir"); s.State() != StateRunning {
		t.Fatalf("ListStacks : %d/%d, want en marche", s.Running, s.Total)
	}
	errs, _ = runOp(t, c.DownManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Down", errs)
	if p, _ := dockerCLI("ps", "-a", "--filter", "label=com.docker.compose.project=zzdsprobeenv", "-q"); p != "" {
		t.Fatal("Down n'a pas retiré le projet nommé par .env")
	}
}

// TestLiveOrphans : un conteneur sans label projet (cas watchtower) est
// rattaché à sa stack, redémarré par Restart, et ré-adopté par Up.
func TestLiveOrphans(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	probeStack(t, root, "zzdsprobeorph",
		"services:\n  web:\n    image: "+probeImage+"\n    command: [\"sleep\", \"3600\"]\n", "")
	if out, err := dockerCLI("run", "-d", "--name", "zzdsprobeorph-web-1", probeImage, "sleep", "3600"); err != nil {
		t.Fatalf("création de l'orphelin : %v %s", err, out)
	}
	startedBefore, _ := dockerCLI("inspect", "-f", "{{.State.StartedAt}}", "zzdsprobeorph-web-1")

	s := stackState(t, c, root, "zzdsprobeorph")
	if !s.Orphaned || s.Running != 1 {
		t.Fatalf("orphelin non rattaché : Orphaned=%v %d/%d", s.Orphaned, s.Running, s.Total)
	}

	errs, evs := runOp(t, c.RestartManyLive(ctx, []Stack{s}, 1))
	mustOK(t, "Restart", errs)
	restarted := false
	for _, e := range evs {
		restarted = restarted || strings.Contains(e.Status, "redémarré (orphelin)")
	}
	startedAfter, _ := dockerCLI("inspect", "-f", "{{.State.StartedAt}}", "zzdsprobeorph-web-1")
	if !restarted || startedAfter == startedBefore {
		t.Fatalf("Restart n'a pas redémarré l'orphelin (événement=%v, StartedAt %s → %s)", restarted, startedBefore, startedAfter)
	}

	errs, _ = runOp(t, c.UpManyLive(ctx, []Stack{s}, 1))
	mustOK(t, "Up (ré-adoption)", errs)
	if s := stackState(t, c, root, "zzdsprobeorph"); s.Orphaned || s.State() != StateRunning {
		t.Fatalf("après Up : Orphaned=%v %d/%d, want ré-adopté et en marche", s.Orphaned, s.Running, s.Total)
	}
}

// TestLiveNameConflictNotStolen : si le container_name est pris par le
// conteneur d'un AUTRE projet, Up échoue et ne démarre pas ce conteneur.
func TestLiveNameConflictNotStolen(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	yaml := "services:\n  web:\n    image: " + probeImage + "\n    command: [\"sleep\", \"3600\"]\n    container_name: zzdsprobe-shared\n"
	a := probeStack(t, root, "zzdsprobea", yaml, "")
	b := probeStack(t, root, "zzdsprobeb", yaml, "")

	errs, _ := runOp(t, c.UpManyLive(ctx, []Stack{a}, 1))
	mustOK(t, "Up A", errs)
	if out, err := dockerCLI("stop", "-t", "0", "zzdsprobe-shared"); err != nil {
		t.Fatalf("stop A : %v %s", err, out)
	}

	errs, _ = runOp(t, c.UpManyLive(ctx, []Stack{b}, 1))
	if errs["zzdsprobeb"] == nil {
		t.Fatal("Up B devait échouer (nom pris par le projet A)")
	}
	if state, _ := dockerCLI("inspect", "-f", "{{.State.Running}}", "zzdsprobe-shared"); state != "false" {
		t.Fatalf("le conteneur du projet A a été démarré par Up B (Running=%s)", state)
	}
}

// TestLiveUsage : la mesure d'occupation répond et reste cohérente.
func TestLiveUsage(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Skip(err)
	}
	u, err := c.Usage(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if u.RootDir == "" || u.Running > u.Containers || !u.VolumesOK || u.ImagesSize <= 0 {
		t.Fatalf("usage incohérent : %+v", u)
	}
	t.Logf("%+v", u)
}
