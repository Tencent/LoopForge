package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/workspaceprep"
)

func TestWorkspaceOpeningDockerBuildReportsOutputBeforeTimeout(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n build) printf 'apt: downloading packages from mirror\\n'; sleep 30 & wait ;;\n *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTFLOW_WORKSPACE_BUILD_TIMEOUT", "250ms")
	var updates []domain.WorkspaceOpening
	ctx := workspaceprep.WithSink(context.Background(), func(p domain.WorkspaceOpening) { updates = append(updates, p) })
	err := workspaceprep.Do(ctx, "build", "正在构建镜像", func(ctx context.Context) error {
		_, err := ensureDockerImage(ctx, t.TempDir(), domain.DockerfileDefinition{ID: "opening-test", Content: "FROM debian:bookworm\nRUN apt-get update\n"})
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "downloading packages") {
		t.Fatalf("lost deadline/build tail: %v", err)
	}
	if len(updates) < 2 || !strings.Contains(updates[1].Output, "downloading packages") {
		t.Fatalf("no build progress: %+v", updates)
	}
}
