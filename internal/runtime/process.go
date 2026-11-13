package runtime

import (
	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const processWaitDelay = time.Second

func runCommand(cmd *exec.Cmd, sink EventSink) (string, error) {
	var buf bytes.Buffer
	var mu sync.Mutex
	if sink != nil {
		sink(domain.TelemetryEvent{Category: "process", Name: "process.started", Phase: "start", Message: compactCommand(cmd.Args)})
	}
	token := ""
	for _, value := range cmd.Env {
		if secret, ok := strings.CutPrefix(value, gitwork.HTTPSCredentialTokenEnv+"="); ok {
			token = secret
		}
	}
	stdout := &processWriter{output: &buf, mu: &mu, sink: sink, name: "process.stdout", level: "info", token: token}
	stderr := &processWriter{output: &buf, mu: &mu, sink: sink, name: "process.stderr", level: "error", token: token}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// When hooks spawn detached children that inherit stdout/stderr, bound how
	// long os/exec waits for those inherited descriptors after the direct child
	// exits. The same timer also cuts off output still buffered in the pipe:
	// at 100ms a busy host (CI running Go and JS tests together) dropped the
	// tail of a 50KB failure log, which is the part users need.
	cmd.WaitDelay = processWaitDelay
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	stdout.flush()
	stderr.flush()
	if sink != nil {
		phase, level := "finish", "info"
		if err != nil {
			level = "error"
		}
		sink(domain.TelemetryEvent{Category: "process", Name: "process.finished", Phase: phase, Level: level, Message: commandResult(err)})
	}
	return buf.String(), err
}

type processWriter struct {
	output  *bytes.Buffer
	mu      *sync.Mutex
	sink    EventSink
	name    string
	level   string
	token   string
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
	if w.token != "" {
		line = gitwork.Redact(line, w.token)
	}
	w.output.WriteString(line)
	w.output.WriteByte('\n')
	if w.sink != nil {
		w.sink(domain.TelemetryEvent{Category: "process", Name: w.name, Phase: "progress", Level: w.level, Message: line})
	}
}

// Docker can copy a variable by name from the CLI process environment.
func dockerExecCommand(ctx context.Context, args []string, environment map[string]string) *exec.Cmd {
	for i := range args {
		if _, ok := strings.CutPrefix(args[i], gitwork.HTTPSCredentialTokenEnv+"="); ok {
			args[i] = gitwork.HTTPSCredentialTokenEnv
			cmd := exec.CommandContext(ctx, "docker", args...)
			cmd.Env = upsertEnv(os.Environ(), gitwork.HTTPSCredentialTokenEnv, environment[gitwork.HTTPSCredentialTokenEnv])
			return cmd
		}
	}
	return exec.CommandContext(ctx, "docker", args...)
}

func compactCommand(args []string) string {
	safe := append([]string(nil), args...)
	for index := 0; index < len(safe); index++ {
		value := safe[index]
		if value == "-e" && index+1 < len(safe) {
			if key, _, found := strings.Cut(safe[index+1], "="); found && sensitiveProcessEnvironmentKey(key) {
				safe[index+1] = key + "=<redacted>"
			}
			index++
			continue
		}
		if key, _, found := strings.Cut(value, "="); found && sensitiveProcessEnvironmentKey(key) {
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
