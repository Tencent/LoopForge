package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDockerSandboxNativeOwner(t *testing.T) {
	if os.Getenv("AGENTFLOW_DOCKER_OWNER_SMOKE") != "1" || os.Geteuid() == 0 {
		t.Skip("run as a non-root service user with AGENTFLOW_DOCKER_OWNER_SMOKE=1 and Docker access")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	// Materialize the mount targets as the host user, as normal run setup does.
	// Otherwise the Docker daemon creates root-owned placeholder directories.
	if err := os.MkdirAll(filepath.Join(workspace, ".agentflow", "isolation", "empty-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agentflow", "isolation", "empty-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("native-owner-%d", time.Now().UnixNano())
	sandbox, err := createDocker(ctx, name, workspace, domain.SandboxProfile{Driver: "docker"}, SandboxRequirements{}, domain.DockerfileDefinition{
		ID: name, Content: "FROM alpine:3.20\nWORKDIR /workspace\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	defer os.RemoveAll(dockerIsolationMaskRoot(workspace))
	want := fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid())
	for _, provider := range []string{"", "script"} {
		command := fmt.Sprintf(`test "$(id -u):$(id -g)" = %s && printf owner-ok > result.txt && grep -q '^CapEff:[[:space:]]*0000000000000000$' /proc/self/status && grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status`, want)
		out, err := sandbox.Exec(ctx, CommandSpec{Provider: provider, Argv: []string{"sh", "-ec", command}}, nil)
		if err != nil {
			t.Fatalf("provider %q cannot use private workspace as %s: %s %v", provider, want, out, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "result.txt")); err != nil || string(data) != "owner-ok" {
		t.Fatalf("host cannot read sandbox output: %q %v", data, err)
	}
	info, err := os.Stat(workspace)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("private workspace permissions changed", err)
	}
	container := sandbox.(*dockerSandbox).name
	if out, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Config.User}}", container).CombinedOutput(); err != nil || string(out) != want+"\n" {
		t.Fatalf("container identity: %q %v", out, err)
	}
}
