package backup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Snapshot struct {
	Path     string
	Date     time.Time
	StackDir string
	Stacks   []string
}

func List(configDir string) ([]Snapshot, error) {
	base := filepath.Join(configDir, "active_stacks")
	matches, _ := filepath.Glob(base + ".*")

	var snaps []Snapshot
	// Include the main file if it exists
	if _, err := os.Stat(base); err == nil {
		if s, err := load(base); err == nil {
			snaps = append(snaps, s)
		}
	}
	for _, m := range matches {
		if s, err := load(m); err == nil {
			snaps = append(snaps, s)
		}
	}

	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].Date.After(snaps[j].Date)
	})

	// Save écrit le même contenu dans `active_stacks` (dernier état) et dans
	// `active_stacks.<ts>` (archive) : sans déduplication, la dernière
	// capture apparaît deux fois.
	type key struct {
		date   time.Time
		stacks int
	}
	seen := make(map[key]bool, len(snaps))
	out := snaps[:0]
	for _, s := range snaps {
		k := key{s.Date, len(s.Stacks)}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out, nil
}

func load(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()

	s := Snapshot{Path: path}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "# Timestamp: "):
			ts := strings.TrimPrefix(line, "# Timestamp: ")
			t, _ := time.Parse("2006-01-02_15-04-05", ts)
			s.Date = t
		case strings.HasPrefix(line, "# Stack dir: "):
			s.StackDir = strings.TrimPrefix(line, "# Stack dir: ")
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			s.Stacks = append(s.Stacks, line)
		}
	}
	return s, sc.Err()
}

func Save(configDir, stackDir string, stacks []string) error {
	now := time.Now()
	ts := now.Format("2006-01-02_15-04-05")
	base := filepath.Join(configDir, "active_stacks")

	content := fmt.Sprintf(
		"# Sauvegarde créée le %s\n# Timestamp: %s\n# Stack dir: %s\n# Nombre de stacks: %d\n#\n%s",
		now.Format("02/01/2006 à 15:04:05"),
		ts, stackDir, len(stacks),
		strings.Join(stacks, "\n")+"\n",
	)
	data := []byte(content)

	if err := os.WriteFile(base+"."+ts, data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(base, data, 0o644); err != nil {
		return err
	}

	pruneOld(base, 10)
	return nil
}

func pruneOld(base string, max int) {
	matches, _ := filepath.Glob(base + ".*")
	if len(matches) <= max {
		return
	}
	sort.Strings(matches)
	for _, f := range matches[:len(matches)-max] {
		os.Remove(f)
	}
}
