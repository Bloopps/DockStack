package compose

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Bloopps/dockstack/internal/fakedocker"
)

// fakeDaemonClient : vrai client dockstack branché sur un faux démon.
func fakeDaemonClient(t *testing.T) (*Client, *fakedocker.Daemon) {
	t.Helper()
	d, err := fakedocker.Start(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	t.Setenv("DOCKER_HOST", d.Host())
	t.Setenv("DOCKER_CONTEXT", "")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return c, d
}

// goroutinesIn compte les goroutines dont la pile contient fn.
func goroutinesIn(fn string) int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, fn) {
			n++
		}
	}
	return n
}

// Plus de 32 événements en attente (tampon plein, plus personne ne lit) puis
// annulation : la goroutine de SubscribeEvents doit se terminer. Avant, elle
// restait bloquée pour toujours sur l'envoi, sans écouter le contexte.
func TestSubscribeEventsCancelWithFullBuffer(t *testing.T) {
	c, d := fakeDaemonClient(t)
	const fn = "compose.(*Client).SubscribeEvents"
	base := goroutinesIn(fn)

	ctx, cancel := context.WithCancel(context.Background())
	ch := c.SubscribeEvents(ctx)
	if err := d.WaitSubscriber(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	ectx, ecancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ecancel()
	if err := d.Emit(ectx, 100, fakedocker.Event{Type: "container", Action: "start"}); err != nil {
		t.Fatal(err)
	}
	// Laisse le tampon de 32 se remplir.
	deadline := time.Now().Add(5 * time.Second)
	for len(ch) < cap(ch) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(ch) != cap(ch) {
		t.Fatalf("tampon %d/%d : la rafale n'est pas arrivée", len(ch), cap(ch))
	}
	cancel()

	deadline = time.Now().Add(3 * time.Second)
	for goroutinesIn(fn) > base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := goroutinesIn(fn); n > base {
		t.Fatalf("goroutine de SubscribeEvents toujours là après annulation (%d > %d)", n, base)
	}
}
