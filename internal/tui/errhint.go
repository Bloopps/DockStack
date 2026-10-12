package tui

import (
	"regexp"
	"strings"
)

// Conseils pour les erreurs du démon les plus courantes. Le message d'origine
// reste affiché tel quel (on peut le chercher), le conseil dit en dessous
// quoi faire : « manifest unknown » ou « port is already allocated » ne
// disent pas où regarder.

// errHintRule : motif cherché dans le message (en minuscules) et conseil,
// calculé à partir du message d'origine quand il en reprend un morceau
// (port, registre).
type errHintRule struct {
	pattern string
	hint    func(msg string) string
}

var (
	// « Bind for 0.0.0.0:8080 failed: port is already allocated », ou
	// « listen tcp4 0.0.0.0:80: bind: address already in use ».
	busyPortRe = regexp.MustCompile(`:(\d+)(?: failed: port is already allocated|: bind: address already in use)`)
	// « pull access denied for ghcr.io/org/app, repository does not exist… »
	deniedRefRe = regexp.MustCompile(`pull access denied for ([^\s,]+)`)
)

var errHintRules = []errHintRule{
	{"manifest unknown", func(string) string {
		return "étiquette absente du registre : corriger image: dans le compose"
	}},
	{"pull access denied", func(msg string) string {
		return "image inexistante ou privée : vérifier son nom, sinon " + dockerLogin(msg)
	}},
	{"port is already allocated", busyPortHint},
	{"address already in use", busyPortHint},
	{"no space left on device", func(string) string {
		return "disque plein : Nettoyage (n depuis la liste)"
	}},
	{"toomanyrequests", func(string) string {
		return "limite de téléchargements Docker Hub atteinte : réessayer plus tard, ou docker login"
	}},
}

// errHint renvoie le conseil associé à un message d'erreur, "" si aucun.
func errHint(msg string) string {
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)
	for _, r := range errHintRules {
		if strings.Contains(lower, r.pattern) {
			return r.hint(msg)
		}
	}
	return ""
}

func busyPortHint(msg string) string {
	if m := busyPortRe.FindStringSubmatch(msg); m != nil {
		return "port " + m[1] + " déjà pris : ss -ltnp | grep :" + m[1]
	}
	return "port déjà pris sur l'hôte : ss -ltnp"
}

// dockerLogin : la commande de connexion au registre de l'image refusée.
// Comme Docker, le premier segment n'est un registre que s'il contient un
// point ou un port, ou vaut localhost ; sinon c'est Docker Hub.
func dockerLogin(msg string) string {
	m := deniedRefRe.FindStringSubmatch(msg)
	if m == nil {
		return "docker login"
	}
	host, _, found := strings.Cut(m[1], "/")
	if found && (strings.ContainsAny(host, ".:") || host == "localhost") {
		return "docker login " + host
	}
	return "docker login"
}
