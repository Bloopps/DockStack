package compose

import (
	"path/filepath"
	"testing"

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
		{ID: "a", Names: []string{"/myweb"}, State: "running"},                                                               // orphelin → rattaché
		{ID: "b", Names: []string{"/autre"}, State: "running", Labels: map[string]string{"com.docker.compose.project": "x"}}, // pas à nous
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
