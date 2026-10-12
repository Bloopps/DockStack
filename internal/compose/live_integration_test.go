//go:build integration

// Tests contre un vrai démon Docker (go test -tags integration ./internal/compose/).
// Tout est créé sous des noms « zzdsprobe* » dans un dossier temporaire et
// supprimé à la fin : aucune stack existante n'est touchée.
package compose

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Bloopps/dockstack/internal/fakedocker"
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
	fakedocker.LiveLock(t)
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

// TestLiveRecreateKeepsAnonymousVolume : un Recreate (comme un up après
// changement de config) garde les données d'un volume anonyme, comme la CLI.
// Avant, le conteneur recréé repartait sur un volume anonyme vide.
func TestLiveRecreateKeepsAnonymousVolume(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobeanon", `services:
  app:
    image: `+probeImage+`
    command: ["sleep", "3600"]
    volumes: ["/data"]
`, "")
	one := []Stack{st}
	// Remove (down -v) emporte le volume anonyme, que cleanupProbes ne
	// retrouve pas par son nom.
	t.Cleanup(func() { runOp(t, c.RemoveManyLive(context.Background(), one, 1)) })

	errs, _ := runOp(t, c.UpManyLive(ctx, one, 1))
	mustOK(t, "Up", errs)
	if out, err := dockerCLI("exec", "zzdsprobeanon-app-1", "sh", "-c", "echo garde > /data/f"); err != nil {
		t.Fatalf("écriture dans le volume : %v (%s)", err, out)
	}

	errs, _ = runOp(t, c.RecreateManyLive(ctx, one, 1))
	mustOK(t, "Recreate", errs)
	out, err := dockerCLI("exec", "zzdsprobeanon-app-1", "cat", "/data/f")
	if err != nil || out != "garde" {
		t.Fatalf("après Recreate : %q, %v ; want « garde » (volume anonyme hérité)", out, err)
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

// TestLiveRemoveOrphaned : Remove sur une stack dont le conteneur a perdu son
// label projet (watchtower) retire aussi ce conteneur, et donc le volume qu'il
// tenait : compose ne le voit pas, le volume restait « en cours
// d'utilisation » et le Remove échouait à moitié.
func TestLiveRemoveOrphaned(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	probeStack(t, root, "zzdsprobeorphrm",
		"services:\n  web:\n    image: "+probeImage+"\n    command: [\"sleep\", \"3600\"]\n    volumes: [data:/data]\nvolumes:\n  data: {}\n", "")
	for _, args := range [][]string{
		{"volume", "create", "--label", "com.docker.compose.project=zzdsprobeorphrm", "--label", "com.docker.compose.volume=data", "zzdsprobeorphrm_data"},
		{"run", "-d", "--name", "zzdsprobeorphrm-web-1", "--label", "com.docker.compose.replace=web",
			"-v", "zzdsprobeorphrm_data:/data", probeImage, "sleep", "3600"},
	} {
		if out, err := dockerCLI(args...); err != nil {
			t.Fatalf("docker %v : %v %s", args, err, out)
		}
	}
	s := stackState(t, c, root, "zzdsprobeorphrm")
	if !s.Orphaned {
		t.Fatal("orphelin non rattaché")
	}
	errs, _ := runOp(t, c.RemoveManyLive(ctx, []Stack{s}, 1))
	mustOK(t, "Remove", errs)
	if id, _ := dockerCLI("ps", "-aq", "--filter", "name=zzdsprobeorphrm-web-1"); id != "" {
		t.Error("le conteneur orphelin a survécu au Remove")
	}
	if v, _ := dockerCLI("volume", "ls", "-q", "--filter", "name=zzdsprobeorphrm_data"); v != "" {
		t.Error("le volume de la stack a survécu au Remove")
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
	// Watchtower perd le label projet mais garde com.docker.compose.replace.
	if out, err := dockerCLI("run", "-d", "--name", "zzdsprobeorph-web-1", "--label", "com.docker.compose.replace=web",
		probeImage, "sleep", "3600"); err != nil {
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

	// PlanUp annonce la recréation de l'orphelin en marche (le up le supprime).
	if plan, err := c.PlanUp(ctx, s); err != nil || len(plan) != 1 || plan[0].Service != "web" {
		t.Errorf("PlanUp sur orphelin en marche = %+v, %v ; want web", plan, err)
	}

	// Stop arrête l'orphelin (compose stop ne le voit pas) et le conserve.
	errs, _ = runOp(t, c.StopManyLive(ctx, []Stack{s}, 1))
	mustOK(t, "Stop", errs)
	if st, _ := dockerCLI("inspect", "-f", "{{.State.Status}}", "zzdsprobeorph-web-1"); st != "exited" {
		t.Fatalf("après Stop : orphelin %q, want exited", st)
	}

	errs, _ = runOp(t, c.UpManyLive(ctx, []Stack{s}, 1))
	mustOK(t, "Up (ré-adoption)", errs)
	if s := stackState(t, c, root, "zzdsprobeorph"); s.Orphaned || s.State() != StateRunning {
		t.Fatalf("après Up : Orphaned=%v %d/%d, want ré-adopté et en marche", s.Orphaned, s.Running, s.Total)
	}
}

// TestLivePlanSharedNamespace : pas de faux positif sur un service en
// network_mode: service:X (compose hache la référence résolue), et la
// recréation du parent est annoncée en cascade — ce que compose fait vraiment.
func TestLivePlanSharedNamespace(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	yaml := func(env string) string {
		return "services:\n  vpn:\n    image: " + probeImage + "\n    command: [\"sleep\", \"3600\"]\n    environment: {X: \"" + env + "\"}\n" +
			"  app:\n    image: " + probeImage + "\n    command: [\"sleep\", \"3600\"]\n    network_mode: \"service:vpn\"\n    depends_on: [vpn]\n"
	}
	st := probeStack(t, root, "zzdsprobens", yaml("1"), "")
	errs, _ := runOp(t, c.UpManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Up", errs)

	if plan, err := c.PlanUp(ctx, st); err != nil || len(plan) != 0 {
		t.Fatalf("stack inchangée : PlanUp = %+v, %v (faux positif network_mode)", plan, err)
	}

	writeFile(t, st.ComposeFile, yaml("2"))
	plan, err := c.PlanUp(ctx, st)
	got := map[string]string{}
	for _, r := range plan {
		got[r.Service] = r.Reason
	}
	if err != nil || got["vpn"] != "config modifiée" || !strings.HasPrefix(got["app"], "suit la recréation") {
		t.Fatalf("après modification de vpn : PlanUp = %+v, %v", plan, err)
	}
	appBefore, _ := dockerCLI("inspect", "-f", "{{.Id}}", "zzdsprobens-app-1")
	errs, _ = runOp(t, c.UpManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Up après modification", errs)
	if appAfter, _ := dockerCLI("inspect", "-f", "{{.Id}}", "zzdsprobens-app-1"); appAfter == appBefore {
		t.Error("compose n'a pas recréé app : la cascade annoncée par PlanUp serait fausse")
	}
}

// TestLiveEnvFileCLIParity : une stack à env_file déployée par la CLI docker
// compose n'est ni annoncée modifiée par PlanUp, ni recréée par un Up de
// dockstack (le hash de config doit être celui de la CLI).
func TestLiveEnvFileCLIParity(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobeenvf",
		"services:\n  web:\n    image: "+probeImage+"\n    command: [\"sleep\", \"3600\"]\n    env_file: [app.env]\n", "")
	writeFile(t, filepath.Join(st.Dir, "app.env"), "FOO=bar\n")
	if out, err := dockerCLI("compose", "-f", st.ComposeFile, "up", "-d"); err != nil {
		t.Skipf("CLI docker compose indisponible : %v %s", err, out)
	}
	if plan, err := c.PlanUp(ctx, st); err != nil || len(plan) != 0 {
		t.Fatalf("stack déployée par la CLI, inchangée : PlanUp = %+v, %v", plan, err)
	}
	before, _ := dockerCLI("inspect", "-f", "{{.Id}}", "zzdsprobeenvf-web-1")
	errs, _ := runOp(t, c.UpManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Up", errs)
	if after, _ := dockerCLI("inspect", "-f", "{{.Id}}", "zzdsprobeenvf-web-1"); after != before {
		t.Error("Up de dockstack a recréé un conteneur que la CLI laisse tel quel")
	}
	if env, _ := dockerCLI("exec", "zzdsprobeenvf-web-1", "printenv", "FOO"); env != "bar" {
		t.Errorf("FOO = %q dans le conteneur, want bar", env)
	}
}

// TestLivePlanNetworkDetached : un conteneur déconnecté de son réseau est
// recréé par compose ; PlanUp doit l'annoncer.
func TestLivePlanNetworkDetached(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobenet",
		"services:\n  web:\n    image: "+probeImage+"\n    command: [\"sleep\", \"3600\"]\n", "")
	errs, _ := runOp(t, c.UpManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Up", errs)
	if plan, err := c.PlanUp(ctx, st); err != nil || len(plan) != 0 {
		t.Fatalf("stack inchangée : PlanUp = %+v, %v", plan, err)
	}
	if out, err := dockerCLI("network", "disconnect", "zzdsprobenet_default", "zzdsprobenet-web-1"); err != nil {
		t.Fatalf("déconnexion : %v %s", err, out)
	}
	plan, err := c.PlanUp(ctx, st)
	if err != nil || len(plan) != 1 || plan[0].Service != "web" {
		t.Fatalf("après déconnexion : PlanUp = %+v, %v", plan, err)
	}
	runOp(t, c.DownManyLive(ctx, []Stack{st}, 1)) // retire aussi le réseau
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

// TestLiveUsage : la mesure d'occupation répond et reste cohérente, et ne
// renvoie que les mesures demandées.
func TestLiveUsage(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	u, err := c.Usage(ctx, UsageOptions{Images: true, Volumes: true})
	if err != nil {
		t.Fatal(err)
	}
	if u.RootDir == "" || u.Running > u.Containers || !u.VolumesOK || !u.ImagesOK || u.ImagesSize <= 0 {
		t.Fatalf("usage incohérent : %+v", u)
	}
	t.Logf("%+v", u)

	info, err := c.Usage(ctx, UsageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.RootDir == "" || info.Containers != u.Containers || info.ImagesOK || info.VolumesOK {
		t.Fatalf("Info seul : %+v", info)
	}
	img, err := c.Usage(ctx, UsageOptions{Images: true})
	if err != nil {
		t.Fatal(err)
	}
	if !img.ImagesOK || img.ImagesSize != u.ImagesSize || img.VolumesOK {
		t.Fatalf("images seules : %+v (complet : %d)", img, u.ImagesSize)
	}
}

// TestLiveEventFilter : le filtre appliqué par le démon laisse passer le
// cycle de vie et les changements de santé des conteneurs, ainsi que les
// changements d'images, mais plus les exec_* des contrôles de santé.
func TestLiveEventFilter(t *testing.T) {
	c := newLiveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ch := c.SubscribeEvents(ctx)
	// Laisse le temps à l'abonnement de s'établir avant de produire des
	// événements.
	time.Sleep(300 * time.Millisecond)

	const name = "zzdsprobe-events"
	const tag = "zzdsprobe-events:t"
	dockerCLI("rm", "-f", name)
	t.Cleanup(func() {
		dockerCLI("rm", "-f", name)
		dockerCLI("rmi", tag)
	})
	if out, err := dockerCLI("run", "-d", "--name", name, "--health-cmd", "true",
		"--health-interval", "1s", "--health-start-period", "0s", probeImage, "sleep", "60"); err != nil {
		t.Fatalf("docker run : %v %s", err, out)
	}
	if out, err := dockerCLI("tag", probeImage, tag); err != nil {
		t.Fatalf("docker tag : %v %s", err, out)
	}

	seen := map[string]bool{}
	want := []string{"container create", "container start", "container health_status", "image tag"}
	done := func() bool {
		for _, w := range want {
			if !seen[w] {
				return false
			}
		}
		return true
	}
	// Continue un peu après avoir tout vu : plusieurs contrôles de santé
	// (donc des exec_*) ont lieu pendant ce temps.
	deadline := time.After(20 * time.Second)
	var extra <-chan time.Time
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("flux fermé")
			}
			if strings.HasPrefix(ev.Action, "exec_") {
				t.Fatalf("événement non filtré : %+v", ev)
			}
			key := ev.Type + " " + strings.SplitN(ev.Action, ":", 2)[0]
			seen[key] = true
			if strings.HasPrefix(ev.Action, "health_status") && !IsRelevantDockerEvent(ev) {
				t.Errorf("changement de santé jugé non pertinent : %+v", ev)
			}
			if ev.Type == "image" && !ImagesChanged(ev) {
				t.Errorf("événement d'image ignoré : %+v", ev)
			}
			if done() && extra == nil {
				extra = time.After(3 * time.Second)
			}
		case <-extra:
			t.Logf("reçus : %v", seen)
			return
		case <-deadline:
			t.Fatalf("événements manquants, reçus : %v", seen)
		}
	}
}

// TestLiveScopedServices : avec Stack.Only, chaque action ne touche QUE les
// services ciblés — les autres gardent le même conteneur (même id) et ne sont
// ni redémarrés ni recréés, même si le service ciblé est leur dépendance.
func TestLiveScopedServices(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobeonly", `services:
  a:
    image: `+probeImage+`
    command: ["sleep", "3600"]
  b:
    image: `+probeImage+`
    command: ["sleep", "3600"]
  c:
    image: `+probeImage+`
    command: ["sleep", "3600"]
    depends_on: [a]
`, "")
	only := func(names ...string) []Stack { s := st; s.Only = names; return []Stack{s} }
	// id et instant de démarrage d'un conteneur (vide s'il n'existe pas).
	inspect := func(svc string) string {
		out, err := dockerCLI("inspect", "-f", "{{.Id}} {{.State.Running}} {{.State.StartedAt}}", "zzdsprobeonly-"+svc+"-1")
		if err != nil {
			return ""
		}
		return out
	}
	id := func(svc string) string { return strings.Fields(inspect(svc) + " ")[0] }

	errs, _ := runOp(t, c.UpManyLive(ctx, []Stack{st}, 1))
	mustOK(t, "Up complet", errs)
	idA, idB, idC := id("a"), id("b"), id("c")
	if idA == "" || idB == "" || idC == "" {
		t.Fatalf("les 3 services doivent tourner : a=%q b=%q c=%q", idA, idB, idC)
	}
	startedC := inspect("c")

	// Down ciblé sur b : b disparaît, a et c intacts.
	errs, _ = runOp(t, c.DownManyLive(ctx, only("b"), 1))
	mustOK(t, "Down b", errs)
	if inspect("b") != "" {
		t.Error("Down ciblé : b devrait être retiré")
	}
	if id("a") != idA || id("c") != idC || !strings.Contains(inspect("a"), " true ") || !strings.Contains(inspect("c"), " true ") {
		t.Errorf("Down ciblé : a/c ont été touchés (a=%q c=%q)", inspect("a"), inspect("c"))
	}

	// Les services déclarés incluent b malgré l'absence de conteneur.
	decl, err := DeclaredServices(st)
	var names []string
	for _, d := range decl {
		names = append(names, d.Name)
	}
	if err != nil || strings.Join(names, ",") != "a,b,c" || strings.Join(decl[2].DependsOn, ",") != "a" {
		t.Errorf("DeclaredServices = %+v, %v", decl, err)
	}

	// Up ciblé sur b : b revient, a et c gardent leur conteneur.
	errs, _ = runOp(t, c.UpManyLive(ctx, only("b"), 1))
	mustOK(t, "Up b", errs)
	if !strings.Contains(inspect("b"), " true ") || id("a") != idA || id("c") != idC {
		t.Errorf("Up ciblé : état inattendu a=%q b=%q c=%q", inspect("a"), inspect("b"), inspect("c"))
	}
	idB = id("b")

	// Restart ciblé sur c : seul c change d'heure de démarrage.
	time.Sleep(1100 * time.Millisecond) // StartedAt a une précision de l'ordre de la seconde
	errs, _ = runOp(t, c.RestartManyLive(ctx, only("c"), 1))
	mustOK(t, "Restart c", errs)
	if inspect("c") == startedC {
		t.Error("Restart ciblé : c n'a pas redémarré")
	}
	if id("c") != idC || id("a") != idA || id("b") != idB {
		t.Error("Restart ciblé : un conteneur a changé d'id")
	}

	// Recreate ciblé sur c (dépend de a) : c est recréé, PAS a ni b.
	errs, _ = runOp(t, c.RecreateManyLive(ctx, only("c"), 1))
	mustOK(t, "Recreate c", errs)
	if id("c") == idC || id("c") == "" {
		t.Error("Recreate ciblé : c aurait dû être recréé (nouvel id)")
	}
	if id("a") != idA || id("b") != idB {
		t.Errorf("Recreate ciblé sur c : a ou b ont été recréés (a=%q b=%q)", id("a"), id("b"))
	}

	// Service inconnu : erreur claire, rien de touché.
	errs, _ = runOp(t, c.UpManyLive(ctx, only("nope"), 1))
	if errs["zzdsprobeonly"] == nil {
		t.Error("service inconnu : une erreur est attendue")
	}
}

// TestLiveStopDepsPlan : Stop garde le conteneur, WithDeps démarre aussi les
// dépendances, PlanUp ne fait pas de faux positif sur une stack inchangée et
// annonce la recréation après modification, et ServiceStatus porte les infos.
func TestLiveStopDepsPlan(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	yaml := func(env string) string {
		return `services:
  db:
    image: ` + probeImage + `
    command: ["sleep", "3600"]
  app:
    image: ` + probeImage + `
    command: ["sleep", "3600"]
    depends_on: [db]
    environment: {X: "` + env + `"}
    ports: ["18099:80"]
`
	}
	st := probeStack(t, root, "zzdsprobedeps", yaml("1"), "")
	scoped := func(deps bool, names ...string) []Stack { s := st; s.Only, s.WithDeps = names, deps; return []Stack{s} }
	state := func(svc string) string {
		out, err := dockerCLI("inspect", "-f", "{{.State.Status}}", "zzdsprobedeps-"+svc+"-1")
		if err != nil {
			return ""
		}
		return out
	}

	// Up de app SANS dépendances : db n'est pas créé.
	errs, _ := runOp(t, c.UpManyLive(ctx, scoped(false, "app"), 1))
	if errs["zzdsprobedeps"] == nil && state("db") != "" {
		t.Errorf("sans dépendances : db ne devrait pas exister (état %q)", state("db"))
	}
	runOp(t, c.DownManyLive(ctx, []Stack{st}, 1))

	// Up de app AVEC dépendances : db démarre aussi.
	errs, _ = runOp(t, c.UpManyLive(ctx, scoped(true, "app"), 1))
	mustOK(t, "Up app+deps", errs)
	if state("db") != "running" || state("app") != "running" {
		t.Fatalf("avec dépendances : db=%q app=%q", state("db"), state("app"))
	}

	// Infos de service : image, ports publiés, statut Docker.
	s := stackState(t, c, root, "zzdsprobedeps")
	var app ServiceStatus
	for _, sv := range s.Services {
		if sv.Name == "app" {
			app = sv
		}
	}
	if app.Image != probeImage || app.Ports != "18099→80" || !strings.HasPrefix(app.Status, "Up") {
		t.Errorf("infos service app = %+v", app)
	}

	// PlanUp : rien à recréer sur une stack inchangée (pas de faux positif).
	if plan, err := c.PlanUp(ctx, st); err != nil || len(plan) != 0 {
		t.Errorf("stack inchangée : PlanUp = %+v, %v", plan, err)
	}
	// Après modification de la config d'app : seul app est annoncé.
	writeFile(t, st.ComposeFile, yaml("2"))
	plan, err := c.PlanUp(ctx, st)
	if err != nil || len(plan) != 1 || plan[0].Service != "app" || plan[0].Reason != "config modifiée" {
		t.Errorf("config modifiée : PlanUp = %+v, %v", plan, err)
	}

	// Stop ciblé : db reste présent (arrêté, pas supprimé), app intact.
	errs, _ = runOp(t, c.StopManyLive(ctx, scoped(false, "db"), 1))
	mustOK(t, "Stop db", errs)
	if state("db") != "exited" || state("app") != "running" {
		t.Errorf("après Stop : db=%q (attendu exited) app=%q", state("db"), state("app"))
	}
}

// TestLiveLenientRestore : restaurer une capture dont un service a disparu du
// compose relance les autres et signale le manquant ; si plus aucun service
// capturé n'existe, l'action échoue clairement.
func TestLiveLenientRestore(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	st := probeStack(t, root, "zzdsprobelen", `services:
  a:
    image: `+probeImage+`
    command: ["sleep", "3600"]
  b:
    image: `+probeImage+`
    command: ["sleep", "3600"]
`, "")
	restore := st
	restore.Only, restore.Lenient = []string{"a", "gone"}, true
	errs, evs := runOp(t, c.UpManyLive(ctx, []Stack{restore}, 1))
	mustOK(t, "Up capture", errs)
	warned := false
	for _, ev := range evs {
		if ev.Container == "Service gone" && ev.State == "warning" && strings.Contains(ev.Status, "absent du compose") {
			warned = true
		}
	}
	if !warned {
		t.Error("le service disparu doit être signalé (warning)")
	}
	state := func(svc string) string {
		out, err := dockerCLI("inspect", "-f", "{{.State.Status}}", "zzdsprobelen-"+svc+"-1")
		if err != nil {
			return ""
		}
		return out
	}
	if state("a") != "running" || state("b") != "" {
		t.Errorf("seul a doit tourner : a=%q b=%q", state("a"), state("b"))
	}

	none := st
	none.Only, none.Lenient = []string{"gone"}, true
	errs, _ = runOp(t, c.UpManyLive(ctx, []Stack{none}, 1))
	if err := errs["zzdsprobelen"]; err == nil || !strings.Contains(err.Error(), "n'existe plus") {
		t.Errorf("aucun service restaurable : erreur claire attendue, eu %v", err)
	}
}

// TestLiveRemoveAfterDown : Remove sur une stack déjà arrêtée par un down
// supprime ses volumes, comme « docker compose down --volumes » lancé dans
// son dossier.
func TestLiveRemoveAfterDown(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	root := t.TempDir()
	yaml := `services:
  web:
    image: ` + probeImage + `
    command: ["sleep", "3600"]
    volumes: [data:/data]
volumes:
  data: {}
`
	st := probeStack(t, root, "zzdsprobermdown", yaml, "")
	one := []Stack{st}
	errs, _ := runOp(t, c.UpManyLive(ctx, one, 1))
	mustOK(t, "Up", errs)
	errs, _ = runOp(t, c.DownManyLive(ctx, one, 1))
	mustOK(t, "Down", errs)
	if v, _ := dockerCLI("volume", "ls", "-q", "--filter", "name=zzdsprobermdown_data"); v == "" {
		t.Fatal("le volume doit survivre au Down")
	}
	errs, _ = runOp(t, c.RemoveManyLive(ctx, one, 1))
	mustOK(t, "Remove", errs)
	if v, _ := dockerCLI("volume", "ls", "-q", "--filter", "name=zzdsprobermdown_data"); v != "" {
		t.Fatal("Remove après Down : le volume de la stack doit être supprimé, comme down --volumes")
	}
}

// TestLiveNoGoroutineLeak : ouvrir puis fermer les logs, s'abonner puis se
// désabonner des événements, lancer puis annuler une opération : le nombre
// de goroutines revient à son niveau de départ.
func TestLiveNoGoroutineLeak(t *testing.T) {
	c := newLiveClient(t)
	ctx := context.Background()
	st := probeStack(t, t.TempDir(), "zzdsprobeleak", `services:
  web:
    image: `+probeImage+`
    command: ["sh", "-c", "while true; do echo tic; sleep 0.05; done"]
`, "")
	one := []Stack{st}
	errs, _ := runOp(t, c.UpManyLive(ctx, one, 1))
	mustOK(t, "Up", errs)

	settle := func() int {
		var n int
		for range 20 {
			time.Sleep(100 * time.Millisecond)
			n = runtime.NumGoroutine()
		}
		return n
	}
	base := settle()

	for range 5 {
		lctx, cancel := context.WithCancel(ctx)
		lch, err := c.LogsAsync(lctx, st)
		if err != nil {
			t.Fatal(err)
		}
		<-lch
		// La vue fermée ne lit plus : l'annulation doit suffire à tout
		// libérer, même avec un flux bavard en cours d'envoi.
		cancel()
	}
	for range 5 {
		ectx, cancel := context.WithCancel(ctx)
		ch := c.SubscribeEvents(ectx)
		// Un événement en attente d'envoi pendant l'annulation.
		dockerCLI("restart", "-t", "0", "zzdsprobeleak-web-1")
		time.Sleep(300 * time.Millisecond)
		cancel()
		_ = ch // plus personne ne lit, comme une vue fermée
	}
	if got := settle(); got > base+2 {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines : %d au départ, %d après (fuite)\n%s", base, got, buf[:n])
	}
}

// Rafale réelle d'événements (conteneurs jetables zzdsprobe*) que personne ne
// lit, puis annulation : la goroutine de SubscribeEvents se termine.
// Pendant de TestSubscribeEventsCancelWithFullBuffer, contre le vrai démon.
func TestLiveEventsBurstCancel(t *testing.T) {
	c := newLiveClient(t)
	if _, err := dockerCLI("image", "inspect", probeImage); err != nil {
		if out, err := dockerCLI("pull", probeImage); err != nil {
			t.Fatalf("pull %s : %v\n%s", probeImage, err, out)
		}
	}
	const fn = "compose.(*Client).SubscribeEvents"
	base := goroutinesIn(fn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := c.SubscribeEvents(ctx)
	time.Sleep(300 * time.Millisecond) // abonnement établi
	// 15 conteneurs × (create, start, die) = 45 événements filtrés, plus que
	// le tampon de 32.
	for i := range 15 {
		if out, err := dockerCLI("run", "-d", "--name", fmt.Sprintf("zzdsprobeburst%d", i), probeImage, "true"); err != nil {
			t.Fatalf("docker run : %v\n%s", err, out)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(ch) < cap(ch) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if len(ch) != cap(ch) {
		t.Fatalf("tampon %d/%d après la rafale", len(ch), cap(ch))
	}
	cancel()
	deadline = time.Now().Add(3 * time.Second)
	for goroutinesIn(fn) > base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := goroutinesIn(fn); n > base {
		t.Fatalf("goroutine de SubscribeEvents toujours là (%d > %d)", n, base)
	}
}
