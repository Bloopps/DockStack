package tui

import (
	"context"

	"github.com/Bloopps/dockstack/internal/compose"
)

// dockerService est la surface de *compose.Client dont le TUI a besoin. En
// dépendre via une interface (plutôt que le type concret) permet d'injecter un
// faux client dans les tests, sans démon Docker ni stacks réelles.
// *compose.Client la satisfait tel quel (cf. l'assertion ci-dessous).
type dockerService interface {
	// Lecture de l'état
	ListStacks(ctx context.Context, stackDir string) ([]compose.Stack, error)
	SubscribeEvents(ctx context.Context) <-chan compose.DockerEvent
	LogsAsync(ctx context.Context, stack compose.Stack) (<-chan compose.LogEntry, error)

	// Actions groupées streamées vers la vue de progression : un canal
	// d'événements par opération, fermé par la sentinelle AllDone.
	// (La restauration de capture passe aussi par UpManyLive.)
	UpManyLive(ctx context.Context, stacks []compose.Stack, parallel int) <-chan compose.ProgressEvent
	DownManyLive(ctx context.Context, stacks []compose.Stack, parallel int) <-chan compose.ProgressEvent
	RestartManyLive(ctx context.Context, stacks []compose.Stack, parallel int) <-chan compose.ProgressEvent
	RecreateManyLive(ctx context.Context, stacks []compose.Stack, parallel int) <-chan compose.ProgressEvent
	PullManyLive(ctx context.Context, stacks []compose.Stack, parallel int) <-chan compose.ProgressEvent
	RemoveManyLive(ctx context.Context, stacks []compose.Stack, parallel int) <-chan compose.ProgressEvent
}

// Vérifie à la compilation que le client concret satisfait l'interface.
var _ dockerService = (*compose.Client)(nil)
