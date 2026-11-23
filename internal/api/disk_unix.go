//go:build !windows

package api

import (
	"syscall"
)

func getDiskUsage(path string) (total, free, used uint64, percent float64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, 0, err
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free = stat.Bavail * uint64(stat.Bsize)
	if total > free {
		used = total - free
	}
	if total > 0 {
		percent = float64(used) / float64(total) * 100.0
	}
	return total, free, used, percent, nil
}
