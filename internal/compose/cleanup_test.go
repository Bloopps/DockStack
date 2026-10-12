package compose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	mobyclient "github.com/moby/moby/client"
)

func kinds(rep CleanupReport) map[string]CleanupKind {
	out := map[string]CleanupKind{}
	for _, it := range rep.Items {
		out[it.Name] = it.Kind
	}
	return out
}

func TestClassifyCleanup(t *testing.T) {
	refs := stackRefs{
		projects:     map[string]string{"authelia": "authelia", "web": "media/web"},
		images:       map[string][]string{"nginx:latest": {"media/web"}, "ghcr.io/x/app@sha256:" + sha("a"): {"authelia"}},
		externalNets: map[string][]string{"traefik_lan": {"media/web"}},
		errors:       map[string]error{},
	}
	in := cleanupInput{refs: refs}
	in.du.Images = mobyclient.ImagesDiskUsage{TotalCount: 9, ActiveCount: 2, TotalSize: 900, Reclaimable: 400, Items: []image.Summary{
		{ID: "i-used", RepoTags: []string{"redis:7"}, Containers: 1, Size: 50},
		{ID: "i-unknown", RepoTags: []string{"x:1"}, Containers: -1, Size: 50},
		{ID: "i-stopped", RepoTags: []string{"busybox:latest"}, Containers: 0, Size: 50}, // conteneur arrêté via ctrs
		{ID: "i-dangling", RepoTags: []string{"<none>:<none>"}, RepoDigests: []string{"myimg@sha256:" + sha("b")}, Size: 100, SharedSize: 30},
		{ID: "i-dangling2", Size: 20, SharedSize: -1},
		{ID: "i-build", Size: 5, Labels: map[string]string{api.ProjectLabel: "kitchen_planner", api.ServiceLabel: "frontend"}},
		{ID: "i-nginx", RepoTags: []string{"nginx:latest"}, Size: 70},
		{ID: "i-digest", RepoDigests: []string{"ghcr.io/x/app@sha256:" + sha("a")}, RepoTags: []string{"ghcr.io/x/app:old"}, Size: 10},
		{ID: "i-free", RepoTags: []string{"postgres:13", "pg:old"}, Size: 80},
	}}
	in.du.Containers = mobyclient.ContainersDiskUsage{TotalCount: 3, ActiveCount: 2}
	in.du.BuildCache = mobyclient.BuildCacheDiskUsage{TotalCount: 4, Reclaimable: 700, Items: []build.CacheRecord{
		{ID: "a"}, {ID: "b"}, {ID: "c", InUse: true}, {ID: "d", Shared: true},
	}}
	rc := func(n int64) *volume.UsageData { return &volume.UsageData{RefCount: n, Size: 5} }
	in.du.Volumes = mobyclient.VolumesDiskUsage{TotalCount: 7, Items: []volume.Volume{
		{Name: "used", UsageData: rc(1)},
		{Name: "unknown"},
		{Name: "anon", Labels: map[string]string{"com.docker.volume.anonymous": ""}, UsageData: rc(0)},
		{Name: sha("c"), UsageData: rc(0)},
		{Name: "jobdash-data", UsageData: rc(0)},
		{Name: "authelia_db", Labels: map[string]string{api.ProjectLabel: "Authelia"}, UsageData: rc(0)},
		{Name: "v3_data", Labels: map[string]string{api.ProjectLabel: "v3"}, UsageData: rc(0)},
	}}
	in.ctrs = []container.Summary{
		{ImageID: "i-used", State: container.StateRunning, NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{"app_default": {NetworkID: "n-app"}}}},
		{ImageID: "i-stopped", State: container.StateExited, NetworkSettings: &container.NetworkSettingsSummary{
			Networks: map[string]*network.EndpointSettings{"old_net": nil}}},
	}
	in.nets = []network.Summary{
		{Network: network.Network{ID: "n-bridge", Name: "bridge", Scope: "local"}},
		{Network: network.Network{ID: "n-app", Name: "app_default", Scope: "local"}},
		{Network: network.Network{ID: "n-old", Name: "old_net", Scope: "local"}},
		{Network: network.Network{ID: "n-ext", Name: "traefik_lan", Scope: "local"}},
		{Network: network.Network{ID: "n-swarm", Name: "ov", Scope: "swarm"}},
		{Network: network.Network{ID: "n-free", Name: "demo_default", Scope: "local"}},
	}

	rep := classifyCleanup(in)
	want := map[string]CleanupKind{
		"cache de build":                        CleanBuildCache,
		"myimg <none>":                          CleanDanglingImage,
		"<none>":                                CleanDanglingImage,
		"ancien build kitchen_planner/frontend": CleanDanglingImage,
		"demo_default":                          CleanUnusedNetwork,
		"postgres:13, pg:old":                   CleanUnusedImage,
		"nginx:latest":                          CleanStackImage,
		"ghcr.io/x/app:old":                     CleanStackImage, // cité par son empreinte
		"v3_data":                               CleanOrphanVolume,
		"anon":                                  CleanAnonymousVolume,
		sha("c"):                                CleanAnonymousVolume,
	}
	if got := kinds(rep); !mapsEqual(got, want) {
		t.Errorf("classement :\n got %v\nwant %v", got, want)
	}
	for _, it := range rep.Items {
		switch it.Name {
		case "cache de build":
			if it.Count != 2 || it.Size != 700 {
				t.Errorf("cache : %+v", it)
			}
		case "myimg <none>":
			if it.Size != 70 {
				t.Errorf("taille propre attendue 70 (100 - 30 partagés) : %d", it.Size)
			}
		case "<none>":
			if it.Size != 20 {
				t.Errorf("taille sans partage connu : %d", it.Size)
			}
		case "nginx:latest":
			if !slices.Equal(it.Stacks, []string{"media/web"}) {
				t.Errorf("stacks de nginx : %v", it.Stacks)
			}
		case "v3_data":
			if !slices.Equal(it.Stacks, []string{"v3"}) {
				t.Errorf("projet du volume orphelin : %v", it.Stacks)
			}
		}
	}
	if len(rep.KeptStackVolumes) != 1 || rep.KeptStackVolumes[0] != (KeptVolume{Name: "authelia_db", Stack: "authelia", Size: 5}) {
		t.Errorf("volumes de stacks conservés : %+v", rep.KeptStackVolumes)
	}
	if len(rep.KeptUnlabeledVolumes) != 1 || rep.KeptUnlabeledVolumes[0].Name != "jobdash-data" {
		t.Errorf("volumes sans étiquette conservés : %+v", rep.KeptUnlabeledVolumes)
	}
	if rep.Images != (DFRow{9, 2, 900, 400}) || rep.Containers.Count != 3 || len(rep.StoppedContainers) != 1 {
		t.Errorf("bilan : images %+v conteneurs %+v arrêtés %v", rep.Images, rep.Containers, rep.StoppedContainers)
	}
	if len(rep.KeptExternalNetworks) != 1 || rep.KeptExternalNetworks[0].Name != "traefik_lan" ||
		!slices.Equal(rep.KeptExternalNetworks[0].Stacks, []string{"media/web"}) {
		t.Errorf("réseaux externes conservés : %+v", rep.KeptExternalNetworks)
	}
	// Ordre : par catégorie, puis taille décroissante.
	for i := 1; i < len(rep.Items); i++ {
		a, b := rep.Items[i-1], rep.Items[i]
		if a.Kind > b.Kind || (a.Kind == b.Kind && a.Size < b.Size) {
			t.Errorf("ordre : %q avant %q", a.Name, b.Name)
		}
	}
}

func TestCleanupRisk(t *testing.T) {
	for k, want := range map[CleanupKind]CleanupRisk{
		CleanBuildCache: RiskNone, CleanDanglingImage: RiskNone, CleanUnusedNetwork: RiskNone,
		CleanUnusedImage: RiskRedownload, CleanStackImage: RiskRedownload,
		CleanOrphanVolume: RiskData, CleanAnonymousVolume: RiskData,
	} {
		if got := k.Risk(); got != want {
			t.Errorf("risque de %d = %d, want %d", k, got, want)
		}
	}
}

func TestNormalizeImageRef(t *testing.T) {
	for in, want := range map[string]string{
		"nginx":                        "nginx:latest",
		"docker.io/library/nginx:1.27": "nginx:1.27",
		"ghcr.io/owner/app":            "ghcr.io/owner/app:latest",
		"localhost:5000/x/y:2":         "localhost:5000/x/y:2",
		"busybox@sha256:" + sha("d"):   "busybox@sha256:" + sha("d"),
		"MAJUSCULES invalide":          "MAJUSCULES invalide",
	} {
		if got := normalizeImageRef(in); got != want {
			t.Errorf("normalizeImageRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// Ce que les stacks citent : image interpolée par le .env, nom de projet
// imposé, image par défaut d'un service construit, réseau externe, compose
// illisible (son nom de projet par défaut reste connu).
func TestCollectStackRefs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("media/Web Site/compose.yaml", `services:
  front:
    image: nginx:${TAG}
    networks: [lan]
networks:
  lan:
    external: true
    name: traefik_lan
`)
	write("media/Web Site/.env", "TAG=1.27\n")
	write("app/compose.yaml", `name: monprojet
services:
  api:
    build: .
`)
	write("casse/compose.yaml", "services: [pas, valide\n")

	refs, err := collectStackRefs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := refs.images["nginx:1.27"]; !slices.Equal(got, []string{"media/Web Site"}) {
		t.Errorf("image interpolée : %v (images %v)", got, refs.images)
	}
	if got := refs.images["monprojet-api:latest"]; !slices.Equal(got, []string{"app"}) {
		t.Errorf("image par défaut d'un service construit : %v (images %v)", got, refs.images)
	}
	if got := refs.externalNets["traefik_lan"]; !slices.Equal(got, []string{"media/Web Site"}) {
		t.Errorf("réseau externe : %v", refs.externalNets)
	}
	for p, s := range map[string]string{"monprojet": "app", "website": "media/Web Site", "casse": "casse"} {
		if refs.projects[p] != s {
			t.Errorf("projet %q → %q, want %q (projets %v)", p, refs.projects[p], s, refs.projects)
		}
	}
	if refs.errors["casse"] == nil {
		t.Error("compose illisible non signalé")
	}
}

func sha(seed string) string {
	h := ""
	for len(h) < 64 {
		h += "0123456789abcdef"
	}
	return (seed + h)[:64]
}

func mapsEqual[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// Cache de build : tout le cache inutilisé (« builder prune -a », la part que
// le démon compte comme libérable), compte rendu du démon dans le détail.
// Faux démon : le vrai cache n'est jamais vidé par les tests.
func TestCleanupLiveBuildCache(t *testing.T) {
	c, d := fakeDaemonClient(t)
	var evs []ProgressEvent
	for ev := range c.CleanupLive(context.Background(), []CleanupItem{{Kind: CleanBuildCache, Name: "cache de build", Count: 2}}) {
		if ev.AllDone {
			break
		}
		evs = append(evs, ev)
	}
	if q := d.Query("/build/prune"); !strings.Contains(q, "all=1") && !strings.Contains(q, "all=true") {
		t.Errorf("requête %q : tout le cache inutilisé attendu", q)
	}
	var detail string
	for _, ev := range evs {
		if ev.State == "done" {
			detail = ev.Details
		}
		if ev.StackDone && ev.Err != nil {
			t.Errorf("erreur : %v", ev.Err)
		}
	}
	if detail != "2 entrées, 2.0 Go récupérés" {
		t.Errorf("détail %q", detail)
	}
}

// Chaque commande prune appelle le démon comme la CLI : mêmes routes, mêmes
// filtres ; « system prune » enchaîne conteneurs, réseaux, images pendantes
// et cache pendant. Faux démon uniquement : un prune sur le vrai toucherait
// l'infrastructure.
func TestPruneLive(t *testing.T) {
	c, d := fakeDaemonClient(t)
	run := func(p PruneCommand) []ProgressEvent {
		d.Order()
		var evs []ProgressEvent
		for ev := range c.PruneLive(context.Background(), p) {
			if ev.AllDone {
				break
			}
			evs = append(evs, ev)
		}
		return evs
	}
	for _, tc := range []struct {
		cmd    PruneCommand
		routes []string
		query  map[string]string // route → morceau attendu dans la requête
		detail string            // détail de la dernière étape
	}{
		{PruneImages, []string{"/images/prune"}, map[string]string{"/images/prune": "dangling"}, "3.0 Go récupérés"},
		{PruneImagesAll, []string{"/images/prune"}, nil, "3.0 Go récupérés"},
		{PruneBuilder, []string{"/build/prune"}, nil, "2 supprimés · 2.0 Go récupérés"},
		{PruneBuilderAll, []string{"/build/prune"}, map[string]string{"/build/prune": "all="}, "2 supprimés · 2.0 Go récupérés"},
		{PruneNetworks, []string{"/networks/prune"}, nil, "2 supprimés · 0 Mo récupérés"},
		{PruneContainers, []string{"/containers/prune"}, nil, "1 supprimé · 1 Mo récupérés"},
		{PruneVolumes, []string{"/volumes/prune"}, nil, "0 supprimé · 0 Mo récupérés"},
		{PruneSystem, []string{"/containers/prune", "/networks/prune", "/images/prune", "/build/prune"}, nil, "2 supprimés · 2.0 Go récupérés"},
	} {
		evs := run(tc.cmd)
		if got := d.Order(); !slices.Equal(got, tc.routes) {
			t.Errorf("%s : routes %v, want %v", tc.cmd.CLI(), got, tc.routes)
		}
		for route, want := range tc.query {
			if q := d.Query(route); !strings.Contains(q, want) {
				t.Errorf("%s : requête %s %q, want %q", tc.cmd.CLI(), route, q, want)
			}
		}
		var last ProgressEvent
		for _, ev := range evs {
			if ev.State == "done" || ev.State == "error" {
				last = ev
			}
			if ev.StackDone && ev.Err != nil {
				t.Errorf("%s : %v", tc.cmd.CLI(), ev.Err)
			}
		}
		if last.Details != tc.detail {
			t.Errorf("%s : détail %q, want %q", tc.cmd.CLI(), last.Details, tc.detail)
		}
	}
	// Filtre des images : dangling=true sans -a, false avec (comme la CLI).
	run(PruneImages)
	if q := d.Query("/images/prune"); !strings.Contains(q, "true") {
		t.Errorf("image prune : %q", q)
	}
	run(PruneImagesAll)
	if q := d.Query("/images/prune"); !strings.Contains(q, "false") {
		t.Errorf("image prune -a : %q", q)
	}
}
