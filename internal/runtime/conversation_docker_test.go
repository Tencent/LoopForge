package runtime

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestWorkspaceDockerSocketFollowsDaemonAndSwitch(t *testing.T) {
	daemon := fakeDockerDaemon(t)
	if got := workspaceDockerSocket(); got != daemon {
		t.Fatalf("workspace socket = %q, want the daemon %q", got, daemon)
	}
	if got := workspaceDockerSocketSource(daemon); got != daemon {
		t.Fatalf("native installs bind the socket at its own path, got %q", got)
	}
	t.Setenv("AGENTFLOW_DOCKER_SOCKET_HOST", "/var/run/docker.sock")
	if got := workspaceDockerSocketSource(daemon); got != "/var/run/docker.sock" {
		t.Fatalf("a containerized platform binds the host path, got %q", got)
	}
	t.Setenv("AGENTFLOW_WORKSPACE_DOCKER", "off")
	if got := workspaceDockerSocket(); got != "" {
		t.Fatalf("disabled workspace Docker still exposes %q", got)
	}
	t.Setenv("AGENTFLOW_WORKSPACE_DOCKER", "")
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "missing.sock"))
	if got := workspaceDockerSocket(); got != "" {
		t.Fatalf("no daemon, but workspace socket %q", got)
	}
}

func TestConversationDockerShimHandsTheDaemonHostPaths(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(t.TempDir(), "workspaces", "run-1")
	alias := filepath.Join(root, "ws")
	for _, dir := range []string{filepath.Join(host, "app"), filepath.Join(alias, "app")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin, err := writeConversationDockerShim(root)
	if err != nil {
		t.Fatal(err)
	}
	realBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(realBin, "docker"), []byte("#!/bin/sh\npwd\nfor a do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(env []string, args ...string) []string {
		cmd := exec.Command(filepath.Join(bin, "docker"), args...)
		cmd.Dir = filepath.Join(alias, "app")
		cmd.Env = append([]string{"PATH=" + bin + ":" + realBin + ":/usr/bin:/bin", "PWD=" + cmd.Dir}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("shim: %v: %s", err, out)
		}
		return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	}
	env := []string{workspaceAliasEnv + "=" + alias, workspaceHostEnv + "=" + host}
	got := run(env, "run", "-v", alias+"/app:/src", "--mount", "type=bind,src="+alias+",dst="+alias, "-w", alias, "--volume="+alias+"x:/y", "alpine")
	want := []string{filepath.Join(host, "app"), "run", "-v", host + "/app:/src", "--mount", "type=bind,src=" + host + ",dst=" + host, "-w", host, "--volume=" + alias + "x:/y", "alpine"}
	if !slices.Equal(got, want) {
		t.Fatalf("shim output\n got %q\nwant %q", got, want)
	}
	if got := run(nil, "ps", "-v", alias+":/src"); got[1] != "ps" || got[3] != alias+":/src" {
		t.Fatalf("without a workspace the shim must pass arguments through: %q", got)
	}
}

func TestConversationProfileRestoresTheShimAfterALoginShellResetsPath(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte("export EDITOR=vi"), 0o644); err != nil {
		t.Fatal(err)
	}
	var bin string
	for range 2 {
		var err error
		if bin, err = writeConversationDockerShim(root); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".profile", ".bash_profile"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || strings.Count(string(data), conversationProfileLine) != 1 {
			t.Fatalf("%s must carry the PATH line once: %v %q", name, err, data)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".bash_profile")); !strings.HasPrefix(string(data), "export EDITOR=vi\n") {
		t.Fatalf("existing login file content must be kept: %q", data)
	}
	login := func(env ...string) string {
		cmd := exec.Command("sh", "-c", `PATH=/usr/bin:/bin; . "$HOME/.profile"; . "$HOME/.profile"; printf '%s' "$PATH"`)
		cmd.Env = append([]string{"HOME=" + home}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("login profile: %v: %s", err, out)
		}
		return string(out)
	}
	if got := login(conversationBinEnv + "=" + bin); got != bin+":/usr/bin:/bin" {
		t.Fatalf("a login shell must get the shim directory back once: %q", got)
	}
	if got := login(); got != "/usr/bin:/bin" {
		t.Fatalf("platform mode must leave PATH alone: %q", got)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".zprofile")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeConversationDockerShim(root); err == nil {
		t.Fatal("a login file linked out of HOME must be refused")
	}
	if data, _ := os.ReadFile(outside); string(data) != "outside\n" {
		t.Fatalf("a file outside HOME must not be written: %q", data)
	}
}

func TestBwrapConversationLayoutAndDockerSocket(t *testing.T) {
	daemon := fakeDockerDaemon(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	masks := func() []string {
		args, err := bwrapArgsRequirements(workspace, SandboxRequirements{ConversationRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--bind "+workspace+" "+workspace) || !strings.Contains(joined, "--bind "+workspace+" "+filepath.Join(root, "ws")) {
			t.Fatalf("the workspace must sit at <root>/ws and at its host path: %v", args)
		}
		var out []string
		for i := 0; i+2 < len(args); i++ {
			if args[i] == "--ro-bind" && args[i+1] == "/dev/null" {
				out = append(out, args[i+2])
			}
		}
		return out
	}
	if got := masks(); len(got) != 0 {
		t.Fatalf("workspace Docker is on, yet sockets are masked: %v", got)
	}

	// Off: the daemon is masked, by its resolved path. bwrap cannot mount
	// over a path with a symlinked parent, like /var/run -> /run in Debian.
	t.Setenv("AGENTFLOW_WORKSPACE_DOCKER", "off")
	alias := filepath.Join(t.TempDir(), "run")
	if err := os.Symlink(filepath.Dir(daemon), alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(alias, "docker.sock"))
	real, err := filepath.EvalSymlinks(daemon)
	if err != nil {
		t.Fatal(err)
	}
	got := masks()
	if !slices.Contains(got, real) {
		t.Fatalf("host socket %s not masked: %v", real, got)
	}
	for _, target := range got {
		if strings.HasPrefix(target, alias) {
			t.Fatalf("mask targets the symlinked path %s", target)
		}
	}
}
