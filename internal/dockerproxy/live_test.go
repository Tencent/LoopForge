package dockerproxy

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveDockerCLI drives the real docker CLI through the proxy against a
// real daemon. Opt in with DOCKERPROXY_LIVE_UPSTREAM=/path/to/docker.sock; it
// only creates containers (never starts them), so a daemon that cannot run
// containers is enough.
func TestLiveDockerCLI(t *testing.T) {
	upstream := os.Getenv("DOCKERPROXY_LIVE_UPSTREAM")
	if upstream == "" {
		t.Skip("set DOCKERPROXY_LIVE_UPSTREAM to a docker daemon socket")
	}
	dir, err := os.MkdirTemp("/tmp", "dplive")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	workspace := filepath.Join(dir, "workspace")
	root := filepath.Join(dir, "conv")
	for _, d := range []string{filepath.Join(workspace, "src"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id := "live" + filepath.Base(dir)
	proxy, err := Start(Config{
		Upstream: upstream, Socket: filepath.Join(root, "home", ".loopforge", "docker.sock"), Workspace: id,
		Paths:     []PathMapping{{From: filepath.Join(root, "ws"), To: workspace}},
		BindRoots: []string{workspace, filepath.Join(root, "home")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })

	docker := func(host string, stdin []byte, args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+host)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	via := func(args ...string) (string, error) { return docker(proxy.Socket(), nil, args...) }
	must := func(out string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return out
	}
	refused := func(what string, out string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(out, "LoopForge") {
			t.Errorf("%s should be refused by the proxy: %v %s", what, err, out)
		}
	}

	var image bytes.Buffer
	tw := tar.NewWriter(&image)
	_ = tw.WriteHeader(&tar.Header{Name: "hello", Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	tag := "loopforge-proxy-live:" + id
	// Importing needs the daemon to unshare; some CI containers cannot. The
	// refusals below happen before the daemon and still run without an image.
	_, importErr := docker(proxy.Socket(), image.Bytes(), "import", "-", tag)
	t.Cleanup(func() { _, _ = docker(upstream, nil, "rmi", "-f", tag) })
	haveImage := importErr == nil
	if !haveImage {
		t.Logf("daemon cannot import images (%v); checking create calls reach the daemon instead", importErr)
		out, err := via("create", "--pull", "never", "--name", id+"-a", "-v", filepath.Join(root, "ws", "src")+":/src:ro", "-v", id+"-cache:/cache", tag, "/hello")
		if err == nil || strings.Contains(out, "LoopForge") || !strings.Contains(strings.ToLower(out), "no such image") {
			t.Errorf("allowed create should reach the daemon: %v %s", err, out)
		}
	}
	mine, foreign := "", ""
	if haveImage {
		mine = must(via("create", "--name", id+"-a", "-v", filepath.Join(root, "ws", "src")+":/src:ro", "-v", id+"-cache:/cache", tag, "/hello"))
		t.Cleanup(func() { _, _ = docker(upstream, nil, "rm", "-f", mine) })
		if got := must(docker(upstream, nil, "inspect", "-f", `{{index .Config.Labels "loopforge.workspace"}} {{json .HostConfig.Mounts}}`, mine)); !strings.HasPrefix(got, id+" ") || !strings.Contains(got, `"Subpath":"src"`) {
			t.Errorf("label/subpath mount as seen by the daemon: %s", got)
		}
		foreign = must(docker(upstream, nil, "create", "--name", id+"-foreign", tag, "/hello"))
		t.Cleanup(func() { _, _ = docker(upstream, nil, "rm", "-f", foreign) })
	}
	t.Cleanup(func() { _, _ = docker(upstream, nil, "volume", "rm", "-f", id+"-cache", id+"-v") })
	// Docker 26+: the workspace subdirectory is mounted as a subpath of a
	// volume bound to the workspace root (no symlink race).
	bindVolumes := must(docker(upstream, nil, "volume", "ls", "-q", "--filter", "label=loopforge.workspace="+id))
	var bindVolume string
	for _, name := range strings.Fields(bindVolumes) {
		if strings.HasPrefix(name, "lf-bind-"+id+"-") {
			bindVolume = name
		}
	}
	if bindVolume == "" {
		t.Errorf("no root-backed bind volume among %q", bindVolumes)
	} else if got := must(docker(upstream, nil, "volume", "inspect", "-f", `{{.Options.o}} {{.Options.device}}`, bindVolume)); got != "bind "+workspace {
		t.Errorf("bind volume options = %q", got)
	}
	if got := must(docker(upstream, nil, "volume", "inspect", "-f", `{{index .Labels "loopforge.workspace"}}`, id+"-cache")); got != id {
		t.Errorf("named volume created without the workspace label: %q", got)
	}
	if haveImage {
		if out := must(via("ps", "-a", "--format", "{{.Names}}")); out != id+"-a" {
			t.Errorf("ps -a should list only this workspace's container, got %q", out)
		}
		must(via("inspect", mine))
		out, err := via("rm", "-f", foreign)
		refused("removing another workspace's container", out, err)
		out, err = via("inspect", foreign)
		refused("inspecting another workspace's container", out, err)
	}
	out, err := via("create", "--privileged", tag, "/hello")
	refused("--privileged", out, err)
	out, err = via("create", "-v", "/etc:/host-etc", tag, "/hello")
	refused("binding /etc", out, err)
	out, err = via("create", "--mount", "type=bind,source=/,target=/host", tag, "/hello")
	refused("--mount of /", out, err)
	out, err = via("create", "--network", "host", tag, "/hello")
	refused("--network host", out, err)
	out, err = via("create", "--pid", "host", tag, "/hello")
	refused("--pid host", out, err)
	out, err = via("create", "--cap-add", "SYS_ADMIN", tag, "/hello")
	refused("--cap-add", out, err)
	out, err = via("create", "--device", "/dev/null", tag, "/hello")
	refused("--device", out, err)
	out, err = via("system", "df")
	refused("system df", out, err)
	out, err = via("image", "prune", "-af")
	refused("image prune", out, err)

	must(via("volume", "create", id+"-v"))
	other := must(docker(upstream, nil, "volume", "create", id+"-other"))
	t.Cleanup(func() { _, _ = docker(upstream, nil, "volume", "rm", "-f", other) })
	if out := must(via("volume", "ls", "-q")); strings.Contains(out, id+"-other") || !strings.Contains(out, id+"-v") {
		t.Errorf("volume ls not scoped: %q", out)
	}
	out, err = via("volume", "rm", other)
	refused("removing another workspace's volume", out, err)
	out, err = via("create", "-v", other+":/x", tag, "/hello")
	refused("mounting another workspace's volume", out, err)

	out, err = via("load", "-i", "/dev/null")
	refused("docker load", out, err)
	// Daemons started with --bridge=none cannot create bridge networks.
	_, netErr := via("network", "create", "--driver", "bridge", "--internal", id+"-net")
	t.Cleanup(func() { _, _ = docker(upstream, nil, "network", "rm", id+"-net") })

	// Deleting the workspace removes everything it created, nothing else.
	if err := Cleanup(context.Background(), upstream, id, true); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if out := must(docker(upstream, nil, "volume", "ls", "-q")); strings.Contains(out, id+"-v") || strings.Contains(out, id+"-cache") || !strings.Contains(out, id+"-other") {
		t.Errorf("after cleanup volumes = %q", out)
	}
	if out := must(docker(upstream, nil, "network", "ls", "--format", "{{.Name}}")); netErr == nil && strings.Contains(out, id+"-net") {
		t.Errorf("after cleanup networks = %q", out)
	}
	if haveImage {
		must(via("rm", mine))
		if _, err := docker(upstream, nil, "inspect", foreign); err != nil {
			t.Errorf("foreign container must survive: %v", err)
		}
	}
}

// TestLiveBuildx runs docker buildx (BuildKit over /grpc) through the proxy.
// Opt in with DOCKERPROXY_LIVE_UPSTREAM; needs the docker-buildx plugin.
func TestLiveBuildx(t *testing.T) {
	upstream := os.Getenv("DOCKERPROXY_LIVE_UPSTREAM")
	if upstream == "" {
		t.Skip("set DOCKERPROXY_LIVE_UPSTREAM to a docker daemon socket")
	}
	if err := exec.Command("docker", "buildx", "version").Run(); err != nil {
		t.Skip("docker buildx not installed")
	}
	dir, err := os.MkdirTemp("/tmp", "dpbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	workspace := filepath.Join(dir, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "Dockerfile"), []byte("FROM scratch\nCOPY hello /hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hello"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := "bx" + filepath.Base(dir)
	proxy, err := Start(Config{Upstream: upstream, Socket: filepath.Join(dir, "d.sock"), Workspace: id, BindRoots: []string{workspace}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	build := func(args ...string) (string, error) {
		cmd := exec.Command("docker", append([]string{"buildx", "build", "--progress", "plain"}, append(args, workspace)...)...)
		cmd.Env = append(os.Environ(), "DOCKER_HOST=unix://"+proxy.Socket(), "BUILDX_BUILDER=", "DOCKER_CONFIG="+dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := build("-t", "lf-buildx-probe:"+id, "--load")
	t.Logf("plain build: err=%v\n%s", err, out)
	if strings.Contains(out, "LoopForge") {
		t.Errorf("an ordinary buildx build must pass the proxy:\n%s", out)
	}
	for name, args := range map[string][]string{
		"network host": {"--network", "host"},
		"entitlement":  {"--allow", "network.host"},
	} {
		out, err := build(args...)
		if err == nil || !strings.Contains(out, "LoopForge") {
			t.Errorf("%s should be refused by the proxy: %v\n%s", name, err, out)
		}
	}
}
