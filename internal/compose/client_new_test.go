package compose

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Démon qui accepte les connexions sans jamais répondre : New échoue au bout
// de pingTimeout au lieu de rendre un client dont tous les appels resteraient
// bloqués derrière la négociation de version.
func TestNewHungDaemon(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "muet.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	t.Setenv("DOCKER_HOST", "unix://"+sock)
	old := pingTimeout
	pingTimeout = 300 * time.Millisecond
	defer func() { pingTimeout = old }()

	t0 := time.Now()
	c, err := New()
	if err == nil || c != nil {
		t.Fatal("un démon muet doit faire échouer New")
	}
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("New a mis %v", d)
	}
	if !strings.Contains(err.Error(), "ne répond pas") {
		t.Fatalf("message : %v", err)
	}
}

// Socket absent : échec immédiat, avec le message du client Docker.
func TestNewNoDaemon(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "absent.sock"))
	if _, err := New(); err == nil || strings.Contains(err.Error(), "ne répond pas") {
		t.Fatalf("socket absent : %v", err)
	}
}
