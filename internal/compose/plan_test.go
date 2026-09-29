package compose

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/containerd/platforms"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// resolveRefs reproduit compose : les références service:X et volumes_from
// pointent vers l'id du conteneur, sinon le hash diffère à chaque up.
func TestResolveRefs(t *testing.T) {
	by := map[string][]container.Summary{
		"vpn":  {{ID: "vpnid"}},
		"data": {{ID: "dataid"}},
	}
	sv := types.ServiceConfig{
		NetworkMode: "service:vpn",
		Ipc:         "service:vpn",
		VolumesFrom: []string{"data:ro", "container:abc"},
	}
	got := resolveRefs(sv, by)
	if got.NetworkMode != "container:vpnid" || got.Ipc != "container:vpnid" {
		t.Errorf("namespaces = %q/%q", got.NetworkMode, got.Ipc)
	}
	if got.VolumesFrom[0] != "dataid" || got.VolumesFrom[1] != "abc" {
		t.Errorf("volumes_from = %v", got.VolumesFrom)
	}
	if sv.VolumesFrom[0] != "data:ro" {
		t.Error("le service d'origine ne doit pas être modifié")
	}
	// Parent absent : la résolution s'arrête, la forme brute reste.
	if got := resolveRefs(types.ServiceConfig{NetworkMode: "service:absent"}, by); got.NetworkMode != "service:absent" {
		t.Errorf("parent absent : %q", got.NetworkMode)
	}
}

func TestRecreatedParent(t *testing.T) {
	reasons := map[string]string{"vpn": "image mise à jour"}
	if p := recreatedParent(types.ServiceConfig{NetworkMode: "service:vpn"}, reasons); p != "vpn" {
		t.Errorf("network_mode : parent %q", p)
	}
	if p := recreatedParent(types.ServiceConfig{VolumesFrom: []string{"vpn:ro"}}, reasons); p != "vpn" {
		t.Errorf("volumes_from : parent %q", p)
	}
	if p := recreatedParent(types.ServiceConfig{NetworkMode: "service:autre"}, reasons); p != "" {
		t.Errorf("parent non recréé : %q", p)
	}
}

// contentDigest reproduit compose : manifeste « image » de la plateforme, sinon
// l'unique manifeste disponible, sinon l'id (stockage overlay2 sans manifestes).
func TestContentDigest(t *testing.T) {
	amd := &image.ImageProperties{Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"}}
	arm := &image.ImageProperties{Platform: ocispec.Platform{OS: "linux", Architecture: "arm64"}}
	ins := image.InspectResponse{ID: "sha256:index", Manifests: []image.ManifestSummary{
		{ID: "sha256:att", Kind: image.ManifestKindAttestation, Available: true},
		{ID: "sha256:arm", Kind: image.ManifestKindImage, Available: true, ImageData: arm},
		{ID: "sha256:amd", Kind: image.ManifestKindImage, Available: true, ImageData: amd},
	}}
	if got := contentDigest(ins, platforms.Only(ocispec.Platform{OS: "linux", Architecture: "amd64"})); got != "sha256:amd" {
		t.Errorf("amd64 : %s", got)
	}
	if got := contentDigest(image.InspectResponse{ID: "sha256:id"}, platforms.Default()); got != "sha256:id" {
		t.Errorf("sans manifestes : %s", got)
	}
}
