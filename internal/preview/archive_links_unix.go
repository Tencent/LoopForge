//go:build !windows

package preview

import (
	"os"
	"syscall"
)

func fileHasMultipleLinks(_ string, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1, nil
}
