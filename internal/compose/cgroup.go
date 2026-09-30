package compose

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Lecture de la consommation des conteneurs directement dans les cgroups v2
// du noyau (cpu.stat, memory.current, memory.stat, io.stat) : mêmes compteurs
// que ceux que le démon lit pour « docker stats », mais sans le solliciter.
// Via l'API, un passage sur 58 conteneurs coûtait ≈74 ms de CPU au démon
// toutes les 3 s ; ici quelques lectures de fichiers, côté dockstack.
//
// Utilisable seulement quand dockstack tourne sur l'hôte du démon, en cgroups
// v2. Sinon (démon distant, cgroups v1, dockstack lui-même conteneurisé) le
// dossier du conteneur est introuvable et ContainerUsages repasse par l'API.

// cgroupRetry : délai avant de rechercher à nouveau le cgroup d'un conteneur
// introuvable (il démarrait peut-être encore).
const cgroupRetry = time.Minute

type cgroupReader struct {
	root string // point de montage cgroup v2, ex. /sys/fs/cgroup
	// pidOf donne le PID principal d'un conteneur (API Docker), pour les
	// cgroups hors des emplacements standard (cgroup_parent, rootless).
	pidOf func(ctx context.Context, id string) (int, error)
	// procRoot : racine de /proc (substituée dans les tests).
	procRoot string

	mu    sync.Mutex
	paths map[string]string    // ID → dossier cgroup du conteneur
	miss  map[string]time.Time // ID → dernier échec de recherche
}

// newCgroupReader retourne nil si root n'est pas une hiérarchie cgroup v2.
func newCgroupReader(root string, pidOf func(context.Context, string) (int, error)) *cgroupReader {
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return nil
	}
	return &cgroupReader{root: root, pidOf: pidOf, procRoot: "/proc",
		paths: make(map[string]string), miss: make(map[string]time.Time)}
}

// cgroupSample : compteurs bruts d'un conteneur à un instant.
type cgroupSample struct {
	cpuUsec     uint64 // temps CPU cumulé (µs)
	mem         uint64 // mémoire hors cache inactif, comme « docker stats »
	read, write uint64 // octets lus / écrits sur les périphériques bloc
	at          time.Time
}

// sample lit les compteurs du conteneur id ; ok=false s'il faut passer par
// l'API (cgroup introuvable ou illisible).
func (r *cgroupReader) sample(ctx context.Context, id string) (cgroupSample, bool) {
	dir, ok := r.dir(ctx, id)
	if !ok {
		return cgroupSample{}, false
	}
	s, err := readCgroup(dir)
	if err != nil {
		// Conteneur arrêté entre-temps (dossier supprimé) ou cgroup déplacé :
		// on oublie le chemin, il sera recherché au prochain passage.
		r.mu.Lock()
		delete(r.paths, id)
		r.mu.Unlock()
		return cgroupSample{}, false
	}
	return s, true
}

// dir trouve (et mémorise) le dossier cgroup du conteneur id.
func (r *cgroupReader) dir(ctx context.Context, id string) (string, bool) {
	r.mu.Lock()
	if d, ok := r.paths[id]; ok {
		r.mu.Unlock()
		return d, true
	}
	if t, ok := r.miss[id]; ok && time.Since(t) < cgroupRetry {
		r.mu.Unlock()
		return "", false
	}
	r.mu.Unlock()

	d, ok := r.find(ctx, id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.paths[id] = d
		delete(r.miss, id)
	} else {
		r.miss[id] = time.Now()
	}
	return d, ok
}

// find : emplacements standard d'abord (pilote systemd, puis cgroupfs), puis
// le cgroup du processus principal du conteneur. Dans tous les cas le dossier
// retenu doit porter l'ID du conteneur : Docker nomme toujours ainsi la
// feuille (docker-<id>.scope ou <id>), ce qui écarte un PID homonyme sur
// l'hôte quand le démon est distant.
func (r *cgroupReader) find(ctx context.Context, id string) (string, bool) {
	if len(id) < 12 || strings.ContainsAny(id, "/.") {
		return "", false
	}
	for _, rel := range []string{
		filepath.Join("system.slice", "docker-"+id+".scope"),
		filepath.Join("docker", id),
	} {
		if d := filepath.Join(r.root, rel); isCgroupDir(d) {
			return d, true
		}
	}
	if r.pidOf == nil {
		return "", false
	}
	pid, err := r.pidOf(ctx, id)
	if err != nil || pid <= 0 {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(r.procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroups v2 : une seule ligne « 0::/chemin ».
		rel, ok := strings.CutPrefix(line, "0::")
		if !ok || !strings.Contains(filepath.Base(rel), id) {
			continue
		}
		if d := filepath.Join(r.root, filepath.Clean("/"+rel)); isCgroupDir(d) {
			return d, true
		}
	}
	return "", false
}

func isCgroupDir(d string) bool {
	_, err := os.Stat(filepath.Join(d, "cpu.stat"))
	return err == nil
}

var errCgroupFormat = errors.New("format cgroup inattendu")

// readCgroup lit les compteurs d'un dossier cgroup v2.
func readCgroup(dir string) (cgroupSample, error) {
	s := cgroupSample{at: time.Now()}
	cpu, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return s, err
	}
	var found bool
	if s.cpuUsec, found = statValue(cpu, "usage_usec"); !found {
		return s, fmt.Errorf("%s/cpu.stat : %w", dir, errCgroupFormat)
	}
	cur, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return s, err
	}
	if s.mem, err = strconv.ParseUint(strings.TrimSpace(string(cur)), 10, 64); err != nil {
		return s, err
	}
	// Comme « docker stats » (et memUsage) : sans le cache de pages inactif.
	if ms, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
		if inactive, ok := statValue(ms, "inactive_file"); ok && inactive < s.mem {
			s.mem -= inactive
		}
	}
	// io.stat absent si le contrôleur io n'est pas délégué : débits inconnus,
	// mais CPU et mémoire restent valables.
	if io, err := os.ReadFile(filepath.Join(dir, "io.stat")); err == nil {
		s.read, s.write = ioStatBytes(io)
	}
	return s, nil
}

// statValue lit « clé valeur » dans un fichier cpu.stat / memory.stat.
func statValue(data []byte, key string) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == key {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// ioStatBytes cumule rbytes / wbytes de tous les périphériques d'io.stat
// (« 8:0 rbytes=… wbytes=… rios=… »), comme le fait le démon.
func ioStatBytes(data []byte) (read, write uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		for _, f := range strings.Fields(line) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			switch k {
			case "rbytes":
				read += n
			case "wbytes":
				write += n
			}
		}
	}
	return
}
