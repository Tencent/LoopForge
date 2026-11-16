package runtime

import (
	"path/filepath"
	"strings"
)

// hostWorkspaceFilePath decodes a file URL drive path without enabling UNC or device paths.
// The gateway already decoded URL escapes; the workspace containment check still follows.
func hostWorkspaceFilePath(file string) (string, bool) {
	if strings.HasPrefix(file, "/") && len(file) >= 4 && file[2] == ':' {
		file = file[1:]
	}
	file = filepath.FromSlash(file)
	volume := filepath.VolumeName(file)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(file) {
		return "", false
	}
	return file, true
}
