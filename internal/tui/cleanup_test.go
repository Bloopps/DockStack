package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// sampleCleanup : un rapport avec chaque catégorie, noms longs compris.
func sampleCleanup() compose.CleanupReport {
	gb := int64(1 << 30)
	return compose.CleanupReport{
		At:         time.Now(),
		Images:     compose.DFRow{Count: 189, Active: 37, Size: 82 * gb, Reclaimable: 22 * gb},
		Containers: compose.DFRow{Count: 70, Active: 68, Size: gb / 2},
		Volumes:    compose.DFRow{Count: 36, Active: 11, Size: 5 * gb, Reclaimable: gb},
		Builds:     compose.DFRow{Count: 843, Size: 47 * gb, Reclaimable: 16 * gb},
		Items: []compose.CleanupItem{
			{Kind: compose.CleanBuildCache, Name: "cache de build", Size: 16 * gb, Count: 251},
			{Kind: compose.CleanDanglingImage, ID: "sha256:d1", Name: "<none>", Size: 2 * gb, Count: 1, Created: time.Now().Add(-48 * time.Hour)},
			{Kind: compose.CleanDanglingImage, ID: "sha256:d2", Name: "ghcr.io/immich-app/immich-machine-learning <none>", Size: gb, Count: 1},
			{Kind: compose.CleanUnusedNetwork, ID: "n1", Name: "demo_default", Size: -1, Count: 1},
			{Kind: compose.CleanUnusedImage, ID: "sha256:u1", Name: "postgres:13, pg:old", Size: 300 << 20, Count: 1},
			{Kind: compose.CleanStackImage, ID: "sha256:s1", Name: "ghcr.io/immich-app/immich-machine-learning:v2-rocm-avec-un-nom-très-long", Size: 23 * gb, Count: 1, Stacks: []string{"immich"}},
			{Kind: compose.CleanOrphanVolume, ID: "v3_data", Name: "v3_data", Size: 70 << 20, Count: 1, Stacks: []string{"v3"}},
		},
		KeptStackVolumes:     []compose.KeptVolume{{Name: "authelia_db", Stack: "authelia"}},
		KeptUnlabeledVolumes: []compose.KeptVolume{{Name: "jobdash-data"}, {Name: "portainer_data"}},
		KeptExternalNetworks: []compose.KeptNetwork{{Name: "traefik_lan", Stacks: []string{"traefik"}}},
		StoppedContainers:    []compose.StoppedContainer{{Name: "immich-ml-1", Project: "immich"}, {Name: "old"}},
	}
}

func cleanupModel(t *testing.T) (Model, *fakeClient) {
	fake := &fakeClient{cleanup: sampleCleanup()}
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 120, 50, false
	m = drive(t, m, keyMsg("n"), 0)
	if m.view != viewCleanup || m.cleanupRep == nil {
		t.Fatalf("n : vue %v, rapport %v", m.view, m.cleanupRep != nil)
	}
	return m, fake
}

// À l'ouverture, seul ce qui est sans risque est coché ; le bilan, les
// sections et ce qui est conservé s'affichent.
func TestCleanupPageDefaults(t *testing.T) {
	m, _ := cleanupModel(t)
	for _, it := range m.cleanupRep.Items {
		if got, want := m.cleanupSel[cleanupKey(it)], it.Kind.Risk() == compose.RiskNone; got != want {
			t.Errorf("%s coché=%v, want %v", it.Name, got, want)
		}
	}
	s := ansi.Strip(m.viewContent())
	for _, want := range []string{"🧹 Nettoyage", "Images", "Cache de build", "Libérable", "Sans risque",
		"À retélécharger", "Conservé", "jobdash-data, portainer_data",
		"Sélection  4 éléments"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q absent :\n%s", want, s)
		}
	}
}

// ␣ sur une catégorie coche ou décoche tous ses éléments ; → la déplie, et
// décocher un élément la met en partiel.
func TestCleanupToggleAndExpand(t *testing.T) {
	m, _ := cleanupModel(t)
	// Curseur sur « Images inutilisées » (4e ligne navigable).
	m.cleanupCursor = 3
	if r, _ := m.cleanupCurrent(); r.kind != compose.CleanUnusedImage {
		t.Fatalf("curseur sur %v", r.kind)
	}
	m = drive(t, m, keyMsg("space"), 0)
	if !m.cleanupSel[cleanupKey(m.cleanupRep.Items[4])] {
		t.Fatal("␣ n'a pas coché la catégorie")
	}
	// Images pendantes : dépliées, un élément décoché → partiel.
	m.cleanupCursor = 1
	m = drive(t, m, keyMsg("right"), 0)
	m = drive(t, m, keyMsg("down"), 0)
	if r, _ := m.cleanupCurrent(); r.item < 0 {
		t.Fatal("→ n'a pas déplié la catégorie")
	}
	m = drive(t, m, keyMsg("space"), 0)
	if got := m.checkBox(m.cleanupItems(compose.CleanDanglingImage)); got != "[-]" {
		t.Errorf("catégorie en partie cochée : %s", got)
	}
	// ← replie et remet le curseur sur la catégorie.
	m = drive(t, m, keyMsg("left"), 0)
	if r, _ := m.cleanupCurrent(); r.item >= 0 || r.kind != compose.CleanDanglingImage {
		t.Errorf("après ← : %+v", r)
	}
}

// Un résultat qui arrive après avoir quitté la page (ou après R) est écarté ;
// esc revient à la liste.
func TestCleanupStaleResultIgnored(t *testing.T) {
	m, _ := cleanupModel(t)
	old := m.cleanupSeq
	m = drive(t, m, keyMsg("esc"), 0)
	if m.view != viewList {
		t.Fatalf("esc : vue %v", m.view)
	}
	m.view = viewCleanup
	m.cleanupRep = nil
	mm, _ := m.Update(cleanupLoadedMsg{seq: old, rep: sampleCleanup()})
	if mm.(Model).cleanupRep != nil {
		t.Error("résultat périmé appliqué")
	}
}

// Analyse en échec : message replié et reprise par R.
func TestCleanupError(t *testing.T) {
	fake := &fakeClient{cleanErr: errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?")}
	m := newTestModel([]compose.Stack{stack("web", "", 1, 1, 0)})
	m.client = fake
	m.width, m.height, m.loading = 80, 30, false
	m = drive(t, m, keyMsg("n"), 0)
	if s := ansi.Strip(m.viewContent()); !strings.Contains(s, "Analyse impossible") || !strings.Contains(s, "R pour réessayer") {
		t.Errorf("écran d'erreur :\n%s", s)
	}
	fake.cleanErr, fake.cleanup = nil, sampleCleanup()
	m = drive(t, m, keyShiftR, 0)
	if m.cleanupRep == nil || m.cleanupErr != nil {
		t.Errorf("R n'a pas relancé l'analyse : %v", m.cleanupErr)
	}
}

// Parcours complet : ↩ → confirmation (détail, sans avertissement pour ce
// qui est sans risque) → esc revient à la page → ↩ y → progression → ↩
// revient à la page, réanalysée, avec l'espace libéré.
func TestCleanupRunFlow(t *testing.T) {
	m, fake := cleanupModel(t)
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "Nettoyage — ") || m.confirmWarning != "" {
		t.Fatalf("confirmation : vue %v, %q, avertissement %q", m.view, m.confirmLabel, m.confirmWarning)
	}
	if got := strings.Join(m.confirmNames, "\n"); !strings.Contains(got, "Cache de build : 251") || !strings.Contains(got, "Images pendantes <none> : 2") {
		t.Errorf("détail de la confirmation :\n%s", got)
	}
	m = drive(t, m, keyMsg("esc"), 0)
	if m.view != viewCleanup {
		t.Fatalf("esc depuis la confirmation : vue %v, want la page", m.view)
	}
	m = drive(t, m, keyMsg("enter"), 0)
	m = drive(t, m, keyMsg("y"), 0)
	if m.view != viewProgress || !m.progressDone {
		t.Fatalf("après y : vue %v, fini %v", m.view, m.progressDone)
	}
	if len(fake.cleaned) != 4 {
		t.Errorf("%d éléments supprimés, want les 4 sans risque : %+v", len(fake.cleaned), fake.cleaned)
	}
	for _, it := range fake.cleaned {
		if it.Kind.Risk() != compose.RiskNone {
			t.Errorf("élément à risque supprimé sans avoir été coché : %+v", it)
		}
	}
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewCleanup || m.cleanupRep == nil {
		t.Fatalf("retour : vue %v", m.view)
	}
	if s := ansi.Strip(m.viewContent()); !strings.Contains(s, "✔ Libéré 19.0 Go · prévu ≈ 19.0 Go") {
		t.Errorf("bandeau de résultat absent :\n%s", s)
	}
}

// Données et retéléchargement : avertissement explicite, volumes nommés.
func TestCleanupConfirmWarnings(t *testing.T) {
	m, _ := cleanupModel(t)
	for _, it := range m.cleanupRep.Items {
		m.cleanupSel[cleanupKey(it)] = true
	}
	m = drive(t, m, keyMsg("enter"), 0)
	if !strings.Contains(m.confirmWarning, "Efface définitivement les données de 1 volume") ||
		!strings.Contains(m.confirmWarning, "2 images devront être retéléchargées") {
		t.Errorf("avertissement : %q", m.confirmWarning)
	}
	if !strings.Contains(strings.Join(m.confirmNames, "\n"), "v3_data") {
		t.Errorf("volume non nommé : %v", m.confirmNames)
	}

	// Rien de coché : pas de confirmation.
	m, _ = cleanupModel(t)
	m.cleanupSel = map[string]bool{}
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewCleanup || m.status != "Rien de coché" {
		t.Errorf("sélection vide : vue %v, statut %q", m.view, m.status)
	}
}

// focusPrune place le curseur sur la ligne d'une commande prune.
func focusPrune(t *testing.T, m Model, p compose.PruneCommand) Model {
	t.Helper()
	rows := m.cleanupRows()
	for i, ri := range m.cleanupNav() {
		if r := rows[ri]; r.isPrune && r.prune == p {
			m.cleanupCursor = i
			return m
		}
	}
	t.Fatalf("ligne %s introuvable", p.CLI())
	return m
}

// Commandes prune : chaque ligne annonce ce que la CLI supprimerait, avec
// un avertissement quand elle dépasse la sélection prudente (réseau externe
// d'une stack, conteneurs arrêtés de stacks, images à retélécharger).
func TestCleanupPrunePreview(t *testing.T) {
	m, _ := cleanupModel(t)
	s := ansi.Strip(m.viewContent())
	for _, p := range compose.PruneCommands {
		if !strings.Contains(s, p.CLI()) {
			t.Errorf("%q absent de la page", p.CLI())
		}
	}
	if strings.Contains(s, "volume prune -a") {
		t.Error("docker volume prune -a proposé")
	}
	for p, want := range map[compose.PruneCommand]string{
		compose.PruneNetworks:   "réseau externe traefik_lan de traefik",
		compose.PruneContainers: "conteneurs arrêtés de immich",
		compose.PruneImagesAll:  "1 image de stacks arrêtées devra être retéléchargée",
	} {
		if got := strings.Join(m.prunePreview(p).warn, " "); !strings.Contains(got, want) {
			t.Errorf("%s : avertissement %q, want %q", p.CLI(), got, want)
		}
	}
	if got := m.prunePreview(compose.PruneBuilder).sizeText(); got != "?" {
		t.Errorf("builder prune : taille %q, want « ? » (imprévisible)", got)
	}
	if got := m.prunePreview(compose.PruneImages).what; got != "Images pendantes : 2" {
		t.Errorf("image prune : %q", got)
	}
}

// ↩ sur une commande : confirmation, exécution, retour sur la page.
func TestCleanupPruneFlow(t *testing.T) {
	m, fake := cleanupModel(t)
	m = focusPrune(t, m, compose.PruneImagesAll)
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewConfirm || !strings.HasPrefix(m.confirmLabel, "docker image prune -a — ") ||
		!strings.Contains(m.confirmWarning, "retéléchargée") {
		t.Fatalf("confirmation : vue %v %q %q", m.view, m.confirmLabel, m.confirmWarning)
	}
	m = drive(t, m, keyMsg("y"), 0)
	if m.view != viewProgress || !m.progressDone || len(fake.pruned) != 1 || fake.pruned[0] != compose.PruneImagesAll {
		t.Fatalf("exécution : vue %v, fini %v, commandes %v", m.view, m.progressDone, fake.pruned)
	}
	if len(fake.cleaned) != 0 {
		t.Errorf("la sélection a été supprimée au lieu de la commande : %v", fake.cleaned)
	}
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewCleanup || !strings.Contains(ansi.Strip(m.viewContent()), "✔ Libéré") {
		t.Errorf("retour sur la page : vue %v", m.view)
	}
}

// Commande sans taille mesurable : pas de « — — » dans le titre.
func TestCleanupPruneLabelWithoutSize(t *testing.T) {
	m, _ := cleanupModel(t)
	m = focusPrune(t, m, compose.PruneContainers)
	m = drive(t, m, keyMsg("enter"), 0)
	if m.confirmLabel != "docker container prune" {
		t.Errorf("titre %q", m.confirmLabel)
	}
}

// Fenêtre : jamais plus de h lignes, la ligne du curseur visible, et les
// indicateurs comptent exactement ce qui est caché.
func TestWindowWithMarks(t *testing.T) {
	for n := 1; n <= 30; n++ {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprint("L", i)
		}
		for h := 3; h <= 12; h++ {
			for cur := 0; cur < n; cur++ {
				out := windowWithMarks(lines, cur, h)
				if len(out) > max(h, 3) {
					t.Fatalf("n=%d h=%d cur=%d : %d lignes", n, h, cur, len(out))
				}
				shown, hidden := 0, 0
				found := false
				for _, l := range out {
					l = ansi.Strip(l)
					var k int
					if _, err := fmt.Sscanf(strings.TrimSpace(l), "··· %d", &k); err == nil {
						hidden += k
						continue
					}
					shown++
					if l == fmt.Sprint("L", cur) {
						found = true
					}
				}
				if !found || shown+hidden != n {
					t.Fatalf("n=%d h=%d cur=%d : curseur visible=%v, %d affichées + %d cachées\n%v", n, h, cur, found, shown, hidden, out)
				}
			}
		}
	}
}

// Toutes les lignes de catégorie et de commande finissent leur taille sur
// la même colonne.
func TestCleanupSizesAligned(t *testing.T) {
	m, _ := cleanupModel(t)
	m.width, m.height = 110, 80
	end := -1
	for _, l := range strings.Split(ansi.Strip(m.viewContent()), "\n") {
		t2 := strings.TrimRight(l, " ")
		if !(strings.Contains(t2, " ▸ ") || strings.Contains(t2, "] Cache de build") || strings.Contains(t2, "docker ")) || strings.Contains(t2, "Commandes prune") {
			continue
		}
		if w := ansi.StringWidth(t2); end < 0 {
			end = w
		} else if w != end {
			t.Errorf("taille non alignée (%d au lieu de %d) : %q", w, end, t2)
		}
	}
	if end < 0 {
		t.Fatal("aucune ligne vérifiée")
	}
}

// Écran large (≥ 140 colonnes) : bilan à droite, toutes les commandes prune
// visibles ; écran bas : « ··· N en dessous » signale la suite.
func TestCleanupLayouts(t *testing.T) {
	m, _ := cleanupModel(t)
	m.width, m.height = 150, 40
	s := ansi.Strip(m.viewContent())
	if !strings.Contains(lineWith(s, "Sélection"), "│") {
		t.Errorf("large : pas de seconde colonne\n%s", s)
	}
	for _, want := range []string{"Libérable", "Conservé", "docker volume prune", "docker container prune"} {
		if !strings.Contains(s, want) {
			t.Errorf("large : %q absent\n%s", want, s)
		}
	}
	for i, l := range strings.Split(m.viewContent(), "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("large : ligne %d de %d colonnes", i, w)
		}
	}
	m.width, m.height = 100, 30
	if s := ansi.Strip(m.viewContent()); !strings.Contains(s, "en dessous") || strings.Contains(lineWith(s, "Sélection"), "│") {
		t.Errorf("étroit et bas : indicateur absent ou disposition large\n%s", s)
	}
}

// lineWith : première ligne de s qui contient sub.
func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// Le panneau de droite (écran large) décrit la ligne sous le curseur.
func TestCleanupDetailPanel(t *testing.T) {
	m, _ := cleanupModel(t)
	m.width, m.height = 160, 50
	screen := func() string { return ansi.Strip(m.viewContent()) }

	// Commande : ce qu'elle supprimerait, conteneurs nommés, avertissement.
	m = focusPrune(t, m, compose.PruneContainers)
	s := screen()
	for _, want := range []string{"Conteneurs arrêtés : 2", "immich-ml-1", "stack immich", "! Supprime les conteneurs arrêtés de immich"} {
		if !strings.Contains(s, want) {
			t.Errorf("container prune : %q absent\n%s", want, s)
		}
	}
	m = focusPrune(t, m, compose.PruneNetworks)
	if s := screen(); !strings.Contains(s, "traefik_lan (externe de traefik)") || !strings.Contains(s, "demo_default") {
		t.Errorf("network prune : réseaux non listés\n%s", s)
	}

	// Catégorie : explication et plus gros éléments.
	m.cleanupCursor = 4 // Images de stacks arrêtées
	if r, _ := m.cleanupCurrent(); r.kind != compose.CleanStackImage || r.item >= 0 {
		t.Fatalf("curseur sur %+v", r)
	}
	s = screen()
	if !strings.Contains(s, "immich-machine-learning") || strings.Contains(s, "devra les retélécharger") {
		t.Errorf("catégorie : détail absent\n%s", s)
	}

	// Élément déplié : identifiant, taille, stack.
	m = drive(t, m, keyMsg("right"), 0)
	m = drive(t, m, keyMsg("down"), 0)
	s = screen()
	if !strings.Contains(s, "Stack") || !strings.Contains(s, "Libéré seul") {
		t.Errorf("élément : détail absent\n%s", s)
	}

	for i, l := range strings.Split(m.viewContent(), "\n") {
		if w := ansi.StringWidth(l); w > m.width {
			t.Errorf("ligne %d de %d colonnes", i, w)
		}
	}
}

// Catégorie dépliée : le panneau de droite ne répète pas la liste déjà
// visible à gauche.
func TestCleanupDetailNoDuplicate(t *testing.T) {
	m, _ := cleanupModel(t)
	m.width, m.height = 160, 50
	m.cleanupCursor = 3 // Images inutilisées
	right := func() string {
		var out []string
		for _, l := range strings.Split(ansi.Strip(m.viewContent()), "\n") {
			if _, r, ok := strings.Cut(l, "│"); ok {
				out = append(out, r)
			}
		}
		return strings.Join(out, "\n")
	}
	if !strings.Contains(right(), "postgres:13") {
		t.Fatalf("repliée : aperçu attendu à droite\n%s", right())
	}
	m = drive(t, m, keyMsg("right"), 0)
	if strings.Contains(right(), "postgres:13") {
		t.Errorf("dépliée : la liste est répétée à droite\n%s", right())
	}
}
