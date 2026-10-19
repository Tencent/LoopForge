package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"agentflow/internal/domain"
	flowruntime "agentflow/internal/runtime"
)

const (
	eventFlushInterval = 500 * time.Millisecond
	resultTailBytes    = 32 << 10
	cancelGrace        = 10 * time.Second
	leaseRenewInterval = 30 * time.Second
	// HTTP control checks cover a missed WebSocket frame. Keep this well
	// under the lease renew interval so Pause/Cancel still feel immediate
	// when the control socket is down or stalled.
	controlFallbackInterval = time.Second
)

// eventBatcher buffers runner output and ships it in batches. The server
// deduplicates on (task_id, seq), so a failed batch is retried inside the
// next batch instead of being lost or duplicated.
type eventBatcher struct {
	mu       sync.Mutex
	taskID   string
	client   *Client
	seq      int64
	size     int
	buf      []domain.RunnerTaskEvent
	tail     []string
	observer *flowruntime.ProtocolObserver
}

// executeSandboxProcess runs a prepared command through a sandbox created on
// the execution machine while retaining the runner task lease and streaming
// process events back to the control plane.
func (d *Daemon) executeSandboxProcess(ctx context.Context, task domain.RunnerTask, sandbox flowruntime.Sandbox, command flowruntime.CommandSpec) execResult {
	batcher := newEventBatcher(task, d.client)
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		output string
		err    error
	}
	done := make(chan outcome, 1)
	sink := func(event domain.TelemetryEvent) {
		if event.Name == "process.stdout" {
			batcher.addLine("stdout", event.Message)
		} else if event.Name == "process.stderr" {
			batcher.addLine("stderr", event.Message)
		} else {
			batcher.addObservation(event)
			batcher.addLine("system", event.Message)
		}
	}
	go func() {
		output, err := sandbox.Exec(execCtx, command, sink)
		done <- outcome{output: output, err: err}
	}()
	renewTicker := time.NewTicker(leaseRenewInterval)
	defer renewTicker.Stop()
	controlTicker := time.NewTicker(controlFallbackInterval)
	defer controlTicker.Stop()
	controlCh := d.taskControl(task.ID)
	flushTicker := time.NewTicker(eventFlushInterval)
	defer flushTicker.Stop()
	canceled := false
	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			canceled = true
			cancel()
		case completed := <-done:
			batcher.finalizeProtocol(completed.err == nil)
			batcher.flush(context.Background())
			result := execResult{result: batcher.taskResult(completed.output)}
			if canceled {
				result.status = domain.RunnerTaskCanceled
				result.errMsg = "runner stopped"
				return result
			}
			result.exitCode = exitCode(completed.err)
			if completed.err != nil {
				result.status = domain.RunnerTaskFailed
				result.errMsg = completed.err.Error()
			} else {
				result.status = domain.RunnerTaskSuccess
			}
			return result
		case <-flushTicker.C:
			batcher.flush(ctx)
		case <-renewTicker.C:
			renewCtx, renewCancel := context.WithTimeout(ctx, 20*time.Second)
			resp, renewErr := d.client.RenewLease(renewCtx, task.ID)
			renewCancel()
			if renewErr == nil && resp != nil && strings.EqualFold(resp.DesiredState, domain.RunnerTaskDesiredCancel) && !canceled {
				canceled = true
				batcher.addLine("system", "cancel requested by platform")
				cancel()
			}
		case <-controlTicker.C:
			controlCtx, controlCancel := context.WithTimeout(ctx, 5*time.Second)
			resp, controlErr := d.client.TaskControl(controlCtx, task.ID)
			controlCancel()
			if controlErr == nil && resp != nil && strings.EqualFold(resp.DesiredState, domain.RunnerTaskDesiredCancel) && !canceled {
				canceled = true
				batcher.addLine("system", "cancel requested by platform")
				cancel()
			}
		case desiredState := <-controlCh:
			if strings.EqualFold(desiredState, domain.RunnerTaskDesiredCancel) && !canceled {
				canceled = true
				batcher.addLine("system", "cancel requested by platform")
				cancel()
			}
		}
	}
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func newEventBatcher(task domain.RunnerTask, client *Client) *eventBatcher {
	b := &eventBatcher{taskID: task.ID, client: client}
	if task.Invocation != nil {
		b.observer = flowruntime.NewProtocolObserver(task.Invocation.Executor, b.addObservation)
	}
	return b
}

func (b *eventBatcher) add(typ, data string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	b.buf = append(b.buf, domain.RunnerTaskEvent{Seq: b.seq, Type: typ, Data: data, Ts: time.Now().UTC()})
	b.size += len(data)
	if typ == "stdout" {
		b.tail = append(b.tail, data)
	}
}

func (b *eventBatcher) addLine(typ, line string) {
	if line == "" {
		return
	}
	b.add(typ, line)
	if b.observer != nil && (typ == "stdout" || typ == "stderr") {
		level := "info"
		if typ == "stderr" {
			level = "error"
		}
		b.observer.Handle(domain.TelemetryEvent{Category: "process", Name: "process." + typ, Phase: "progress", Level: level, Message: line, Timestamp: time.Now().UTC()})
	}
}

func (b *eventBatcher) addObservation(event domain.TelemetryEvent) {
	// Plain stdout already travels through the raw runner channel. The server
	// turns non-protocol lines into process observations, while JSONL frames are
	// discarded after normalization. Stderr remains a structured observation,
	// matching local/cloud execution.
	if event.Name == "process.stdout" {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	raw, err := json.Marshal(event)
	if err != nil {
		b.add("system", "encode observation: "+err.Error())
		return
	}
	b.add("observation", string(raw))
}

func (b *eventBatcher) finalizeProtocol(success bool) {
	if b.observer != nil {
		b.observer.Finalize(success)
	}
}

func (b *eventBatcher) flush(ctx context.Context) {
	b.mu.Lock()
	pending := b.buf
	b.buf = nil
	b.size = 0
	b.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	if err := b.client.SendEvents(ctx, b.taskID, pending); err != nil {
		// Re-queue for the next batch; seq keys make the retry idempotent.
		b.mu.Lock()
		b.buf = append(pending, b.buf...)
		b.size = 0
		for _, ev := range b.buf {
			b.size += len(ev.Data)
		}
		b.mu.Unlock()
	}
}

func (b *eventBatcher) result() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	joined := strings.Join(b.tail, "\n")
	if len(joined) > resultTailBytes {
		joined = joined[len(joined)-resultTailBytes:]
	}
	return joined
}

func (b *eventBatcher) visibleOutput() string {
	if b == nil || b.observer == nil {
		return ""
	}
	return strings.TrimSpace(b.observer.VisibleOutput())
}

// taskResult prefers the protocol's visible text (terminal result, else the
// latest assistant message). Raw stdout is only the fallback for commands
// that are not an Agent stream, and for CLIs that emitted neither.
func (b *eventBatcher) taskResult(rawFallback string) string {
	if text := b.visibleOutput(); text != "" {
		if len(text) <= resultTailBytes {
			return text
		}
		return text[:resultTailBytes] + fmt.Sprintf("\n… [output truncated, %d bytes]", len(text))
	}
	return tailString(rawFallback, resultTailBytes)
}

// execResult is the outcome reported to the control plane.
type execResult struct {
	status   string
	exitCode int
	result   string
	errMsg   string
	outputs  *domain.RunnerTaskOutputs
}

// executeProcess spawns the Agent CLI in its own process group so a cancel
// reliably reaches the CLI's shell and MCP children, streams output through
// the batcher, renews the task lease while running, and honors a cancel
// requested through the lease renewal response. extraEnv injects
// task-scoped credentials (e.g. CODEBUDDY_API_KEY for CodeBuddy tasks).
func (d *Daemon) executeProcess(ctx context.Context, task domain.RunnerTask, argv []string, extraEnv ...map[string]string) execResult {
	batcher := newEventBatcher(task, d.client)
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		if err == ErrUnauthorized {
			return execResult{status: domain.RunnerTaskFailed, errMsg: "runner session rejected"}
		}
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}

	dir := d.taskWorkspace(task)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("workspace %s: %v", dir, err)}
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	// Agent CLI credentials live in the user environment on this machine;
	// the platform additionally injects task-scoped credentials (currently
	// the CodeBuddy API key, which cannot come from a local CLI login).
	cmd.Env = os.Environ()
	for _, env := range extraEnv {
		for key, value := range env {
			cmd.Env = upsertProcessEnv(cmd.Env, key, value)
		}
	}
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("spawn %s: %v", argv[0], err)}
	}
	batcher.addLine("system", fmt.Sprintf("spawned pid=%d workspace=%s", cmd.Process.Pid, dir))
	batcher.addObservation(domain.TelemetryEvent{Category: "process", Name: "process.started", Phase: "start", Message: fmt.Sprintf("spawned pid=%d workspace=%s", cmd.Process.Pid, dir)})

	// Drain stdout/stderr before Wait. Calling Wait while Scan is still
	// reading StdoutPipe closes the pipe and drops the last lines of a
	// short command (os/exec: "incorrect to call Wait before all reads
	// from the pipe have completed").
	readDone := make(chan struct{}, 2)
	go streamPipe(stdout, "stdout", batcher, readDone)
	go streamPipe(stderr, "stderr", batcher, readDone)
	// One goroutine owns Wait, including cancellation and early control failures.
	// Closing processDone publishes processErr after both streams are drained.
	processDone := make(chan struct{})
	var processErr error
	go func() {
		<-readDone
		<-readDone
		processErr = cmd.Wait()
		close(processDone)
	}()
	defer func() {
		select {
		case <-processDone:
		default:
			terminateProcessGroup(cmd, processDone)
			<-processDone
		}
	}()

	renewTicker := time.NewTicker(leaseRenewInterval)
	defer renewTicker.Stop()
	controlTicker := time.NewTicker(controlFallbackInterval)
	defer controlTicker.Stop()
	controlCh := d.taskControl(task.ID)
	flushTicker := time.NewTicker(eventFlushInterval)
	defer flushTicker.Stop()

	canceled := false
	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			if !canceled {
				canceled = true
				terminateProcessGroup(cmd, processDone)
			}
		case <-processDone:
			err := processErr
			if canceled {
				err = nil
			}
			code := exitCode(err)
			level := "info"
			if err != nil {
				level = "error"
			}
			batcher.addObservation(domain.TelemetryEvent{Category: "process", Name: "process.finished", Phase: "finish", Level: level, Message: fmt.Sprintf("executor exited %d", code), Attributes: map[string]any{"exitCode": code}})
			batcher.finalizeProtocol(err == nil)
			batcher.flush(context.Background())
			result := execResult{result: batcher.taskResult(batcher.result())}
			if canceled {
				result.status = domain.RunnerTaskCanceled
				result.errMsg = "runner stopped"
				return result
			}
			result.exitCode = code
			if err != nil {
				result.status = domain.RunnerTaskFailed
				result.errMsg = err.Error()
			} else {
				result.status = domain.RunnerTaskSuccess
			}
			return result
		case <-flushTicker.C:
			batcher.flush(ctx)
		case <-renewTicker.C:
			renewCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			resp, renewErr := d.client.RenewLease(renewCtx, task.ID)
			cancel()
			if renewErr != nil {
				if renewErr == ErrUnauthorized {
					return execResult{status: domain.RunnerTaskFailed, errMsg: "runner session rejected"}
				}
				continue
			}
			if resp != nil && strings.EqualFold(resp.DesiredState, domain.RunnerTaskDesiredCancel) && !canceled {
				canceled = true
				batcher.addLine("system", "cancel requested by platform")
				terminateProcessGroup(cmd, processDone)
			}
		case <-controlTicker.C:
			controlCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			resp, controlErr := d.client.TaskControl(controlCtx, task.ID)
			cancel()
			if controlErr == ErrUnauthorized {
				return execResult{status: domain.RunnerTaskFailed, errMsg: "runner session rejected"}
			}
			if controlErr == nil && resp != nil && strings.EqualFold(resp.DesiredState, domain.RunnerTaskDesiredCancel) && !canceled {
				canceled = true
				batcher.addLine("system", "cancel requested by platform")
				terminateProcessGroup(cmd, processDone)
			}
		case desiredState := <-controlCh:
			if strings.EqualFold(desiredState, domain.RunnerTaskDesiredCancel) && !canceled {
				canceled = true
				batcher.addLine("system", "cancel requested by platform")
				terminateProcessGroup(cmd, processDone)
			}
		}
	}
}

func streamPipe(r io.Reader, typ string, batcher *eventBatcher, done chan struct{}) {
	defer func() { done <- struct{}{} }()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		batcher.addLine(typ, scanner.Text())
	}
	if err := scanner.Err(); err != nil && !isBenignStreamClose(err) {
		batcher.addLine("system", fmt.Sprintf("stream %s: %v", typ, err))
	}
}

func isBenignStreamClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "file already closed") || strings.Contains(msg, "use of closed file")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// setProcessGroup puts the CLI and all of its children into a new process
// group so a cancel cannot leave orphaned shells or MCP servers behind.
func setProcessGroup(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcessGroup sends SIGTERM to the whole group, waits a grace
// period, then escalates to SIGKILL.
// processDone belongs to the single Wait caller; termination must not reap it.
func terminateProcessGroup(cmd *exec.Cmd, processDone <-chan struct{}) {
	select {
	case <-processDone:
		return
	default:
	}
	if cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = cmd.Process.Kill()
		return
	}
	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	timer := time.NewTimer(cancelGrace)
	defer timer.Stop()
	select {
	case <-processDone:
	case <-timer.C:
		_ = syscall.Kill(pgid, syscall.SIGKILL)
	}
}

func upsertProcessEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !found {
				out = append(out, prefix+value)
				found = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, prefix+value)
	}
	return out
}
