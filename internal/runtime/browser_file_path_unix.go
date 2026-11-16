//go:build !windows

package runtime

import "path/filepath"

func hostWorkspaceFilePath(file string) (string, bool) { return filepath.FromSlash(file), true }
