package compose

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// collectLive lance runLive et renvoie, dans l'ordre, les événements émis.
func collectLive(t *testing.T, ctx context.Context, stacks []Stack, parallel int, fn stackLiveFn) []ProgressEvent {
	t.Helper()
	ch := make(chan ProgressEvent, 100)
	done := make(chan struct{})
	go func() { (&Client{}).runLive(ctx, stacks, parallel, ch, fn); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runLive bloqué")
	}
	close(ch)
	var evs []ProgressEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func liveStacks(n int) []Stack {
	stacks := make([]Stack, n)
	for i := range stacks {
		stacks[i].Name = fmt.Sprintf("s%d", i)
	}
	return stacks
}

// Toutes les stacks s'annoncent en file d'emblée, puis chacune signale son
// départ et sa fin : une stack qui attend sa place est visible dès le début.
func TestRunLiveAnnouncesQueue(t *testing.T) {
	evs := collectLive(t, context.Background(), liveStacks(3), 1,
		func(context.Context, Stack, chan<- ProgressEvent) error { return nil })
	var got []string
	for _, ev := range evs {
		switch {
		case ev.StackQueued:
			got = append(got, "Q"+ev.StackName)
		case ev.StackStarted:
			got = append(got, "S"+ev.StackName)
		case ev.StackDone:
			got = append(got, "D"+ev.StackName)
		}
	}
	want := "[Qs0 Qs1 Qs2 Ss0 Ds0 Ss1 Ds1 Ss2 Ds2]"
	if fmt.Sprint(got) != want {
		t.Errorf("événements %v, want %s", got, want)
	}
}

// Après une annulation, les stacks qui attendaient leur place ne sont jamais
// lancées (fn n'est pas appelée avec un contexte annulé) : chacune se
// termine aussitôt sur context.Canceled, sans annonce de départ.
func TestRunLiveCancelSkipsQueued(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	started := make(chan struct{}, 5)
	fn := func(ctx context.Context, _ Stack, _ chan<- ProgressEvent) error {
		calls.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		<-started
		<-started
		cancel() // deux stacks en cours, trois en file
	}()
	evs := collectLive(t, ctx, liveStacks(5), 2, fn)
	if n := calls.Load(); n != 2 {
		t.Errorf("fn appelée %d fois, want 2", n)
	}
	var startedN, doneN int
	for _, ev := range evs {
		if ev.StackStarted {
			startedN++
		}
		if ev.StackDone {
			doneN++
			if !errors.Is(ev.Err, context.Canceled) {
				t.Errorf("%s : err %v, want context.Canceled", ev.StackName, ev.Err)
			}
		}
	}
	if startedN != 2 || doneN != 5 {
		t.Errorf("%d départs, %d fins ; want 2 et 5", startedN, doneN)
	}
}
