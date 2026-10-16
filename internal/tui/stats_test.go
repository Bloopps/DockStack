package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/charmbracelet/x/ansi"
)

// usageStack : une stack de n services, un conteneur chacun (ID « <nom>-<i> »).
func usageStack(name string, n int) compose.Stack {
	st := compose.Stack{Name: name, Dir: "/srv/" + name, Running: n, Total: n, Project: name}
	for i := 0; i < n; i++ {
		st.Services = append(st.Services, compose.ServiceStatus{
			Name: fmt.Sprintf("svc-%d", i), Running: 1, Total: 1, IDs: []string{fmt.Sprintf("%s-%d", name, i)},
			Image: "ghcr.io/immich-app/immich-server:release", Ports: "2283→2283, 8443→443",
		})
	}
	return st
}

func usageOf(st compose.Stack, cpu float64, mem uint64) map[string]compose.ContainerUsage {
	out := map[string]compose.ContainerUsage{}
	for _, id := range stackContainerIDs(st) {
		out[id] = compose.ContainerUsage{CPU: cpu, CPUOK: true, MemBytes: mem}
	}
	return out
}

func TestUsageCells(t *testing.T) {
	ids := []string{"a", "b", "zz"}
	usage := map[string]compose.ContainerUsage{
		"a": {CPU: 1.5, CPUOK: true, MemBytes: 100 << 20},
		"b": {CPU: 2.0, CPUOK: true, MemBytes: 300 << 20},
	}
	if cpu, mem := usageCells(ids, usage); cpu != "3.5%" || mem != "400 Mo" {
		t.Errorf("somme : %q %q", cpu, mem)
	}
	if cpu, mem := usageCells(ids, nil); cpu != "…" || mem != "…" {
		t.Errorf("rien de mesuré : %q %q", cpu, mem)
	}
	// Première lecture : la mémoire est là, pas le CPU (écart entre deux lectures).
	usage["b"] = compose.ContainerUsage{MemBytes: 300 << 20}
	if cpu, mem := usageCells(ids, usage); cpu != "…" || mem != "400 Mo" {
		t.Errorf("CPU inconnu : %q %q", cpu, mem)
	}
	if got := fmtCPU(250); got != "250%" {
		t.Errorf("fmtCPU(250) = %q", got)
	}
}

// La ligne d'une stack en marche porte CPU et RAM, alignés d'une ligne à l'autre ;
// une stack arrêtée n'en a pas ; un écran étroit les abandonne avant de déborder.
func TestRenderStackLineUsage(t *testing.T) {
	a := usageStack("beszel", 2)
	b := usageStack("immich", 4)
	lineA := ansi.Strip(renderStackLine(a, false, false, false, 100, false, 10, rowUsage{cpu: "0.0%", mem: "31 Mo", show: true}))
	lineB := ansi.Strip(renderStackLine(b, false, false, false, 100, false, 10, rowUsage{cpu: "12.5%", mem: "1.2 Go", show: true}))
	for _, want := range []string{"0.0%", "31 Mo", "en marche"} {
		if !strings.Contains(lineA, want) {
			t.Errorf("%q absent de %q", want, lineA)
		}
	}
	// Alignement à droite : les deux lignes finissent à la même colonne.
	if ansi.StringWidth(lineA) != ansi.StringWidth(lineB) {
		t.Errorf("colonnes désalignées :\n%q\n%q", lineA, lineB)
	}
	// En cours de mesure.
	if got := ansi.Strip(renderStackLine(a, false, false, false, 100, false, 10, rowUsage{cpu: "…", mem: "…", show: true})); !strings.Contains(got, "…") {
		t.Errorf("« … » attendu : %q", got)
	}
	// Stack arrêtée : pas de colonnes.
	off := compose.Stack{Name: "off", Total: 2}
	if got := ansi.Strip(renderStackLine(off, false, false, false, 100, false, 10, rowUsage{})); strings.Contains(got, "Mo") || strings.Contains(got, "%") {
		t.Errorf("stack arrêtée : %q", got)
	}
	// Jamais plus large que l'écran, curseur ou non, et l'état est gardé tant
	// que possible : CPU/RAM partent en premier.
	for _, w := range []int{20, 30, 40, 50, 60, 80} {
		for _, cur := range []bool{false, true} {
			line := renderStackLine(b, cur, false, false, w, false, 10, rowUsage{cpu: "12.5%", mem: "1.2 Go", show: true})
			if got := ansi.StringWidth(line); got > w {
				t.Errorf("w=%d cur=%v : %d colonnes : %q", w, cur, got, ansi.Strip(line))
			}
		}
	}
	if got := ansi.Strip(renderStackLine(b, false, false, false, 40, false, 10, rowUsage{cpu: "12.5%", mem: "1.2 Go", show: true})); strings.Contains(got, "Go") || !strings.Contains(got, "en marche") {
		t.Errorf("à 40 colonnes : CPU/RAM abandonnés, état gardé : %q", got)
	}
}

// Le volet n'est plus chargé de CPU/RAM : image, ports, volumes et le rappel de tab.
func TestPreviewDetailLight(t *testing.T) {
	st := usageStack("immich", 3)
	ex := previewExtra{volumes: 5 << 30, layers: 459 << 20, usage: usageOf(st, 3.2, 412<<20)}
	out := ansi.Strip(strings.Join(previewDetail("/srv", st, st.Services, 60, 40, selState{}, ex), "\n"))
	for _, want := range []string{"Disque 5.4 Go (volumes 5.0 Go + couches 459 Mo)", "image ", "ports ", "2283→2283", "tab", "plein écran"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q absent :\n%s", want, out)
		}
	}
	for _, not := range []string{"CPU", "RAM", "412 Mo"} {
		if strings.Contains(out, not) {
			t.Errorf("%q n'a plus sa place dans le volet :\n%s", not, out)
		}
	}
}

// Quelle que soit la hauteur, le volet ne déborde pas, et le détail
// (image/ports) se dégrade avant que des services soient masqués.
func TestPreviewDetailFitsEveryHeight(t *testing.T) {
	for _, n := range []int{1, 3, 5, 12} {
		st := usageStack("immich", n)
		for _, w := range []int{40, 60, 80} {
			for h := 10; h <= 45; h++ {
				for _, l := range previewDetail("/srv", st, st.Services, w, h, selState{}, previewExtra{volumes: 1 << 30}) {
					if got := ansi.StringWidth(l); got > w {
						t.Fatalf("n=%d w=%d h=%d : ligne de %d colonnes : %q", n, w, h, got, ansi.Strip(l))
					}
				}
			}
		}
	}
	st := usageStack("immich", 3)
	out := ansi.Strip(strings.Join(previewDetail("/srv", st, st.Services, 60, 30, selState{}, previewExtra{}), "\n"))
	if strings.Count(out, "ports ") != 3 {
		t.Errorf("ports attendus pour 3 services :\n%s", out)
	}
	st = usageStack("immich", 12)
	if lines := previewDetail("/srv", st, st.Services, 60, 25, selState{}, previewExtra{}); len(lines) > 25 {
		t.Errorf("%d lignes pour une hauteur de 25", len(lines))
	}
}

// On ne lit que les conteneurs proches de l'écran, pas ceux de toute la liste.
func TestVisibleContainerIDsWindow(t *testing.T) {
	var stacks []compose.Stack
	for i := 0; i < 300; i++ {
		stacks = append(stacks, usageStack(fmt.Sprintf("s%03d", i), 1))
	}
	m := newTestModel(stacks)
	m.client = &fakeClient{}
	m.width, m.height = 160, 40
	ids := m.visibleContainerIDs()
	if len(ids) == 0 || len(ids) > 2*m.height+1 {
		t.Fatalf("%d conteneurs lus pour 300 stacks (écran de 40 lignes)", len(ids))
	}
	m.cursor = 150
	mid := m.visibleContainerIDs()
	if len(mid) == 0 || mid[0] == ids[0] {
		t.Errorf("la fenêtre devrait suivre le curseur : %v…", mid[:1])
	}
	// Stack arrêtée : rien à lire. Autre vue que la liste : rien non plus.
	m2 := newTestModel([]compose.Stack{{Name: "off", Total: 1}})
	m2.client = &fakeClient{}
	if got := m2.visibleContainerIDs(); len(got) != 0 {
		t.Errorf("stack arrêtée lue : %v", got)
	}
	m.view = viewBackup
	if got := m.visibleContainerIDs(); got != nil {
		t.Errorf("hors liste : %v", got)
	}
}

func TestStatsTickGating(t *testing.T) {
	m := newTestModel([]compose.Stack{usageStack("beszel", 2)})
	m.client = &fakeClient{}
	m.width, m.height = 160, 40
	m, cmd := m.onStatsTick()
	if !m.statsBusy || cmd == nil {
		t.Fatal("la lecture aurait dû démarrer")
	}
	m, _ = m.onStatsTick()
	if !m.statsBusy {
		t.Fatal("pas de seconde lecture pendant la première")
	}
	want := map[string]compose.ContainerUsage{"beszel-0": {MemBytes: 1 << 20}}
	m = m.onStatsResult(statsMsg{usage: want})
	if m.statsBusy || m.ctrUsage["beszel-0"].MemBytes != 1<<20 {
		t.Fatalf("résultat non pris en compte : %+v", m.ctrUsage)
	}
	// Une erreur garde les dernières valeurs au lieu de tout effacer.
	m = m.onStatsResult(statsMsg{err: fmt.Errorf("boom")})
	if m.ctrUsage["beszel-0"].MemBytes != 1<<20 {
		t.Fatal("les dernières valeurs devraient survivre à une erreur")
	}
}

// Écran complet : CPU/RAM sur la ligne de chaque stack en marche.
func TestListShowsUsageOnRows(t *testing.T) {
	a, b := usageStack("beszel", 2), usageStack("immich", 4)
	m := newTestModel([]compose.Stack{a, b})
	m.client = &fakeClient{}
	m.loading = false
	m.width, m.height = 160, 40
	m.ctrUsage = usageOf(a, 0.5, 20<<20)
	out := ansi.Strip(m.View().Content)
	if !strings.Contains(out, "1.0%") || !strings.Contains(out, "40 Mo") {
		t.Errorf("CPU/RAM de beszel absents de la liste :\n%s", out)
	}
	if strings.Contains(out, "CPU 1.0%") {
		t.Errorf("le volet ne devrait plus porter le CPU :\n%s", out)
	}
}

// Disque d'une stack : volumes nommés + couches d'écriture, par projet ; « … »
// tant que la mesure n'a pas eu lieu, « — » quand il n'y a rien.
func TestDiskCell(t *testing.T) {
	st := usageStack("immich", 2)
	m := newTestModel([]compose.Stack{st})
	if got := m.diskCell(st); got != "…" {
		t.Errorf("mesure absente : %q", got)
	}
	m.usage = &compose.DockerUsage{VolumesOK: true,
		VolumesByProject: map[string]int64{"immich": 800 << 20},
		LayersByProject:  map[string]int64{"immich": 200 << 20}}
	if got := m.diskCell(st); got != "1000 Mo" {
		t.Errorf("volumes + couches : %q", got)
	}
	other := usageStack("beszel", 1)
	if got := m.diskCell(other); got != "—" {
		t.Errorf("aucun volume : %q", got)
	}
	m.usage.VolumesByProject["beszel"] = 100
	if got := m.diskCell(other); got != "<1 Mo" {
		t.Errorf("très petit : %q", got)
	}
	// Une stack arrêtée garde son disque, mais pas de CPU/RAM.
	off := compose.Stack{Name: "immich", Project: "immich", Total: 1}
	ru := m.rowUsageFor(off)
	if ru.show || ru.disk != "1000 Mo" {
		t.Errorf("stack arrêtée : %+v", ru)
	}
}

// Les colonnes CPU/RAM/Disque/Lect/Écr restent alignées d'une ligne à l'autre
// (une stack arrêtée n'a que le disque), sous leur en-tête ; elles partent dans
// l'ordre lecture/écriture, disque, CPU/RAM quand la place manque.
func TestRowColumnsAlignedWithHeader(t *testing.T) {
	run := usageStack("beszel-stack", 2) // 12 caractères : c'est la colonne du nom
	off := compose.Stack{Name: "old", Project: "old", Total: 1}
	ru := rowUsage{cpu: "0.4%", mem: "31 Mo", show: true, disk: "5.0 Go", rd: "12 Ko", wr: "1.2 Mo"}
	m := newTestModel([]compose.Stack{run})
	m.width, m.height = 200, 40
	m.listRows()
	nameW := m.rowsCache.nameW
	if nameW != 12 {
		t.Fatalf("colonne du nom = %d, veut 12", nameW)
	}

	// colEnd : colonne d'affichage où se termine la valeur dans la ligne.
	colEnd := func(line, val string) int {
		i := strings.LastIndex(line, val)
		if i < 0 {
			return -1
		}
		return ansi.StringWidth(line[:i]) + ansi.StringWidth(val)
	}
	hdr := ansi.Strip(m.listColumnHeader(120))
	row := ansi.Strip(renderStackLine(run, false, false, false, 120, false, nameW, ru))
	for label, val := range map[string]string{"CPU": "0.4%", "RAM": "31 Mo", "Disque": "5.0 Go", "Lect/s": "12 Ko", "Écr/s": "1.2 Mo"} {
		if h, r := colEnd(hdr, label), colEnd(row, val); h != r || h < 0 {
			t.Errorf("%s : en-tête finit en %d, valeur en %d\n%q\n%q", label, h, r, hdr, row)
		}
	}
	// Stack arrêtée : seulement le disque, dans la même colonne que les autres lignes.
	stopped := ansi.Strip(renderStackLine(off, false, false, false, 120, false, nameW, rowUsage{disk: "1.2 Go"}))
	if colEnd(stopped, "1.2 Go") != colEnd(row, "5.0 Go") {
		t.Errorf("disque désaligné :\n%q\n%q", stopped, row)
	}
	if strings.Contains(stopped, "Mo") || strings.Contains(stopped, "%") {
		t.Errorf("stack arrêtée : disque seul attendu : %q", stopped)
	}

	// Place qui manque : les colonnes partent dans l'ordre IO, disque, CPU/RAM.
	for _, tc := range []struct {
		width int
		want  []string
		not   []string
	}{
		{120, []string{"1.2 Mo", "5.0 Go", "31 Mo", "0.4%", "en marche"}, nil},
		{75, []string{"1.2 Mo", "5.0 Go", "31 Mo"}, nil},
		{74, []string{"5.0 Go", "31 Mo", "0.4%"}, []string{"1.2 Mo", "12 Ko"}},
		{58, []string{"31 Mo", "0.4%", "en marche"}, []string{"5.0 Go", "1.2 Mo"}},
		{50, []string{"en marche"}, []string{"31 Mo", "5.0 Go", "0.4%"}},
		{30, []string{"2/2"}, []string{"en marche", "31 Mo"}},
	} {
		line := renderStackLine(run, false, false, false, tc.width, false, nameW, ru)
		if got := ansi.StringWidth(line); got > tc.width {
			t.Errorf("w=%d : %d colonnes : %q", tc.width, got, ansi.Strip(line))
		}
		plain := ansi.Strip(line)
		for _, w := range tc.want {
			if !strings.Contains(plain, w) {
				t.Errorf("w=%d : %q absent de %q", tc.width, w, plain)
			}
		}
		for _, w := range tc.not {
			if strings.Contains(plain, w) {
				t.Errorf("w=%d : %q devrait avoir disparu de %q", tc.width, w, plain)
			}
		}
	}
	// En-tête : il suit les colonnes que les lignes peuvent afficher, et s'efface
	// là où elles n'existent plus.
	if got := ansi.Strip(m.listColumnHeader(74)); !strings.HasSuffix(got, "Disque") {
		t.Errorf("à 74 colonnes : en-tête sans les débits : %q", got)
	}
	if got := ansi.Strip(m.listColumnHeader(75)); !strings.HasSuffix(got, "Écr/s") {
		t.Errorf("à 75 colonnes : en-tête complet attendu : %q", got)
	}
	if got := m.listColumnHeader(30); got != "" {
		t.Errorf("écran étroit : %q", got)
	}
	// Curseur ou non, jamais plus large que l'écran.
	for w := 20; w <= 140; w += 5 {
		for _, cur := range []bool{false, true} {
			if got := ansi.StringWidth(renderStackLine(run, cur, false, false, w, false, nameW, ru)); got > w {
				t.Errorf("w=%d cur=%v : %d colonnes", w, cur, got)
			}
		}
	}
}

func TestIOCells(t *testing.T) {
	ids := []string{"a", "b"}
	usage := map[string]compose.ContainerUsage{
		"a": {ReadBps: 512, WriteBps: 3 << 20, IOOK: true},
		"b": {ReadBps: 1 << 10, WriteBps: 0, IOOK: true},
	}
	if rd, wr := ioCells(ids, usage); rd != "2 Ko" || wr != "3.0 Mo" {
		t.Errorf("somme : %q %q", rd, wr)
	}
	if rd, wr := ioCells(ids, nil); rd != "…" || wr != "…" {
		t.Errorf("rien de mesuré : %q %q", rd, wr)
	}
	usage["b"] = compose.ContainerUsage{} // première lecture : pas de débit
	if rd, _ := ioCells(ids, usage); rd != "…" {
		t.Errorf("débit inconnu : %q", rd)
	}
	for bps, want := range map[float64]string{0: "0", 10: "<1 Ko", 2048: "2 Ko", 5 << 20: "5.0 Mo", 3 << 30: "3.0 Go"} {
		if got := fmtIO(bps); got != want {
			t.Errorf("fmtIO(%v) = %q, veut %q", bps, got, want)
		}
	}
}

// Une référence d'image trop longue est coupée au milieu : le nom court et
// le tag restent lisibles (coupée par la fin, elle perdait le tag).
func TestTruncImage(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		w    int
		want string
	}{
		{"redis:6.2-alpine", 40, "redis:6.2-alpine"},
		{"ghcr.io/immich-app/immich-server:release", 38, "ghcr.io/…/immich-server:release"},
		{"ghcr.io/immich-app/immich-machine-learning:release", 38, "…/immich-machine-learning:release"},
		{"ghcr.io/immich-app/immich-machine-learning:release", 20, "…ne-learning:release"},
		{"tensorchord/pgvecto-rs:pg14-v0.2.0", 20, "…ecto-rs:pg14-v0.2.0"},
		{"tensorchord/pgvecto-rs:pg14-v0.2.0", 24, "…/pgvecto-rs:pg14-v0.2.0"},
		{"nextcloud:29-apache", 1, "…"},
		{"nextcloud:29-apache", 0, ""},
	} {
		got := truncImage(tc.ref, tc.w)
		if got != tc.want {
			t.Errorf("truncImage(%q, %d) = %q, want %q", tc.ref, tc.w, got, tc.want)
		}
		if w := ansi.StringWidth(got); w > max(tc.w, 0) {
			t.Errorf("truncImage(%q, %d) : %d colonnes", tc.ref, tc.w, w)
		}
	}
}

// La ligne disque du volet passe sur deux lignes plutôt que de perdre les
// couches quand elle ne tient pas (de 100 à 139 colonnes).
func TestStackUsageLinesWrap(t *testing.T) {
	ex := previewExtra{volumes: 18 << 30, layers: 300 << 20}
	if got := stackUsageLines(ex, 60); len(got) != 1 || ansi.Strip(got[0]) != "Disque 18.3 Go (volumes 18.0 Go + couches 300 Mo)" {
		t.Errorf("large : %q", got)
	}
	got := stackUsageLines(ex, 46)
	if len(got) != 2 || ansi.Strip(got[0]) != "Disque 18.3 Go" || ansi.Strip(got[1]) != "  volumes 18.0 Go · couches 300 Mo" {
		t.Errorf("étroit : %q", got)
	}
	if got := stackUsageLines(previewExtra{}, 46); got != nil {
		t.Errorf("sans disque : %q", got)
	}
}

// Les services s'accordent au masculin (« cron arrêté », et non « arrêtée »
// comme une stack) ; un service qui ne tourne pas dit son code de sortie et
// depuis quand, sans passer par le mode services.
func TestServiceBlockStateAndExitCode(t *testing.T) {
	svcs := []compose.ServiceStatus{
		{Name: "app", Running: 1, Total: 1, Image: "nextcloud:29-apache", Status: "Up 2 days"},
		{Name: "cron", Total: 1, Image: "nextcloud:29-apache", Status: "Exited (137) 2 hours ago"},
		{Name: "init", Status: ""},
	}
	out := ansi.Strip(strings.Join(renderServiceBlock(svcs, 46, 20), "\n"))
	for _, want := range []string{"cron  arrêté\n", "état  code 137 · il y a 2 h", "init  non créé"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q absent :\n%s", want, out)
		}
	}
	if strings.Contains(out, "arrêtée") || strings.Contains(out, "depuis 2 j") {
		t.Errorf("accord féminin, ou état d'un service en marche :\n%s", out)
	}

	var many []compose.ServiceStatus
	for i := range 6 {
		many = append(many, compose.ServiceStatus{Name: fmt.Sprintf("s%d", i), Total: 1})
	}
	if out := ansi.Strip(strings.Join(renderServiceBlock(many, 46, 4), "\n")); !strings.Contains(out, "… +4 autres services arrêtés") {
		t.Errorf("résumé des services masqués :\n%s", out)
	}
}

// Une image épinglée par digest garde son nom et son tag lisibles : le
// digest cède la place en premier, jamais le nom.
func TestTruncImageKeepsNameWithDigest(t *testing.T) {
	ref := "ghcr.io/immich-app/immich-server:v1.119.0@sha256:" + strings.Repeat("3f0a9c1b", 8)
	for _, w := range []int{30, 36, 46} {
		got := truncImage(ref, w)
		if !strings.Contains(got, "immich-server") || ansi.StringWidth(got) > w {
			t.Errorf("truncImage(…, %d) = %q : nom perdu ou trop large", w, got)
		}
	}
	st := stack("immich", "", 1, 1, 0)
	st.Services = []compose.ServiceStatus{{Name: "server", Running: 1, Total: 1, Image: ref, Status: "Up 2 hours"}}
	m := newTestModel([]compose.Stack{st})
	m.client = &fakeClient{}
	m.width, m.height, m.loading = 110, 36, false
	for _, l := range strings.Split(ansi.Strip(m.viewContent()), "\n") {
		if strings.Contains(l, "image") && !strings.Contains(l, "immich-server") {
			t.Errorf("volet : ligne d'image sans le nom :\n%s", l)
		}
	}
}
