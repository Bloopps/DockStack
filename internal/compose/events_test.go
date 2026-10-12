package compose

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestDockerEventRouting(t *testing.T) {
	for _, tc := range []struct {
		ev             DockerEvent
		stacks, images bool
	}{
		{DockerEvent{"container", "start"}, true, false},
		{DockerEvent{"container", "die"}, true, false},
		{DockerEvent{"container", "create"}, true, true},
		{DockerEvent{"container", "destroy"}, true, true},
		{DockerEvent{"container", "health_status: unhealthy"}, true, false},
		{DockerEvent{"container", "exec_start: sh -c true"}, false, false},
		{DockerEvent{"container", "exec_die"}, false, false},
		{DockerEvent{"image", "pull"}, false, true},
		{DockerEvent{"image", "delete"}, false, true},
		{DockerEvent{"image", "untag"}, false, true},
		{DockerEvent{"image", "push"}, false, false},
		// Une action d'image ne vaut pas pour un conteneur, et inversement.
		{DockerEvent{"image", "start"}, false, false},
		{DockerEvent{"container", "delete"}, false, false},
	} {
		if got := IsRelevantDockerEvent(tc.ev); got != tc.stacks {
			t.Errorf("IsRelevantDockerEvent(%+v) = %v, attendu %v", tc.ev, got, tc.stacks)
		}
		if got := ImagesChanged(tc.ev); got != tc.images {
			t.Errorf("ImagesChanged(%+v) = %v, attendu %v", tc.ev, got, tc.images)
		}
	}
}

// Le filtre du démon doit laisser passer tout ce que la TUI exploite.
func TestEventFilters(t *testing.T) {
	f := eventFilters()
	types := slices.Sorted(maps.Keys(f["type"]))
	if !slices.Equal(types, []string{"container", "image"}) {
		t.Fatalf("types = %v", types)
	}
	events := f["event"]
	for a := range relevantEventActions {
		if !events[a] {
			t.Errorf("action de conteneur %q absente du filtre", a)
		}
	}
	for a := range imageEventActions {
		if !events[a] {
			t.Errorf("action d'image %q absente du filtre", a)
		}
	}
	if !events["health_status"] {
		t.Error("health_status absent du filtre")
	}
	for a := range events {
		if strings.HasPrefix(a, "exec_") {
			t.Errorf("%q ne doit pas être dans le filtre", a)
		}
	}
}
