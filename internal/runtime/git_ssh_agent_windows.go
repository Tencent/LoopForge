package runtime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agentflow/internal/gitwork"
	"agentflow/internal/processgroup"
)

func gitSSHAgentFactory(ctx context.Context, sandbox Sandbox) gitwork.SSHAgentFactory {
	docker, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return nil // retain the native Git-for-Windows agent transport
	}
	return func(_ string, identities []string) (string, func(), error) {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		if err := docker.ensureStarted(ctx); err != nil {
			return "", nil, err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", nil, err
		}
		directory := "/tmp/loopforge-git-ssh-" + hex.EncodeToString(random[:])
		socket := directory + "/agent"
		// Keep a Runner-owned attachment open for the agent's entire lifetime.
		// The container also expires missed heartbeats, because killing a Docker
		// client does not guarantee that the daemon kills its exec process.
		start := exec.CommandContext(ctx, "docker", "exec", "-i", docker.name, "sh", "-c", dockerSSHAgentStart, "loopforge-ssh-start", directory)
		input, err := start.StdinPipe()
		if err != nil {
			return "", nil, err
		}
		output, err := start.StdoutPipe()
		if err != nil {
			_ = input.Close()
			return "", nil, err
		}
		start.Stderr = io.Discard
		closeJob, err := processgroup.Start(start)
		if err != nil {
			_ = input.Close()
			_ = output.Close()
			return "", nil, fmt.Errorf("start isolated Docker Git SSH agent: %w", err)
		}
		done, stopHeartbeat := make(chan struct{}), make(chan struct{})
		go func() {
			_ = start.Wait()
			closeJob()
			close(done)
		}()
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if _, err := io.WriteString(input, "keepalive\n"); err != nil {
					return
				}
				select {
				case <-stopHeartbeat:
					return
				case <-done:
					return
				case <-ticker.C:
				}
			}
		}()
		cleanup := sync.OnceFunc(func() {
			close(stopHeartbeat)
			_ = input.Close()
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Start time prevents a stale PID from targeting another command.
			_ = exec.CommandContext(stopCtx, "docker", "exec", docker.name, "sh", "-c", dockerSSHAgentCleanup, "loopforge-ssh-cleanup", directory).Run()
			select {
			case <-done:
			case <-stopCtx.Done():
				_ = processgroup.Kill(start.Process.Pid)
				<-done
			}
		})
		ready := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(output).ReadString('\n')
			ready <- strings.TrimSpace(line)
		}()
		startup := time.NewTimer(10 * time.Second)
		defer startup.Stop()
		var message string
		select {
		case message = <-ready:
		case <-ctx.Done():
			cleanup()
			return "", nil, ctx.Err()
		case <-startup.C:
			cleanup()
			return "", nil, fmt.Errorf("isolated Docker Git SSH agent did not become ready")
		}
		if message != "LOOPFORGE_SSH_AGENT_READY" {
			cleanup()
			if message == "LOOPFORGE_OPENSSH_CLIENT_REQUIRED" {
				return "", nil, fmt.Errorf("Docker personal Git SSH requires openssh-client (ssh, ssh-agent and ssh-add) in the workspace image")
			}
			return "", nil, fmt.Errorf("isolated Docker Git SSH agent exited before becoming ready")
		}
		for _, identity := range identities {
			load := exec.CommandContext(ctx, "docker", "exec", "-i", "-e", "SSH_AUTH_SOCK="+socket, docker.name, "ssh-add", "-")
			load.Stdin = strings.NewReader(identity)
			load.Stdout, load.Stderr = io.Discard, io.Discard
			closeLoadJob, err := processgroup.Start(load)
			if err == nil {
				err = load.Wait()
				closeLoadJob()
			}
			if err != nil {
				cleanup()
				// Do not propagate ssh-add output or the identity into task events.
				return "", nil, fmt.Errorf("load selected key into isolated Docker Git SSH agent: %w", err)
			}
		}
		if err := ctx.Err(); err != nil {
			cleanup()
			return "", nil, err
		}
		stopWatch := context.AfterFunc(ctx, cleanup)
		return socket, func() { stopWatch(); cleanup() }, nil
	}
}

const dockerSSHProcessIdentity = `process_start() {
  IFS= read -r process_stat < "/proc/$1/stat" || return 1
  process_fields=${process_stat##*) }
  set -f
  set -- $process_fields
  shift 19
  printf '%s' "$1"
}
stop_agent() {
  target_pid=$1 target_start=$2
  case "$target_pid" in ''|*[!0-9]*) return ;; esac
  case "$target_start" in ''|*[!0-9]*) return ;; esac
  [ "$target_pid" -gt 0 ] || return 0
  if [ "$(process_start "$target_pid" 2>/dev/null || :)" = "$target_start" ]; then
    kill "$target_pid" 2>/dev/null || :
    attempt=0
    while [ "$(process_start "$target_pid" 2>/dev/null || :)" = "$target_start" ] && [ "$attempt" -lt 50 ]; do
      attempt=$((attempt+1))
      sleep 0.02
    done
    if [ "$(process_start "$target_pid" 2>/dev/null || :)" = "$target_start" ]; then kill -KILL "$target_pid" 2>/dev/null || :; fi
  fi
}
monotonic_seconds() {
  IFS='. ' read -r seconds remainder < /proc/uptime || return 1
  printf '%s' "$seconds"
}
`

const dockerSSHAgentStart = `set -eu
` + dockerSSHProcessIdentity + `
for tool in ssh ssh-agent ssh-add; do
  command -v "$tool" >/dev/null 2>&1 || { echo LOOPFORGE_OPENSSH_CLIENT_REQUIRED; exit 78; }
done
directory=$1
umask 077
mkdir "$directory"
agent_pid= agent_start= watch_pid= watch_start=
supervisor_pid=$$
supervisor_start=$(process_start "$supervisor_pid")
cleanup_agent() {
  stop_agent "$agent_pid" "$agent_start"
  rm -rf -- "$directory"
}
trap 'if [ -n "$watch_pid" ] && [ "$(process_start "$watch_pid" 2>/dev/null || :)" = "$watch_start" ]; then kill "$watch_pid" 2>/dev/null || :; fi; cleanup_agent' EXIT
trap 'exit 0' HUP INT TERM
ssh-agent -D -a "$directory/agent" >/dev/null 2>&1 &
agent_pid=$!
agent_start=$(process_start "$agent_pid")
printf '%s %s\n' "$agent_pid" "$agent_start" > "$directory/owner"
monotonic_seconds > "$directory/lease"
attempt=0
while [ ! -S "$directory/agent" ]; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 50 ] || exit 1
  kill -0 "$agent_pid" 2>/dev/null || exit 1
  sleep 0.02
done
(
  trap - EXIT HUP INT TERM
  while sleep 1; do
    lease=$(cat "$directory/lease" 2>/dev/null || echo 0)
    case "$lease" in ''|*[!0-9]*) lease=0 ;; esac
    now=$(monotonic_seconds)
    if [ "$((now-lease))" -gt 6 ] || [ "$(process_start "$supervisor_pid" 2>/dev/null || :)" != "$supervisor_start" ]; then
      cleanup_agent
      if [ "$(process_start "$supervisor_pid" 2>/dev/null || :)" = "$supervisor_start" ]; then kill "$supervisor_pid" 2>/dev/null || :; fi
      exit 0
    fi
  done
) &
watch_pid=$!
watch_start=$(process_start "$watch_pid")
echo LOOPFORGE_SSH_AGENT_READY
while IFS= read -r heartbeat; do
  [ "$heartbeat" = keepalive ] || break
  monotonic_seconds > "$directory/lease.next"
  mv -f -- "$directory/lease.next" "$directory/lease"
done
`

const dockerSSHAgentCleanup = `set -eu
` + dockerSSHProcessIdentity + `
directory=$1
case "$directory" in /tmp/loopforge-git-ssh-*) ;; *) exit 1 ;; esac
if [ -r "$directory/owner" ]; then
  read -r agent_pid agent_start < "$directory/owner"
  stop_agent "$agent_pid" "$agent_start"
fi
rm -rf -- "$directory"
`
