package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/monitor"
)

// ---- progress stream ----

// Les messages de progression portent le numéro d'opération (cf.
// Model.progressSeq, même recette que logSeq) : un message en vol d'une
// opération précédente est ignoré au lieu d'être appliqué à l'état de la
// suivante — sans ça, deux pompes pourraient lire le même canal et mélanger
// les flux de deux opérations.
type progressEventMsg struct {
	ev  compose.ProgressEvent
	seq int
}
type progressAllDoneMsg struct{ seq int }

// readProgressCmd lit le prochain événement du flux. Le canal vit dans le
// Model (pas de variable globale) : chaque opération a son propre flux.
// La fin est signalée par la sentinelle AllDone (le canal n'est pas fermé).
func readProgressCmd(ch <-chan compose.ProgressEvent, seq int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok || ev.AllDone {
			return progressAllDoneMsg{seq: seq}
		}
		return progressEventMsg{ev: ev, seq: seq}
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
// qui ne répond plus bloquerait le refresh (et l'indicateur « actualisation… »)
// pour toujours — les ticks suivants étant inhibés tant que m.refreshing dure.
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

func loadStacks(client dockerService, stackDir string) tea.Cmd {
	return func() tea.Msg {
		// Défense en profondeur : un appelant qui passerait un client nil
		// (init Docker échoué) ferait paniquer ListStacks dans ce Cmd.
		if client == nil {
			return fatalErrMsg(errors.New("client Docker non initialisé (R pour réessayer)"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadStacksTimeout)
		defer cancel()
		stacks, err := client.ListStacks(ctx, stackDir)
		if err != nil {
			return fatalErrMsg(err)
		}
		return stacksLoadedMsg(stacks)
	}
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

func saveBackup(configDir, stackDir string, stacks []compose.Stack) tea.Cmd {
	return func() tea.Msg {
		running := make([]string, 0, len(stacks))
		for _, s := range stacks {
			switch s.State() {
			case compose.StateRunning, compose.StatePartial, compose.StateUnhealthy:
				running = append(running, s.Name)
			}
		}
		return opDoneMsg{err: backup.Save(configDir, stackDir, running)}
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
const logBatchMax = 256

func readLogCmd(ch <-chan compose.LogEntry, seq int) tea.Cmd {
	return func() tea.Msg {
		// Lecture bloquante de la première ligne, puis on vide ce qui est déjà
		// disponible sans attendre, pour livrer un lot d'un coup.
		entry, ok := <-ch
		if !ok {
			return logDoneMsg{seq: seq}
		}
		batch := make([]compose.LogEntry, 1, logBatchMax)
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
