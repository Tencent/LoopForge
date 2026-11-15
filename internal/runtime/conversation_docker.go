package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Conversation workspaces use the machine's Docker daemon directly, as a
// developer on that machine would: what they create are ordinary Docker
// objects that any later workspace can list, reuse or remove. Reaching the
// daemon socket is root on the daemon host; AGENTFLOW_WORKSPACE_DOCKER=off
// keeps workspaces away from it.

// conversationDockerSocketTarget is where a Docker workspace sees the daemon
// socket, Docker's default endpoint.
const conversationDockerSocketTarget = "/var/run/docker.sock"

// workspaceDockerSocket is the daemon socket workspaces use, or "" when
// workspace Docker is disabled or the machine has no daemon.
func workspaceDockerSocket() string {
	if dockerWorkspaceDisabled() {
		return ""
	}
	socket := daemonSocketPath()
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		return ""
	}
	return socket
}

func daemonSocketPath() string {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); strings.HasPrefix(host, "unix://") {
		return strings.TrimPrefix(host, "unix://")
	}
	return "/var/run/docker.sock"
}

// workspaceDockerSocketSource is the bind source of socket for a Docker
// workspace, which the daemon resolves on its own host. A containerized
// platform reaches the host socket through a mount of its own, so
// compose.yaml passes the host path in AGENTFLOW_DOCKER_SOCKET_HOST; on a
// native install both paths are the same.
func workspaceDockerSocketSource(socket string) string {
	if host := strings.TrimSpace(os.Getenv("AGENTFLOW_DOCKER_SOCKET_HOST")); host != "" {
		return host
	}
	return socket
}

// workspaceDockerSocketGroup is the socket's group, added to a Docker
// workspace that runs as a non-root service user so it can connect.
func workspaceDockerSocketGroup(socket string) string {
	info, err := os.Stat(socket)
	if err != nil {
		return ""
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Gid != 0 {
		return strconv.FormatUint(uint64(st.Gid), 10)
	}
	return ""
}

// The CLI works in <root>/ws in every mode so native sessions resume across
// mode switches, but on the daemon's host that path is the platform-mode
// directory, not the workspace. The docker shim, first on the workspace PATH,
// gives the daemon the workspace's host path instead: in arguments (bind
// sources such as -v "$PWD":/src) and as the cwd that docker compose resolves
// relative paths against. It is a convenience, not a boundary.
const (
	conversationBinEnv = "AGENTFLOW_CONVERSATION_BIN"
	workspaceAliasEnv  = "AGENTFLOW_WORKSPACE_ALIAS"
	workspaceHostEnv   = "AGENTFLOW_WORKSPACE_HOST"
)

const conversationDockerShim = `#!/bin/sh
alias=${` + workspaceAliasEnv + `:-}
host=${` + workspaceHostEnv + `:-}
self=${0%/*}
set -f
rest=
IFS=:
for dir in $PATH; do
  [ "$dir" = "$self" ] || rest=${rest:+$rest:}$dir
done
unset IFS
set +f
PATH=$rest
export PATH
real=$(command -v docker) || { echo "docker: command not found" >&2; exit 127; }
if [ "$real" = "$0" ]; then
  echo "docker: command not found" >&2
  exit 127
fi
if [ -z "$alias" ] || [ -z "$host" ] || [ "$alias" = "$host" ]; then
  exec "$real" "$@"
fi
rewrite() {
  out=
  rest=$1
  while :; do
    pre=${rest%%"$alias"*}
    [ "$pre" = "$rest" ] && break
    post=${rest#*"$alias"}
    case $pre in
      ""|*=|*,|*:|*" ") case $post in ""|/*|:*|,*|" "*) out=$out$pre$host; rest=$post; continue ;; esac ;;
    esac
    out=$out$pre$alias
    rest=$post
  done
  printf '%s' "$out$rest"
}
for arg do
  set -- "$@" "$(rewrite "$arg")"
  shift
done
case $PWD in
  "$alias"|"$alias"/*) cd "$host${PWD#"$alias"}" 2>/dev/null || true ;;
esac
exec "$real" "$@"
`

// Native CLIs run their shell tool from a login-shell snapshot, and system
// profiles (Debian's /etc/profile) reset PATH there. The conversation HOME's
// login files put the shim directory back; without the variable (platform
// mode) the line does nothing.
const conversationProfileLine = `if [ -n "${` + conversationBinEnv + `:-}" ]; then case ":$PATH:" in *":$` + conversationBinEnv + `:"*) ;; *) PATH=$` + conversationBinEnv + `:$PATH; export PATH ;; esac; fi # loopforge workspace docker shim`

// writeConversationDockerShim installs the docker shim under root, which both
// sandbox drivers mount read-only, and returns its directory for PATH.
func writeConversationDockerShim(root string) (string, error) {
	dir := filepath.Join(root, "workspace-bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(conversationDockerShim), 0o755); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(dir, "docker"), 0o755); err != nil {
		return "", err
	}
	return dir, ensureConversationProfile(filepath.Join(root, "home"))
}

// ensureConversationProfile appends the PATH line to ~/.profile and to any
// login file a shell would read instead of it. The sandbox can write this
// HOME, so every access stays inside it.
func ensureConversationProfile(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	home, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer home.Close()
	for _, name := range []string{".profile", ".bash_profile", ".bash_login", ".zprofile"} {
		data, err := home.ReadFile(name)
		if os.IsNotExist(err) && name != ".profile" {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("读取会话 HOME %s 失败: %w", name, err)
		}
		if strings.Contains(string(data), conversationProfileLine) {
			continue
		}
		line := conversationProfileLine + "\n"
		if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
			line = "\n" + line
		}
		file, err := home.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("写入会话 HOME %s 失败: %w", name, err)
		}
		_, err = file.WriteString(line)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("写入会话 HOME %s 失败: %w", name, err)
		}
	}
	return nil
}

func dockerWorkspaceDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_WORKSPACE_DOCKER"))) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// hostDockerSockets are the daemon sockets a native workspace has masked
// when workspace Docker is off. Only fully resolved paths are returned: bwrap
// cannot mount over a path with a symlinked parent (such as /var/run -> /run
// in Debian images), and masking the real socket also masks every symlink
// that leads to it.
func hostDockerSockets() []string {
	candidates := []string{"/var/run/docker.sock", "/run/docker.sock", "/run/containerd/containerd.sock", "/run/podman/podman.sock", daemonSocketPath()}
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
