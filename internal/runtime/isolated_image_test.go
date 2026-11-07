package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestIsolatedRuntimeImageReusesInstalledImage(t *testing.T) {
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	workspace := t.TempDir()
	logfile := filepath.Join(t.TempDir(), "docker.log")
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
case "$*" in
  "image inspect loopforge-platform:tested") exit 0 ;;
  "inspect "*) exit 1 ;;
  "exec "*) echo /workspace ;;
  "build "*) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_TEST_LOG", logfile)
	definition := domain.DockerfileDefinition{Content: IsolatedDockerfile}
	if !DockerImageCached(context.Background(), definition) {
		t.Fatal("installed runtime image is incorrectly reported as requiring a build")
	}
	image, err := ensureDockerImage(context.Background(), workspace, definition)
	if err != nil || image != "loopforge-platform:tested" {
		t.Fatalf("runtime image: %q %v", image, err)
	}
	sandbox := &dockerSandbox{id: "reuse-test", name: "agentflow-reuse-test", workspace: workspace, dockerfile: definition}
	if err := sandbox.ensureStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(logfile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "build ") || !strings.Contains(string(commands), "--entrypoint sh loopforge-platform:tested -lc") {
		t.Fatalf("unexpected sandbox commands: %s", commands)
	}
	for _, custom := range []domain.DockerfileDefinition{{Content: "FROM custom\n"}, {Content: IsolatedDockerfile, Files: map[string]string{"custom.txt": "keep"}}} {
		if isolatedRuntimeImage(custom) != "" {
			t.Fatal("custom environment replaced by platform runtime")
		}
	}
}
