package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Bloopps/dockstack/internal/compose"
)

// Chaque erreur courante du démon reçoit son conseil, avec le port ou le
// registre repris du message ; une erreur inconnue n'en reçoit aucun.
func TestErrHint(t *testing.T) {
	for _, tc := range []struct{ msg, want string }{
		{"Error response from daemon: manifest for app:9 not found: manifest unknown: manifest unknown",
			"étiquette absente du registre : corriger image: dans le compose"},
		{"Error response from daemon: driver failed programming external connectivity on endpoint web: Bind for 0.0.0.0:8080 failed: port is already allocated",
			"port 8080 déjà pris : ss -ltnp | grep :8080"},
		{"Error starting userland proxy: listen tcp4 0.0.0.0:80: bind: address already in use",
			"port 80 déjà pris : ss -ltnp | grep :80"},
		{"pull access denied for ghcr.io/org/privee, repository does not exist or may require 'docker login'",
			"image inexistante ou privée : vérifier son nom, sinon docker login ghcr.io"},
		{"pull access denied for registry.local:5000/app, repository does not exist",
			"image inexistante ou privée : vérifier son nom, sinon docker login registry.local:5000"},
		{"pull access denied for monorg/app, repository does not exist",
			"image inexistante ou privée : vérifier son nom, sinon docker login"},
		{"write /var/lib/docker/tmp/x: no space left on device", "disque plein : Nettoyage (n depuis la liste)"},
		{"toomanyrequests: You have reached your pull rate limit.", "limite de téléchargements Docker Hub atteinte : réessayer plus tard, ou docker login"},
		{"Conflict. The container name \"/web\" is already in use", ""},
		{"", ""},
	} {
		if got := errHint(tc.msg); got != tc.want {
			t.Errorf("errHint(%q) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

// Le conseil s'affiche en dessous de l'erreur, dans le bloc de la stack.
func TestErrHintInProgress(t *testing.T) {
	m := opProgress("Up: web")
	msg := "Bind for 0.0.0.0:8080 failed: port is already allocated"
	m.progress.apply(compose.ProgressEvent{StackName: "web", Container: "Container web", Status: msg, State: "error"})
	m.progress.apply(compose.ProgressEvent{StackName: "web", StackDone: true, Err: errors.New(msg)})
	lines, _ := m.progressLines(0, 100)
	out := ansi.Strip(strings.Join(lines, "\n"))
	if i, j := strings.Index(out, msg), strings.Index(out, "→ port 8080 déjà pris"); i < 0 || j < i {
		t.Errorf("conseil absent ou avant l'erreur :\n%s", out)
	}
}
