//go:build integration

package tui

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/fakedocker"
)

// guardClient : vrai client, mais CleanupLive refuse tout élément hors de la
// liste autorisée (objets jetables du test) : rien de l'infra réelle ne peut
// être supprimé, même si la sélection du test était fausse.
type guardClient struct {
	*compose.Client
	t       *testing.T
	allowed map[string]bool
}

func (g guardClient) CleanupLive(ctx context.Context, items []compose.CleanupItem) <-chan compose.ProgressEvent {
	for _, it := range items {
		if !g.allowed[it.ID] {
			g.t.Errorf("suppression refusée par la garde : %+v", it)
			ch := make(chan compose.ProgressEvent, 1)
			ch <- compose.ProgressEvent{AllDone: true}
			return ch
		}
	}
	return g.Client.CleanupLive(ctx, items)
}

// PruneLive : interdit dans ce test (il toucherait l'infrastructure).
func (g guardClient) PruneLive(ctx context.Context, p compose.PruneCommand) <-chan compose.ProgressEvent {
	g.t.Errorf("prune refusé par la garde : %s", p.CLI())
	ch := make(chan compose.ProgressEvent, 1)
	ch <- compose.ProgressEvent{AllDone: true}
	return ch
}

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v : %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Bout en bout sur le vrai démon : page, sélection d'objets jetables, ↩,
// confirmation, vraie suppression, retour sur la page réanalysée avec
// l'espace libéré.
func TestLiveCleanupEndToEnd(t *testing.T) {
	if _, err := compose.New(); err != nil {
		t.Skip(err)
	}
	fakedocker.LiveLock(t)
	dockerRun(t, "pull", "-q", "busybox:latest")
	dockerRun(t, "run", "--name", "zzdsprobee2e", "busybox:latest", "sh", "-c", "head -c 3000000 /dev/urandom > /f")
	img := dockerRun(t, "commit", "zzdsprobee2e")
	dockerRun(t, "rm", "zzdsprobee2e")
	net := dockerRun(t, "network", "create", "zzdsprobe_e2e")
	dockerRun(t, "volume", "create", "--label", "com.docker.compose.project=zzdsprobeghost", "zzdsprobe_e2e_vol")
	t.Cleanup(func() {
		exec.Command("docker", "rmi", "-f", img).Run()
		exec.Command("docker", "network", "rm", "zzdsprobe_e2e").Run()
		exec.Command("docker", "volume", "rm", "zzdsprobe_e2e_vol").Run()
	})
	allowed := map[string]bool{img: true, net: true, "zzdsprobe_e2e_vol": true}

	h := startHarness(t, t.TempDir(), func(d dockerService) dockerService {
		return guardClient{Client: d.(*compose.Client), t: t, allowed: allowed}
	})
	h.waitFor("client prêt", func(m Model) bool { return m.client != nil && !m.loading }, 30*time.Second)
	h.key("n")
	h.waitFor("analyse", func(m Model) bool { return m.cleanupRep != nil }, 60*time.Second)
	// Seuls les objets jetables sont cochés.
	h.p.Send(mutateMsg(func(m *Model) {
		m.cleanupSel = map[string]bool{}
		for _, it := range m.cleanupRep.Items {
			if allowed[it.ID] {
				m.cleanupSel[cleanupKey(it)] = true
			}
		}
	}))
	m := h.waitFor("sélection", func(m Model) bool { return len(m.cleanupSelected()) == 3 }, 5*time.Second)
	_ = m
	h.key("enter")
	sn := h.waitScreen("confirmation", func(m Model, _ string) bool { return m.view == viewConfirm }, 5*time.Second)
	if !strings.Contains(sn.m.confirmWarning, "Efface définitivement les données de 1 volume") {
		t.Errorf("avertissement : %q", sn.m.confirmWarning)
	}
	h.key("y")
	sn = h.waitScreen("suppression", func(m Model, _ string) bool { return m.view == viewProgress && m.progressDone }, 60*time.Second)
	if sn.m.progressOk != sn.m.progressTotal || sn.m.progressTotal != 3 {
		t.Errorf("progression %d/%d\n%s", sn.m.progressOk, sn.m.progressTotal, sn.screen)
	}
	h.key("enter")
	sn = h.waitScreen("retour réanalysé", func(m Model, s string) bool {
		return m.view == viewCleanup && !m.cleanupLoading && strings.Contains(s, "✔ Libéré")
	}, 60*time.Second)
	t.Logf("bandeau : %s", strings.SplitN(strings.SplitN(sn.screen, "✔", 2)[1], "\n", 2)[0])
	for _, args := range [][]string{{"image", "inspect", img}, {"network", "inspect", "zzdsprobe_e2e"}, {"volume", "inspect", "zzdsprobe_e2e_vol"}} {
		if exec.Command("docker", args...).Run() == nil {
			t.Errorf("%v : toujours présent", args)
		}
	}
}
