package runtime

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestMissingIsolatedImageExplainsRecovery(t *testing.T) {
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "example.invalid/loopforge:fixture")
	useFakeCommands(t, map[string]string{"docker": "#!/bin/sh\nexit 1\n"})
	_, err := ensureDockerImage(context.Background(), t.TempDir(), domain.DockerfileDefinition{Content: IsolatedDockerfile})
	if err == nil {
		t.Fatal("missing image was accepted")
	}
	for _, want := range []string{"example.invalid/loopforge:fixture", "daemon", "pull", "AGENTFLOW_ISOLATED_IMAGE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in error: %v", want, err)
		}
	}
}
