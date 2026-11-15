package runtime

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// Reached through a symlinked parent, like /var/run -> /run in Debian
	// images: bwrap cannot mount over such a path, so the real one is masked.
	alias := filepath.Join(t.TempDir(), "run")
	if err := os.Symlink(filepath.Dir(daemon), alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(alias, "docker.sock"))
	real, err := filepath.EvalSymlinks(daemon)
	if err != nil {
		t.Fatal(err)
	}
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
		masked = masked || args[i] == "--ro-bind" && args[i+1] == "/dev/null" && args[i+2] == real
		bound = bound || args[i] == "--bind" && args[i+1] == dockerDir && args[i+2] == dockerDir
		if args[i+1] == "/dev/null" && strings.HasPrefix(args[i+2], alias) {
			t.Fatalf("mask targets the symlinked path %s: %v", args[i+2], args)
		}
	}
	if !masked || !bound {
		t.Fatalf("host socket %s masked=%t, proxy dir %s bound=%t: %v", real, masked, dockerDir, bound, args)
	}
}

func TestDestroyRunRemovesWorkspaceDockerObjects(t *testing.T) {
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
	var mu sync.Mutex
	var calls []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[{"Id":"c1"}]`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/networks"):
			_, _ = w.Write([]byte(`[{"Id":"n1"}]`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/volumes"):
			_, _ = w.Write([]byte(`{"Volumes":[{"Name":"v1"}]}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv("DOCKER_HOST", "unix://"+socket)

	m := NewManager(t.TempDir())
	workspace := m.RunWorkspace("run-docker")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	label := WorkspaceDockerLabel(workspace)
	drain := func() string {
		mu.Lock()
		defer mu.Unlock()
		joined := strings.Join(calls, "\n")
		calls = nil
		return joined
	}
	// A checkpoint reclaim may be restored at the same path: volumes stay.
	if err := m.ReclaimRun("run-docker"); err != nil {
		t.Fatal(err)
	}
	reclaimed := drain()
	if !strings.Contains(reclaimed, "DELETE /containers/c1?force=1&v=1") || !strings.Contains(reclaimed, "DELETE /networks/n1") || strings.Contains(reclaimed, "/volumes") {
		t.Errorf("reclaim must drop containers and networks but keep volumes:\n%s", reclaimed)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("reclaim should remove the directory: %v", err)
	}
	// Deleting the run later still finds the objects by the same label.
	if err := m.DestroyRun("run-docker"); err != nil {
		t.Fatal(err)
	}
	destroyed := drain()
	for _, want := range []string{"DELETE /containers/c1?force=1&v=1", "DELETE /networks/n1", "DELETE /volumes/v1?force=1", "loopforge.workspace%3D" + label} {
		if !strings.Contains(destroyed, want) {
			t.Errorf("missing %q in daemon calls:\n%s", want, destroyed)
		}
	}
}

// /tmp is shared: a socket directory another user prepared is never used.
func TestEnsurePrivateDirRefusesSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "docker")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("dir must be private: %v %v", info, err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("a symlinked socket directory must be refused")
	}
}

func TestReapConversationDockerDirsOfDeletedConversations(t *testing.T) {
	live := t.TempDir()
	gone := filepath.Join(t.TempDir(), "conversation")
	if err := os.MkdirAll(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	liveDir, err := ensureConversationDockerDir(live)
	if err != nil {
		t.Fatal(err)
	}
	goneDir, err := ensureConversationDockerDir(gone)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(liveDir); _ = os.RemoveAll(goneDir) })
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	reapConversationDockerDirs()
	if _, err := os.Stat(goneDir); !os.IsNotExist(err) {
		t.Fatalf("socket dir of a deleted conversation should be reaped: %v", err)
	}
	if _, err := os.Stat(liveDir); err != nil {
		t.Fatalf("live conversation's socket dir must stay: %v", err)
	}
}
