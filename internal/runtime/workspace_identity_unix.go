//go:build !windows

package runtime

import (
	"fmt"
	"os"
	"syscall"
)

func filesystemObjectIdentity(_ string, info os.FileInfo) (uint64, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("workspace filesystem identity unavailable")
	}
	return uint64(stat.Dev), stat.Ino, nil
}
