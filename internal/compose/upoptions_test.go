package compose

import (
	"slices"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// TestUpOptionsInheritAnonymousVolumes : up et recreate reprennent les
// volumes anonymes du conteneur remplacé, comme « docker compose up » sans
// --renew-anon-volumes. Sans Inherit, un service dont l'image déclare VOLUME
// (base de données sans volume nommé) redémarrait vide après une recréation.
func TestUpOptionsInheritAnonymousVolumes(t *testing.T) {
	proj := &types.Project{Name: "p"}
	for _, recreate := range []string{api.RecreateDiverged, api.RecreateForce} {
		for _, only := range [][]string{nil, {"db"}} {
			o := upOptions(Stack{Only: only}, proj, recreate)
			if !o.Create.Inherit {
				t.Errorf("recreate=%s only=%v : Inherit=false, les volumes anonymes seraient perdus", recreate, only)
			}
			if o.Create.Recreate != recreate {
				t.Errorf("Recreate = %q, want %q", o.Create.Recreate, recreate)
			}
			if !slices.Equal(o.Create.Services, only) || !slices.Equal(o.Start.Services, only) {
				t.Errorf("services ciblés = %v / %v, want %v", o.Create.Services, o.Start.Services, only)
			}
			if o.Start.Project != proj {
				t.Error("le projet doit être passé au démarrage (comme la CLI)")
			}
			if o.Start.Attach != nil {
				t.Error("up doit rester détaché (Attach nil)")
			}
		}
	}
}
