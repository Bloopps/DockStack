package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Remove efface les volumes : ↩ (la touche qui ouvre le panneau puis lance
// l'entrée) ne le confirme pas, seul y le fait. Avant, ↩ ↩ sur « Remove »
// supprimait les données d'une stack.
func TestRemoveNeedsExplicitY(t *testing.T) {
	fake := &fakeClient{autoDone: true}
	m := newTestModel([]compose.Stack{stack("app", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false

	m, _ = update(m, keyMsg("enter")) // panneau d'actions
	for i, a := range stackActions {
		if a.id == "remove" {
			m.actionCursor = i
		}
	}
	m, _ = update(m, keyMsg("enter"))
	if m.view != viewConfirm || !m.confirmDanger {
		t.Fatalf("Remove : vue %v, danger %v ; want une confirmation renforcée", m.view, m.confirmDanger)
	}
	screen := ansi.Strip(m.viewContent())
	if strings.Contains(screen, "y / ↩") {
		t.Error("la confirmation annonce encore ↩")
	}

	m, _ = update(m, keyMsg("enter"))
	if m.view != viewConfirm || len(fake.called("remove")) != 0 {
		t.Fatalf("↩ a confirmé le Remove (vue %v, appels %v)", m.view, fake.called("remove"))
	}
	if !m.statusErr || !strings.Contains(m.status, "y pour confirmer") {
		t.Errorf("statut = %q, want un rappel de y", m.status)
	}

	m, _ = update(m, keyMsg("y"))
	if got := fake.called("remove"); len(got) != 1 || got[0].Name != "app" {
		t.Fatalf("y : Remove appelé avec %v", got)
	}
}

// Les confirmations ordinaires gardent ↩, et une confirmation renforcée
// annulée ne déteint pas sur la suivante.
func TestPlainConfirmKeepsEnter(t *testing.T) {
	fake := &fakeClient{autoDone: true}
	m := newTestModel([]compose.Stack{stack("app", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 40, false
	m.confirmDanger = true // reste d'une confirmation précédente

	m, _ = update(m, keyMsg("d"))
	if m.view != viewConfirm || m.confirmDanger {
		t.Fatalf("Down : vue %v, danger %v", m.view, m.confirmDanger)
	}
	m, _ = update(m, keyMsg("enter"))
	if len(fake.called("down")) != 1 {
		t.Error("↩ doit confirmer un Down")
	}
}

// Commandes prune qui effacent des données (volumes, conteneurs arrêtés).
func TestPruneErases(t *testing.T) {
	for p, want := range map[compose.PruneCommand]bool{
		compose.PruneVolumes: true, compose.PruneContainers: true, compose.PruneSystem: true,
		compose.PruneImages: false, compose.PruneImagesAll: false, compose.PruneBuilderAll: false, compose.PruneNetworks: false,
	} {
		if got := p.Erases(); got != want {
			t.Errorf("%s : Erases = %v, want %v", p.CLI(), got, want)
		}
	}
}

// Nettoyage : sans volume coché, ↩ confirme ; avec un volume (données),
// seul y confirme.
func TestCleanupVolumesNeedY(t *testing.T) {
	m, fake := cleanupModel(t)
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewConfirm || m.confirmDanger {
		t.Fatalf("sans volume : vue %v, danger %v", m.view, m.confirmDanger)
	}
	m = drive(t, m, keyMsg("esc"), 0)
	for _, it := range m.cleanupRep.Items {
		if it.Kind == compose.CleanOrphanVolume {
			m.cleanupSel[cleanupKey(it)] = true
		}
	}
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewConfirm || !m.confirmDanger {
		t.Fatalf("avec volume : vue %v, danger %v", m.view, m.confirmDanger)
	}
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewConfirm || len(fake.cleaned) != 0 {
		t.Fatalf("↩ a lancé la suppression des volumes (vue %v)", m.view)
	}
}
