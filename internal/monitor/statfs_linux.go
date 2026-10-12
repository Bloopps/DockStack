package monitor

import "golang.org/x/sys/unix"

type syscallStatfs = unix.Statfs_t

func statfs(path string, stat *syscallStatfs) error {
	return unix.Statfs(path, stat)
}

// deviceOf renvoie le périphérique qui porte path (st_dev).
func deviceOf(path string) (uint64, bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}
