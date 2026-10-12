package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
)

func TestProjectContainerNames(t *testing.T) {
	proj := &types.Project{
		Name: "beszel",
		Services: types.Services{
			"beszel": types.ServiceConfig{ContainerName: "beszel"},
			"agent":  types.ServiceConfig{}, // pas de container_name → motifs par défaut
		},
	}
	got := projectContainerNames(proj)

	cases := map[string]string{
		"beszel":          "beszel", // container_name explicite
		"beszel-beszel-1": "beszel", // motif par défaut tiret
		"beszel_beszel_1": "beszel", // motif par défaut underscore
		"beszel-agent-1":  "agent",
		"beszel_agent_1":  "agent",
	}
	for name, wantSvc := range cases {
		if got[name] != wantSvc {
			t.Errorf("nom %q → service %q, attendu %q", name, got[name], wantSvc)
		}
	}
}

func TestMatchByNames(t *testing.T) {
	cand := map[string]string{"beszel": "beszel", "beszel-agent-1": "agent"}

	if svc, ok := matchByNames(cand, []string{"/beszel"}); !ok || svc != "beszel" {
		t.Errorf("/beszel → (%q,%v), attendu (beszel,true)", svc, ok)
	}
	if svc, ok := matchByNames(cand, []string{"/beszel-agent-1"}); !ok || svc != "agent" {
		t.Errorf("/beszel-agent-1 → (%q,%v), attendu (agent,true)", svc, ok)
	}
	if _, ok := matchByNames(cand, []string{"/inconnu"}); ok {
		t.Error("/inconnu ne devrait pas matcher")
	}
}

// TestAttachOrphans : un conteneur en marche SANS label projet mais portant le
// container_name d'un service est rattaché à sa stack (ressortie à 0), qui
// devient Orphaned ; un conteneur AVEC label projet n'est jamais volé.
func TestAttachOrphans(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	writeFile(t, file, "services:\n  web:\n    image: nginx\n    container_name: myweb\n")

	stacks := []Stack{{Name: "app", Dir: dir, ComposeFile: file}} // Total == 0 (paraît down)
	items := []container.Summary{
		{ID: "a", Names: []string{"/myweb"}, State: "running", Labels: map[string]string{"com.docker.compose.replace": "web"}}, // orphelin watchtower → rattaché
		{ID: "b", Names: []string{"/autre"}, State: "running", Labels: map[string]string{"com.docker.compose.project": "x"}},   // pas à nous
	}

	attachOrphans(stacks, items)

	s := stacks[0]
	if !s.Orphaned {
		t.Fatal("stack devrait être marquée Orphaned")
	}
	if s.Total != 1 || s.Running != 1 {
		t.Fatalf("counts = %d/%d, attendu 1/1", s.Running, s.Total)
	}
	if len(s.Services) != 1 || s.Services[0].Name != "web" {
		t.Fatalf("services = %+v, attendu [web]", s.Services)
	}
}

// TestAttachOrphansNoOrphan : sans conteneur orphelin, attachOrphans ne charge
// rien et ne touche pas les stacks (coût nul en régime nominal).
func TestAttachOrphansNoOrphan(t *testing.T) {
	stacks := []Stack{{Name: "app", Dir: "/inexistant", ComposeFile: "/inexistant/compose.yaml"}}
	items := []container.Summary{
		{ID: "a", Names: []string{"/x"}, State: "running", Labels: map[string]string{"com.docker.compose.project": "x"}},
	}
	attachOrphans(stacks, items)
	if stacks[0].Orphaned || stacks[0].Total != 0 {
		t.Fatal("aucune attribution attendue sans orphelin (et loadProject ne doit pas être appelé)")
	}
}

// Un conteneur lancé à la main (docker run, aucune trace compose/watchtower)
// n'est jamais pris pour un orphelin, même s'il porte le container_name d'un
// service : sinon un Down sur la stack le supprimerait.
func TestAttachOrphansIgnoresHandRun(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	writeFile(t, file, "services:\n  db:\n    image: postgres\n    container_name: postgres\n")
	stacks := []Stack{{Name: "dev", Dir: dir, ComposeFile: file}}
	items := []container.Summary{{ID: "a", Names: []string{"/postgres"}, State: "running"}}
	attachOrphans(stacks, items)
	if stacks[0].Orphaned || stacks[0].Total != 0 {
		t.Fatalf("conteneur docker run rattaché à tort : Orphaned=%v total=%d", stacks[0].Orphaned, stacks[0].Total)
	}
}

func TestIsOrphanCandidate(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   bool
	}{
		{nil, false}, // docker run nu
		{map[string]string{"maintainer": "x"}, false},
		{map[string]string{"com.docker.compose.replace": "web"}, true},
		{map[string]string{"com.centurylinklabs.watchtower.enable": "true"}, true},
		{map[string]string{"com.docker.compose.project": "p", "com.docker.compose.service": "web"}, false}, // rattaché
	}
	for _, c := range cases {
		if got := isOrphanCandidate(c.labels); got != c.want {
			t.Errorf("isOrphanCandidate(%v) = %v, want %v", c.labels, got, c.want)
		}
	}
}

// TestCachedContainerNames : le projet n'est rechargé que si l'un de ses
// fichiers (compose, override, .env) change, apparaît ou disparaît.
func TestCachedContainerNames(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	write := func(path, body string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour)
	write(file, "services:\n  web:\n    image: busybox\n", t0)
	st := Stack{Name: "s", Dir: dir, ComposeFile: file}

	name, cand, err := cachedContainerNames(st)
	if err != nil || cand[filepath.Base(dir)+"-web-1"] != "web" {
		t.Fatalf("premier chargement : %q %v %v", name, cand, err)
	}
	_, again, _ := cachedContainerNames(st)
	if fmt.Sprintf("%p", again) != fmt.Sprintf("%p", cand) {
		t.Fatal("fichiers inchangés : le résultat doit venir du cache")
	}

	// Le compose change (même date, autre taille) : rechargé.
	write(file, "services:\n  api:\n    image: busybox:1\n", t0)
	_, cand, _ = cachedContainerNames(st)
	if cand[filepath.Base(dir)+"-api-1"] != "api" {
		t.Fatalf("compose modifié non relu : %v", cand)
	}
	// Un .env qui apparaît peut renommer le projet.
	write(filepath.Join(dir, ".env"), "COMPOSE_PROJECT_NAME=autre\n", t0)
	name, cand, _ = cachedContainerNames(st)
	if name != "autre" || cand["autre-api-1"] != "api" {
		t.Fatalf(".env ajouté non pris en compte : %q %v", name, cand)
	}
	// Un override qui apparaît ajoute un service.
	write(filepath.Join(dir, "compose.override.yaml"), "services:\n  extra:\n    image: busybox\n", t0)
	_, cand, _ = cachedContainerNames(st)
	if cand["autre-extra-1"] != "extra" {
		t.Fatalf("override ajouté non pris en compte : %v", cand)
	}
	// Une erreur de chargement est mémorisée aussi, jusqu'à correction.
	write(file, "services: [", t0.Add(time.Minute))
	if _, _, err := cachedContainerNames(st); err == nil {
		t.Fatal("compose invalide : erreur attendue")
	}
}
