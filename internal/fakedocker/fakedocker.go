// Package fakedocker fournit un faux démon Docker sur socket Unix, réservé
// aux tests : il répond aux quelques routes que dockstack utilise (/_ping,
// /version, /info, /containers/json, /system/df, /events en flux), puis peut
// être coupé et relancé sur le même socket, pour simuler un démon qui tombe
// en cours de route sans toucher au vrai.
package fakedocker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// APIVersion annoncée par le faux démon.
const APIVersion = "1.53"

// Event est un événement Docker minimal diffusé sur /events.
type Event struct {
	Type   string
	Action string
}

// Daemon est le faux démon. Ses méthodes sont sûres entre goroutines.
type Daemon struct {
	Sock string

	mu      sync.Mutex
	srv     *http.Server
	down    bool          // en panne : chaque requête est lue, comptée, puis la connexion coupée
	hang    chan struct{} // muet : les requêtes attendent sa fermeture (Revive)
	subs    map[chan Event]struct{}
	hits    map[string]int
	refused map[string]int    // tentatives par route pendant la panne
	queries map[string]string // dernière requête (paramètres) par route
	order   []string          // routes appelées, dans l'ordre (hors /_ping, /version)
	ctrs    []map[string]any
	evStart chan struct{} // signalé à chaque nouvel abonnement aux événements
}

// Start lance un faux démon sur un socket créé dans dir.
func Start(dir string) (*Daemon, error) {
	d := &Daemon{
		Sock:    filepath.Join(dir, "docker.sock"),
		subs:    map[chan Event]struct{}{},
		hits:    map[string]int{},
		refused: map[string]int{},
		queries: map[string]string{},
		evStart: make(chan struct{}, 64),
	}
	return d, d.listen()
}

// Host renvoie la valeur de DOCKER_HOST qui désigne ce démon.
func (d *Daemon) Host() string { return "unix://" + d.Sock }

func (d *Daemon) listen() error {
	_ = os.Remove(d.Sock)
	l, err := net.Listen("unix", d.Sock)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serve)}
	d.mu.Lock()
	d.srv = srv
	d.mu.Unlock()
	go srv.Serve(l)
	return nil
}

// Kill fait tomber le démon : connexions en cours (flux d'événements et de
// logs compris) coupées net. Le socket reste à l'écoute, mais chaque
// nouvelle requête est lue, comptée (Refused), puis la connexion fermée sans
// réponse, comme un démon qui redémarre.
func (d *Daemon) Kill() {
	d.mu.Lock()
	srv := d.srv
	d.srv = nil
	d.down = true
	d.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
		_ = d.listen()
	}
}

// Stop arrête tout, socket compris (fin de test).
func (d *Daemon) Stop() {
	d.mu.Lock()
	srv := d.srv
	d.srv = nil
	d.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	_ = os.Remove(d.Sock)
}

// Hang rend le démon muet : connexions acceptées, requêtes lues, aucune
// réponse jusqu'à Revive (ou l'abandon par le client).
func (d *Daemon) Hang() {
	d.mu.Lock()
	if d.hang == nil {
		d.hang = make(chan struct{})
	}
	d.mu.Unlock()
}

// Revive remet le démon en service après Kill ou Hang.
func (d *Daemon) Revive() {
	d.mu.Lock()
	d.down = false
	if d.hang != nil {
		close(d.hang)
		d.hang = nil
	}
	d.mu.Unlock()
}

// Refused renvoie le nombre de requêtes reçues pendant la panne sur une route.
func (d *Daemon) Refused(route string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.refused[route]
}

// SetContainers fixe la réponse de /containers/json.
func (d *Daemon) SetContainers(c []map[string]any) {
	d.mu.Lock()
	d.ctrs = c
	d.mu.Unlock()
}

// Hits renvoie le nombre de requêtes reçues sur une route (sans préfixe de
// version, par exemple "/events").
func (d *Daemon) Hits(route string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hits[route]
}

// Query renvoie les paramètres de la dernière requête reçue sur une route.
func (d *Daemon) Query(route string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queries[route]
}

// Order renvoie les routes appelées, dans l'ordre, puis remet la liste à zéro.
func (d *Daemon) Order() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.order
	d.order = nil
	return out
}

// Subscribers renvoie le nombre de flux d'événements ouverts.
func (d *Daemon) Subscribers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.subs)
}

// WaitSubscriber attend qu'un abonnement aux événements s'ouvre.
func (d *Daemon) WaitSubscriber(timeout time.Duration) error {
	select {
	case <-d.evStart:
		return nil
	case <-time.After(timeout):
		return errors.New("aucun abonnement aux événements")
	}
}

// Emit diffuse n événements à chaque abonné. Les envois sont bloquants :
// comme le vrai démon, le faux attend que le client lise.
func (d *Daemon) Emit(ctx context.Context, n int, ev Event) error {
	d.mu.Lock()
	subs := make([]chan Event, 0, len(d.subs))
	for c := range d.subs {
		subs = append(subs, c)
	}
	d.mu.Unlock()
	for range n {
		for _, c := range subs {
			select {
			case c <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

var containerRoute = regexp.MustCompile(`^/containers/([^/]+)/(json|logs)$`)

func (d *Daemon) serve(w http.ResponseWriter, r *http.Request) {
	route := versionPrefix.ReplaceAllString(r.URL.Path, "")
	d.mu.Lock()
	down := d.down
	if down {
		d.refused[route]++
	} else {
		d.hits[route]++
		d.queries[route] = r.URL.RawQuery
		if route != "/_ping" && route != "/version" {
			d.order = append(d.order, route)
		}
	}
	ctrs := d.ctrs
	hang := d.hang
	d.mu.Unlock()
	if hang != nil {
		select {
		case <-hang:
		case <-r.Context().Done():
			return
		}
	}
	if down {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	if m := containerRoute.FindStringSubmatch(route); m != nil {
		d.serveContainer(w, r, m[1], m[2], ctrs)
		return
	}

	w.Header().Set("Api-Version", APIVersion)
	w.Header().Set("Ostype", "linux")
	switch route {
	case "/_ping":
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "OK")
	case "/version":
		writeJSON(w, map[string]any{"Version": "29.2.0", "ApiVersion": APIVersion, "MinAPIVersion": "1.24", "Os": "linux", "Arch": "amd64"})
	case "/info":
		writeJSON(w, map[string]any{"ID": "fake", "Name": "fake", "ServerVersion": "29.2.0", "NCPU": 8, "MemTotal": 8 << 30, "DockerRootDir": "/var/lib/docker", "OSType": "linux"})
	case "/containers/json":
		if ctrs == nil {
			ctrs = []map[string]any{}
		}
		writeJSON(w, ctrs)
	case "/images/json", "/volumes":
		writeJSON(w, []any{})
	case "/system/df":
		writeJSON(w, map[string]any{})
	case "/events":
		d.streamEvents(w, r)
	case "/build/prune":
		writeJSON(w, map[string]any{"CachesDeleted": []string{"c1", "c2"}, "SpaceReclaimed": 2 << 30})
	case "/containers/prune":
		writeJSON(w, map[string]any{"ContainersDeleted": []string{"a"}, "SpaceReclaimed": 1 << 20})
	case "/images/prune":
		writeJSON(w, map[string]any{"ImagesDeleted": []map[string]string{{"Deleted": "sha256:i"}, {"Deleted": "sha256:couche"}}, "SpaceReclaimed": 3 << 30})
	case "/networks/prune":
		writeJSON(w, map[string]any{"NetworksDeleted": []string{"n1", "n2"}})
	case "/volumes/prune":
		writeJSON(w, map[string]any{"VolumesDeleted": []string{}, "SpaceReclaimed": 0})
	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"message": "fakedocker : route inconnue " + route})
	}
}

func (d *Daemon) streamEvents(w http.ResponseWriter, r *http.Request) {
	c := make(chan Event)
	d.mu.Lock()
	d.subs[c] = struct{}{}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.subs, c)
		d.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	select {
	case d.evStart <- struct{}{}:
	default:
	}
	enc := json.NewEncoder(w)
	for {
		select {
		case ev := <-c:
			msg := map[string]any{"Type": ev.Type, "Action": ev.Action, "Actor": map[string]any{"ID": "fake"}, "time": time.Now().Unix(), "timeNano": time.Now().UnixNano()}
			if err := enc.Encode(msg); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

// serveContainer répond à l'inspection d'un conteneur de SetContainers
// (en mode TTY) et à ses logs : une ligne « tic N » toutes les 100 ms.
func (d *Daemon) serveContainer(w http.ResponseWriter, r *http.Request, id, what string, ctrs []map[string]any) {
	var ctr map[string]any
	for _, c := range ctrs {
		if c["Id"] == id || strings.TrimPrefix(firstName(c), "/") == id {
			ctr = c
		}
	}
	if ctr == nil {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"message": "No such container: " + id})
		return
	}
	if what == "json" {
		writeJSON(w, map[string]any{
			"Id": ctr["Id"], "Name": firstName(ctr), "Image": ctr["Image"],
			"Config": map[string]any{"Tty": true, "Labels": ctr["Labels"], "Image": ctr["Image"]},
			"State":  map[string]any{"Status": ctr["State"], "Running": ctr["State"] == "running", "Pid": 0},
		})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	for i := 0; ; i++ {
		if _, err := fmt.Fprintf(w, "tic %d\n", i); err != nil {
			return
		}
		if fl != nil {
			fl.Flush()
		}
		if r.URL.Query().Get("follow") != "1" && r.URL.Query().Get("follow") != "true" {
			return
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
}

func firstName(c map[string]any) string {
	if n, ok := c["Names"].([]string); ok && len(n) > 0 {
		return n[0]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	_ = json.NewEncoder(w).Encode(v)
}
