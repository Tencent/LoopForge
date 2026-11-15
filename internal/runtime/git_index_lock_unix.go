//go:build !windows

package runtime

import (
	"os"
	"syscall"
)

// gitIndexLockOwnerUID preserves the Unix ownership used by the /proc scan.
func gitIndexLockOwnerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}
