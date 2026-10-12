package tui

import (
	"strconv"
	"strings"
)

// dockerStatusFR traduit le statut d'un conteneur tel que le démon le rédige
// (State.String : « Exited (137) 2 hours ago », « Up 3 days (healthy) »…,
// durées de go-units). Le code de sortie et l'ancienneté sont ce qui oriente
// le diagnostic : un 0 ne se lit pas comme un 137. Un texte non reconnu est
// rendu tel quel : mieux vaut l'anglais qu'une traduction fausse.
func dockerStatusFR(status string) string {
	s := strings.TrimSpace(status)
	switch s {
	case "":
		return ""
	case "Created":
		return "créé, jamais démarré"
	case "Dead":
		return "dead (suppression inachevée)"
	case "Removal In Progress":
		return "suppression en cours"
	}
	if rest, ok := strings.CutPrefix(s, "Up "); ok {
		dur, note := rest, ""
		if i := strings.LastIndex(rest, " ("); i >= 0 && strings.HasSuffix(rest, ")") {
			dur, note = rest[:i], rest[i+2:len(rest)-1]
		}
		d, ok := durationFR(dur)
		if !ok {
			return status
		}
		switch note {
		case "":
			return "en marche depuis " + d
		case "healthy":
			return "en marche depuis " + d + " · sain"
		case "unhealthy":
			return "en marche depuis " + d + " · unhealthy"
		case "health: starting":
			return "en marche depuis " + d + " · healthcheck en cours"
		case "Paused":
			return "en pause, démarré il y a " + d
		}
		return status
	}
	// « Exited (N) … ago » et « Restarting (N) … ago » : code et ancienneté.
	// Pour un conteneur arrêté, le mot d'état est déjà sur la ligne du
	// service : seul le redémarrage en boucle mérite d'être nommé.
	for _, p := range []struct{ en, fr string }{{"Exited (", ""}, {"Restarting (", "redémarre · "}} {
		rest, ok := strings.CutPrefix(s, p.en)
		if !ok {
			continue
		}
		code, ago, ok := strings.Cut(rest, ") ")
		if !ok {
			return status
		}
		dur, ok := strings.CutSuffix(ago, " ago")
		if !ok {
			return status
		}
		d, ok := durationFR(dur)
		if _, err := strconv.Atoi(code); err != nil || !ok {
			return status
		}
		return p.fr + "code " + code + " · il y a " + d
	}
	return status
}

// durationFR traduit une durée de go-units (HumanDuration) dans les unités
// courtes de l'interface (relativeTime) : « 2 hours » → « 2 h ».
func durationFR(d string) (string, bool) {
	switch d {
	case "Less than a second":
		return "moins d'1 s", true
	case "About a minute":
		return "1 min", true
	case "About an hour":
		return "1 h", true
	}
	num, unit, ok := strings.Cut(d, " ")
	n, err := strconv.Atoi(num)
	if !ok || err != nil {
		return "", false
	}
	switch strings.TrimSuffix(unit, "s") {
	case "second":
		unit = "s"
	case "minute":
		unit = "min"
	case "hour":
		unit = "h"
	case "day":
		unit = "j"
	case "week":
		unit = "sem."
	case "month":
		unit = "mois"
	case "year":
		unit = "an" + plural(n)
	default:
		return "", false
	}
	return num + " " + unit, true
}
