package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/dockerproxy"
)

// conversationDockerDir holds a workspace CLI's Docker socket. Unix socket
// paths are limited to ~108 bytes, too short for the conversation HOME, so it
// is a short per-conversation directory that Docker and bwrap workspaces bind
// at the same path. It is created before the sandbox mounts it and kept for
// the conversation: a Docker workspace's bind would not see a recreated one.
func conversationDockerDir(root string) string {
	sum := sha256.Sum256([]byte(root))
	return "/tmp/lf-docker-" + hex.EncodeToString(sum[:8])
}

func conversationDockerSocket(root string) string {
	return filepath.Join(conversationDockerDir(root), "docker.sock")
}

// ensureConversationDockerDir creates the socket directory, private to the
// platform user (bwrap and Docker workspaces run as that user).
func ensureConversationDockerDir(root string) (string, error) {
	dir := conversationDockerDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("工作区 Docker 目录 %s 不可用", dir)
	}
	return dir, os.Chmod(dir, 0o700)
}

// hostDockerSockets are daemon sockets a native workspace must not reach
// directly; it gets the filtering proxy instead.
func hostDockerSockets() []string {
	candidates := []string{"/var/run/docker.sock", "/run/docker.sock", "/run/containerd/containerd.sock", "/run/podman/podman.sock", dockerproxy.DefaultUpstream()}
	seen := map[string]bool{}
	var out []string
	for _, path := range candidates {
		for _, p := range []string{path, resolvedPath(path)} {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSocket != 0 {
				out = append(out, p)
			}
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
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_WORKSPACE_DOCKER"))) {
	case "0", "off", "false", "no":
		return "", nil, nil
	}
	upstream := dockerproxy.DefaultUpstream()
	if info, err := os.Stat(upstream); err != nil || info.Mode()&os.ModeSocket == 0 {
		return "", nil, nil
	}
	if _, err := ensureConversationDockerDir(root); err != nil {
		return "", nil, err
	}
	hostWorkspace := workspace
	if resolved := resolvedPath(workspace); resolved != "" {
		hostWorkspace = resolved
	}
	sum := sha256.Sum256([]byte(hostWorkspace))
	home := filepath.Join(root, "home")
	proxy, err := dockerproxy.Start(dockerproxy.Config{
		Upstream:  upstream,
		Socket:    conversationDockerSocket(root),
		Workspace: hex.EncodeToString(sum[:8]),
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
