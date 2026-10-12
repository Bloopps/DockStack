package tui

import "testing"

// Les statuts que rédige le démon (State.String, durées de go-units) sont
// traduits avec leur code de sortie et leur ancienneté ; un statut inconnu
// reste tel quel plutôt que mal traduit.
func TestDockerStatusFR(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Exited (137) 2 hours ago", "code 137 · il y a 2 h"},
		{"Exited (0) 3 weeks ago", "code 0 · il y a 3 sem."},
		{"Exited (1) About a minute ago", "code 1 · il y a 1 min"},
		{"Exited (1) Less than a second ago", "code 1 · il y a moins d'1 s"},
		{"Restarting (1) 5 seconds ago", "redémarre · code 1 · il y a 5 s"},
		{"Up 3 days (healthy)", "en marche depuis 3 j · sain"},
		{"Up About an hour (unhealthy)", "en marche depuis 1 h · unhealthy"},
		{"Up 12 seconds (health: starting)", "en marche depuis 12 s · healthcheck en cours"},
		{"Up 2 years", "en marche depuis 2 ans"},
		{"Up 4 months (Paused)", "en pause, démarré il y a 4 mois"},
		{"Created", "créé, jamais démarré"},
		{"Dead", "dead (suppression inachevée)"},
		{"", ""},
		// Inconnus : rendus tels quels.
		{"Up 3 fortnights", "Up 3 fortnights"},
		{"Exited (x) 2 hours ago", "Exited (x) 2 hours ago"},
		{"Up 2 hours (bizarre)", "Up 2 hours (bizarre)"},
		{"Removing", "Removing"},
	} {
		if got := dockerStatusFR(tc.in); got != tc.want {
			t.Errorf("dockerStatusFR(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
