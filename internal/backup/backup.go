// Package backup gère les captures d'état : la liste des stacks (et de leurs
// services) en marche à un instant donné, pour pouvoir les relancer ensuite.
// Une capture ne contient AUCUNE donnée, seulement des noms.
//
// Format courant (v2) : un fichier JSON par capture, capture.<horodatage>.json,
// avec libellé, épinglage et services par stack. Les anciennes captures texte
// (active_stacks.<horodatage>) restent lisibles ; les modifier (libellé,
// épinglage) les convertit en v2. Le fichier `active_stacks` (dernier état, texte)
// est toujours réécrit pour les scripts externes qui le liraient.
package backup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	tsLayout = "2006-01-02_15-04-05"
	// KeepUnpinned : nombre de captures non épinglées conservées ; les plus
	// anciennes au-delà sont supprimées à chaque Save.
	KeepUnpinned = 10
)

// Entry : une stack capturée. Services vide = la stack entière (captures
// anciennes, sans détail) ; sinon seulement ces services tournaient.
type Entry struct {
	Name     string   `json:"name"`
	Services []string `json:"services,omitempty"`
}

type Snapshot struct {
	Path     string
	Date     time.Time
	StackDir string
	Stacks   []string // noms, dans l'ordre de la capture
	Entries  []Entry  // même ordre ; vide pour une capture ancienne ou construite à la main
	Label    string
	Pinned   bool
	Legacy   bool // ancien format texte
}

// Items renvoie les entrées de la capture : Entries, ou à défaut Stacks
// (chacune sans détail de services).
func (s Snapshot) Items() []Entry {
	if len(s.Entries) > 0 {
		return s.Entries
	}
	out := make([]Entry, len(s.Stacks))
	for i, n := range s.Stacks {
		out[i] = Entry{Name: n}
	}
	return out
}

type fileV2 struct {
	Version  int       `json:"version"`
	Created  time.Time `json:"created"`
	Label    string    `json:"label,omitempty"`
	Pinned   bool      `json:"pinned,omitempty"`
	StackDir string    `json:"stack_dir"`
	Stacks   []Entry   `json:"stacks"`
}

func base(configDir string) string { return filepath.Join(configDir, "active_stacks") }

// List renvoie les captures, la plus récente d'abord. Un fichier illisible est
// ignoré ; une date illisible retombe sur la date de modification du fichier
// (jamais de capture perdue en silence).
func List(configDir string) ([]Snapshot, error) {
	var snaps []Snapshot
	add := func(path string, legacyMain bool) {
		s, err := load(path)
		if err != nil {
			return
		}
		s.Legacy = !strings.HasSuffix(path, ".json")
		if legacyMain {
			s.Path = path // le fichier « dernier état » : sert seulement de doublon
		}
		snaps = append(snaps, s)
	}
	v2, _ := filepath.Glob(filepath.Join(configDir, "capture.*.json"))
	for _, p := range v2 {
		add(p, false)
	}
	old, _ := filepath.Glob(base(configDir) + ".*")
	for _, p := range old {
		add(p, false)
	}
	if _, err := os.Stat(base(configDir)); err == nil {
		add(base(configDir), true)
	}

	// Plus récent d'abord ; à date égale le v2 passe avant l'ancien format.
	sort.SliceStable(snaps, func(i, j int) bool {
		if !snaps[i].Date.Equal(snaps[j].Date) {
			return snaps[i].Date.After(snaps[j].Date)
		}
		return !snaps[i].Legacy && snaps[j].Legacy
	})

	// Le fichier texte `active_stacks` (dernier état) duplique la capture la
	// plus récente : il est écarté dès qu'une autre capture porte sa date. Les
	// autres captures ne sont jamais écartées, même à date égale.
	// Clé en secondes Unix, pas en time.Time : la date du JSON (relue en UTC
	// quand le fuseau local est UTC) et celle du texte (heure locale) sont le
	// même instant avec deux Location différentes, donc deux clés de map
	// distinctes. Sur un serveur en UTC, la dernière capture apparaissait en
	// double.
	seen := make(map[int64]bool, len(snaps))
	out := snaps[:0]
	for _, s := range snaps {
		if s.Path == base(configDir) && seen[s.Date.Unix()] {
			continue
		}
		seen[s.Date.Unix()] = true
		out = append(out, s)
	}
	return out, nil
}

func load(path string) (Snapshot, error) {
	if strings.HasSuffix(path, ".json") {
		return loadV2(path)
	}
	return loadLegacy(path)
}

func loadV2(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, err
	}
	var f fileV2
	if err := json.Unmarshal(data, &f); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Path: path, Date: f.Created, StackDir: f.StackDir, Entries: f.Stacks, Label: f.Label, Pinned: f.Pinned}
	if s.Date.IsZero() {
		s.Date = modTime(path)
	}
	for _, e := range f.Stacks {
		s.Stacks = append(s.Stacks, e.Name)
	}
	return s, nil
}

func loadLegacy(path string) (Snapshot, error) {
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
			// Heure locale : l'ancien format n'enregistre pas de fuseau, et une
			// lecture en UTC décalait les dates relatives (« il y a … »).
			if t, err := time.ParseInLocation(tsLayout, strings.TrimPrefix(line, "# Timestamp: "), time.Local); err == nil {
				s.Date = t
			}
		case strings.HasPrefix(line, "# Stack dir: "):
			s.StackDir = strings.TrimPrefix(line, "# Stack dir: ")
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		default:
			s.Stacks = append(s.Stacks, line)
		}
	}
	if s.Date.IsZero() {
		s.Date = modTime(path)
	}
	return s, sc.Err()
}

func modTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().Truncate(time.Second)
	}
	return time.Time{}
}

// Save écrit une nouvelle capture (v2, écriture atomique) et met à jour le
// fichier texte `active_stacks`. Renvoie le nombre d'anciennes captures
// supprimées par la rotation (les épinglées ne le sont jamais).
func Save(configDir, stackDir string, entries []Entry) (pruned int, err error) {
	now := time.Now().Truncate(time.Second)
	// Le dossier n'existe que si la config a déjà été enregistrée.
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return 0, writeErr(configDir, err)
	}
	f := fileV2{Version: 2, Created: now, StackDir: stackDir, Stacks: entries}
	if _, err := writeV2(configDir, f); err != nil {
		return 0, err
	}

	// Fichier « dernier état » pour les scripts externes (format texte).
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	legacy := fmt.Sprintf(
		"# Sauvegarde créée le %s\n# Timestamp: %s\n# Stack dir: %s\n# Nombre de stacks: %d\n#\n%s",
		now.Format("02/01/2006 à 15:04:05"), now.Format(tsLayout), stackDir, len(names),
		strings.Join(names, "\n")+"\n")
	if err := writeAtomic(base(configDir), []byte(legacy)); err != nil {
		return 0, writeErr(configDir, err)
	}
	return prune(configDir), nil
}

// writeV2 écrit une capture v2 dans capture.<ts>.json ; deux captures dans la
// même seconde ne s'écrasent pas (suffixe _2, _3…).
func writeV2(configDir string, f fileV2) (string, error) {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	ts := f.Created.Format(tsLayout)
	path := filepath.Join(configDir, "capture."+ts+".json")
	for i := 2; ; i++ {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			break
		}
		path = filepath.Join(configDir, fmt.Sprintf("capture.%s_%d.json", ts, i))
	}
	if err := writeAtomic(path, data); err != nil {
		return "", writeErr(configDir, err)
	}
	return path, nil
}

// writeAtomic écrit dans un fichier temporaire du même dossier puis renomme :
// un arrêt en pleine écriture ne laisse jamais une capture tronquée.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".capture-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// SetMeta modifie le libellé et l'épinglage d'une capture. Une capture ancienne
// est d'abord convertie en v2 (son fichier d'origine est retiré).
func SetMeta(configDir string, snap Snapshot, label string, pinned bool) error {
	label = strings.TrimSpace(label)
	if !snap.Legacy {
		s, err := loadV2(snap.Path)
		if err != nil {
			return err
		}
		f := fileV2{Version: 2, Created: s.Date, Label: label, Pinned: pinned, StackDir: s.StackDir, Stacks: s.Entries}
		data, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		if err := writeAtomic(snap.Path, data); err != nil {
			return writeErr(configDir, err)
		}
		return nil
	}
	f := fileV2{Version: 2, Created: snap.Date, Label: label, Pinned: pinned, StackDir: snap.StackDir, Stacks: snap.Items()}
	if _, err := writeV2(configDir, f); err != nil {
		return err
	}
	// L'archive ancienne est remplacée ; le fichier « dernier état » reste (il est
	// écarté comme doublon tant qu'il porte la même date).
	if snap.Path != base(configDir) {
		if err := os.Remove(snap.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return writeErr(configDir, err)
		}
	}
	return nil
}

// Delete supprime la capture snap : son fichier et, s'il en est le doublon
// (même date), le fichier texte `active_stacks`.
func Delete(configDir string, snap Snapshot) error {
	if snap.Path != "" && snap.Path != base(configDir) {
		if err := os.Remove(snap.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return writeErr(configDir, err)
		}
	}
	if s, err := loadLegacy(base(configDir)); err == nil && s.Date.Equal(snap.Date) {
		if err := os.Remove(base(configDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return writeErr(configDir, err)
		}
	}
	return nil
}

func writeErr(dir string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("écriture impossible dans %s : %w", dir, err)
}

// prune supprime les captures non épinglées au-delà de KeepUnpinned (les plus
// anciennes d'abord) et renvoie leur nombre. Le fichier `active_stacks` n'est
// jamais compté ni supprimé ici.
func prune(configDir string) int {
	snaps, _ := List(configDir)
	kept, pruned := 0, 0
	for _, s := range snaps {
		if s.Pinned || s.Path == base(configDir) {
			continue
		}
		kept++
		if kept > KeepUnpinned {
			if os.Remove(s.Path) == nil {
				pruned++
			}
		}
	}
	return pruned
}
