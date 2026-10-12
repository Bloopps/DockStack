//go:build integration

package compose

import (
	"context"
	"math"
	"testing"

	mobyclient "github.com/moby/moby/client"
)

// Lecture seule : compare, pour chaque conteneur en marche, les compteurs lus
// dans les cgroups à ceux que renvoie l'API (docker stats). Ignoré si
// l'hôte n'a pas de cgroups v2.
func TestLiveCgroupMatchesAPI(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if c.cg == nil {
		t.Skip("pas de cgroups v2 sur cet hôte")
	}
	ctx := context.Background()
	api := c.cli.Client()
	list, err := api.ContainerList(ctx, mobyclient.ContainerListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Skip("aucun conteneur en marche")
	}
	near := func(a, b uint64, rel float64, abs uint64) bool {
		d := math.Abs(float64(a) - float64(b))
		return d <= float64(abs) || d <= rel*math.Max(float64(a), float64(b))
	}
	found := 0
	for _, ctr := range list.Items {
		cg, ok := c.cg.sample(ctx, ctr.ID)
		if !ok {
			t.Errorf("%s : cgroup introuvable", ctr.Names)
			continue
		}
		found++
		st, err := readStats(ctx, api, ctr.ID)
		if err != nil {
			continue // arrêté entre-temps
		}
		apiCPU := st.CPUStats.CPUUsage.TotalUsage
		rd, wr := blkioBytes(st)
		// Le démon lit un peu après : écarts bornés au bruit de l'intervalle.
		if !near(cg.cpuUsec*1000, apiCPU, 0.02, 200_000_000) {
			t.Errorf("%s : CPU cumulé cgroup %d ns, API %d ns", ctr.Names, cg.cpuUsec*1000, apiCPU)
		}
		if !near(cg.mem, memUsage(st), 0.05, 4<<20) {
			t.Errorf("%s : mémoire cgroup %d, API %d", ctr.Names, cg.mem, memUsage(st))
		}
		if !near(cg.read, rd, 0.01, 1<<20) || !near(cg.write, wr, 0.01, 1<<20) {
			t.Errorf("%s : disque cgroup %d/%d, API %d/%d", ctr.Names, cg.read, cg.write, rd, wr)
		}
	}
	t.Logf("%d/%d conteneurs lus dans les cgroups, valeurs conformes à l'API", found, len(list.Items))
}

// Conteneur jetable supprimé entre deux lectures : pas de panique, chemin
// oublié, et l'erreur de l'API remonte seulement si plus rien n'est lisible.
func TestLiveCgroupContainerRemoved(t *testing.T) {
	c := newLiveClient(t)
	if c.cg == nil {
		t.Skip("pas de cgroups v2 sur cet hôte")
	}
	id, err := dockerCLI("run", "-d", "--name", "zzdsprobecg", probeImage, "sleep", "300")
	if err != nil {
		t.Fatalf("docker run : %v\n%s", err, id)
	}
	ctx := context.Background()
	u, err := c.ContainerUsages(ctx, []string{id})
	if _, ok := u[id]; err != nil || !ok || u[id].MemBytes == 0 {
		t.Fatalf("première lecture : %v %+v", err, u)
	}
	if _, ok := c.cg.paths[id]; !ok {
		t.Fatal("cgroup non mémorisé")
	}
	if out, err := dockerCLI("rm", "-f", id); err != nil {
		t.Fatalf("docker rm : %v\n%s", err, out)
	}
	u, err = c.ContainerUsages(ctx, []string{id})
	if err == nil || len(u) != 0 {
		t.Errorf("conteneur supprimé : %+v, erreur %v", u, err)
	}
	if _, ok := c.cg.paths[id]; ok {
		t.Error("chemin périmé conservé")
	}
}
