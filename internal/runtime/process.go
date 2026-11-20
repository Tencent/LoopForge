package runtime

import (
	"agentflow/internal/domain"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const processWaitDelay = time.Second

var errProcessNotStarted = errors.New("process was not started")

// runCommand streams process telemetry, coordinating the Claude login callback
// and redacting bound credential environment values from process.started.
func runCommand(cmd *exec.Cmd, sink EventSink, commands ...CommandSpec) (string, error) {
	var command CommandSpec
	if len(commands) > 0 {
		command = commands[0]
	}
	var bridge *claudeLoginBridge
	if isManagedClaude(command) {
		var err error
		bridge, err = newClaudeLoginBridge(cmd, command)
		if err != nil {
			return "", fmt.Errorf("%w: %w", errProcessNotStarted, err)
		}
		defer bridge.close()
	}
	var buf bytes.Buffer
	var mu sync.Mutex
	stdout := &processWriter{output: &buf, mu: &mu, sink: sink, name: "process.stdout", level: "info"}
	if bridge != nil {
		stdout.filter = bridge.filter
	}
	stderr := &processWriter{output: &buf, mu: &mu, sink: sink, name: "process.stderr", level: "error"}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// When hooks spawn detached children that inherit stdout/stderr, bound how
	// long os/exec waits for those inherited descriptors after the direct child
	// exits. The same timer also cuts off output still buffered in the pipe:
	// at 100ms a busy host (CI running Go and JS tests together) dropped the
	// tail of a 50KB failure log, which is the part users need.
	cmd.WaitDelay = processWaitDelay
	// Start launches the stdout/stderr copy goroutines. Hold the writers' lock
	// until process.started is delivered so the sink stays serialized and the
	// event always precedes the first output line.
	mu.Lock()
	err := cmd.Start()
	if err == nil && sink != nil {
		sink(domain.TelemetryEvent{Category: "process", Name: "process.started", Timestamp: time.Now().UTC(), Phase: "start", Message: compactCommand(cmd.Args, command.credentialEnvKeys...), Attributes: map[string]any{"pid": cmd.Process.Pid}})
	}
	mu.Unlock()
	if err == nil {
		if bridge != nil {
			bridge.start()
		}
		err = cmd.Wait()
	} else if sink != nil {
		sink(domain.TelemetryEvent{Category: "process", Name: "process.start_failed", Timestamp: time.Now().UTC(), Phase: "start", Level: "error", Message: err.Error()})
	}
	if err != nil && cmd.Process == nil {
		if errors.Is(err, syscall.E2BIG) {
			// Retrying with the same input fails again; name the limit so the
			// diagnostic points at the oversized argument, not the CLI.
			err = fmt.Errorf("%w (a single argument exceeds the ~128KiB kernel limit; pass long prompts on stdin or in a file)", err)
		}
		err = fmt.Errorf("%w: %w", errProcessNotStarted, err)
	}
	if cmd.Process != nil && cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	outputIncomplete := errors.Is(err, exec.ErrWaitDelay)
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	stdout.flush()
	stderr.flush()
	if bridge != nil && bridge.result() != nil {
		err = bridge.result()
	}
	if sink != nil {
		phase, level := "finish", "info"
		if err != nil {
			level = "error"
		}
		attrs := map[string]any{"outputIncomplete": outputIncomplete}
		if state := cmd.ProcessState; state != nil {
			attrs["pid"], attrs["exitCode"] = state.Pid(), state.ExitCode()
			attrs["userCpuMs"], attrs["systemCpuMs"] = state.UserTime().Milliseconds(), state.SystemTime().Milliseconds()
			if status, ok := state.Sys().(interface {
				Signaled() bool
				Signal() syscall.Signal
			}); ok && status.Signaled() {
				attrs["signal"] = status.Signal().String()
			}
		}
		sink(domain.TelemetryEvent{Category: "process", Name: "process.finished", Timestamp: time.Now().UTC(), Phase: phase, Level: level, Message: commandResult(err), Attributes: attrs})
	}
	return buf.String(), err
}

type processWriter struct {
	filter  func(string) bool
	output  *bytes.Buffer
	mu      *sync.Mutex
	sink    EventSink
	name    string
	level   string
	pending bytes.Buffer
}

func (w *processWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.pending.Write(p)
	w.emitLines(false)
	return len(p), nil
}

func (w *processWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.emitLines(true)
}

func (w *processWriter) emitLines(includePartial bool) {
	for {
		raw := w.pending.String()
		index := strings.IndexByte(raw, '\n')
		if index < 0 {
			if includePartial && raw != "" {
				w.pending.Reset()
				w.emit(raw)
			}
			return
		}
		line := strings.TrimSuffix(raw[:index], "\r")
		w.pending.Reset()
		_, _ = w.pending.WriteString(raw[index+1:])
		w.emit(line)
	}
}

func (w *processWriter) emit(line string) {
	if w.filter != nil && !w.filter(line) {
		return
	}
	w.output.WriteString(line)
	w.output.WriteByte('\n')
	if w.sink != nil {
		w.sink(domain.TelemetryEvent{Category: "process", Name: w.name, Phase: "progress", Level: w.level, Message: line})
	}
}

func compactCommand(args []string, credentialEnvKeys ...string) string {
	credentialKeys := make(map[string]bool, len(credentialEnvKeys))
	for _, key := range credentialEnvKeys {
		credentialKeys[strings.ToUpper(key)] = true
	}
	redact := func(key string) bool {
		return sensitiveProcessEnvironmentKey(key) || credentialKeys[strings.ToUpper(key)]
	}
	safe := append([]string(nil), args...)
	for index := 0; index < len(safe); index++ {
		value := safe[index]
		if value == "-e" && index+1 < len(safe) {
			if key, _, found := strings.Cut(safe[index+1], "="); found && redact(key) {
				safe[index+1] = key + "=<redacted>"
			}
			index++
			continue
		}
		if key, _, found := strings.Cut(value, "="); found && redact(key) {
			safe[index] = key + "=<redacted>"
		}
	}
	joined := strings.Join(safe, " ")
	runes := []rune(joined)
	const maxRunes = 1_024
	if len(runes) <= maxRunes {
		return joined
	}
	return string(runes[:maxRunes]) + fmt.Sprintf(" … [arguments truncated, %d chars]", len(runes))
}

// sensitiveProcessEnvironmentKey matches by suffix so the key variable of a
// newly added agent CLI is redacted without being listed here.
func sensitiveProcessEnvironmentKey(key string) bool {
	key = strings.ToUpper(key)
	if key == "CODEX_REFRESH_TOKEN_URL_OVERRIDE" {
		return true
	}
	for _, suffix := range []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return strings.HasPrefix(key, "GIT_CONFIG_KEY_")
}

func commandResult(err error) string {
	if err == nil {
		return "process exited successfully"
	}
	return err.Error()
}
