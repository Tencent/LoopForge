package runtime

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"agentflow/internal/processgroup"
)

// startDockerOwnerLease keeps active remote work tied to its owning Runner.
// The separate attachment never shares task stdin. Windows Jobs close the
// client on forced Runner exit; a guest-monotonic TTL also handles daemon
// attachments that do not deliver EOF when their client disappears.
func dockerProcessToken() (string, error) {
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", identity[:]), nil
}

func runDockerOwnerControl(ctx context.Context, arguments ...string) error {
	cmd := exec.CommandContext(ctx, "docker", arguments...)
	closeJob, err := processgroup.Start(cmd)
	if err != nil {
		return err
	}
	defer closeJob()
	return cmd.Wait()
}

func startDockerOwnerLease(ctx context.Context, container string) (string, func(), error) {
	identity, err := dockerProcessToken()
	if err != nil {
		return "", nil, err
	}
	directory := "/tmp/loopforge-process-lease-" + identity
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", container, "/bin/sh", "-c", dockerOwnerLeaseStart, "loopforge-owner-lease", directory)
	input, err := cmd.StdinPipe()
	if err != nil {
		return "", nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return "", nil, err
	}
	cmd.Stderr = io.Discard
	closeJob, err := processgroup.Start(cmd)
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		return "", nil, fmt.Errorf("start Docker operation owner lease: %w", err)
	}
	done, stopHeartbeat := make(chan struct{}), make(chan struct{})
	go func() {
		_ = cmd.Wait()
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
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Removing only this random directory revokes its lease. The remote
		// controller's watchdog then exits even if its attachment missed EOF.
		_ = runDockerOwnerControl(stopCtx, "exec", container, "/bin/sh", "-c", `case "$1" in /tmp/loopforge-process-lease-*) rm -rf -- "$1" ;; *) exit 1 ;; esac`, "loopforge-owner-release", directory)
		select {
		case <-done:
		case <-stopCtx.Done():
			_ = processgroup.Kill(cmd.Process.Pid)
			<-done
		}
	})
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(output).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case line := <-ready:
		if line == "LOOPFORGE_OWNER_LEASE_READY" {
			return directory + "/lease", cleanup, nil
		}
	case <-ctx.Done():
		cleanup()
		return "", nil, ctx.Err()
	case <-timer.C:
	}
	cleanup()
	return "", nil, fmt.Errorf("Docker operation owner lease did not become ready")
}

const dockerOwnerLeaseStart = `set -eu
process_start() {
  IFS= read -r process_stat < "/proc/$1/stat" || return 1
  process_fields=${process_stat##*) }
  set -f
  set -- $process_fields
  shift 19
  printf '%s' "$1"
}
monotonic_seconds() {
  IFS='. ' read -r seconds remainder < /proc/uptime || return 1
  printf '%s' "$seconds"
}
directory=$1
umask 077
mkdir "$directory"
owner=$$
owner_start=$(process_start "$owner")
watch= watch_start=
trap 'if [ -n "$watch" ] && [ "$(process_start "$watch" 2>/dev/null || :)" = "$watch_start" ]; then kill "$watch" 2>/dev/null || :; fi; rm -rf -- "$directory"' EXIT
trap 'exit 0' HUP INT TERM
monotonic_seconds > "$directory/lease"
(
  trap - EXIT HUP INT TERM
  while sleep 1; do
    lease=$(cat "$directory/lease" 2>/dev/null || echo 0)
    case "$lease" in ''|*[!0-9]*) lease=0 ;; esac
    now=$(monotonic_seconds)
    if [ "$((now-lease))" -gt 6 ] || [ "$((now-lease))" -lt 0 ] || [ "$(process_start "$owner" 2>/dev/null || :)" != "$owner_start" ]; then
      rm -rf -- "$directory"
      if [ "$(process_start "$owner" 2>/dev/null || :)" = "$owner_start" ]; then
        kill "$owner" 2>/dev/null || :
        attempt=0
        while [ "$(process_start "$owner" 2>/dev/null || :)" = "$owner_start" ] && [ "$attempt" -lt 50 ]; do
          attempt=$((attempt+1))
          sleep 0.02
        done
        if [ "$(process_start "$owner" 2>/dev/null || :)" = "$owner_start" ]; then kill -KILL "$owner" 2>/dev/null || :; fi
      fi
      exit 0
    fi
  done
) &
watch=$!
watch_start=$(process_start "$watch")
echo LOOPFORGE_OWNER_LEASE_READY
while IFS= read -r heartbeat; do
  [ "$heartbeat" = keepalive ] || break
  monotonic_seconds > "$directory/lease.next"
  mv -f -- "$directory/lease.next" "$directory/lease"
done
`
