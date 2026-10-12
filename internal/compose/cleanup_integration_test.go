//go:build integration

package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	mobyclient "github.com/moby/moby/client"

	"github.com/Bloopps/dockstack/internal/fakedocker"
)

// Objets jetables zzdsprobe* de chaque catégorie, dans un dossier de stacks
// temporaire : l'analyse doit les classer comme prévu. Tout est supprimé à
// la fin ; seules les assertions sur les objets zzdsprobe comptent (le vrai
// démon a aussi les objets de l'infra, jamais touchés).
func TestLiveAnalyzeCleanup(t *testing.T) {
	c := newLiveClient(t)
	if _, err := dockerCLI("image", "inspect", probeImage); err != nil {
		if out, err := dockerCLI("pull", probeImage); err != nil {
			t.Fatalf("pull : %v\n%s", err, out)
		}
	}
	var images []string
	t.Cleanup(func() {
		for _, i := range images {
			dockerCLI("rmi", "-f", i)
		}
	})
	must := func(args ...string) string {
		out, err := dockerCLI(args...)
		if err != nil {
			t.Fatalf("docker %v : %v\n%s", args, err, out)
		}
		return out
	}
	// Trois images propres : pendante, citée par une stack, libre.
	must("run", "--name", "zzdsprobecln", probeImage, "sh", "-c", "echo x > /zzd")
	dangling := must("commit", "zzdsprobecln")
	images = append(images, dangling)
	must("commit", "-c", "LABEL zzd=ref", "zzdsprobecln", "zzdsprobe-ref:1")
	must("commit", "-c", "LABEL zzd=free", "zzdsprobecln", "zzdsprobe-free:1")
	images = append(images, "zzdsprobe-ref:1", "zzdsprobe-free:1")
	must("rm", "zzdsprobecln")

	root := t.TempDir()
	keep := filepath.Join(root, "zzdsprobekeep")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(keep, "compose.yaml"), []byte(`services:
  app:
    image: zzdsprobe-ref:1
    networks: [ext]
networks:
  ext:
    external: true
    name: zzdsprobe_ext
`), 0o644)

	must("network", "create", "zzdsprobe_free")
	must("network", "create", "zzdsprobe_ext")
	must("volume", "create", "--label", "com.docker.compose.project=zzdsprobeghost", "zzdsprobeghost_data")
	must("volume", "create", "--label", "com.docker.compose.project=zzdsprobekeep", "zzdsprobekeep_data")
	must("volume", "create", "zzdsprobe-nolabel")

	rep, err := c.AnalyzeCleanup(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CleanupKind{}
	for _, it := range rep.Items {
		got[it.ID] = it.Kind
		got[it.Name] = it.Kind
	}
	for key, want := range map[string]CleanupKind{
		dangling:              CleanDanglingImage,
		"zzdsprobe-ref:1":     CleanStackImage,
		"zzdsprobe-free:1":    CleanUnusedImage,
		"zzdsprobe_free":      CleanUnusedNetwork,
		"zzdsprobeghost_data": CleanOrphanVolume,
	} {
		if k, ok := got[key]; !ok || k != want {
			// Diagnostic : ce que le démon disait de l'objet au moment de
			// l'analyse (échec isolé non reproduit le 2026-10-02).
			var seen []string
			for _, img := range rep.debugImages {
				if img.ID == key || slices.Contains(img.RepoTags, key) {
					seen = append(seen, fmt.Sprintf("image %s tags=%v conteneurs=%d parent=%q", img.ID, img.RepoTags, img.Containers, img.ParentID))
				}
			}
			t.Errorf("%s : catégorie %v (présent=%v), want %v ; vu par le démon : %v ; erreurs de stacks : %v",
				key, k, ok, want, seen, rep.LoadErrors)
		}
	}
	if _, ok := got["zzdsprobe_ext"]; ok {
		t.Error("réseau externe d'une stack proposé")
	}
	found := map[string]string{}
	for _, v := range rep.KeptStackVolumes {
		found[v.Name] = "stack:" + v.Stack
	}
	for _, v := range rep.KeptUnlabeledVolumes {
		found[v.Name] = "sans étiquette"
	}
	if found["zzdsprobekeep_data"] != "stack:zzdsprobekeep" || found["zzdsprobe-nolabel"] != "sans étiquette" {
		t.Errorf("volumes conservés : %v", found)
	}
}

// Le bilan reprend exactement « docker system df » : mêmes nombres, mêmes
// actifs, tailles égales à l'arrondi de la CLI près. Lecture seule.
func TestLiveCleanupDFParity(t *testing.T) {
	fakedocker.LiveLock(t)
	c, err := New()
	if err != nil {
		t.Skip(err)
	}
	rep, err := c.AnalyzeCleanup(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := dockerCLI("system", "df", "--format", "{{.Type}}|{{.TotalCount}}|{{.Active}}|{{.Size}}|{{.Reclaimable}}")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]DFRow{"Images": rep.Images, "Containers": rep.Containers, "Local Volumes": rep.Volumes, "Build Cache": rep.Builds}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "|")
		row, ok := rows[f[0]]
		if len(f) != 5 || !ok {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		a, _ := strconv.ParseInt(f[2], 10, 64)
		if n != row.Count || a != row.Active {
			t.Errorf("%s : %d/%d actifs, CLI %d/%d", f[0], row.Count, row.Active, n, a)
		}
		for i, v := range []int64{row.Size, row.Reclaimable} {
			cli := parseHumanSize(strings.Fields(f[3+i])[0])
			if d := float64(v) - cli; d > cli*0.02+1e6 || -d > cli*0.02+1e6 {
				t.Errorf("%s colonne %d : %d octets, CLI %s", f[0], 3+i, v, f[3+i])
			}
		}
		t.Logf("%-13s %4d %4d actifs  %s  libérable %s  (CLI identique)", f[0], row.Count, row.Active, human(row.Size), human(row.Reclaimable))
	}
}

func parseHumanSize(s string) float64 {
	for _, u := range []struct {
		suf string
		mul float64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1}} {
		if v, ok := strings.CutSuffix(s, u.suf); ok {
			f, _ := strconv.ParseFloat(v, 64)
			return f * u.mul
		}
	}
	return 0
}

func human(b int64) string { return fmt.Sprintf("%.2f Go", float64(b)/1e9) }

// Vérité terrain, lecture seule : l'analyse du vrai dossier de stacks. Rien
// n'est vérifié automatiquement (dépend de la machine) ; le journal sert à
// comparer avec un relevé manuel.
func TestLiveCleanupRealStacks(t *testing.T) {
	if os.Getenv("DOCKSTACK_MEASURE") == "" {
		t.Skip("DOCKSTACK_MEASURE=1 pour l'analyse du vrai dossier")
	}
	c, err := New()
	if err != nil {
		t.Skip(err)
	}
	t0 := time.Now()
	rep, err := c.AnalyzeCleanup(context.Background(), "/home/ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("analyse : %v", time.Since(t0).Round(time.Millisecond))
	var approx int
	ids := map[CleanupKind][]string{}
	for _, it := range rep.Items {
		if it.Approx {
			approx++
			t.Logf("image estimée : %s", it.Name)
		}
		if it.ID != "" && (it.Kind == CleanDanglingImage || it.Kind == CleanUnusedImage || it.Kind == CleanStackImage) {
			ids[it.Kind] = append(ids[it.Kind], it.ID)
		}
	}
	t.Logf("images proposées non détaillées : %d", approx)
	sel := []string{}
	for _, k := range []CleanupKind{CleanDanglingImage, CleanUnusedImage, CleanStackImage} {
		sel = append(sel, ids[k]...)
		b, exact := rep.Layers.Freed(sel)
		t.Logf("libéré jusqu'à la catégorie %d incluse : %s (exact %v)", k, human(b), exact)
	}
	count := map[CleanupKind]int{}
	size := map[CleanupKind]int64{}
	for _, it := range rep.Items {
		count[it.Kind] += it.Count
		if it.Size > 0 {
			size[it.Kind] += it.Size
		}
		if it.Kind == CleanOrphanVolume || it.Kind == CleanAnonymousVolume {
			t.Logf("volume proposé : %s (projet %v)", it.Name, it.Stacks)
		}
	}
	for k := CleanBuildCache; k <= CleanAnonymousVolume; k++ {
		t.Logf("catégorie %d : %d éléments, %s", k, count[k], human(size[k]))
	}
	for _, v := range rep.KeptStackVolumes {
		t.Logf("conservé (stack %s) : %s", v.Stack, v.Name)
	}
	for _, v := range rep.KeptUnlabeledVolumes {
		t.Logf("conservé (sans étiquette) : %s", v.Name)
	}
	t.Logf("stacks illisibles : %v ; conteneurs arrêtés : %d", rep.LoadErrors, len(rep.StoppedContainers))
}

// L'espace prédit par Layers.Freed est exactement celui que libère la
// suppression, mesuré par le démon (taille totale des couches avant et
// après). Trois images jetables : P1 (busybox + 8 Mo), P2 et P3 construites
// sur P1 (+1 Mo, +2 Mo). La couche de 8 Mo n'est libérée qu'avec P1, P2 et
// P3 ensemble.
func TestLiveLayersFreedExact(t *testing.T) {
	c := newLiveClient(t)
	if _, err := dockerCLI("image", "inspect", probeImage); err != nil {
		if out, err := dockerCLI("pull", probeImage); err != nil {
			t.Fatalf("pull : %v\n%s", err, out)
		}
	}
	tags := []string{"zzdsprobe-lyr:1", "zzdsprobe-lyr:2", "zzdsprobe-lyr:3"}
	t.Cleanup(func() {
		for _, tg := range tags {
			dockerCLI("rmi", "-f", tg)
		}
	})
	must := func(args ...string) string {
		out, err := dockerCLI(args...)
		if err != nil {
			t.Fatalf("docker %v : %v\n%s", args, err, out)
		}
		return out
	}
	build := func(from, tag, file string, mb int) string {
		must("run", "--name", "zzdsprobelyr", from, "sh", "-c", fmt.Sprintf("dd if=/dev/urandom of=/%s bs=1M count=%d 2>/dev/null", file, mb))
		id := must("commit", "zzdsprobelyr", tag)
		must("rm", "zzdsprobelyr")
		return id
	}
	p1 := build(probeImage, tags[0], "big", 8)
	p2 := build(tags[0], tags[1], "small", 1)
	p3 := build(tags[0], tags[2], "other", 2)

	ctx := context.Background()
	rep, err := c.AnalyzeCleanup(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{p1, p2, p3} {
		if !rep.Layers.Exact(id) {
			t.Fatalf("%s non détaillée", id)
		}
	}
	only2, _ := rep.Layers.Freed([]string{p2})
	two, _ := rep.Layers.Freed([]string{p2, p3})
	all, _ := rep.Layers.Freed([]string{p1, p2, p3})
	t.Logf("prévu : P2 seule %d o, P2+P3 %d o, les trois %d o", only2, two, all)
	if !(only2 > 1<<20-1 && two > only2 && all > two+8<<20-1) {
		t.Errorf("prévisions incohérentes : %d, %d, %d", only2, two, all)
	}

	total := func() int64 {
		du, err := c.cli.Client().DiskUsage(ctx, mobyclient.DiskUsageOptions{Images: true})
		if err != nil {
			t.Fatal(err)
		}
		return du.Images.TotalSize
	}
	before := total()
	for _, tg := range []string{tags[1], tags[2], tags[0]} {
		if _, err := c.cli.Client().ImageRemove(ctx, tg, mobyclient.ImageRemoveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	freed := before - total()
	t.Logf("libéré mesuré par le démon : %d o ; prévu : %d o", freed, all)
	if freed != all {
		t.Errorf("écart entre prévu (%d) et libéré (%d)", all, freed)
	}
}

// Le libérable de l'en-tête (Usage) est le même chiffre exact que celui de la
// page de nettoyage, et le cache des couches rend la mesure suivante
// presque gratuite. Lecture seule.
func TestLiveUsageImagesExact(t *testing.T) {
	fakedocker.LiveLock(t)
	c, err := New()
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	t0 := time.Now()
	u1, err := c.Usage(ctx, UsageOptions{Images: true})
	if err != nil {
		t.Fatal(err)
	}
	first := time.Since(t0)
	t0 = time.Now()
	u2, err := c.Usage(ctx, UsageOptions{Images: true})
	if err != nil {
		t.Fatal(err)
	}
	second := time.Since(t0)
	t.Logf("libérable exact %s (exact %v) ; 1re mesure %v, suivante %v (cache)", human(u2.ImagesReclaimable), u2.ImagesExact,
		first.Round(time.Millisecond), second.Round(time.Millisecond))
	if !u1.ImagesExact || u1.ImagesReclaimable != u2.ImagesReclaimable {
		t.Errorf("mesures : %+v puis %+v", u1, u2)
	}
	rep, err := c.AnalyzeCleanup(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range rep.Items {
		switch it.Kind {
		case CleanDanglingImage, CleanUnusedImage, CleanStackImage:
			ids = append(ids, it.ID)
		}
	}
	page, _ := rep.Layers.Freed(ids)
	if page != u2.ImagesReclaimable {
		t.Errorf("en-tête %d, page %d", u2.ImagesReclaimable, page)
	}
}

// CleanupLive supprime exactement les éléments choisis, sans forcer : une
// image et un volume remis en service entre l'analyse et la suppression sont
// refusés par le démon et restent en place. Le cache de build n'est jamais
// touché ici (ce serait le vrai) : voir TestCleanupLiveBuildCache.
func TestLiveCleanupRemoves(t *testing.T) {
	c := newLiveClient(t)
	if _, err := dockerCLI("image", "inspect", probeImage); err != nil {
		if out, err := dockerCLI("pull", probeImage); err != nil {
			t.Fatalf("pull : %v\n%s", err, out)
		}
	}
	tags := []string{"zzdsprobe-two:a", "zzdsprobe-two:b", "zzdsprobe-busy:1", "zzdsprobe-busy:2"}
	t.Cleanup(func() {
		dockerCLI("rm", "-f", "zzdsprobebusy")
		for _, tg := range tags {
			dockerCLI("rmi", "-f", tg)
		}
	})
	must := func(args ...string) string {
		out, err := dockerCLI(args...)
		if err != nil {
			t.Fatalf("docker %v : %v\n%s", args, err, out)
		}
		return out
	}
	must("run", "--name", "zzdsprobesrc", probeImage, "sh", "-c", "echo 1 > /a")
	dangling := must("commit", "zzdsprobesrc")
	two := must("commit", "-c", "LABEL zzd=two", "zzdsprobesrc", tags[0])
	must("tag", tags[0], tags[1])
	busy := must("commit", "-c", "LABEL zzd=busy", "zzdsprobesrc", tags[2])
	must("tag", tags[2], tags[3]) // deux étiquettes : la première se retirerait sans contrôle
	must("rm", "zzdsprobesrc")
	must("network", "create", "zzdsprobe_gone")
	must("volume", "create", "zzdsprobe_orphan")
	must("volume", "create", "zzdsprobe_mounted")
	t.Cleanup(func() { dockerCLI("rmi", "-f", dangling) })

	items := []CleanupItem{
		{Kind: CleanDanglingImage, ID: dangling, Name: "<none>"},
		{Kind: CleanUnusedImage, ID: two, Name: tags[0] + ", " + tags[1], Refs: tags[:2]},
		{Kind: CleanUnusedImage, ID: busy, Name: tags[2] + ", " + tags[3], Refs: tags[2:]},
		{Kind: CleanUnusedNetwork, ID: "zzdsprobe_gone", Name: "zzdsprobe_gone"},
		{Kind: CleanOrphanVolume, ID: "zzdsprobe_orphan", Name: "zzdsprobe_orphan"},
		{Kind: CleanOrphanVolume, ID: "zzdsprobe_mounted", Name: "zzdsprobe_mounted"},
	}
	// Entre l'analyse et la suppression, l'image « busy » et un volume se
	// remettent à servir.
	must("run", "-d", "--name", "zzdsprobebusy", "-v", "zzdsprobe_mounted:/data", tags[2], "sleep", "300")

	done := map[string]error{}
	states := map[string]string{}
	for ev := range c.CleanupLive(context.Background(), items) {
		if ev.AllDone {
			break
		}
		if ev.StackDone {
			done[ev.StackName] = ev.Err
			continue
		}
		states[ev.Container] = ev.State
	}
	exists := func(args ...string) bool { _, err := dockerCLI(args...); return err == nil }
	if exists("image", "inspect", dangling) || exists("image", "inspect", two) {
		t.Error("images inutilisées encore présentes")
	}
	if !exists("image", "inspect", tags[2]) || !exists("image", "inspect", tags[3]) {
		t.Error("image remise en service supprimée ou amputée d'une étiquette")
	}
	if exists("network", "inspect", "zzdsprobe_gone") || exists("volume", "inspect", "zzdsprobe_orphan") {
		t.Error("réseau ou volume orphelin encore présent")
	}
	if !exists("volume", "inspect", "zzdsprobe_mounted") {
		t.Error("volume monté supprimé")
	}
	if done["Images inutilisées"] == nil || done["Volumes orphelins"] == nil {
		t.Errorf("refus non signalés : %v", done)
	}
	if done["Images pendantes <none>"] != nil || done["Réseaux inutilisés"] != nil {
		t.Errorf("erreurs inattendues : %v", done)
	}
	t.Logf("états : %v", states)
}
