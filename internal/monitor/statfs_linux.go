package monitor

import "golang.org/x/sys/unix"

type syscallStatfs = unix.Statfs_t

func statfs(path string, stat *syscallStatfs) error {
	return unix.Statfs(path, stat)
}
