package runtime

import (
	"fmt"
	"path/filepath"
	"strings"

	"agentflow/internal/domain"
)

// SandboxResources are catalog assets resolved before a sandbox is opened.
// Docker builds from Dockerfile; native sandboxes need nothing beyond their
// profile's environment variables.
type SandboxResources struct {
	Dockerfile domain.DockerfileDefinition
}

func catalogDirName(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return "", fmt.Errorf("invalid catalog id %q", id)
	}
	return id, nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func mergeEnvironmentMaps(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for key, value := range layer {
			out[key] = value
		}
	}
	return out
}
