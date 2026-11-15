package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// TestLiveDockerDriverConversationWorkspace starts a real Docker-driver
// sandbox with a conversation workspace and checks the layout and the host
// Docker daemon from inside. Opt in with LOOPFORGE_LIVE_DOCKER=1 on a machine
// whose daemon can run containers and pull alpine.
func TestLiveDockerDriverConversationWorkspace(t *testing.T) {
	if os.Getenv("LOOPFORGE_LIVE_DOCKER") == "" {
		t.Skip("set LOOPFORGE_LIVE_DOCKER=1 to run against the real Docker daemon")
	}
	if out, err := exec.Command("docker", "run", "--rm", "alpine:3", "true").CombinedOutput(); err != nil {
		t.Skipf("the Docker daemon cannot run containers here: %v %s", err, out)
	}
	base, err := os.MkdirTemp("/tmp", "lfdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "conversation")
	workspace := filepath.Join(base, "workspace")
	for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "ws"), workspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id := "live-" + filepath.Base(base)
	sandbox, err := createDocker(ctx, id, workspace, domain.SandboxProfile{Driver: "docker"}, SandboxRequirements{ConversationRoot: root}, domain.DockerfileDefinition{Content: "FROM alpine:3\nRUN apk add --no-cache docker-cli\n"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close(context.Background()) })
	bin, err := writeConversationDockerShim(root)
	if err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(root, conversationGuardFiles[0])
	script := strings.Join([]string{
		"set -u",
		"PATH=" + bin + ":$PATH",
		"cd " + filepath.Join(root, "ws"),
		"echo hi > from-sandbox || echo FAIL-ws-write",
		"[ -f " + filepath.Join(workspace, "from-sandbox") + " ] || echo FAIL-host-path",
		"touch " + filepath.Join(root, "home", "note") + " || echo FAIL-home-write",
		"( echo x > " + guard + " ) 2>/dev/null && echo FAIL-guard-writable",
		"( echo x > " + filepath.Join(bin, "docker") + " ) 2>/dev/null && echo FAIL-shim-writable",
		// The shim hands the host daemon the workspace's host path for $PWD.
		`[ "$(docker run --rm -v "$PWD:/m" alpine:3 cat /m/from-sandbox)" = hi ] || echo FAIL-docker-bind`,
		"echo DONE",
	}, "\n")
	command := WithCommandEnvironment(CommandSpec{Argv: []string{"sh", "-c", script}}, map[string]string{"DOCKER_HOST": "unix://" + conversationDockerSocketTarget, workspaceAliasEnv: filepath.Join(root, "ws"), workspaceHostEnv: workspace})
	out, err := sandbox.Exec(ctx, command, nil)
	if err != nil || !strings.Contains(out, "DONE") || strings.Contains(out, "FAIL") {
		t.Fatalf("inside the Docker sandbox: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "from-sandbox")); err != nil || string(data) != "hi\n" {
		t.Fatalf("the workspace must be the run workspace: %q %v", data, err)
	}
}
