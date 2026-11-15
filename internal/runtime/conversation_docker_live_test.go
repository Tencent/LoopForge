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
// sandbox with a conversation workspace and checks the layout and the
// workspace Docker proxy from inside. Opt in with LOOPFORGE_LIVE_DOCKER=1 on
// a machine whose daemon can run containers and pull alpine.
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
	dockerHost, stop, err := startConversationDocker(root, workspace)
	if err != nil || dockerHost == "" {
		t.Fatalf("docker proxy: %q %v", dockerHost, err)
	}
	t.Cleanup(func() { stop(); _ = os.RemoveAll(conversationDockerDir(root)) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id := "live-" + filepath.Base(base)
	sandbox, err := createDocker(ctx, id, workspace, domain.SandboxProfile{Driver: "docker"}, SandboxRequirements{ConversationRoot: root}, domain.DockerfileDefinition{Content: "FROM alpine:3\nRUN apk add --no-cache curl\n"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Close(context.Background()) })
	guard := filepath.Join(root, conversationGuardFiles[0])
	proxySocket := strings.TrimPrefix(dockerHost, "unix://")
	script := strings.Join([]string{
		"set -u",
		"cd " + filepath.Join(root, "ws"),
		"echo hi > from-sandbox || echo FAIL-ws-write",
		"touch " + filepath.Join(root, "home", "note") + " || echo FAIL-home-write",
		"( echo x > " + guard + " ) 2>/dev/null && echo FAIL-guard-writable",
		"[ -S /var/run/docker.sock ] && echo FAIL-host-docker-visible",
		"[ -S " + proxySocket + " ] || echo FAIL-proxy-missing",
		// The proxy answers and refuses a privileged container.
		"curl -sf --unix-socket " + proxySocket + " http://docker/_ping >/dev/null || echo FAIL-proxy-ping",
		"curl -s --unix-socket " + proxySocket + ` -H 'Content-Type: application/json' -d '{"Image":"alpine:3","HostConfig":{"Privileged":true}}' http://docker/containers/create | grep -q LoopForge || echo FAIL-privileged-allowed`,
		"echo DONE",
	}, "\n")
	out, err := sandbox.Exec(ctx, CommandSpec{Argv: []string{"sh", "-c", script}}, nil)
	if err != nil || !strings.Contains(out, "DONE") || strings.Contains(out, "FAIL") {
		t.Fatalf("inside the Docker sandbox: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "from-sandbox")); err != nil || string(data) != "hi\n" {
		t.Fatalf("<root>/ws must be the run workspace: %q %v", data, err)
	}
}
