package runtime

import (
	"agentflow/internal/domain"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const processWaitDelay = time.Second

func runCommand(cmd *exec.Cmd, sink EventSink) (string, error) {
	redact := processRedactor(cmd)
	if sink != nil {
		original := sink
		sink = func(event domain.TelemetryEvent) {
			event.Message = redact(event.Message)
			original(event)
		}
	}
	var buf bytes.Buffer
	var mu sync.Mutex
	if sink != nil {
		sink(domain.TelemetryEvent{Category: "process", Name: "process.started", Phase: "start", Message: compactCommand(cmd.Args)})
	}
	stdout := &processWriter{output: &buf, mu: &mu, sink: sink, name: "process.stdout", level: "info"}
	stderr := &processWriter{output: &buf, mu: &mu, sink: sink, name: "process.stderr", level: "error"}
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
	if err != nil && redact(err.Error()) != err.Error() {
		err = &redactedProcessError{cause: err, message: redact(err.Error())}
	}
	if sink != nil {
		phase, level := "finish", "info"
		if err != nil {
			level = "error"
		}
		sink(domain.TelemetryEvent{Category: "process", Name: "process.finished", Phase: phase, Level: level, Message: commandResult(err)})
	}
	return redact(buf.String()), err
}

// Docker carries bindings in argv, while local sandboxes use the environment.
// Keep credentials out of both telemetry and returned failure diagnostics.
func processRedactor(cmd *exec.Cmd) func(string) string {
	var secrets []string
	for _, value := range append(append([]string(nil), cmd.Env...), cmd.Args...) {
		key, secret, found := strings.Cut(value, "=")
		if found && sensitiveProcessEnvironmentKey(key) && secret != "" {
			secrets = append(secrets, secret)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return func(text string) string {
		for _, secret := range secrets {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
		return text
	}
}

type redactedProcessError struct {
	cause   error
	message string
}

func (e *redactedProcessError) Error() string { return e.message }
func (e *redactedProcessError) Unwrap() error { return e.cause }

type processWriter struct {
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
	w.output.WriteString(line)
	w.output.WriteByte('\n')
	if w.sink != nil {
		w.sink(domain.TelemetryEvent{Category: "process", Name: w.name, Phase: "progress", Level: w.level, Message: line})
	}
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

func sensitiveProcessEnvironmentKey(key string) bool {
	return key == "CODEBUDDY_API_KEY" ||
		key == "OPENAI_API_KEY" || key == "CODEX_API_KEY" ||
		key == "ANTHROPIC_API_KEY" || key == "ANTHROPIC_AUTH_TOKEN" || key == "CLAUDE_CODE_OAUTH_TOKEN" ||
		key == "AGENTFLOW_GIT_TOKEN" || key == "AGENTFLOW_GITLAB_TOKEN" ||
		key == "AGENTFLOW_GIT_HTTP_PASSWORD" ||
		strings.HasPrefix(key, "GIT_CONFIG_KEY_")
}

func commandResult(err error) string {
	if err == nil {
		return "process exited successfully"
	}
	return err.Error()
}
