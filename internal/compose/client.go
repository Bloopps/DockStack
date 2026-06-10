package compose

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	dockercli "github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/compose-spec/compose-go/v2/types"
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
}

func (s *Stack) State() State {
	switch {
	case s.Total == 0:
		return StateUnknown
	case s.Running == s.Total:
		return StateRunning
	case s.Running > 0:
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

	type counts struct{ running, total int }
	dirCounts  := make(map[string]counts, len(result.Items))
	projCounts := make(map[string]counts, len(result.Items)) // fallback: par nom de projet
	projDir    := make(map[string]string)                    // nom de projet → premier working_dir vu

	for _, ctr := range result.Items {
		// Populate both maps: dir-based (primary) and project-name-based (fallback).
		// Always populate both so that whichever key matches at lookup time wins.
		wd := filepath.Clean(ctr.Labels["com.docker.compose.project.working_dir"])
		if wd != "." {
			cnt := dirCounts[wd]
			cnt.total++
			if ctr.State == "running" {
				cnt.running++
			}
			dirCounts[wd] = cnt
		}
		proj := strings.ToLower(ctr.Labels["com.docker.compose.project"])
		if proj != "" {
			cnt := projCounts[proj]
			cnt.total++
			if ctr.State == "running" {
				cnt.running++
			}
			projCounts[proj] = cnt
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
		cnt := dirCounts[filepath.Clean(dir)]
		if cnt.total == 0 {
			projName := strings.ToLower(filepath.Base(dir))
			wd, deployed := projDir[projName]
			// Fallback seulement s'il est non ambigu : un seul candidat
			// avec ce basename, et pas de working_dir connu pointant vers
			// un autre répertoire.
			if baseCount[projName] == 1 && (!deployed || wd == filepath.Clean(dir)) {
				cnt = projCounts[projName]
			}
		}
		stacks = append(stacks, Stack{
			Name: rel, NameLC: strings.ToLower(rel), Dir: dir, ComposeFile: f,
			Group: group, Running: cnt.running, Total: cnt.total,
		})
	}

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
	proj, err := loadProject(stack)
	if err != nil {
		return err
	}
	err = svc.Up(ctx, proj, api.UpOptions{
		Create: api.CreateOptions{Recreate: api.RecreateDiverged},
		Start:  api.StartOptions{Project: proj},
	})
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "has no container to start") ||
			strings.Contains(errStr, "already in use") {
			err = c.startByContainerName(ctx, proj, err)
		}
	}
	return err
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
			svcName,
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

func (c *Client) downLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadProject(stack)
	if err != nil {
		return err
	}
	return svc.Down(ctx, proj.Name, api.DownOptions{})
}

func (c *Client) restartLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadProject(stack)
	if err != nil {
		return err
	}
	return svc.Restart(ctx, proj.Name, api.RestartOptions{})
}

func (c *Client) recreateLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadProject(stack)
	if err != nil {
		return err
	}
	return svc.Up(ctx, proj, api.UpOptions{
		Create: api.CreateOptions{Recreate: api.RecreateForce},
		Start:  api.StartOptions{Project: proj},
	})
}

func (c *Client) pullLive(ctx context.Context, stack Stack, ch chan<- ProgressEvent) error {
	svc, err := newSvcWithProcessor(c.cli, &streamingCollector{stackName: stack.Name, out: ch})
	if err != nil {
		return err
	}
	proj, err := loadProject(stack)
	if err != nil {
		return err
	}
	return svc.Pull(ctx, proj, api.PullOptions{IgnoreFailures: true})
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

// LogsAsync starts streaming logs and returns a channel of log entries.
// The channel is closed when streaming ends or ctx is cancelled.
func (c *Client) LogsAsync(ctx context.Context, stack Stack) (<-chan LogEntry, error) {
	proj, err := loadProject(stack)
	if err != nil {
		return nil, err
	}
	ch := make(chan LogEntry, 200)
	consumer := &chanConsumer{ch: ch}
	go func() {
		defer close(ch)
		c.svc.Logs(ctx, proj.Name, consumer, api.LogOptions{Follow: true})
	}()
	return ch, nil
}

type chanConsumer struct{ ch chan LogEntry }

func (c *chanConsumer) Log(name, msg string)    { c.ch <- LogEntry{Name: name, Line: msg} }
func (c *chanConsumer) Err(name, msg string)    { c.ch <- LogEntry{Name: name, Line: msg, IsErr: true} }
func (c *chanConsumer) Status(_, _ string)      {}

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
