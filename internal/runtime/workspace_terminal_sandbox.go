package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func terminalBaseEnvironment(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TERM=xterm-256color", "LANG=C.UTF-8", "PS1=workspace $ "}
}

// sandboxTerminalCommand deliberately supports only concrete platform drivers.
// An unknown Sandbox must fail closed instead of silently opening a host shell.
func sandboxTerminalCommand(ctx context.Context, sandbox Sandbox) (*exec.Cmd, string, error) {
	if sandbox == nil {
		return nil, "", fmt.Errorf("工作区执行环境不可用")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	workspace := sandbox.Workspace()
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("工作区不可用")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	home, err := EnsureControlDirectory(workspace, "private-home", "terminal-"+hex.EncodeToString(random))
	if err != nil {
		return nil, "", err
	}
	command := CommandSpec{Argv: []string{"/bin/sh", "-i"}, environment: map[string]string{
		"HOME": home, "TERM": "xterm-256color", "LANG": "C.UTF-8", "PS1": "workspace $ ",
		"AGENTFLOW_TERMINAL_ID": filepath.Base(home),
	}}
	if sandboxDriver(sandbox) == "docker" {
		rel, relErr := filepath.Rel(workspace, home)
		if relErr != nil {
			_ = removeTerminalHome(home)
			return nil, "", relErr
		}
		command.environment["HOME"] = filepath.ToSlash(filepath.Join("/workspace", rel))
		command.Argv = []string{"sh", "-c", `umask 077
printf '%s\n' "$$" > "$HOME/.terminal-pid" || exit
exec /bin/sh -i`}
	}
	cmd, err := sandboxTerminalCommandBound(ctx, sandbox, command, terminalBaseEnvironment(home))
	if err != nil {
		_ = removeTerminalHome(home)
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		_ = removeTerminalHome(home)
		return nil, "", err
	}
	return cmd, home, nil
}

func sandboxTerminalCommandBound(ctx context.Context, sandbox Sandbox, command CommandSpec, base []string) (*exec.Cmd, error) {
	// A terminal is user-directed and needs the run's Git binding, never its model
	// provider keys. Overlay variables are the same explicit sandbox configuration
	// that SOP commands receive; unrelated server environment is not inherited.
	switch s := sandbox.(type) {
	case *managedSandbox:
		s.credentialsMu.RLock()
		credentials := s.credentials
		s.credentialsMu.RUnlock()
		command.CredentialBindings = []CredentialBinding{BindGit}
		bound, err := bindCommand(s.Sandbox, s.workspace, command, credentials)
		if err != nil {
			return nil, err
		}
		return sandboxTerminalCommandBound(ctx, s.Sandbox, bound, base)
	case *localSandbox:
		return s.command(context.Background(), command, base)
	case *processSandbox:
		return s.command(context.Background(), command, base)
	case *dockerSandbox:
		if err := s.ensureStarted(ctx); err != nil {
			return nil, err
		}
		return s.terminalCommand(command), nil
	default:
		return nil, fmt.Errorf("工作区沙箱不支持交互终端；不能降级为宿主机终端")
	}
}

func (s *dockerSandbox) terminalCommand(command CommandSpec) *exec.Cmd {
	args := []string{"exec", "-it"}
	for _, value := range appendEnvironment(nil, s.profile.Environment, command.environment) {
		args = append(args, "-e", value)
	}
	args = append(args, s.name)
	args = append(args, dockerWorkspaceExecArgv(command.Argv)...)
	cmd := exec.Command("docker", args...)
	// Docker's own client needs its context/socket configuration on the host. The
	// container receives only the explicit -e list above, never this host HOME.
	for _, key := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	return cmd
}

// SandboxEnvironmentIdentity identifies an execution environment generation.
// Workspace preparation stamps must include this value: a bind-mounted file
// stamp survives container recreation, while installed tools and processes do
// not. Values are hashed so configured environment secrets never enter receipts.
func SandboxEnvironmentIdentity(ctx context.Context, sandbox Sandbox) (string, error) {
	switch s := sandbox.(type) {
	case *managedSandbox:
		return SandboxEnvironmentIdentity(ctx, s.Sandbox)
	case *dockerSandbox:
		if err := s.ensureStarted(ctx); err != nil {
			return "", err
		}
		if err := s.ensureScriptContainer(ctx); err != nil {
			return "", err
		}
	}
	return ReadSandboxEnvironmentIdentity(ctx, sandbox)
}

// ReadSandboxEnvironmentIdentity inspects an existing execution environment
// without creating or starting a container. Read-only status/peek paths use it
// to validate a warm preparation receipt while another group member is running.
func ReadSandboxEnvironmentIdentity(ctx context.Context, sandbox Sandbox) (string, error) {
	switch s := sandbox.(type) {
	case *managedSandbox:
		return ReadSandboxEnvironmentIdentity(ctx, s.Sandbox)
	case *dockerSandbox:
		output, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}|{{.Id}}|{{.State.StartedAt}}", s.name).Output()
		if err != nil {
			return "", fmt.Errorf("inspect workspace environment: %w", err)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(output)), "true|") {
			return "", fmt.Errorf("workspace execution environment is not running")
		}
		companion, companionErr := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}}|{{.Id}}|{{.State.StartedAt}}", dockerScriptContainerName(s.name)).CombinedOutput()
		if companionErr != nil && !dockerObjectGone(string(companion), companionErr) {
			return "", fmt.Errorf("inspect script environment: %w", companionErr)
		}
		if companionErr == nil {
			output = append(output, companion...)
		}
		overlay, err := json.Marshal(s.profile.Environment)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(append(output, overlay...))
		return "docker:" + hex.EncodeToString(sum[:]), nil
	case *localSandbox:
		return filesystemEnvironmentIdentity(s.Workspace(), s.Driver(), s.environment)
	case *processSandbox:
		return filesystemEnvironmentIdentity(s.Workspace(), s.Driver(), s.profile.Environment)
	default:
		return "", fmt.Errorf("unsupported workspace environment identity")
	}
}

func filesystemEnvironmentIdentity(workspace, driver string, environment map[string]string) (string, error) {
	info, err := os.Stat(workspace)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("workspace filesystem identity unavailable")
	}
	data, err := json.Marshal(struct {
		Driver       string
		Workspace    string
		Device       uint64
		Inode        uint64
		Environment  map[string]string
		ProcessEpoch string
	}{driver, filepath.Clean(workspace), uint64(stat.Dev), stat.Ino, environment, terminalProcessEnvironmentEpoch()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return driver + ":" + hex.EncodeToString(sum[:]), nil
}

// Root-scoped deletion prevents a workspace shell from replacing .agentflow or
// private-home with a symlink and turning terminal cleanup into a host deletion.
func removeTerminalHome(home string) error {
	if home == "" {
		return nil
	}
	parent := filepath.Dir(home)
	if strings.HasPrefix(filepath.Base(home), "terminal-") && filepath.Base(parent) == "private-home" && filepath.Base(filepath.Dir(parent)) == ".agentflow" {
		workspace := filepath.Dir(filepath.Dir(parent))
		root, err := os.OpenRoot(workspace)
		if err != nil {
			return err
		}
		defer root.Close()
		return root.RemoveAll(filepath.Join(".agentflow", "private-home", filepath.Base(home)))
	}
	return os.RemoveAll(home)
}

// Local tool installs may persist on disk, but background services die when
// this execution process restarts. Share one epoch across handles in a process,
// and force preparation to recheck after server/runner restart.
var terminalProcessEnvironmentEpoch = sync.OnceValue(func() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err == nil {
		return hex.EncodeToString(random)
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
})

func sandboxTerminalCleanup(sandbox Sandbox, home string) {
	switch s := sandbox.(type) {
	case *managedSandbox:
		sandboxTerminalCleanup(s.Sandbox, home)
	case *dockerSandbox:
		rel, err := filepath.Rel(s.workspace, home)
		if err != nil {
			return
		}
		path := filepath.ToSlash(filepath.Join("/workspace", rel, ".terminal-pid"))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A Docker exec client's death does not hang up its container shell. Verify
		// the unique terminal token before signalling the exact shell/process group;
		// a stale or edited pid file must never kill another member's execution.
		_ = exec.CommandContext(ctx, "docker", "exec", s.name, "sh", "-c", dockerTerminalStopScript, "terminal-stop", path, filepath.Base(home)).Run()
	}
}

const dockerTerminalStopScript = `pid=$(cat "$1" 2>/dev/null) || exit 0
case "$pid" in ''|*[!0-9]*) exit 0;; esac
[ "$pid" -gt 1 ] || exit 0
matches() {
  [ -r "/proc/$pid/environ" ] && tr '\000' '\n' < "/proc/$pid/environ" | grep -Fxq "AGENTFLOW_TERMINAL_ID=$2"
}
matches "$@" || exit 0
# The shell session's current foreground job may have its own process group.
stat=$(cat "/proc/$pid/stat" 2>/dev/null) || exit 0
set -- "${stat##*) }" "$2"
fields=$1
token=$2
set -- $fields
session=$4
foreground=$6
if [ "$session" = "$pid" ]; then
 case "$foreground" in ''|*[!0-9]*) ;; *)
  [ "$foreground" -gt 1 ] && kill -HUP "-$foreground" 2>/dev/null
 ;; esac
fi
kill -HUP "$pid" 2>/dev/null
sleep 0.1
# A foreground command can ignore HUP. Recheck its session before killing its
# group so a recycled PID cannot target an unrelated member's process.
case "$foreground" in ''|*[!0-9]*) ;; *)
 if [ "$foreground" -gt 1 ] && [ -r "/proc/$foreground/stat" ]; then
  childstat=$(cat "/proc/$foreground/stat" 2>/dev/null)
  set -- ${childstat##*) }
  [ "$4" = "$pid" ] && kill -KILL "-$foreground" 2>/dev/null
 fi
;; esac
if [ -r "/proc/$pid/environ" ] && tr '\000' '\n' < "/proc/$pid/environ" | grep -Fxq "AGENTFLOW_TERMINAL_ID=$token"; then
 kill -KILL "$pid" 2>/dev/null
fi
exit 0
`
