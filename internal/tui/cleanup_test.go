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
	if m.view != viewCleanup || !strings.HasPrefix(m.status, "Rien de coché") || m.statusKind != statusInfo {
		t.Errorf("sélection vide : vue %v, statut %q (%v), want un rappel", m.view, m.status, m.statusKind)
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
// indicateurs comptent exactement ce qui est caché. Sous 3 lignes, pas de
// repère et pas de plancher : 3 lignes rendues là où 2 tenaient, le bas de
// l'écran coupait justement « ··· N en dessous ».
func TestWindowWithMarks(t *testing.T) {
	for n := 1; n <= 30; n++ {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprint("L", i)
		}
		for h := 1; h <= 12; h++ {
			for cur := 0; cur < n; cur++ {
				out := windowWithMarks(lines, cur, h)
				if len(out) > h {
					t.Fatalf("n=%d h=%d cur=%d : %d lignes", n, h, cur, len(out))
				}
				shown, hidden, marks := 0, 0, 0
				found := false
				for _, l := range out {
					l = ansi.Strip(l)
					var k int
					if _, err := fmt.Sscanf(strings.TrimSpace(l), "··· %d", &k); err == nil {
						hidden += k
						marks++
						continue
					}
					shown++
					if l == fmt.Sprint("L", cur) {
						found = true
					}
				}
				if h < 3 {
					if !found || marks > 0 || shown != min(h, n) {
						t.Fatalf("n=%d h=%d cur=%d : curseur visible=%v, %d repères, %d lignes\n%v", n, h, cur, found, marks, shown, out)
					}
					continue
				}
				if !found || shown+hidden != n {
					t.Fatalf("n=%d h=%d cur=%d : curseur visible=%v, %d affichées + %d cachées\n%v", n, h, cur, found, shown, hidden, out)
				}
			}
		}
	}
}

// cleanupBand : lignes de la bande de détail (après le dernier séparateur,
// avant le pied de page) d'un écran de la page en disposition étroite.
func cleanupBand(screen string) []string {
	lines := strings.Split(screen, "\n")
	last := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "────") {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	var out []string
	for _, l := range lines[last+1 : len(lines)-1] {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

// Sous 140 colonnes, une bande sous la sélection détaille la ligne du
// curseur : sans elle, « ! docker network prune » ne disait qu'à la
// confirmation que le réseau externe d'une stack serait supprimé. Les
// avertissements passent avant le reste, coupés s'il le faut.
func TestCleanupNarrowBand(t *testing.T) {
	m, _ := cleanupModel(t)
	for _, size := range [][2]int{{80, 24}, {120, 36}} {
		m.width, m.height = size[0], size[1]
		m = focusPrune(t, m, compose.PruneNetworks)
		screen := ansi.Strip(m.viewContent())
		band := cleanupBand(screen)
		if len(band) < 2 || len(band) > cleanupBandLines ||
			band[0] != "docker network prune · demo_default, traefik_lan (externe de traefik)" ||
			!strings.HasPrefix(band[1], "! Supprime aussi le réseau externe traefik_lan de traefik") {
			t.Errorf("%dx%d : bande %q\n%s", size[0], size[1], band, screen)
		}
		for i, l := range strings.Split(m.viewContent(), "\n") {
			if w := ansi.StringWidth(l); w > m.width {
				t.Errorf("%dx%d : ligne %d de %d colonnes", size[0], size[1], i, w)
			}
		}
	}

	// Deux avertissements en 80 colonnes : un par ligne, coupés.
	m.width, m.height = 80, 24
	m = focusPrune(t, m, compose.PruneSystem)
	band := cleanupBand(ansi.Strip(m.viewContent()))
	if len(band) != 3 || !strings.Contains(band[0], "2 conteneurs + 2 réseaux + 2 images + cache pendant") ||
		!strings.HasPrefix(band[1], "! Supprime les conteneurs arrêtés de immich") || !strings.HasPrefix(band[2], "! Supprime aussi le réseau externe") {
		t.Errorf("system prune : bande %q", band)
	}

	// Catégorie repliée : ses éléments et leur taille ; élément : ses
	// propriétés.
	m.width, m.height = 100, 30
	m.cleanupCursor = 4 // Images de stacks arrêtées
	band = cleanupBand(ansi.Strip(m.viewContent()))
	if len(band) < 2 || !strings.HasPrefix(band[0], "Images de stacks arrêtées · ≈ 23.0 Go") || !strings.Contains(band[1], "immich-machine-learning") {
		t.Errorf("catégorie : bande %q", band)
	}
	m = drive(t, m, keyMsg("right"), 0)
	m = drive(t, m, keyMsg("down"), 0)
	band = cleanupBand(ansi.Strip(m.viewContent()))
	if len(band) < 2 || !strings.Contains(band[1], "Libéré seul 23.0 Go") || !strings.Contains(band[1], "Stack immich") {
		t.Errorf("élément : bande %q", band)
	}

	// Écran large : le détail est à droite, pas de bande.
	m.width, m.height = 160, 40
	if strings.Contains(ansi.Strip(m.viewContent()), "Libéré seul 23.0 Go · ") {
		t.Error("large : bande affichée en plus du panneau de droite")
	}
}

// Trop d'avertissements pour la bande : la dernière ligne renvoie à la
// confirmation plutôt que d'en taire.
func TestBriefWarningsOverflow(t *testing.T) {
	got := briefWarnings([]string{"un", "deux", "trois"}, 60, 2)
	if len(got) != 2 || got[0] != "! un" || got[1] != "! +2 autres avertissements (↩ pour les lire)" {
		t.Errorf("trop d'avertissements : %q", got)
	}
	long := strings.Repeat("mot ", 20)
	if got := briefWarnings([]string{long}, 40, 3); len(got) != 3 || !strings.HasPrefix(got[0], "! mot") {
		t.Errorf("un long avertissement replié : %q", got)
	}
	if got := briefWarnings([]string{long, "court"}, 40, 2); len(got) != 2 || !strings.HasSuffix(got[0], "…") || got[1] != "! court" {
		t.Errorf("replié trop long : un par ligne, coupés : %q", got)
	}
}

// En 80x24, le bilan complet (8 lignes) ne laissait que quelques lignes à
// la sélection : il passe sur 2 lignes (disque, puis Libérable par type, du
// plus gros au plus petit) quand la sélection en aurait moins de 8, et
// reste complet, parité avec docker system df, quand la place le permet.
func TestCleanupCompactBilan(t *testing.T) {
	m, _ := cleanupModel(t)
	m.metrics = narrowMetrics(8)
	m.width, m.height = 80, 24
	s := ansi.Strip(m.viewContent())
	if !strings.Contains(s, "Libérable ≈ 39.0 Go : images ≈ 22.0 Go · cache 16.0 Go · volumes 1.0 Go") ||
		!strings.Contains(s, "162 / 194 Go (88 %)") || strings.Contains(s, "Nombre") || strings.Contains(s, "* volumes") {
		t.Errorf("80x24 : bilan compact attendu\n%s", s)
	}
	if l := m.cleanupNarrowLayout(m.bodyHeight()); !l.compact || l.listH < 8 {
		t.Errorf("80x24 : %+v, want compact et au moins 8 lignes de sélection", l)
	}
	m.width, m.height = 120, 36
	s = ansi.Strip(m.viewContent())
	if !strings.Contains(s, "Nombre") || !strings.Contains(s, "* volumes des stacks arrêtées inclus") {
		t.Errorf("120x36 : bilan complet attendu\n%s", s)
	}
}

// Sur un écran bas, la page ne dépasse jamais sa hauteur : sous 3 lignes de
// sélection, le séparateur puis la bande cèdent leur place. Le repère
// « ··· N en dessous » reste visible dès que la sélection a 3 lignes, et
// pgup/pgdown (cleanupListH) suivent la même mise en page.
func TestCleanupNarrowFitsHeight(t *testing.T) {
	m, _ := cleanupModel(t)
	m.metrics = narrowMetrics(8)
	m.width = 80
	rows := len(m.cleanupRows())
	for body := 5; body <= 40; body++ {
		m.height = body + m.headerLines() + 1
		if m.bodyHeight() != body {
			t.Fatalf("hauteur utile %d, want %d", m.bodyHeight(), body)
		}
		out := ansi.Strip(renderCleanup(m, body))
		l := m.cleanupNarrowLayout(body)
		if n := strings.Count(out, "\n") + 1; n > body {
			t.Errorf("hauteur %d : %d lignes rendues (%+v)\n%s", body, n, l, out)
		}
		if got := m.cleanupListH(); got != l.listH {
			t.Errorf("hauteur %d : cleanupListH %d, want %d", body, got, l.listH)
		}
		if l.listH >= 3 && l.listH < rows && !strings.Contains(out, "en dessous") {
			t.Errorf("hauteur %d : repère du bas absent\n%s", body, out)
		}
		if l.sep && !l.band {
			t.Errorf("hauteur %d : la bande a cédé avant le séparateur", body)
		}
	}
}

// Le titre de section de la ligne du curseur reste en tête quand la
// fenêtre l'a fait sortir par le haut : la page est classée par risque, et
// un volume orphelin sans « Données » au-dessus ne disait plus qu'il efface
// des données. Le titre n'apparaît jamais deux fois et la liste ne
// s'allonge pas.
func TestCleanupSectionTitlePinned(t *testing.T) {
	m, _ := cleanupModel(t)
	rep := sampleCleanup()
	for i := range 20 {
		rep.Items = append(rep.Items, compose.CleanupItem{Kind: compose.CleanOrphanVolume, ID: fmt.Sprintf("vol%02d", i),
			Name: fmt.Sprintf("ancien_%02d_data", i), Size: 1 << 20, Count: 1, Stacks: []string{"ancien"}})
	}
	m = drive(t, m, cleanupLoadedMsg{seq: m.cleanupSeq, rep: rep}, 0)
	m.cleanupOpen[compose.CleanOrphanVolume] = true
	rows, nav := m.cleanupRows(), m.cleanupNav()
	for c := range nav {
		m.cleanupCursor = c
		section := ""
		for _, r := range rows[:nav[c]] {
			if r.section != "" {
				section = r.section
			}
		}
		for _, h := range []int{4, 6, 9, 14} {
			out := m.cleanupList(80, h, true)
			if len(out) > h {
				t.Fatalf("curseur %d, h=%d : %d lignes", c, h, len(out))
			}
			n := 0
			for _, l := range out {
				if strings.TrimSpace(ansi.Strip(l)) == section {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("curseur %d, h=%d : titre %q affiché %d fois\n%s", c, h, section, n, ansi.Strip(strings.Join(out, "\n")))
			}
		}
	}
	// Au milieu des volumes : le titre, puis le repère du haut.
	m.cleanupCursor = 15
	out := m.cleanupList(80, 8, true)
	if got := ansi.Strip(out[0]); strings.TrimSpace(got) != "Données" || !strings.Contains(ansi.Strip(out[1]), "au-dessus") {
		t.Errorf("titre épinglé absent :\n%s", ansi.Strip(strings.Join(out, "\n")))
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

// Le bilan au retour sur la page dit l'issue réelle : il annonçait « ✔
// Libéré » en vert même après un refus du démon ou une annulation, et « — »
// pour zéro, qui se lisait « inconnu ».
func TestCleanupResultText(t *testing.T) {
	gb := int64(1 << 30)
	for _, tc := range []struct {
		o       cleanupOutcome
		freed   int64
		planned int64
		want    string
		tone    tone
	}{
		{cleanupOutcome{}, 19 * gb, 19 * gb, "✔ Libéré 19.0 Go · prévu ≈ 19.0 Go", toneOK},
		{cleanupOutcome{}, 0, 0, "✔ Libéré rien", toneOK},
		{cleanupOutcome{fails: 3}, 3 * gb, 5 * gb, "⚠ 3 éléments en échec · libéré 3.0 Go", toneWarn},
		{cleanupOutcome{fails: 1}, 0, 5 * gb, "✕ Rien libéré : 1 échec", toneErr},
		{cleanupOutcome{fails: 1, ok: 3}, 0, 5 * gb, "⚠ 1 échec · 3 supprimés · libéré rien", toneWarn},
		{cleanupOutcome{cancelled: true}, 2 * gb, 5 * gb, "⊘ Annulé · libéré 2.0 Go", toneWarn},
		{cleanupOutcome{cancelled: true, fails: 2}, 0, 5 * gb, "⊘ Annulé · 2 échecs · libéré rien", toneWarn},
	} {
		got, tn := cleanupResultText(tc.o, tc.freed, tc.planned, true)
		if got != tc.want || tn != tc.tone {
			t.Errorf("%+v, libéré %d : %q (%v), want %q (%v)", tc.o, tc.freed, got, tn, tc.want, tc.tone)
		}
	}
}

// Un nettoyage en échec : en quittant la progression, l'issue est relevée et
// la page réanalysée l'annonce en rouge. L'ancienneté de l'analyse est en
// toutes lettres. Un élément arrêté par l'annulation n'est pas un échec,
// mais dit que l'annulation a pris effet.
func TestCleanupResultAfterFailure(t *testing.T) {
	ps := newProgressState()
	for _, ev := range []compose.ProgressEvent{
		{StackName: "Images pendantes <none>", Container: "<none> d1", Status: "conflict: unable to delete d1 (must be forced)", State: "error"},
		{StackName: "Images pendantes <none>", Container: "<none> d2", Status: "context canceled", State: "error"},
		{StackName: "Images pendantes <none>", StackDone: true, Err: errors.New("conflict: unable to delete d1 (must be forced)")},
	} {
		ps.apply(ev)
	}
	if o := ps.cleanupOutcome(); o != (cleanupOutcome{fails: 1, cancelled: true}) {
		t.Errorf("issue relevée : %+v", o)
	}

	m, _ := cleanupModel(t)
	before := *m.cleanupRep
	m.cleanupBefore, m.cleanupPlanned = &before, 2<<30
	m.view, m.progressReturn, m.progress, m.progressDone = viewProgress, viewCleanup, newProgressState(), true
	for _, ev := range []compose.ProgressEvent{
		{StackName: "Images pendantes <none>", Container: "<none> d1", Status: "conflict: unable to delete d1 (must be forced)", State: "error"},
		{StackName: "Images pendantes <none>", StackDone: true, Err: errors.New("conflict: unable to delete d1 (must be forced)")},
	} {
		m.progress.apply(ev)
	}
	m = drive(t, m, keyMsg("enter"), 0)
	if m.view != viewCleanup || m.cleanupRep == nil {
		t.Fatalf("retour : vue %v", m.view)
	}
	s := ansi.Strip(m.viewContent())
	if !strings.Contains(s, "✕ Rien libéré : 1 échec") || m.cleanupTone != toneErr {
		t.Errorf("bilan d'échec absent (%v) :\n%s", m.cleanupTone, s)
	}
	if !strings.Contains(s, "analysé à l'instant") {
		t.Errorf("ancienneté de l'analyse :\n%s", s)
	}
	if m.cleanupOutcome != (cleanupOutcome{}) {
		t.Errorf("issue non consommée : %+v", m.cleanupOutcome)
	}
}

// Un nettoyage dont les réseaux sont partis mais dont un volume est refusé
// n'est pas un échec total : jaune, avec ce qui a abouti (un réseau ne libère
// aucun octet, donc « libéré rien » ne dit pas « rien fait »).
func TestCleanupPartialFailureNotShownAsTotal(t *testing.T) {
	ps := newProgressState()
	net, vol := compose.CleanUnusedNetwork.Label(), compose.CleanOrphanVolume.Label()
	for _, ev := range []compose.ProgressEvent{
		{StackName: net, StackQueued: true}, {StackName: vol, StackQueued: true},
		{StackName: net, StackStarted: true},
		{StackName: net, Container: "a_default", Status: "Supprimé", State: "done"},
		{StackName: net, Container: "b_default", Status: "Supprimé", State: "done"},
		{StackName: net, Container: "c_default", Status: "Supprimé", State: "done"},
		{StackName: net, StackDone: true},
		{StackName: vol, StackStarted: true},
		{StackName: vol, Container: "v3_data", Status: "remove v3_data: volume is in use", State: "error"},
		{StackName: vol, StackDone: true, Err: fmt.Errorf("remove v3_data: volume is in use")},
	} {
		ps.apply(ev)
	}
	text, tn := cleanupResultText(ps.cleanupOutcome(), 0, 70<<20, false)
	if tn != toneWarn || strings.Contains(text, "Rien libéré") || !strings.Contains(text, "3 supprimés") {
		t.Errorf("bilan %q (ton %v) : attendu un échec partiel avec les 3 supprimés", text, tn)
	}
}

// Le bilan d'un nettoyage ne survit pas à une visite écourtée : quitter la
// page avant le résultat de l'analyse qui suit le nettoyage l'écarte, et la
// visite suivante n'affiche pas l'issue de l'ancien nettoyage.
func TestCleanupOutcomeDroppedWhenLeavingBeforeAnalysis(t *testing.T) {
	m, _ := cleanupModel(t)
	before := *m.cleanupRep
	m.cleanupBefore, m.cleanupPlanned = &before, 2<<30
	m.view, m.progressReturn, m.progress, m.progressDone = viewProgress, viewCleanup, newProgressState(), true
	for _, ev := range []compose.ProgressEvent{
		{StackName: "Images pendantes <none>", Container: "<none> d1", Status: "conflict: unable to delete d1", State: "error"},
		{StackName: "Images pendantes <none>", StackDone: true, Err: fmt.Errorf("conflict: unable to delete d1")},
	} {
		m.progress.apply(ev)
	}
	m, _ = update(m, keyMsg("enter"))
	m, _ = update(m, keyMsg("esc"))
	if m.view != viewList {
		t.Fatalf("esc : vue %v", m.view)
	}
	m = drive(t, m, keyMsg("n"), 0)
	if s := ansi.Strip(m.viewContent()); strings.Contains(s, "Rien libéré") || strings.Contains(s, "échec") {
		t.Errorf("nouvelle visite : bilan d'un nettoyage précédent affiché (%q)", m.cleanupResult)
	}
}

// Bilan compact (80x24) : les volumes des stacks arrêtées sont comptés dans
// « Libérable » mais jamais proposés ; l'astérisque le signale, comme la note
// du bilan complet.
func TestCleanupCompactBilanMarksKeptVolumes(t *testing.T) {
	m, _ := cleanupModel(t)
	m.metrics = narrowMetrics(8)
	m.width, m.height = 80, 24
	var line string
	for _, l := range strings.Split(ansi.Strip(m.viewContent()), "\n") {
		if strings.Contains(l, "Libérable") {
			line = l
		}
	}
	if !strings.Contains(line, "volumes") || !strings.Contains(line, "Go*") {
		t.Errorf("ligne Libérable compacte sans astérisque sur les volumes : %q", line)
	}
}
