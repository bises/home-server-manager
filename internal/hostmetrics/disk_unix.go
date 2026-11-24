//go:build !windows

package hostmetrics

import "syscall"

func readDiskBytes(path string) (total, free, avail uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bfree * bsize, st.Bavail * bsize, nil
}
