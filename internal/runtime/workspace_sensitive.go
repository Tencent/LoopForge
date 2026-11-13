package runtime

import (
	"path/filepath"
	"strings"
)

// WorkspacePathSensitive is the shared policy for paths exposed by the
// workspace browser and by node checkpoint metadata.
func WorkspacePathSensitive(path string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		switch part {
		case ".git", ".agentflow", ".ssh", ".gnupg", ".netrc", ".npmrc", "id_rsa", "id_ed25519":
			return true
		}
		if part == ".env" || strings.HasPrefix(part, ".env.") && !strings.HasSuffix(part, ".example") {
			return true
		}
	}
	return false
}
