package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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

	bufMu sync.Mutex
	buf   []byte // tampon de lecture réutilisé (cf. read)
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
	s, err := r.read(dir)
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
		now := time.Now()
		r.miss[id] = now
		// Les échecs périmés sont oubliés : avec un démon distant, chaque
		// conteneur échoue, et la map gardait tous les ID vus depuis le
		// lancement (recréations comprises).
		if len(r.miss) > cgroupMissMax {
			for k, t := range r.miss {
				if now.Sub(t) >= cgroupRetry {
					delete(r.miss, k)
				}
			}
		}
	}
	return d, ok
}

// cgroupMissMax : taille de r.miss au-delà de laquelle les échecs périmés
// sont purgés.
const cgroupMissMax = 256

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

// cgroupBufSize : tampon de lecture des fichiers cgroup. memory.stat, le
// plus gros, fait moins de 2 Ko ; un fichier plus grand est relu en entier.
const cgroupBufSize = 8 << 10

// readCgroup lit les compteurs d'un dossier cgroup v2.
func readCgroup(dir string) (cgroupSample, error) {
	return readCgroupBuf(dir, make([]byte, cgroupBufSize))
}

// read est readCgroup avec le tampon du lecteur, réutilisé d'un conteneur et
// d'un passage à l'autre : la lecture a lieu toutes les 3 s pour chaque
// conteneur à l'écran.
func (r *cgroupReader) read(dir string) (cgroupSample, error) {
	r.bufMu.Lock()
	defer r.bufMu.Unlock()
	if r.buf == nil {
		r.buf = make([]byte, cgroupBufSize)
	}
	return readCgroupBuf(dir, r.buf)
}

// readCgroupBuf lit les compteurs dans buf, sans autre allocation que celles
// des appels système. Le résultat ne retient pas buf.
func readCgroupBuf(dir string, buf []byte) (cgroupSample, error) {
	s := cgroupSample{at: time.Now()}
	cpu, err := readSmallFile(filepath.Join(dir, "cpu.stat"), buf)
	if err != nil {
		return s, err
	}
	var found bool
	if s.cpuUsec, found = statValue(cpu, "usage_usec"); !found {
		return s, fmt.Errorf("%s/cpu.stat : %w", dir, errCgroupFormat)
	}
	cur, err := readSmallFile(filepath.Join(dir, "memory.current"), buf)
	if err != nil {
		return s, err
	}
	if s.mem, found = parseUintBytes(bytes.TrimSpace(cur)); !found {
		return s, fmt.Errorf("%s/memory.current : %w", dir, errCgroupFormat)
	}
	// Comme « docker stats » (et memUsage) : sans le cache de pages inactif.
	if ms, err := readSmallFile(filepath.Join(dir, "memory.stat"), buf); err == nil {
		if inactive, ok := statValue(ms, "inactive_file"); ok && inactive < s.mem {
			s.mem -= inactive
		}
	}
	// io.stat absent si le contrôleur io n'est pas délégué : débits inconnus,
	// mais CPU et mémoire restent valables.
	if io, err := readSmallFile(filepath.Join(dir, "io.stat"), buf); err == nil {
		s.read, s.write = ioStatBytes(io)
	}
	return s, nil
}

// readSmallFile lit path dans buf et renvoie la partie remplie ; un fichier
// qui ne tient pas dans buf est relu par os.ReadFile.
func readSmallFile(path string, buf []byte) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n := 0
	for n < len(buf) {
		k, err := f.Read(buf[n:])
		n += k
		if err == io.EOF || (err == nil && k == 0) {
			return buf[:n], nil
		}
		if err != nil {
			return nil, err
		}
	}
	return os.ReadFile(path)
}

// statValue lit « clé valeur » dans un fichier cpu.stat / memory.stat.
func statValue(data []byte, key string) (uint64, bool) {
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		if len(line) > len(key) && line[len(key)] == ' ' && string(line[:len(key)]) == key {
			return parseUintBytes(bytes.TrimSpace(line[len(key)+1:]))
		}
	}
	return 0, false
}

// parseUintBytes : entier décimal non signé, sans allocation.
func parseUintBytes(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		d := uint64(c - '0')
		if n > (math.MaxUint64-d)/10 {
			return 0, false // dépassement
		}
		n = n*10 + d
	}
	return n, true
}

// ioStatBytes cumule rbytes / wbytes de tous les périphériques d'io.stat
// (« 8:0 rbytes=… wbytes=… rios=… »), comme le fait le démon.
func ioStatBytes(data []byte) (read, write uint64) {
	for len(data) > 0 {
		// Champ suivant, séparé par des espaces ou des fins de ligne.
		i := 0
		for i < len(data) && (data[i] == ' ' || data[i] == '\n') {
			i++
		}
		data = data[i:]
		j := 0
		for j < len(data) && data[j] != ' ' && data[j] != '\n' {
			j++
		}
		f := data[:j]
		data = data[j:]
		switch {
		case bytes.HasPrefix(f, []byte("rbytes=")):
			if n, ok := parseUintBytes(f[len("rbytes="):]); ok {
				read += n
			}
		case bytes.HasPrefix(f, []byte("wbytes=")):
			if n, ok := parseUintBytes(f[len("wbytes="):]); ok {
				write += n
			}
		}
	}
	return
}
