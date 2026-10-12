package tui

import (
	"context"
	"errors"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/config"
	"github.com/Bloopps/dockstack/internal/monitor"
)

// ---- progress stream ----

// Les messages de progression portent le numéro d'opération (cf.
// Model.progressSeq, même recette que logSeq) : un message en vol d'une
// opération précédente est ignoré au lieu d'être appliqué à l'état de la
// suivante — sans ça, deux pompes pourraient lire le même canal et mélanger
// les flux de deux opérations.
//
// Un message porte un lot d'événements : bubbletea recalcule la vue après
// chaque message, et un pull émet des milliers d'événements de couches. À un
// rendu par événement (≈ 0,8 ms), la vue plafonnait vers 1 300 événements/s
// en occupant un cœur entier.
type progressEventMsg struct {
	evs []compose.ProgressEvent
	seq int
	// done : la sentinelle AllDone a été lue après ces événements.
	done bool
}
type progressAllDoneMsg struct{ seq int }

// progressBatchMax borne un lot, comme logBatchMax pour les logs.
const progressBatchMax = 256

// readProgressCmd lit le prochain événement du flux (lecture bloquante), puis
// ceux déjà disponibles, sans attendre, jusqu'à progressBatchMax. Le canal vit
// dans le Model (pas de variable globale) : chaque opération a son propre
// flux. La fin est signalée par la sentinelle AllDone (le canal n'est pas
// fermé).
func readProgressCmd(ch <-chan compose.ProgressEvent, seq int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok || ev.AllDone {
			return progressAllDoneMsg{seq: seq}
		}
		msg := progressEventMsg{evs: []compose.ProgressEvent{ev}, seq: seq}
		for len(msg.evs) < progressBatchMax {
			select {
			case ev, ok := <-ch:
				if !ok || ev.AllDone {
					msg.done = true
					return msg
				}
				msg.evs = append(msg.evs, ev)
			default:
				return msg
			}
		}
		return msg
	}
}

// ---- docker events stream ----

// debounceDelay coalesce les rafales d'événements (ex: tous les conteneurs
// d'un `up`/`down` de stack) en un seul rechargement de la liste.
const debounceDelay = 300 * time.Millisecond

// reconnectDelay avant de se réabonner après une coupure du flux d'événements
// (ex: redémarrage du daemon Docker).
const reconnectDelay = 2 * time.Second

// loadStacksTimeout borne le rechargement de la liste : sans lui, un daemon
// qui ne répond plus bloquerait ce refresh (et l'indicateur « actualisation… »)
// indéfiniment.
const loadStacksTimeout = 30 * time.Second

type dockerEventsStartedMsg struct{ ch <-chan compose.DockerEvent }
type dockerEventMsg compose.DockerEvent
type dockerEventsClosedMsg struct{}
type debounceRefreshMsg struct{ gen int }

// subscribeDockerEvents lance l'abonnement au flux d'événements Docker.
func subscribeDockerEvents(client dockerService) tea.Cmd {
	return func() tea.Msg {
		ch := client.SubscribeEvents(context.Background())
		return dockerEventsStartedMsg{ch: ch}
	}
}

// readDockerEventCmd lit le prochain événement du flux. Sur le modèle de
// readProgressCmd/readLogCmd : le canal vit dans le Model, et la fermeture
// du canal (fin du flux/erreur) est signalée par dockerEventsClosedMsg.
func readDockerEventCmd(ch <-chan compose.DockerEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return dockerEventsClosedMsg{}
		}
		return dockerEventMsg(ev)
	}
}

// debounceRefresh programme une vérification après debounceDelay : si aucun
// événement plus récent n'est arrivé entre-temps (gen inchangé), la liste
// des stacks est rechargée.
func debounceRefresh(gen int) tea.Cmd {
	return tea.Tick(debounceDelay, func(time.Time) tea.Msg {
		return debounceRefreshMsg{gen: gen}
	})
}

func initClient() tea.Cmd {
	return func() tea.Msg {
		c, err := compose.New()
		if err != nil {
			return fatalErrMsg(err)
		}
		return clientReadyMsg{c}
	}
}

// collectMetricsNow gathers metrics once, immediately, so the header is populated
// on the first frame instead of staying blank until the first 2s tick.
func collectMetricsNow() tea.Cmd {
	return func() tea.Msg {
		return metricsMsg(monitor.Collect())
	}
}

// prewarmStacks walks the stack directory in the background so the (potentially
// slow) filesystem scan overlaps the Docker client init instead of running after.
func prewarmStacks(stackDir string) tea.Cmd {
	if stackDir == "" {
		return nil
	}
	return func() tea.Msg {
		compose.PrewarmComposeCache(stackDir)
		return nil
	}
}

// loadStacks liste les stacks puis retire celles qui tombent sous exclude
// (copie de cfg.Exclude : le Cmd tourne hors de la boucle Update).
func loadStacks(client dockerService, stackDir string, exclude []string) tea.Cmd {
	return func() tea.Msg {
		// Défense en profondeur : un appelant qui passerait un client nil
		// (init Docker échoué) ferait paniquer ListStacks dans ce Cmd.
		if client == nil {
			return fatalErrMsg(errors.New("client Docker non initialisé (R pour réessayer)"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadStacksTimeout)
		defer cancel()
		at := time.Now()
		stacks, err := client.ListStacks(ctx, stackDir)
		if err != nil {
			return stacksLoadedMsg{err: err, dir: stackDir, at: at}
		}
		if len(exclude) > 0 {
			ex := config.Config{Exclude: exclude}
			kept := stacks[:0]
			for _, st := range stacks {
				if !ex.IsExcluded(st.Name) {
					kept = append(kept, st)
				}
			}
			stacks = kept
		}
		return stacksLoadedMsg{stacks: stacks, dir: stackDir, at: at}
	}
}

// loadStacksCmd recharge la liste avec la config courante.
func (m Model) loadStacksCmd() tea.Cmd {
	return loadStacks(m.client, m.cfg.StackDir, slices.Clone(m.cfg.Exclude))
}

func loadBackups(configDir string) tea.Cmd {
	return func() tea.Msg {
		snaps, err := backup.List(configDir)
		if err != nil {
			return opErrMsg{err: err}
		}
		return backupsLoadedMsg(snaps)
	}
}

// captureEntries : les stacks à mémoriser (en marche, partielles ou unhealthy),
// chacune avec les services qui tournent — pour ne pas relancer plus tard des
// services arrêtés volontairement.
func captureEntries(stacks []compose.Stack) []backup.Entry {
	out := make([]backup.Entry, 0, len(stacks))
	for _, s := range stacks {
		switch s.State() {
		case compose.StateRunning, compose.StatePartial, compose.StateUnhealthy:
			e := backup.Entry{Name: s.Name}
			for _, sv := range s.Services {
				if sv.Running > 0 {
					e.Services = append(e.Services, sv.Name)
				}
			}
			out = append(out, e)
		}
	}
	return out
}

func saveBackup(configDir, stackDir string, stacks []compose.Stack) tea.Cmd {
	return func() tea.Msg {
		entries := captureEntries(stacks)
		pruned, err := backup.Save(configDir, stackDir, entries)
		return opDoneMsg{err: err, n: len(entries), pruned: pruned}
	}
}

// backupMetaMsg : résultat d'une modification de libellé / d'épinglage.
type backupMetaMsg struct {
	err  error
	done string
}

func setBackupMeta(configDir string, snap backup.Snapshot, label string, pinned bool, done string) tea.Cmd {
	return func() tea.Msg {
		return backupMetaMsg{err: backup.SetMeta(configDir, snap, label, pinned), done: done}
	}
}

// livePhase : une étape d'une opération composée (alignement). suffix est
// ajouté au nom de stack dans la vue de progression pour que deux étapes sur la
// même stack ne soient ni fusionnées ni comptées deux fois.
type livePhase struct {
	suffix string
	start  func() <-chan compose.ProgressEvent
}

// chainLive exécute les étapes l'une après l'autre et relaie leurs événements
// dans un seul flux terminé par la sentinelle AllDone. Une annulation du
// contexte empêche de démarrer les étapes suivantes.
func chainLive(ctx context.Context, phases []livePhase) <-chan compose.ProgressEvent {
	out := make(chan compose.ProgressEvent, 200)
	go func() {
		for _, ph := range phases {
			if ctx.Err() != nil {
				break
			}
			ch := ph.start()
			for {
				ev := <-ch
				if ev.AllDone {
					break
				}
				if ph.suffix != "" && ev.StackName != "" {
					ev.StackName += ph.suffix
				}
				out <- ev
			}
		}
		out <- compose.ProgressEvent{AllDone: true}
	}()
	return out
}

// ---- occupation Docker (en-tête) ----

// usageInterval : compteurs de conteneurs (≈7 ms côté démon).
// volumesInterval : la taille des volumes oblige le démon à parcourir leurs
// données (≈1,5 s ici, bien plus sur de gros volumes) — on l'espace. La
// taille des images (≈115 ms) n'est relue que sur événement (imagesDelay
// après le premier), et à la même cadence que les volumes en filet.
const (
	usageInterval   = 30 * time.Second
	volumesInterval = 10 * time.Minute
	usageTimeout    = 2 * time.Minute
	imagesDelay     = 2 * time.Second
)

type usageMsg struct {
	opts  compose.UsageOptions // mesures demandées
	usage compose.DockerUsage
	err   error
}
type usageTickMsg struct{}

// imagesRefreshMsg : relecture des images après un événement qui les touche.
type imagesRefreshMsg struct{}

func fetchUsage(client dockerService, opts compose.UsageOptions) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), usageTimeout)
		defer cancel()
		u, err := client.Usage(ctx, opts)
		return usageMsg{opts: opts, usage: u, err: err}
	}
}

// refreshImagesIn programme la relecture des images. Le délai regroupe les
// rafales (un up de 30 services crée 30 conteneurs).
func refreshImagesIn(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return imagesRefreshMsg{} })
}

func tickUsage() tea.Cmd {
	return tea.Tick(usageInterval, func(time.Time) tea.Msg { return usageTickMsg{} })
}

type backupDeletedMsg struct{ err error }

func deleteBackup(configDir string, snap backup.Snapshot) tea.Cmd {
	return func() tea.Msg {
		return backupDeletedMsg{err: backup.Delete(configDir, snap)}
	}
}

// logsStartedMsg porte le canal de la session de logs qui vient de démarrer ;
// le Model le conserve pour planifier les lectures suivantes. seq identifie
// la session (cf. Model.logSeq).
type logsStartedMsg struct {
	ch  <-chan compose.LogEntry
	seq int
}

func startLogs(ctx context.Context, client dockerService, stack compose.Stack, seq int) tea.Cmd {
	return func() tea.Msg {
		ch, err := client.LogsAsync(ctx, stack)
		if err != nil {
			return opErrMsg{err: err}
		}
		return logsStartedMsg{ch: ch, seq: seq}
	}
}

// logBatchMax borne le nombre de lignes regroupées en un seul message : assez
// pour amortir les rafales sans rendre le rendu saccadé ni bloquer la lecture.
const logBatchMax = compose.LogBuffer

// logFrame : durée pendant laquelle un lot attend les lignes suivantes. Une
// image à 20 images/s : bubbletea recalcule toute la vue après chaque
// message, et des lignes arrivant une à une (≈ 500/s pour un conteneur
// bavard ordinaire) coûtaient un rendu complet chacune, soit 40 % d'un cœur.
// L'écran n'étant redessiné qu'à cette cadence, l'attente ne se voit pas.
// Pendant l'attente, la lecture dort au lieu de guetter le canal : chaque
// ligne aurait sinon réveillé la goroutine de lecture (compose.LogBuffer
// lignes d'avance tiennent dans le canal).
const logFrame = 50 * time.Millisecond

func readLogCmd(ch <-chan compose.LogEntry, seq int) tea.Cmd {
	return func() tea.Msg {
		// Lecture bloquante de la première ligne, puis on laisse les suivantes
		// s'accumuler pendant une image et on les livre d'un coup.
		entry, ok := <-ch
		if !ok {
			return logDoneMsg{seq: seq}
		}
		time.Sleep(logFrame)
		batch := make([]compose.LogEntry, 1, min(len(ch)+1, logBatchMax))
		batch[0] = entry
		for len(batch) < logBatchMax {
			select {
			case e, ok := <-ch:
				if !ok {
					// Canal fermé en cours de lot : on livre le lot ; le prochain
					// readLogCmd relira le canal fermé et renverra logDoneMsg.
					return logLinesMsg{entries: batch, seq: seq}
				}
				batch = append(batch, e)
			default:
				return logLinesMsg{entries: batch, seq: seq}
			}
		}
		return logLinesMsg{entries: batch, seq: seq}
	}
}
