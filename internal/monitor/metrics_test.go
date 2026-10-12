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

// Occupation calculée comme df : la réserve de root n'est pas « utilisée »,
// et le pourcentage rapporte l'utilisé à l'utilisé + disponible (arrondi au
// supérieur). L'ancien calcul (total - disponible) affichait 75 % ici, df 74 %.
func TestFillDiskLikeDf(t *testing.T) {
	const gb = 1 << 30
	var m Metrics
	fillDisk(&m, 1000, 300, 250, gb) // 50 blocs réservés à root
	if m.DiskUsedGB != 700 || m.DiskTotalGB != 1000 || m.DiskFreeGB != 250 || m.DiskPercent != 74 {
		t.Errorf("disque = %d/%d Go, %d libres, %d %% ; want 700/1000, 250, 74 %%", m.DiskUsedGB, m.DiskTotalGB, m.DiskFreeGB, m.DiskPercent)
	}
	var empty Metrics
	fillDisk(&empty, 0, 0, 0, 4096)
	if empty.DiskPercent != 0 {
		t.Errorf("disque vide : %d %%", empty.DiskPercent)
	}
}

func TestLongestMountEscapes(t *testing.T) {
	mounts := "/dev/sdb1 /mnt/mes\\040données ext4 rw 0 0\n/dev/sdc1 /mnt/a\\134b ext4 rw 0 0\n"
	if got := longestMount("/mnt/mes données/docker", mounts); got != "/mnt/mes données" {
		t.Errorf("espace échappé : %q", got)
	}
	if got := longestMount(`/mnt/a\b/x`, mounts); got != `/mnt/a\b` {
		t.Errorf("barre oblique inverse échappée : %q", got)
	}
}
