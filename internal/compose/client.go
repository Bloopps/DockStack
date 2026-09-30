package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	dockercli "github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"
	"github.com/sirupsen/logrus"
)

// State represents the aggregate state of a stack's containers.
type State int

const (
	StateUnknown State = iota
	StateStopped
	StatePartial
	StateRunning
	StateUnhealthy
)

// Stack holds info about one docker-compose stack found on disk.
type Stack struct {
	Name        string
	NameLC      string // Name en minuscules, précalculé pour le filtre TUI
	Dir         string
	ComposeFile string
	Group       string
	Running     int
	Total       int
	Unhealthy   int
	Services    []ServiceStatus // par service compose, trié par nom
	// Project : nom du projet compose (label des conteneurs, à défaut le nom du
	// dossier). Sert à retrouver les volumes de la stack.
	Project string
	// Only restreint toute action (up/down/restart/recreate/pull/logs) à ces
	// services, sans dépendances ni effet sur les autres. Vide = toute la stack.
	// Posé par l'appelant sur une copie de la Stack, jamais par ListStacks.
	Only []string
	// WithDeps inclut aussi les dépendances (depends_on) des services de Only,
	// comme « up » sans --no-deps. Ignoré si Only est vide.
	WithDeps bool
	// Lenient (restauration d'une capture) : les services de Only absents du
	// compose sont ignorés et signalés, au lieu de faire échouer l'action.
	Lenient  bool
	Orphaned bool // au moins un conteneur rattaché par nom faute de label projet (recréé par watchtower)
}

// ServiceStatus représente l'état agrégé d'un service compose au sein
// d'une stack (plusieurs conteneurs si le service est scalé).
type ServiceStatus struct {
	Name      string
	Running   int
	Total     int
	Unhealthy int
	// Informations tirées du conteneur le plus parlant (unhealthy, puis à
	// l'arrêt, puis en marche) : gratuites, déjà dans l'appel Docker de la liste.
	Image string // image du conteneur
	Ports string // ports publiés « 8080→80, 443→443 »
	// DependsOn : dépendances directes déclarées dans le compose (rempli par la
	// TUI depuis DeclaredServices, pas par ListStacks).
	DependsOn []string
	Status    string // statut Docker brut : « Exited (137) 3 hours ago », « Up 2 days (unhealthy) »…
	// IDs des conteneurs EN MARCHE du service : de quoi mesurer leur CPU/RAM à
	// la demande, sans relister le démon.
	IDs []string
}

// statusRank classe les conteneurs d'un service pour choisir celui dont on
// affiche le statut : le plus problématique gagne.
func statusRank(running, unhealthy bool) int {
	switch {
	case unhealthy:
		return 2
	case !running:
		return 1
	}
	return 0
}

// publishedPorts formate les ports publiés d'un conteneur (dédoublonnés :
// Docker liste séparément IPv4 et IPv6).
func publishedPorts(ports []container.PortSummary) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		s := fmt.Sprintf("%d→%d", p.PublicPort, p.PrivatePort)
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func (s ServiceStatus) State() State {
	return aggregateState(s.Running, s.Total, s.Unhealthy)
}

func (s *Stack) State() State {
	return aggregateState(s.Running, s.Total, s.Unhealthy)
}

func aggregateState(running, total, unhealthy int) State {
	switch {
	case total == 0:
		return StateUnknown
	case unhealthy > 0:
		return StateUnhealthy
	case running == total:
		return StateRunning
	case running > 0:
		return StatePartial
	default:
		return StateStopped
	}
}

// LogEntry is one line emitted by a service.
type LogEntry struct {
	Name  string
	Line  string
	IsErr bool
}

type Client struct {
	svc api.Compose
	cli dockercli.Cli

	// Dernier échantillon CPU de chaque conteneur : le CPU est un écart entre
	// deux lectures (cf. ContainerUsages).
	statsMu   sync.Mutex
	statsPrev map[string]usageSample
}

func New() (*Client, error) {
	// Suppress logrus WARN/INFO from compose-go (variable not set, etc.)
	// These go directly to os.Stderr and corrupt the TUI display.
	logrus.SetOutput(io.Discard)

	// Redirect Docker CLI streams away from the terminal
	cli, err := dockercli.NewDockerCli(
		dockercli.WithOutputStream(io.Discard),
		dockercli.WithErrorStream(io.Discard),
	)
	if err != nil {
		return nil, err
	}
	if err := cli.Initialize(flags.NewClientOptions()); err != nil {
		return nil, err
	}
	svc, err := compose.NewComposeService(cli,
		compose.WithOutputStream(io.Discard),
		compose.WithErrorStream(io.Discard),
	)
	if err != nil {
		return nil, err
	}
	return &Client{svc: svc, cli: cli}, nil
}

// ListStacks scans stackDir for compose files and enriches each with
// container counts from a single docker ps --all call.
func (c *Client) ListStacks(ctx context.Context, stackDir string) ([]Stack, error) {
	files, err := findComposeFiles(stackDir)
	if err != nil {
		return nil, err
	}

	result, err := c.cli.Client().ContainerList(ctx, mobyclient.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}

	type counts struct {
		running, total, unhealthy int
		image, ports, status      string
		rank                      int
		ids                       []string
	}
	addServiceCount := func(m map[string]map[string]counts, key, svc string, ctr *container.Summary, running, unhealthy bool) {
		sc, ok := m[key]
		if !ok {
			sc = make(map[string]counts)
			m[key] = sc
		}
		c := sc[svc]
		c.total++
		if running {
			c.running++
			c.ids = append(c.ids, ctr.ID)
		}
		if unhealthy {
			c.unhealthy++
		}
		if r := statusRank(running, unhealthy); c.total == 1 || r > c.rank {
			c.rank, c.status, c.image = r, ctr.Status, ctr.Image
		}
		if c.ports == "" {
			c.ports = publishedPorts(ctr.Ports)
		}
		sc[svc] = c
	}
	dirCounts := make(map[string]counts, len(result.Items))
	projCounts := make(map[string]counts, len(result.Items)) // fallback: par nom de projet
	projDir := make(map[string]string)                       // nom de projet → premier working_dir vu
	dirProject := make(map[string]string)                    // working_dir → nom de projet
	dirServices := make(map[string]map[string]counts)        // working_dir -> service -> counts
	projServices := make(map[string]map[string]counts)       // projet -> service -> counts (fallback)

	for idx := range result.Items {
		ctr := &result.Items[idx]
		unhealthy := ctr.Health != nil && ctr.Health.Status == container.Unhealthy
		running := ctr.State == "running"
		svc := ctr.Labels["com.docker.compose.service"]
		// Populate both maps: dir-based (primary) and project-name-based (fallback).
		// Always populate both so that whichever key matches at lookup time wins.
		wd := filepath.Clean(ctr.Labels["com.docker.compose.project.working_dir"])
		if wd != "." {
			cnt := dirCounts[wd]
			cnt.total++
			if running {
				cnt.running++
			}
			if unhealthy {
				cnt.unhealthy++
			}
			dirCounts[wd] = cnt
			if p := ctr.Labels["com.docker.compose.project"]; p != "" {
				dirProject[wd] = p
			}
			if svc != "" {
				addServiceCount(dirServices, wd, svc, &result.Items[idx], running, unhealthy)
			}
		}
		proj := strings.ToLower(ctr.Labels["com.docker.compose.project"])
		if proj != "" {
			cnt := projCounts[proj]
			cnt.total++
			if running {
				cnt.running++
			}
			if unhealthy {
				cnt.unhealthy++
			}
			projCounts[proj] = cnt
			if svc != "" {
				addServiceCount(projServices, proj, svc, &result.Items[idx], running, unhealthy)
			}
			if wd != "." {
				if _, ok := projDir[proj]; !ok {
					projDir[proj] = wd
				}
			}
		}
	}

	// Nombre de stacks découvertes par basename : le fallback par nom de
	// projet est ambigu dès que deux répertoires partagent leur basename
	// (ex. immich/ et immich/immich/ → projet « immich » pour les deux), et
	// attribuerait les mêmes conteneurs aux deux stacks.
	baseCount := make(map[string]int, len(files))
	for _, f := range files {
		baseCount[strings.ToLower(filepath.Base(filepath.Dir(f)))]++
	}

	stacks := make([]Stack, 0, len(files))
	for _, f := range files {
		dir := filepath.Dir(f)
		rel, _ := filepath.Rel(stackDir, dir)
		group := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
		cleanDir := filepath.Clean(dir)
		cnt := dirCounts[cleanDir]
		svcCounts := dirServices[cleanDir]
		if cnt.total == 0 {
			projName := strings.ToLower(filepath.Base(dir))
			wd, deployed := projDir[projName]
			// Fallback seulement s'il est non ambigu : un seul candidat
			// avec ce basename, et pas de working_dir connu pointant vers
			// un autre répertoire.
			if baseCount[projName] == 1 && (!deployed || wd == cleanDir) {
				cnt = projCounts[projName]
				svcCounts = projServices[projName]
			}
		}
		project := dirProject[cleanDir]
		if project == "" {
			project = strings.ToLower(filepath.Base(dir))
		}
		services := make([]ServiceStatus, 0, len(svcCounts))
		for name, c := range svcCounts {
			services = append(services, ServiceStatus{Name: name, Running: c.running, Total: c.total, Unhealthy: c.unhealthy, Image: c.image, Ports: c.ports, Status: c.status, IDs: c.ids})
		}
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		stacks = append(stacks, Stack{
			Name: rel, NameLC: strings.ToLower(rel), Dir: dir, ComposeFile: f,
			Group: group, Running: cnt.running, Total: cnt.total, Unhealthy: cnt.unhealthy,
			Services: services, Project: project,
		})
	}

	attachOrphans(stacks, result.Items)

	sort.Slice(stacks, func(i, j int) bool {
		if stacks[i].Group != stacks[j].Group {
			return stacks[i].Group < stacks[j].Group
		}
		si, sj := stacks[i].State(), stacks[j].State()
		if si != sj {
			return si < sj
		}
		return stacks[i].Name < stacks[j].Name
	})
	return stacks, nil
}

// attachOrphans rattache par NOM les conteneurs qui tournent sans label
// com.docker.compose.project (typiquement recréés par watchtower, qui n'a pas
// repropagé ce label) aux stacks ressorties à 0 conteneur — sinon elles
// paraissent « down » alors qu'elles tournent. Le nom du conteneur survit à la
// recréation, c'est le lien fiable. Coût maîtrisé : on ne charge le projet
// (loadProject) que s'il existe des orphelins ET seulement pour les stacks à
// 0 conteneur — donc nul en régime nominal (tout bien labellisé).
func attachOrphans(stacks []Stack, items []container.Summary) {
	hasOrphan := false
	for i := range items {
		if isOrphanCandidate(items[i].Labels) {
			hasOrphan = true
			break
		}
	}
	if !hasOrphan {
		return
	}
	consumed := make(map[string]bool) // par ID, pour ne pas rattacher un orphelin à 2 stacks
	for i := range stacks {
		if stacks[i].Total > 0 {
			continue // déjà rattachée par label
		}
		proj, err := loadProject(stacks[i])
		if err != nil {
			continue
		}
		cand := projectContainerNames(proj)
		if len(cand) == 0 {
			continue
		}
		svcAgg := make(map[string]*ServiceStatus)
		for j := range items {
			ctr := &items[j]
			if consumed[ctr.ID] || !isOrphanCandidate(ctr.Labels) {
				continue
			}
			svcName, ok := matchByNames(cand, ctr.Names)
			if !ok {
				continue
			}
			consumed[ctr.ID] = true
			running := ctr.State == "running"
			unhealthy := ctr.Health != nil && ctr.Health.Status == container.Unhealthy
			ss := svcAgg[svcName]
			if ss == nil {
				ss = &ServiceStatus{Name: svcName}
				svcAgg[svcName] = ss
			}
			if r := statusRank(running, unhealthy); ss.Total == 0 || r > statusRank(ss.Running == ss.Total, ss.Unhealthy > 0) {
				ss.Image, ss.Status = ctr.Image, ctr.Status
			}
			if ss.Ports == "" {
				ss.Ports = publishedPorts(ctr.Ports)
			}
			ss.Total++
			stacks[i].Total++
			if running {
				ss.Running++
				stacks[i].Running++
				ss.IDs = append(ss.IDs, ctr.ID)
			}
			if unhealthy {
				ss.Unhealthy++
				stacks[i].Unhealthy++
			}
		}
		if len(svcAgg) == 0 {
			continue
		}
		stacks[i].Orphaned = true
		stacks[i].Project = proj.Name
		services := make([]ServiceStatus, 0, len(svcAgg))
		for _, ss := range svcAgg {
			services = append(services, *ss)
		}
		sort.Slice(services, func(a, b int) bool { return services[a].Name < services[b].Name })
		stacks[i].Services = services
	}
}

// projectContainerNames retourne les noms de conteneurs qu'un projet peut
// porter sur disque (container_name explicite + motifs compose par défaut
// projet-service-1 / projet_service_1), associés à leur service. Sert à
// rattacher par NOM un conteneur qui a perdu son label projet.
func projectContainerNames(proj *types.Project) map[string]string {
	names := make(map[string]string, len(proj.Services)*3)
	for svcName, svc := range proj.Services {
		if svc.ContainerName != "" {
			names[svc.ContainerName] = svcName
		}
		names[proj.Name+"-"+svcName+"-1"] = svcName
		names[proj.Name+"_"+svcName+"_1"] = svcName
	}
	return names
}

// matchByNames renvoie le service associé au premier nom de conteneur (parmi
// ctrNames, préfixe « / » retiré) présent dans cand.
func matchByNames(cand map[string]string, ctrNames []string) (string, bool) {
	for _, n := range ctrNames {
		if svc, ok := cand[strings.TrimPrefix(n, "/")]; ok {
			return svc, true
		}
	}
	return "", false
}

// isOrphanCandidate dit si un conteneur peut être un orphelin compose : pas de
// label projet, mais une trace de compose ou de watchtower (watchtower ne
// repropage pas le label projet mais garde com.docker.compose.replace). Un
// conteneur lancé à la main (docker run) n'en porte aucune : il n'est jamais
// rattaché à une stack, même si son nom correspond à un container_name — sans
// ce garde-fou, un Down sur la stack le supprimerait.
func isOrphanCandidate(labels map[string]string) bool {
	if strings.TrimSpace(labels["com.docker.compose.project"]) != "" {
		return false
	}
	for k := range labels {
		if strings.HasPrefix(k, "com.docker.compose.") || strings.HasPrefix(k, "com.centurylinklabs.watchtower.") {
			return true
		}
	}
	return false
}

// ProgressEvent is a single live event streamed during an operation.
type ProgressEvent struct {
	StackName string
	Container string // resource ID ("Container x", "Network y", layer id…) ; empty when StackDone=true
	ParentID  string // parent resource (pull layers hang under their image/service)
	Status    string // compose status text ("Started", "Pulling", "Downloading"…)
	Details   string // progress detail as rendered by compose ("[==>   ] 12MB/45MB")
	Percent   int
	State     string // "working" | "done" | "warning" | "error"
	Err       error  // non-nil when StackDone=true and op failed
	StackDone bool   // true = this stack's op is complete
	AllDone   bool   // sentinelle : toutes les stacks de l'opération sont terminées
}

// streamingCollector implements api.EventProcessor and forwards per-resource
// status changes to a channel so the TUI can render a live, compose-like view.
type streamingCollector struct {
	stackName string
	out       chan<- ProgressEvent
}

func (s *streamingCollector) Start(_ context.Context, _ string) {}
func (s *streamingCollector) Done(_ string, _ bool)             {}
func (s *streamingCollector) On(resources ...api.Resource) {
	for _, r := range resources {
		if r.ID == "" || r.ID == api.ResourceCompose {
			continue
		}
		ev := ProgressEvent{
			StackName: s.stackName, Container: r.ID, ParentID: r.ParentID,
			Status: r.Text, Details: r.Details, Percent: r.Percent,
		}
		switch r.Status {
		case api.Working:
			// Non-blocking send: intermediate progress is superseded by later
			// events anyway, so dropping it when the TUI is slow is harmless.
			ev.State = "working"
			select {
			case s.out <- ev:
			default:
			}
		case api.Done, api.Warning, api.Error:
			// Blocking send: a terminal state (Started/Removed/…) must never be
			// dropped, otherwise a line would stay stuck on its "Working" status.
			switch r.Status {
			case api.Done:
				ev.State = "done"
			case api.Warning:
				ev.State = "warning"
			default:
				ev.State = "error"
			}
			s.out <- ev
		}
	}
}

func newSvcWithProcessor(cli dockercli.Cli, proc api.EventProcessor) (api.Compose, error) {
	return compose.NewComposeService(cli,
		compose.WithOutputStream(io.Discard),
		compose.WithErrorStream(io.Discard),
		compose.WithEventProcessor(proc),
	)
}

// drainEvents consomme les événements quand personne ne les affiche : les
// envois terminaux du streamingCollector sont bloquants, sans lecteur un
// buffer plein (>50 événements) bloquerait l'opération pour toujours.
// Le canal n'est jamais fermé : une goroutine interne de compose peut émettre
// un événement tardif après le retour de l'opération, et un envoi sur canal
// fermé paniquerait. Le drain s'arrête par signal ; le buffer absorbe les
// retardataires éventuels.
func drainEvents() (chan ProgressEvent, func()) {
	ch := make(chan ProgressEvent, 50)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
			case <-stop:
				return
			}
		}
	}()
	return ch, func() { close(stop) }
}

func (c *Client) Up(ctx context.Context, stack Stack) error {
	ch, done := drainEvents()
	defer done()
	return c.upLive(ctx, stack, ch)
}

func (c *Client) upLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, dropped, err := loadScopedProjectReport(&stack)
	for _, n := range dropped {
		ch <- ProgressEvent{StackName: stack.Name, Container: "Service " + n, Status: "absent du compose, ignoré", State: "warning"}
	}
	if err != nil {
		return err
	}
	opts := api.UpOptions{
		Create: api.CreateOptions{Recreate: api.RecreateDiverged, Services: stack.Only},
		Start:  api.StartOptions{Project: proj, Services: stack.Only},
	}
	err = c.upWithReadopt(ctx, stack, proj, svc, opts, ch)
	// « already in use » restant après upWithReadopt = le nom est pris par le
	// conteneur d'un AUTRE projet : ne surtout pas le démarrer à sa place.
	if err != nil && strings.Contains(err.Error(), "has no container to start") {
		err = c.startByContainerName(ctx, proj, err)
	}
	return err
}

// upWithReadopt lance svc.Up et, en cas de conflit de nom (« already in use »),
// retire le conteneur orphelin qui squatte le nom puis relance le Up : compose
// le recrée alors proprement avec ses labels (ré-adoption). Le coût n'est payé
// que lorsqu'un conflit survient réellement.
func (c *Client) upWithReadopt(ctx context.Context, stack Stack, proj *types.Project, svc api.Compose, opts api.UpOptions, ch chan<- ProgressEvent) error {
	err := svc.Up(ctx, proj, opts)
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		return err
	}
	if c.removeProjectOrphans(ctx, stack.Name, proj, ch, "ré-adopté (orphelin watchtower)") == 0 {
		return err // rien à ré-adopter : ne pas masquer le conflit d'origine
	}
	return svc.Up(ctx, proj, opts)
}

// startByContainerName tente de démarrer les conteneurs existants du projet
// par leur nom. origErr (l'erreur du Up d'origine) est renvoyée si aucun
// conteneur n'a été trouvé : ce fallback ne doit jamais transformer un échec
// en faux succès.
func (c *Client) startByContainerName(ctx context.Context, proj *types.Project, origErr error) error {
	handled := false
	var lastErr error
	for svcName, svc := range proj.Services {
		candidates := []string{
			svc.ContainerName,
			proj.Name + "-" + svcName + "-1",
			proj.Name + "_" + svcName + "_1",
		}
		for _, name := range candidates {
			if name == "" {
				continue
			}
			f := make(mobyclient.Filters).Add("name", "/"+name)
			result, listErr := c.cli.Client().ContainerList(ctx, mobyclient.ContainerListOptions{
				All:     true,
				Filters: f,
			})
			if listErr != nil {
				continue
			}
			// Le filtre name du daemon matche par sous-chaîne (« /db » matche
			// aussi « /db-backup ») : n'accepter que l'égalité exacte.
			found := false
			for _, ctr := range result.Items {
				for _, n := range ctr.Names {
					if n == "/"+name {
						found = true
						break
					}
				}
				if !found {
					continue
				}
				// Même nom mais rattaché à un autre projet : pas le nôtre.
				if p := ctr.Labels["com.docker.compose.project"]; p != "" && p != proj.Name {
					found = false
					continue
				}
				handled = true
				if ctr.State == "exited" || ctr.State == "created" || ctr.State == "paused" {
					if _, startErr := c.cli.Client().ContainerStart(ctx, ctr.ID, mobyclient.ContainerStartOptions{}); startErr != nil {
						lastErr = startErr
					}
				}
				break
			}
			if found {
				break
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	if !handled {
		return origErr
	}
	return nil
}

// removeProjectOrphans supprime les conteneurs qui portent un nom du projet
// mais plus le label com.docker.compose.project (orphelins, typiquement
// recréés par watchtower qui n'a pas repropagé ce label). Sans ça, compose ne
// les voit pas : il tente de recréer un conteneur de même nom et échoue sur
// « name already in use » (Up), ou les ignore en silence (Down). On les
// retire (Force, volumes PRÉSERVÉS → données saines) ; au Up qui suit, compose
// les recrée proprement avec leurs labels (ré-adoption). On ne touche JAMAIS
// un conteneur déjà rattaché à un projet. doneStatus décrit l'effet dans la
// vue de progression. Renvoie le nombre d'orphelins retirés.
func (c *Client) removeProjectOrphans(ctx context.Context, stackName string, proj *types.Project, ch chan<- ProgressEvent, doneStatus string) int {
	return c.eachProjectOrphan(ctx, stackName, proj, ch, doneStatus, "ré-adoption impossible : ", false,
		func(id string) error {
			_, err := c.cli.Client().ContainerRemove(ctx, id, mobyclient.ContainerRemoveOptions{Force: true})
			return err
		})
}

// restartProjectOrphans redémarre sur place les orphelins du projet : compose
// restart ne les voit pas (label projet absent), sans ça Restart serait un
// no-op silencieux sur une stack ⟳.
func (c *Client) restartProjectOrphans(ctx context.Context, stackName string, proj *types.Project, ch chan<- ProgressEvent) {
	c.eachProjectOrphan(ctx, stackName, proj, ch, "redémarré (orphelin)", "redémarrage impossible : ", false,
		func(id string) error {
			_, err := c.cli.Client().ContainerRestart(ctx, id, mobyclient.ContainerRestartOptions{})
			return err
		})
}

// stopProjectOrphans arrête les orphelins EN MARCHE du projet : compose stop ne
// les voit pas (label projet absent), sans ça Stop serait un no-op silencieux
// sur une stack ⟳. Le conteneur est conservé, comme pour un stop compose.
func (c *Client) stopProjectOrphans(ctx context.Context, stackName string, proj *types.Project, ch chan<- ProgressEvent) {
	c.eachProjectOrphan(ctx, stackName, proj, ch, "arrêté (orphelin)", "arrêt impossible : ", true,
		func(id string) error {
			_, err := c.cli.Client().ContainerStop(ctx, id, mobyclient.ContainerStopOptions{})
			return err
		})
}

// eachProjectOrphan applique act à chaque orphelin du projet (nom du projet,
// pas de label com.docker.compose.project, cf. isOrphanCandidate) — seulement
// à ceux en marche si runningOnly — et publie le résultat dans la vue de
// progression. Le projet peut être réduit (Stack.Only) : seuls les noms de ses
// services sont candidats. Renvoie le nombre de succès.
func (c *Client) eachProjectOrphan(ctx context.Context, stackName string, proj *types.Project, ch chan<- ProgressEvent, doneStatus, failPrefix string, runningOnly bool, act func(id string) error) int {
	cand := projectContainerNames(proj)
	if len(cand) == 0 {
		return 0
	}
	result, err := c.cli.Client().ContainerList(ctx, mobyclient.ContainerListOptions{All: true})
	if err != nil {
		return 0
	}
	ok := 0
	for _, ctr := range result.Items {
		if !isOrphanCandidate(ctr.Labels) {
			continue // rattaché à un projet (ne jamais voler) ou lancé hors compose
		}
		if runningOnly && ctr.State != container.StateRunning {
			continue
		}
		if _, match := matchByNames(cand, ctr.Names); !match {
			continue
		}
		id := "Orphelin " + primaryName(ctr.Names)
		if err := act(ctr.ID); err != nil {
			ch <- ProgressEvent{StackName: stackName, Container: id, Status: failPrefix + err.Error(), State: "warning"}
			continue
		}
		ok++
		ch <- ProgressEvent{StackName: stackName, Container: id, Status: doneStatus, State: "done"}
	}
	return ok
}

// primaryName renvoie le nom principal d'un conteneur (premier de la liste,
// préfixe « / » retiré).
func primaryName(ctrNames []string) string {
	if len(ctrNames) == 0 {
		return ""
	}
	return strings.TrimPrefix(ctrNames[0], "/")
}

func (c *Client) downLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadScopedProject(stack)
	if err != nil {
		return err
	}
	if derr := svc.Down(ctx, proj.Name, api.DownOptions{Project: proj, Services: stack.Only}); derr != nil {
		return derr
	}
	// Orphelins watchtower : compose down ne les a pas vus (label projet
	// absent) → on les arrête nous-mêmes, sinon « down » serait un no-op
	// silencieux sur des conteneurs qui tournent toujours.
	if stack.Orphaned && len(stack.Only) == 0 {
		c.removeProjectOrphans(ctx, stack.Name, proj, ch, "arrêté (orphelin)")
	}
	return nil
}

func (c *Client) restartLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadScopedProject(stack)
	if err != nil {
		return err
	}
	// Project fourni comme la CLI : sans lui, compose le reconstruit depuis les
	// conteneurs labellisés et échoue si la stack n'a que des orphelins.
	if err := svc.Restart(ctx, proj.Name, api.RestartOptions{Project: proj, Services: stack.Only}); err != nil {
		return err
	}
	if stack.Orphaned && len(stack.Only) == 0 {
		c.restartProjectOrphans(ctx, stack.Name, proj, ch)
	}
	return nil
}

func (c *Client) stopLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadScopedProject(stack)
	if err != nil {
		return err
	}
	// Stop garde les conteneurs (contrairement à Down) : le redémarrage est
	// immédiat et rien n'est perdu.
	if err := svc.Stop(ctx, proj.Name, api.StopOptions{Project: proj, Services: stack.Only}); err != nil {
		return err
	}
	// Orphelins watchtower : compose stop ne les voit pas. Le projet est déjà
	// réduit aux services ciblés, les orphelins des autres ne sont pas touchés.
	if stack.Orphaned {
		c.stopProjectOrphans(ctx, stack.Name, proj, ch)
	}
	return nil
}

func (c *Client) recreateLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadScopedProject(stack)
	if err != nil {
		return err
	}
	return c.upWithReadopt(ctx, stack, proj, svc, api.UpOptions{
		Create: api.CreateOptions{Recreate: api.RecreateForce, Services: stack.Only},
		Start:  api.StartOptions{Project: proj, Services: stack.Only},
	}, ch)
}

func (c *Client) pullLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadScopedProject(stack)
	if err != nil {
		return err
	}
	// Parité CLI (--ignore-pull-failures désactivé par défaut) : une image
	// introuvable fait échouer la stack au lieu de s'afficher « OK ».
	return svc.Pull(ctx, proj, api.PullOptions{})
}

// removeLive = down + orphelins + volumes : les données des volumes sont détruites.
func (c *Client) removeLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadProject(stack)
	if err != nil {
		return err
	}
	return svc.Down(ctx, proj.Name, api.DownOptions{RemoveOrphans: true, Volumes: true})
}

// logTail : lignes d'historique par conteneur à l'ouverture des logs.
const logTail = "500"

// LogsAsync starts streaming logs and returns a channel of log entries.
// The channel is closed when streaming ends or ctx is cancelled.
func (c *Client) LogsAsync(ctx context.Context, stack Stack) (<-chan LogEntry, error) {
	proj, err := loadScopedProject(stack)
	if err != nil {
		return nil, err
	}
	ch := make(chan LogEntry, 200)
	consumer := &chanConsumer{ctx: ctx, ch: ch}
	go func() {
		defer close(ch)
		// L'erreur du flux est poussée comme entrée de log : la TUI n'a pas
		// d'autre canal de retour ici, et sans elle un échec immédiat
		// s'afficherait comme un simple « Logs terminés ». L'annulation par
		// l'utilisateur (ctx.Err() != nil) n'est pas une erreur.
		// Tail borné : sans lui, tout l'historique est relu à l'ouverture alors
		// que la vue n'en garde que maxLogLines.
		if err := c.svc.Logs(ctx, proj.Name, consumer, api.LogOptions{Follow: true, Tail: logTail, Services: stack.Only}); err != nil && ctx.Err() == nil {
			consumer.send(LogEntry{Name: "⚠", Line: "flux de logs interrompu : " + err.Error(), IsErr: true})
		}
	}()
	return ch, nil
}

// chanConsumer relaie les lignes de compose vers le canal lu par la TUI.
// Les envois écoutent ctx : une fois la vue logs fermée (ctx annulé), plus
// personne ne lit le canal, et un envoi bloquant retiendrait la goroutine de
// Logs — et le flux HTTP sous-jacent — indéfiniment.
type chanConsumer struct {
	ctx context.Context
	ch  chan LogEntry
}

func (c *chanConsumer) send(e LogEntry) {
	select {
	case c.ch <- e:
	case <-c.ctx.Done():
	}
}

func (c *chanConsumer) Log(name, msg string) { c.send(LogEntry{Name: name, Line: msg}) }
func (c *chanConsumer) Err(name, msg string) { c.send(LogEntry{Name: name, Line: msg, IsErr: true}) }
func (c *chanConsumer) Status(_, _ string)   {}

// DockerEvent is a minimal, decoupled view of a moby system event: just
// enough for the TUI to decide whether the stack list needs a refresh.
type DockerEvent struct {
	Action string
}

// SubscribeEvents streams container lifecycle events (start/stop/die/
// health_status…) until ctx is cancelled or the underlying API stream ends,
// at which point the returned channel is closed.
func (c *Client) SubscribeEvents(ctx context.Context) <-chan DockerEvent {
	out := make(chan DockerEvent, 32)
	go func() {
		defer close(out)
		f := make(mobyclient.Filters).Add("type", "container")
		res := c.cli.Client().Events(ctx, mobyclient.EventsListOptions{Filters: f})
		for {
			select {
			case msg, ok := <-res.Messages:
				// Canal fermé (fin du flux) : sans le test ok, ce case serait
				// toujours prêt et tournerait en boucle chaude sur des messages
				// vides.
				if !ok {
					return
				}
				out <- DockerEvent{Action: string(msg.Action)}
			case <-res.Err:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// relevantEventActions are the container actions that can change a stack's
// aggregate State() (running count or health) and therefore warrant a
// stack-list refresh.
var relevantEventActions = map[string]bool{
	"create":  true,
	"start":   true,
	"restart": true,
	"stop":    true,
	"die":     true,
	"pause":   true,
	"unpause": true,
	"destroy": true,
}

// IsRelevantDockerEvent reports whether action should trigger a stack-list
// refresh: container lifecycle transitions and health-check results.
func IsRelevantDockerEvent(action string) bool {
	return relevantEventActions[action] || strings.HasPrefix(action, "health_status")
}

type stackLiveFn func(context.Context, Stack, chan<- ProgressEvent) error

func (c *Client) runLive(ctx context.Context, stacks []Stack, parallel int, ch chan<- ProgressEvent, fn stackLiveFn) {
	if parallel <= 0 {
		parallel = 1
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, s := range stacks {
		wg.Add(1)
		sem <- struct{}{}
		s := s
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := fn(ctx, s, ch)
			ch <- ProgressEvent{StackName: s.Name, Err: err, StackDone: true}
		}()
	}
	wg.Wait()
}

// manyLive lance fn sur chaque stack et signale la fin par une sentinelle
// AllDone plutôt qu'en fermant le canal : une goroutine interne de compose
// peut émettre un événement tardif après la fin, et un envoi sur canal fermé
// paniquerait.
func (c *Client) manyLive(ctx context.Context, stacks []Stack, parallel int, fn stackLiveFn) <-chan ProgressEvent {
	ch := make(chan ProgressEvent, 200)
	go func() {
		c.runLive(ctx, stacks, parallel, ch, fn)
		ch <- ProgressEvent{AllDone: true}
	}()
	return ch
}

func (c *Client) UpManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.upLive)
}

func (c *Client) DownManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.downLive)
}

func (c *Client) StopManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.stopLive)
}

func (c *Client) RestartManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.restartLive)
}

func (c *Client) RecreateManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.recreateLive)
}

func (c *Client) PullManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.pullLive)
}

func (c *Client) RemoveManyLive(ctx context.Context, stacks []Stack, parallel int) <-chan ProgressEvent {
	return c.manyLive(ctx, stacks, parallel, c.removeLive)
}

// DockerUsage résume l'occupation Docker affichée dans l'en-tête.
type DockerUsage struct {
	Running, Containers int   // conteneurs en marche / total (tous, hors stacks compris)
	ImagesSize          int64 // octets
	ImagesReclaimable   int64 // octets libérables (images inutilisées)
	VolumesSize         int64 // octets ; valide seulement si VolumesOK
	VolumesOK           bool
	// VolumesByProject : taille des volumes nommés par projet compose (mis en
	// minuscules). Issue du même appel que VolumesSize, donc valide avec lui.
	VolumesByProject map[string]int64
	// LayersByProject : taille des couches d'écriture des conteneurs (ce qu'ils
	// ont écrit hors volumes) par projet compose, même cadence que les volumes.
	LayersByProject map[string]int64
	RootDir         string // dossier de données du démon (ex. /var/lib/docker)
}

// Usage interroge le démon : Info (compteurs, dossier de données) et l'espace
// des images, peu coûteux. La taille des volumes oblige le démon à parcourir
// leurs données (≈1,5 s pour 5 Go ici, davantage sur de gros volumes) : elle
// n'est demandée que si withVolumes, à charge pour l'appelant de l'espacer.
func (c *Client) Usage(ctx context.Context, withVolumes bool) (DockerUsage, error) {
	api := c.cli.Client()
	info, err := api.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return DockerUsage{}, err
	}
	du, err := api.DiskUsage(ctx, mobyclient.DiskUsageOptions{Images: true, Volumes: withVolumes, Containers: withVolumes,
		// Sans Verbose le démon ne renvoie que le total des volumes, pas la liste
		// (donc pas la ventilation par projet) ; le coût est le même.
		Verbose: withVolumes})
	if err != nil {
		return DockerUsage{}, err
	}
	var byProject, layers map[string]int64
	if withVolumes {
		layers = make(map[string]int64)
		for _, ctr := range du.Containers.Items {
			if p := strings.ToLower(ctr.Labels["com.docker.compose.project"]); p != "" && ctr.SizeRw > 0 {
				layers[p] += ctr.SizeRw
			}
		}
		byProject = make(map[string]int64)
		for _, v := range du.Volumes.Items {
			p := strings.ToLower(v.Labels["com.docker.compose.project"])
			if p != "" && v.UsageData != nil && v.UsageData.Size > 0 {
				byProject[p] += v.UsageData.Size
			}
		}
	}
	return DockerUsage{
		VolumesByProject:  byProject,
		LayersByProject:   layers,
		Running:           info.Info.ContainersRunning,
		Containers:        info.Info.Containers,
		ImagesSize:        du.Images.TotalSize,
		ImagesReclaimable: du.Images.Reclaimable,
		VolumesSize:       du.Volumes.TotalSize,
		VolumesOK:         withVolumes,
		RootDir:           info.Info.DockerRootDir,
	}, nil
}

// ContainerUsage : consommation instantanée d'un conteneur en marche.
type ContainerUsage struct {
	CPU      float64 // % d'un cœur, comme « docker stats » (peut dépasser 100)
	CPUOK    bool    // faux à la première lecture : le CPU est un écart entre deux lectures
	MemBytes uint64  // mémoire utilisée hors cache de pages
	// Débits disque (octets/s) du conteneur, vus par le cgroup : lecture et
	// écriture sur les périphériques bloc. IOOK est faux à la première lecture,
	// comme CPUOK : un débit est un écart entre deux lectures.
	ReadBps, WriteBps float64
	IOOK              bool
}

// usageSample : compteurs (cumulatifs) d'un conteneur à une lecture.
type usageSample struct {
	total, system uint64    // CPU conteneur / CPU système
	read, write   uint64    // octets lus / écrits sur disque
	at            time.Time // instant de la lecture côté démon
}

const (
	// usageParallel borne les lectures simultanées.
	usageParallel = 16
	// cpuSampleMaxAge : au-delà, l'écart avec la lecture précédente ne dit plus
	// rien de l'instant présent (la liste n'était plus à l'écran, par exemple).
	cpuSampleMaxAge = 30 * time.Second
)

// ContainerUsages lit la consommation des conteneurs donnés, en une lecture
// chacun (≈0,6 ms : 68 conteneurs en 40 ms ici). Le CPU est calculé contre la
// lecture précédente gardée par le client, donc disponible dès le second appel
// seulement ; la mémoire l'est tout de suite. Les conteneurs disparus sont
// ignorés ; l'erreur n'est renvoyée que si aucune lecture n'a abouti.
func (c *Client) ContainerUsages(ctx context.Context, ids []string) (map[string]ContainerUsage, error) {
	api := c.cli.Client()
	type result struct {
		id string
		st container.StatsResponse
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, usageParallel)
		results  = make([]result, 0, len(ids))
		firstErr error
	)
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			st, err := readStats(ctx, api, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			results = append(results, result{id, st})
		}()
	}
	wg.Wait()
	if len(ids) > 0 && len(results) == 0 {
		return nil, firstErr
	}

	now := time.Now()
	out := make(map[string]ContainerUsage, len(results))
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	if c.statsPrev == nil {
		c.statsPrev = make(map[string]usageSample)
	}
	for _, r := range results {
		u := ContainerUsage{MemBytes: memUsage(r.st)}
		rd, wr := blkioBytes(r.st)
		at := r.st.Read
		if at.IsZero() {
			at = now
		}
		cur := usageSample{total: r.st.CPUStats.CPUUsage.TotalUsage, system: r.st.CPUStats.SystemUsage, read: rd, write: wr, at: at}
		if prev, ok := c.statsPrev[r.id]; ok && cur.at.Sub(prev.at) <= cpuSampleMaxAge {
			u.CPU, u.CPUOK = cpuPercent(prev, cur, cpuCores(r.st))
			u.ReadBps, u.WriteBps, u.IOOK = ioRates(prev, cur)
		}
		c.statsPrev[r.id] = cur
		out[r.id] = u
	}
	// Oublier les conteneurs qu'on ne lit plus (supprimés ou hors écran).
	for id, p := range c.statsPrev {
		if now.Sub(p.at) > cpuSampleMaxAge {
			delete(c.statsPrev, id)
		}
	}
	return out, nil
}

func readStats(ctx context.Context, api mobyclient.APIClient, id string) (container.StatsResponse, error) {
	var st container.StatsResponse
	res, err := api.ContainerStats(ctx, id, mobyclient.ContainerStatsOptions{})
	if err != nil {
		return st, err
	}
	defer res.Body.Close()
	err = json.NewDecoder(res.Body).Decode(&st)
	return st, err
}

func cpuCores(st container.StatsResponse) float64 {
	cores := float64(st.CPUStats.OnlineCPUs)
	if cores == 0 {
		cores = float64(len(st.CPUStats.CPUUsage.PercpuUsage))
	}
	return max(cores, 1)
}

// cpuPercent : formule de « docker stats » entre deux lectures. Faux si les
// compteurs ont reculé (conteneur redémarré) ou si le temps système n'a pas avancé.
func cpuPercent(prev, cur usageSample, cores float64) (float64, bool) {
	if cur.total < prev.total || cur.system <= prev.system {
		return 0, false
	}
	return float64(cur.total-prev.total) / float64(cur.system-prev.system) * cores * 100, true
}

// blkioBytes cumule les octets lus et écrits par le conteneur sur ses
// périphériques bloc (cgroup v1 ou v2 : « read » / « write », casse variable).
func blkioBytes(st container.StatsResponse) (read, write uint64) {
	for _, e := range st.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			read += e.Value
		case "write":
			write += e.Value
		}
	}
	return
}

// ioRates : débits moyens entre deux lectures. Faux si les compteurs ont reculé
// (conteneur redémarré) ou si l'intervalle est nul.
func ioRates(prev, cur usageSample) (readBps, writeBps float64, ok bool) {
	dt := cur.at.Sub(prev.at).Seconds()
	if dt <= 0 || cur.read < prev.read || cur.write < prev.write {
		return 0, 0, false
	}
	return float64(cur.read-prev.read) / dt, float64(cur.write-prev.write) / dt, true
}

// memUsage retire le cache de pages inactif, comme « docker stats » (cgroup v2
// puis v1) : sinon un conteneur qui lit beaucoup de fichiers paraît gourmand.
func memUsage(st container.StatsResponse) uint64 {
	used := st.MemoryStats.Usage
	cache := st.MemoryStats.Stats["inactive_file"]
	if cache == 0 {
		cache = st.MemoryStats.Stats["total_inactive_file"]
	}
	if cache < used {
		used -= cache
	}
	return used
}
