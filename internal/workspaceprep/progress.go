// Package workspaceprep carries workspace-opening progress through local and
// runner execution without coupling commands to the HTTP or persistence layer.
package workspaceprep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"agentflow/internal/domain"
)

type contextKey struct{}
type observer struct {
	sink  func(domain.WorkspaceOpening)
	phase string
	label string
}

func WithSink(ctx context.Context, sink func(domain.WorkspaceOpening)) context.Context {
	return context.WithValue(ctx, contextKey{}, observer{sink: sink})
}

func Enabled(ctx context.Context) bool { return get(ctx).sink != nil }
func get(ctx context.Context) observer { o, _ := ctx.Value(contextKey{}).(observer); return o }

// Timeout accepts Go durations, e.g. 20m or 90s. Invalid/nonpositive values
// retain the bounded default rather than silently disabling cancellation.
func Timeout(phase string) time.Duration {
	fallback := map[string]time.Duration{"build": 15 * time.Minute, "clone": 10 * time.Minute, "branch": 2 * time.Minute, "prepare": 5 * time.Minute}[phase]
	if fallback == 0 {
		fallback = 5 * time.Minute
	}
	if value, err := time.ParseDuration(os.Getenv("AGENTFLOW_WORKSPACE_" + strings.ToUpper(phase) + "_TIMEOUT")); err == nil && value > 0 {
		return value
	}
	return fallback
}

type phaseError struct {
	phase, label string
	err          error
}

func (e *phaseError) Error() string { return e.label + ": " + e.err.Error() }
func (e *phaseError) Unwrap() error { return e.err }

func Do(ctx context.Context, phase, label string, work func(context.Context) error) error {
	if !Enabled(ctx) {
		return work(ctx)
	}
	o := get(ctx)
	o.phase, o.label = phase, label
	ctx = context.WithValue(ctx, contextKey{}, o)
	duration := Timeout(phase)
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	Report(ctx, domain.WorkspaceOpening{State: "running", Phase: phase, Message: label, TimeoutSeconds: int64(duration.Seconds())})
	err := work(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%w (timeout %s): %v", ctx.Err(), duration, err)
	} else if ctx.Err() != nil {
		err = fmt.Errorf("%w: %v", ctx.Err(), err)
	}
	if err != nil {
		return &phaseError{phase: phase, label: label, err: err}
	}
	return nil
}

func Report(ctx context.Context, progress domain.WorkspaceOpening) {
	if sink := get(ctx).sink; sink != nil {
		progress.Message = Sanitize(progress.Message)
		progress.Output = Sanitize(progress.Output)
		sink(progress)
	}
}

func Result(ctx context.Context, err error) {
	p := domain.WorkspaceOpening{State: "ready", Message: "工作区已就绪"}
	if err != nil {
		p.State, p.Message = "failed", err.Error()
		var stage *phaseError
		if errors.As(err, &stage) {
			p.Phase = stage.phase
		}
	}
	Report(ctx, p)
}

var urlCredentials = regexp.MustCompile(`(?i)(https?://)[^\s/]+@`)
var authHeader = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:basic|bearer|token)\s+[^\s'"<>]+`)
var urlSecrets = regexp.MustCompile(`(?i)([?&](?:access_token|token|password|key)=)[^\s&#'"<>]+`)

// Sanitize is also applied at the receiving control plane. Known Git tokens
// are redacted by the command's caller before reaching this boundary.
func Sanitize(value string) string {
	value = urlCredentials.ReplaceAllString(value, "${1}[redacted]@")
	value = authHeader.ReplaceAllString(value, "${1}[redacted]")
	value = urlSecrets.ReplaceAllString(value, "${1}[redacted]")
	if len(value) > 8192 {
		start := len(value) - 8192
		for start < len(value) && !utf8.RuneStart(value[start]) {
			start++
		}
		value = "…" + value[start:]
	}
	return strings.TrimSpace(value)
}
