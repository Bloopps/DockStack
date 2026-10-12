package monitor

import "testing"

func TestIsPhysicalDisk(t *testing.T) {
	for name, want := range map[string]bool{
		"sda": true, "sdaa": true, "vdb": true, "xvda": true, "hdc": true,
		"nvme0n1": true, "nvme10n1": true, "mmcblk0": true,
		"sda1": false, "nvme0n1p1": false, "mmcblk0p1": false, "xvda1": false,
		"dm-0": false, "loop0": false, "sr0": false,
	} {
		if got := isPhysicalDisk(name); got != want {
			t.Errorf("isPhysicalDisk(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLongestMount(t *testing.T) {
	mounts := "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /var/lib/docker ext4 rw 0 0\n/dev/sdc1 /var/lib/dockerx ext4 rw 0 0\ntmpfs /run tmpfs rw 0 0\n"
	for path, want := range map[string]string{
		"/var/lib/docker":         "/var/lib/docker",
		"/var/lib/docker/volumes": "/var/lib/docker",
		"/var/lib/dock":           "/",
		"/home/ubuntu":            "/",
	} {
		if got := longestMount(path, mounts); got != want {
			t.Errorf("longestMount(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestMountPointMemo : le point de montage est réutilisé pour le même chemin
// et recalculé quand le chemin change ou que la mémoire a expiré.
func TestMountPointMemo(t *testing.T) {
	mounts = mountMemo{}
	t.Cleanup(func() { mounts = mountMemo{} })
	dir := t.TempDir()
	first := mountPoint(dir)
	if mounts.path != dir || mounts.mount != first {
		t.Fatalf("non mémorisé : %+v", mounts)
	}
	// Valeur factice : si elle revient, c'est que la mémoire a servi.
	mounts.mount = "/memo"
	if got := mountPoint(dir); got != "/memo" {
		t.Errorf("même chemin : %q, attendu la valeur mémorisée", got)
	}
	mounts.at = mounts.at.Add(-mountTTL)
	if got := mountPoint(dir); got != first {
		t.Errorf("après expiration : %q, attendu %q", got, first)
	}
	mounts.mount = "/memo"
	mounts.dev++
	if got := mountPoint(dir); got != first {
		t.Errorf("autre périphérique : %q, attendu %q", got, first)
	}
	mounts.mount = "/memo"
	if got := mountPoint("/"); got != "/" {
		t.Errorf("autre chemin : %q, attendu /", got)
	}
}

// BenchmarkCollect : un relevé complet des métriques de l'hôte (toutes les
// 2 s dans la TUI).
func BenchmarkCollect(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = Collect()
	}
}

// Collect peut être appelé depuis plusieurs goroutines (minuteurs de deux
// programmes dans un même processus de test) : -race le vérifie.
func TestCollectConcurrent(t *testing.T) {
	done := make(chan struct{})
	for range 4 {
		go func() {
			for range 20 {
				Collect()
			}
			done <- struct{}{}
		}()
	}
	for range 4 {
		<-done
	}
}
