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
