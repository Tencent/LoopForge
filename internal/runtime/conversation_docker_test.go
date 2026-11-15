package runtime

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeDockerDaemon(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dockerd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK")) })}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	return socket
}

func TestConversationDockerProxiesTheHostDaemon(t *testing.T) {
	daemon := fakeDockerDaemon(t)
	root, err := os.MkdirTemp("/tmp", "conv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root); _ = os.RemoveAll(conversationDockerDir(root)) })
	// A real conversation root is far longer than a unix socket path allows.
	root = filepath.Join(root, strings.Repeat("c", 64), "conversation")
	workspace := t.TempDir()
	host, stop, err := startConversationDocker(root, workspace)
	if err != nil || host == "" {
		t.Fatalf("proxy not started: %q %v", host, err)
	}
	socket := strings.TrimPrefix(host, "unix://")
	if socket != conversationDockerSocket(root) || socket == daemon || len(socket) > 100 {
		t.Fatalf("workspace must get its own short socket path, got %s", socket)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket must be private to the platform user: %v %v", info, err)
	}
	client := &http.Client{Transport: &http.Transport{Dial: func(_, _ string) (net.Conn, error) { return net.Dial("unix", socket) }}}
	resp, err := client.Get("http://docker/v1.45/_ping")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ping through proxy: %v %v", resp, err)
	}
	resp.Body.Close()
	stop()
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket should be removed on stop: %v", err)
	}

	t.Setenv("AGENTFLOW_WORKSPACE_DOCKER", "off")
	if host, _, err := startConversationDocker(root, workspace); host != "" || err != nil {
		t.Fatalf("disabled proxy started: %q %v", host, err)
	}
}

func TestBwrapConversationHidesHostDockerSocket(t *testing.T) {
	daemon := fakeDockerDaemon(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	dockerDir, err := ensureConversationDockerDir(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dockerDir) })
	args, err := bwrapArgsRequirements(t.TempDir(), SandboxRequirements{ConversationRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	masked, bound := false, false
	for i := 0; i+2 < len(args); i++ {
		masked = masked || args[i] == "--ro-bind" && args[i+1] == "/dev/null" && args[i+2] == daemon
		bound = bound || args[i] == "--bind" && args[i+1] == dockerDir && args[i+2] == dockerDir
	}
	if !masked || !bound {
		t.Fatalf("host socket %s masked=%t, proxy dir %s bound=%t: %v", daemon, masked, dockerDir, bound, args)
	}
}
