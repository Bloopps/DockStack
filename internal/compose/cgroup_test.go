package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const testID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeCgroup crée un dossier cgroup v2 de conteneur sous root/rel.
func fakeCgroup(t *testing.T, root, rel string, cpuUsec, mem, inactive uint64, io string) string {
	t.Helper()
	d := filepath.Join(root, rel)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu.stat":       "usage_usec " + strconv.FormatUint(cpuUsec, 10) + "\nuser_usec 1\nsystem_usec 2\n",
		"memory.current": strconv.FormatUint(mem, 10) + "\n",
		"memory.stat":    "anon 5\nfile 9\ninactive_file " + strconv.FormatUint(inactive, 10) + "\nactive_file 3\n",
	}
	if io != "" {
		files["io.stat"] = io
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func cgroupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu io memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCgroupReaderRequiresV2(t *testing.T) {
	if newCgroupReader(t.TempDir(), nil) != nil {
		t.Error("sans cgroup.controllers (cgroups v1 ou absents), le lecteur doit être nil")
	}
	if newCgroupReader(cgroupRoot(t), nil) == nil {
		t.Error("hiérarchie v2 non reconnue")
	}
}

func TestCgroupStandardPaths(t *testing.T) {
	for _, rel := range []string{"system.slice/docker-" + testID + ".scope", "docker/" + testID} {
		root := cgroupRoot(t)
		fakeCgroup(t, root, rel, 1_500_000, 100<<20, 30<<20, "8:0 rbytes=1000 wbytes=2000 rios=1 wios=2\n259:0 rbytes=24 wbytes=0\n")
		r := newCgroupReader(root, func(context.Context, string) (int, error) {
			t.Fatal("emplacement standard : l'API ne doit pas être appelée")
			return 0, nil
		})
		s, ok := r.sample(context.Background(), testID)
		if !ok {
			t.Fatalf("%s : cgroup non trouvé", rel)
		}
		if s.cpuUsec != 1_500_000 || s.mem != 70<<20 || s.read != 1024 || s.write != 2000 {
			t.Errorf("%s : lecture %+v", rel, s)
		}
	}
}

// cgroup_parent ou rootless : le chemin vient du processus du conteneur, et
// n'est retenu que s'il porte l'ID du conteneur.
func TestCgroupFromPID(t *testing.T) {
	root := cgroupRoot(t)
	proc := t.TempDir()
	fakeCgroup(t, root, "custom.slice/docker-"+testID+".scope", 10, 10, 0, "")
	fakeCgroup(t, root, "user.slice/session-3.scope", 10, 10, 0, "")
	writeProc := func(pid int, line string) {
		d := filepath.Join(proc, strconv.Itoa(pid))
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "cgroup"), []byte(line+"\n"), 0o644)
	}
	writeProc(42, "0::/custom.slice/docker-"+testID+".scope")
	writeProc(43, "0::/user.slice/session-3.scope") // PID d'un autre processus

	calls := 0
	pid := 42
	r := newCgroupReader(root, func(_ context.Context, id string) (int, error) {
		calls++
		return pid, nil
	})
	r.procRoot = proc
	if _, ok := r.sample(context.Background(), testID); !ok {
		t.Fatal("cgroup personnalisé non trouvé via le PID")
	}
	r.sample(context.Background(), testID)
	if calls != 1 {
		t.Errorf("chemin non mémorisé : %d appels à l'API", calls)
	}

	// Démon distant : le PID désigne un processus local sans rapport.
	other := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	pid = 43
	if _, ok := r.sample(context.Background(), other); ok {
		t.Error("un cgroup qui ne porte pas l'ID du conteneur ne doit jamais être lu")
	}
	// Échec mémorisé : pas de nouvelle requête avant cgroupRetry.
	calls = 0
	r.sample(context.Background(), other)
	if calls != 0 {
		t.Error("un échec récent doit être mémorisé")
	}
}

func TestCgroupRejectsBadIDs(t *testing.T) {
	r := newCgroupReader(cgroupRoot(t), func(context.Context, string) (int, error) {
		return 0, errors.New("pas d'API")
	})
	for _, id := range []string{"", "abc", "../../etc/passwd/xxxxxx", testID[:20] + "/x"} {
		if _, ok := r.sample(context.Background(), id); ok {
			t.Errorf("ID %q accepté", id)
		}
	}
}

// Conteneur arrêté : le dossier disparaît, le chemin mémorisé est oublié.
func TestCgroupContainerGone(t *testing.T) {
	root := cgroupRoot(t)
	d := fakeCgroup(t, root, "docker/"+testID, 10, 10, 0, "")
	r := newCgroupReader(root, nil)
	if _, ok := r.sample(context.Background(), testID); !ok {
		t.Fatal("première lecture")
	}
	os.RemoveAll(d)
	if _, ok := r.sample(context.Background(), testID); ok {
		t.Error("lecture d'un cgroup supprimé")
	}
	if _, ok := r.paths[testID]; ok {
		t.Error("chemin périmé conservé")
	}
}

func TestReadCgroupWithoutIOController(t *testing.T) {
	root := cgroupRoot(t)
	d := fakeCgroup(t, root, "docker/"+testID, 7, 50, 80, "") // inactive > current
	s, err := readCgroup(d)
	if err != nil {
		t.Fatal(err)
	}
	if s.mem != 50 || s.read != 0 || s.write != 0 {
		t.Errorf("lecture %+v : mémoire brute gardée si le cache dépasse, débits nuls", s)
	}
	os.WriteFile(filepath.Join(d, "cpu.stat"), []byte("user_usec 3\n"), 0o644)
	if _, err := readCgroup(d); err == nil {
		t.Error("cpu.stat sans usage_usec accepté")
	}
}

func TestCgroupCPUPercent(t *testing.T) {
	t0 := time.Now()
	prev := usageSample{total: 1_000_000_000, at: t0, cgroup: true}
	cur := usageSample{total: 1_150_000_000, at: t0.Add(100 * time.Millisecond), cgroup: true}
	if got, ok := cgroupCPUPercent(prev, cur); !ok || got < 149.99 || got > 150.01 {
		t.Errorf("150 ms de CPU en 100 ms = 150 %%, obtenu %v %v", got, ok)
	}
	if _, ok := cgroupCPUPercent(cur, prev); ok {
		t.Error("compteur qui recule (redémarrage) accepté")
	}
}

// Tous les conteneurs servis par les cgroups : aucune requête au démon, et
// le CPU apparaît à la deuxième lecture comme avec l'API.
func TestContainerUsagesFromCgroups(t *testing.T) {
	root := cgroupRoot(t)
	d := fakeCgroup(t, root, "system.slice/docker-"+testID+".scope", 1_000_000, 64<<20, 0, "8:0 rbytes=0 wbytes=0\n")
	c := &Client{cg: newCgroupReader(root, nil)} // cli nil : tout appel API paniquerait
	ctx := context.Background()
	u, err := c.ContainerUsages(ctx, []string{testID})
	if err != nil || u[testID].MemBytes != 64<<20 || u[testID].CPUOK {
		t.Fatalf("première lecture : %+v, %v", u, err)
	}
	time.Sleep(20 * time.Millisecond)
	os.WriteFile(filepath.Join(d, "cpu.stat"), []byte("usage_usec 1010000\n"), 0o644)
	os.WriteFile(filepath.Join(d, "io.stat"), []byte("8:0 rbytes=4096 wbytes=0\n"), 0o644)
	u, _ = c.ContainerUsages(ctx, []string{testID})
	got := u[testID]
	if !got.CPUOK || got.CPU <= 0 || got.CPU > 60 || !got.IOOK || got.ReadBps <= 0 {
		t.Errorf("deuxième lecture : %+v (10 ms de CPU en ≥20 ms)", got)
	}
}
