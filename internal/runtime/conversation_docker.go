package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/dockerproxy"
)

// conversationDockerBase holds every workspace's Docker socket directory. A
// Docker workspace is a sibling container whose binds the host daemon
// resolves, so a containerized platform must share this directory with the
// host at the same path (compose.yaml mounts it); a per-conversation directory
// directly under the platform container's /tmp would reach the workspace as an
// empty host directory. It stays on the daemon host's own filesystem: a unix
// socket cannot live on a shared data directory such as a macOS file share.
const conversationDockerBase = "/tmp/lf-docker"

// conversationDockerDir holds a workspace CLI's Docker socket. Unix socket
// paths are limited to ~108 bytes, too short for the conversation HOME, so it
// is a short per-conversation directory that Docker and bwrap workspaces bind
// at the same path. It is created before the sandbox mounts it and kept for
// the conversation: a Docker workspace's bind would not see a recreated one.
func conversationDockerDir(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(conversationDockerBase, hex.EncodeToString(sum[:8]))
}

func conversationDockerSocket(root string) string {
	return filepath.Join(conversationDockerDir(root), "docker.sock")
}

// conversationDockerOwner records which conversation root a socket directory
// belongs to, so directories of deleted conversations can be reaped.
const conversationDockerOwner = "owner"

// ensureConversationDockerDir creates the socket directory, private to the
// platform user (bwrap and Docker workspaces run as that user).
func ensureConversationDockerDir(root string) (string, error) {
	if err := ensurePrivateDir(conversationDockerBase); err != nil {
		return "", err
	}
	dir := conversationDockerDir(root)
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("工作区 Docker 目录 %s 不可用", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, conversationDockerOwner), []byte(root), 0o600)
}

// ensurePrivateDir makes dir a real directory owned by the platform user and
// private to it. /tmp is shared, so a directory or symlink another user put
// there first is refused rather than used.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("工作区 Docker 目录 %s 不可用", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("工作区 Docker 目录 %s 不属于平台用户", dir)
	}
	return os.Chmod(dir, 0o700)
}

// reapConversationDockerDirs removes socket directories whose conversation
// root is gone (deleted conversations), wherever the root was deleted.
func reapConversationDockerDirs() {
	dirs, _ := filepath.Glob(filepath.Join(conversationDockerBase, "*"))
	for _, dir := range dirs {
		owner, err := os.ReadFile(filepath.Join(dir, conversationDockerOwner))
		if err != nil || !filepath.IsAbs(string(owner)) || conversationDockerDir(string(owner)) != dir {
			continue
		}
		if _, err := os.Stat(string(owner)); os.IsNotExist(err) {
			_ = os.RemoveAll(dir)
		}
	}
}

// WorkspaceDockerLabel is the loopforge.workspace label value of the Docker
// objects a workspace creates: a hash of its resolved host path. A workspace
// whose directory is already gone resolves through its parent, so cleanup
// after a reclaim still finds its objects.
func WorkspaceDockerLabel(workspace string) string {
	sum := sha256.Sum256([]byte(resolveExistingPath(workspace)))
	return hex.EncodeToString(sum[:8])
}

func resolveExistingPath(p string) string {
	rest := ""
	for cur := filepath.Clean(p); ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// cleanupWorkspaceDocker removes the containers and networks (and, with
// volumes, the volumes) a workspace created through its proxy.
func cleanupWorkspaceDocker(workspace string, volumes bool) {
	if dockerWorkspaceDisabled() {
		return
	}
	upstream := dockerproxy.DefaultUpstream()
	if info, err := os.Stat(upstream); err != nil || info.Mode()&os.ModeSocket == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := dockerproxy.Cleanup(ctx, upstream, WorkspaceDockerLabel(workspace), volumes); err != nil {
		log.Printf("clean workspace docker objects %s: %v", workspace, err)
	}
}

func dockerWorkspaceDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_WORKSPACE_DOCKER"))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// hostDockerSockets are daemon sockets a native workspace must not reach
// directly; it gets the filtering proxy instead. Only fully resolved paths
// are returned: bwrap cannot mount over a path with a symlinked parent (such
// as /var/run -> /run in Debian images), and masking the real socket also
// masks every symlink that leads to it.
func hostDockerSockets() []string {
	candidates := []string{"/var/run/docker.sock", "/run/docker.sock", "/run/containerd/containerd.sock", "/run/podman/podman.sock", dockerproxy.DefaultUpstream()}
	seen := map[string]bool{}
	var out []string
	for _, path := range candidates {
		p := resolvedPath(path)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSocket != 0 {
			out = append(out, p)
		}
	}
	return out
}

func resolvedPath(p string) string {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return resolved
}

// startConversationDocker gives a workspace CLI its own Docker endpoint when
// the machine has a daemon: everything it creates is labelled with the
// workspace, and it can only touch those objects. It returns the DOCKER_HOST
// value and a stop function, or "" when Docker is unavailable or disabled
// (AGENTFLOW_WORKSPACE_DOCKER=off).
func startConversationDocker(root, workspace string) (string, func(), error) {
	if dockerWorkspaceDisabled() {
		return "", nil, nil
	}
	reapConversationDockerDirs()
	upstream := dockerproxy.DefaultUpstream()
	if info, err := os.Stat(upstream); err != nil || info.Mode()&os.ModeSocket == 0 {
		return "", nil, nil
	}
	if _, err := ensureConversationDockerDir(root); err != nil {
		return "", nil, err
	}
	hostWorkspace := resolveExistingPath(workspace)
	home := filepath.Join(root, "home")
	proxy, err := dockerproxy.Start(dockerproxy.Config{
		Upstream:  upstream,
		Socket:    conversationDockerSocket(root),
		Workspace: WorkspaceDockerLabel(hostWorkspace),
		Paths:     []dockerproxy.PathMapping{{From: filepath.Join(root, "ws"), To: hostWorkspace}},
		BindRoots: []string{hostWorkspace, home},
	})
	if err != nil {
		return "", nil, err
	}
	stop := func() {
		_ = proxy.Close()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_ = proxy.StopContainers(ctx)
		}()
	}
	return "unix://" + proxy.Socket(), stop, nil
}
