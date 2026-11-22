//go:build unix

package httpapi

import "syscall"

// diskSpace reports free and total bytes of the filesystem containing path.
func diskSpace(path string) (free int64, total int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	blockSize := int64(stat.Bsize)
	return int64(stat.Bavail) * blockSize, int64(stat.Blocks) * blockSize
}
