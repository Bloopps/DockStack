package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/Bloopps/dockstack/internal/backup"
	"github.com/Bloopps/dockstack/internal/compose"
	"github.com/Bloopps/dockstack/internal/monitor"
)

// ---- progress stream ----

type progressEventMsg compose.ProgressEvent
type progressAllDoneMsg struct{}

// readProgressCmd lit le prochain événement du flux. Le canal vit dans le
// Model (pas de variable globale) : chaque opération a son propre flux.
// La fin est signalée par la sentinelle AllDone (le canal n'est pas fermé).
func readProgressCmd(ch <-chan compose.ProgressEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok || ev.AllDone {
			return progressAllDoneMsg{}
		}
		return progressEventMsg(ev)
	}
}

func initClient() tea.Cmd {
	return func() tea.Msg {
		c, err := compose.New()
		if err != nil {
			return errMsg(err)
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

func loadStacks(client *compose.Client, stackDir string) tea.Cmd {
	return func() tea.Msg {
		stacks, err := client.ListStacks(context.Background(), stackDir)
		if err != nil {
			return errMsg(err)
		}
		return stacksLoadedMsg(stacks)
	}
}

func loadBackups(configDir string) tea.Cmd {
	return func() tea.Msg {
		snaps, err := backup.List(configDir)
		if err != nil {
			return errMsg(err)
		}
		return backupsLoadedMsg(snaps)
	}
}

func runOp(fn func() error) tea.Cmd {
	return func() tea.Msg {
		return opDoneMsg{err: fn()}
	}
}

func saveBackup(configDir, stackDir string, stacks []compose.Stack) tea.Cmd {
	return func() tea.Msg {
		running := make([]string, 0, len(stacks))
		for _, s := range stacks {
			if s.State() == compose.StateRunning || s.State() == compose.StatePartial {
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

func startLogs(ctx context.Context, client *compose.Client, stack compose.Stack, seq int) tea.Cmd {
	return func() tea.Msg {
		ch, err := client.LogsAsync(ctx, stack)
		if err != nil {
			return errMsg(err)
		}
		return logsStartedMsg{ch: ch, seq: seq}
	}
}

func readLogCmd(ch <-chan compose.LogEntry, seq int) tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-ch
		if !ok {
			return logDoneMsg{seq: seq}
		}
		return logLineMsg{entry: entry, seq: seq}
	}
}
