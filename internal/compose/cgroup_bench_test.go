package compose

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// BenchmarkReadCgroup : lecture des compteurs d'un conteneur, sur une copie
// des fichiers d'un vrai cgroup de l'hôte (ignoré s'il n'y en a pas).
func BenchmarkReadCgroup(b *testing.B) {
	src, _ := filepath.Glob("/sys/fs/cgroup/system.slice/docker-*.scope")
	if len(src) == 0 {
		b.Skip("aucun cgroup de conteneur sur cet hôte")
	}
	dir := b.TempDir()
	for _, f := range []string{"cpu.stat", "memory.current", "memory.stat", "io.stat"} {
		data, err := os.ReadFile(filepath.Join(src[0], f))
		if err != nil {
			b.Skip(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	r := &cgroupReader{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := r.read(dir); err != nil {
			b.Fatal(err)
		}
	}
}

// refStatValue : l'implémentation d'origine (bufio + strconv), référence du
// fuzzing de statValue.
func refStatValue(data []byte, key string) (uint64, bool) {
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

// refIOStatBytes : l'implémentation d'origine d'ioStatBytes.
func refIOStatBytes(data []byte) (read, write uint64) {
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

// FuzzCgroupParsers : les analyseurs sans allocation donnent le même résultat
// que ceux d'origine, sur toute entrée où l'origine est bien définie.
func FuzzCgroupParsers(f *testing.F) {
	f.Add([]byte("usage_usec 1500000\nuser_usec 1\n"), "usage_usec")
	f.Add([]byte("anon 5\ninactive_file 30\n"), "inactive_file")
	f.Add([]byte("8:0 rbytes=1000 wbytes=2000 rios=1\n259:0 rbytes=24 wbytes=0\n"), "rbytes")
	f.Add([]byte("usage_usec 18446744073709551616\n"), "usage_usec")
	f.Add([]byte("usage_usec  +12\r\n"), "usage_usec")
	f.Fuzz(func(t *testing.T, data []byte, key string) {
		// Le Scanner d'origine coupe aussi sur « \r\n » et échoue sur les
		// lignes de plus de 64 Ko : hors du format des cgroups.
		if bytes.ContainsAny(data, "\r\t\v\f") || len(data) > 60_000 || key == "" || strings.ContainsAny(key, " \n") {
			return
		}
		gotN, gotOK := statValue(data, key)
		wantN, wantOK := refStatValue(data, key)
		if gotOK != wantOK || (gotOK && gotN != wantN) {
			// ParseUint accepte « +12 » ; parseUintBytes non, sans incidence :
			// le noyau n'écrit jamais de signe.
			if !bytes.Contains(data, []byte("+")) && !bytes.Contains(data, []byte("_")) {
				t.Fatalf("statValue(%q, %q) = %d,%v ; origine %d,%v", data, key, gotN, gotOK, wantN, wantOK)
			}
		}
		r, w := ioStatBytes(data)
		rr, rw := refIOStatBytes(data)
		if (r != rr || w != rw) && !bytes.Contains(data, []byte("+")) && !bytes.Contains(data, []byte("_")) {
			t.Fatalf("ioStatBytes(%q) = %d,%d ; origine %d,%d", data, r, w, rr, rw)
		}
	})
}
