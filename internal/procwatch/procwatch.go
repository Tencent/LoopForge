// Package procwatch runs the long external commands that prepare a workspace
// (git clone, docker build) so a slow step is visible and bounded instead of
// silent: output is read as it arrives, progress reaches an observer carried on
// the context, a command that stops printing is killed, and the error keeps
// only the last lines of output.
package procwatch

import (
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

// Kind is the lifecycle step of one watched command.
type Kind string

const (
	Started   Kind = "started"
	Progress  Kind = "progress"
	Completed Kind = "completed"
	Failed    Kind = "failed"
)

// Event reports one lifecycle step. Stage is a stable key ("clone", "image");
// Message is ready to show to a person.
type Event struct {
	Stage   string
	Kind    Kind
	Message string
	Elapsed time.Duration
}

// Observer receives events. It is called from the goroutine reading output and
// must not block for long.
type Observer func(Event)

type observerKey struct{}

// WithObserver attaches the observer that Run reports to. Callers that do not
// attach one still get the watchdog, timeout and short errors.
func WithObserver(ctx context.Context, observer Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, observer)
}

func observerFrom(ctx context.Context) Observer {
	observer, _ := ctx.Value(observerKey{}).(Observer)
	return observer
}

// Options describes one watched command.
type Options struct {
	Stage string // stable key reported on events
	Label string // what the command does, e.g. "克隆仓库 web"
	// Stall kills the command after this long without any output. Zero
	// disables it. StallEnv names the setting that changes it, for the error.
	Stall    time.Duration
	StallEnv string
	// Timeout bounds the whole command. Zero leaves only the caller's context.
	Timeout    time.Duration
	TimeoutEnv string
	// Redact scrubs secrets from every line before it is kept or reported.
	Redact func(string) string
}

const (
	tailLines     = 20
	progressEvery = 10 * time.Second
	waitDelay     = 5 * time.Second
)

// Run starts the command build returns and waits for it. build receives the
// context to bind with exec.CommandContext; Run owns Stdout and Stderr.
// The returned text is the redacted tail of the output; on failure the error
// says why (exit, stall, timeout) and does not repeat that tail.
func Run(ctx context.Context, build func(context.Context) *exec.Cmd, opts Options) (string, error) {
	observer := observerFrom(ctx)
	emit := func(kind Kind, message string, elapsed time.Duration) {
		if observer != nil {
			observer(Event{Stage: opts.Stage, Kind: kind, Message: message, Elapsed: elapsed})
		}
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if opts.Timeout > 0 {
		var stop context.CancelFunc
		runCtx, stop = context.WithTimeoutCause(runCtx, opts.Timeout, errTimeout)
		defer stop()
	}
	started := time.Now()
	out := &lineWriter{redact: opts.Redact, last: started}
	out.onLine = func(line string, now time.Time) {
		if now.Sub(out.lastEmit) < progressEvery {
			return
		}
		out.lastEmit = now
		emit(Progress, opts.Label+"："+line, now.Sub(started))
	}
	cmd := build(runCtx)
	killGroup(cmd)
	cmd.Stdout, cmd.Stderr = out, out
	// git and docker leave helpers holding the pipes after a kill; do not
	// wait on them forever.
	cmd.WaitDelay = waitDelay
	emit(Started, "正在"+opts.Label, 0)

	done := make(chan struct{})
	if opts.Stall > 0 {
		go watchStall(runCtx, cancel, out, opts.Stall, done)
	}
	err := cmd.Run()
	close(done)
	out.flush()
	elapsed := time.Since(started)
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	tail := out.tail()
	if err == nil {
		emit(Completed, fmt.Sprintf("%s 完成，耗时 %s", opts.Label, roundDuration(elapsed)), elapsed)
		return tail, nil
	}
	switch cause := context.Cause(runCtx); {
	case errors.Is(cause, errStalled):
		err = &StoppedError{fmt.Sprintf("%s 已 %s 没有任何输出，判定卡住并停止%s", opts.Label, roundDuration(opts.Stall), settingHint(opts.StallEnv)), err}
	case errors.Is(cause, errTimeout):
		err = &StoppedError{fmt.Sprintf("%s 超过 %s 仍未完成，已停止%s", opts.Label, roundDuration(opts.Timeout), settingHint(opts.TimeoutEnv)), err}
	case errors.Is(cause, context.DeadlineExceeded):
		// The caller's own limit (an assistant turn, a preparation request).
		err = &StoppedError{fmt.Sprintf("%s 在调用方时限内没有完成（已运行 %s），已停止", opts.Label, roundDuration(elapsed)), err}
	}
	message := fmt.Sprintf("%s 失败（耗时 %s）：%v", opts.Label, roundDuration(elapsed), err)
	if last := lastLines(tail, 3); last != "" {
		message += "\n" + last
	}
	emit(Failed, message, elapsed)
	return tail, err
}

var (
	errStalled = errors.New("procwatch: no output")
	errTimeout = errors.New("procwatch: timeout")
)

// StoppedError is a command Run killed because it stalled or ran past its
// timeout. Running the same command again would wait just as long, so callers
// use Stopped to skip their retries.
type StoppedError struct {
	reason string
	err    error
}

func (e *StoppedError) Error() string { return e.reason + ": " + e.err.Error() }
func (e *StoppedError) Unwrap() error { return e.err }

// Stopped reports whether err comes from a command Run killed for a stall or
// timeout.
func Stopped(err error) bool {
	var stopped *StoppedError
	return errors.As(err, &stopped)
}

func watchStall(ctx context.Context, cancel context.CancelCauseFunc, out *lineWriter, stall time.Duration, done <-chan struct{}) {
	tick := min(stall/4, time.Second)
	if tick <= 0 {
		tick = time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.Sub(out.lastActivity()) >= stall {
				cancel(errStalled)
				return
			}
		}
	}
}

func settingHint(env string) string {
	if env == "" {
		return ""
	}
	return "（可用 " + env + " 调整）"
}

// DurationEnv reads a Go duration such as "10m" from name. "0" disables the
// limit; a missing or malformed value keeps def.
func DurationEnv(name string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	if raw == "0" {
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return def
	}
	return parsed
}

func roundDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func lastLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// lineWriter splits output on \n and \r (git redraws progress with \r), keeps
// the last lines, and records when output last arrived.
type lineWriter struct {
	mu       sync.Mutex
	redact   func(string) string
	onLine   func(string, time.Time)
	pending  bytes.Buffer
	lines    []string
	last     time.Time
	lastEmit time.Time
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.last = now
	for _, b := range p {
		if b == '\n' || b == '\r' {
			w.endLine(now)
			continue
		}
		w.pending.WriteByte(b)
	}
	return len(p), nil
}

func (w *lineWriter) endLine(now time.Time) {
	line := strings.TrimSpace(w.pending.String())
	w.pending.Reset()
	if line == "" {
		return
	}
	if w.redact != nil {
		line = w.redact(line)
	}
	// "Receiving objects: 27% ..." redraws the same counter; keep only the
	// newest so the tail shows what happened, not 20 copies of one counter.
	if n := len(w.lines); n > 0 && sameCounter(w.lines[n-1], line) {
		w.lines[n-1] = line
	} else {
		w.lines = append(w.lines, line)
		if len(w.lines) > tailLines {
			w.lines = w.lines[len(w.lines)-tailLines:]
		}
	}
	if w.onLine != nil {
		w.onLine(line, now)
	}
}

func sameCounter(previous, line string) bool {
	i := strings.Index(line, ": ")
	if i <= 0 || !strings.Contains(line[i:], "%") {
		return false
	}
	return strings.HasPrefix(previous, line[:i+2])
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.endLine(time.Now())
}

func (w *lineWriter) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.lines, "\n")
}

func (w *lineWriter) lastActivity() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}
