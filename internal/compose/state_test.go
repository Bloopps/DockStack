package compose

import "testing"

// TestAggregateState vérifie la classification d'état, qui pilote le tri, les
// pastilles et les confirmations dans le TUI. La priorité « malade » prime sur
// tout le reste, y compris quand la stack est par ailleurs arrêtée.
func TestAggregateState(t *testing.T) {
	cases := []struct {
		name                      string
		running, total, unhealthy int
		want                      State
	}{
		{"non déployée (total 0)", 0, 0, 0, StateUnknown},
		{"tout en marche", 3, 3, 0, StateRunning},
		{"un conteneur malade prime", 3, 3, 1, StateUnhealthy},
		{"malade prime même à l'arrêt", 0, 3, 2, StateUnhealthy},
		{"partielle", 1, 3, 0, StatePartial},
		{"tout arrêté", 0, 3, 0, StateStopped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Stack{Running: c.running, Total: c.total, Unhealthy: c.unhealthy}
			if got := s.State(); got != c.want {
				t.Errorf("Stack.State() = %v, want %v", got, c.want)
			}
			// ServiceStatus.State() doit donner le même verdict (même règle).
			ss := ServiceStatus{Running: c.running, Total: c.total, Unhealthy: c.unhealthy}
			if got := ss.State(); got != c.want {
				t.Errorf("ServiceStatus.State() = %v, want %v", got, c.want)
			}
		})
	}
}
