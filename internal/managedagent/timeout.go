package managedagent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// turnTimeout bounds one managed Agent turn. A variable so tests can shorten it.
var turnTimeout = 5 * time.Minute

// ErrTurnTimedOut marks a turn that ran past the budget. The prompt was already
// sent and the cancellation is advisory, so the remote outcome is unknown and
// the same turn must not be resent automatically.
var ErrTurnTimedOut = errors.New("托管 Agent 轮次超时")

type turnTimeoutFailure struct {
	provider string
	err      error
}

func (f *turnTimeoutFailure) Error() string {
	return fmt.Sprintf("%s Agent 未在 %s 内完成本轮，已请求取消：%v", f.provider, turnTimeout, f.err)
}

// Unwrap exposes both the sentinel and the transport error so callers can
// match either one without parsing the message.
func (f *turnTimeoutFailure) Unwrap() []error {
	return []error{ErrTurnTimedOut, f.err}
}

// turnTimeoutError names the turn budget when it, rather than the caller's
// context (run cancellation) or a single request, ended the turn.
func turnTimeoutError(provider string, turn, parent context.Context, err error) error {
	if err == nil || !errors.Is(turn.Err(), context.DeadlineExceeded) || parent.Err() != nil {
		return err
	}
	return &turnTimeoutFailure{provider: provider, err: err}
}

// UnknownTurnError reports a managed turn whose remote outcome is unknown,
// rebuilt from a message that crossed a process boundary (a personal Runner
// completing its task). The wrapped sentinel cannot cross that boundary as a
// value, so this type matches ErrTurnTimedOut for retry classification while
// keeping the reporter's explanation intact.
type UnknownTurnError struct {
	Message string
}

func (e *UnknownTurnError) Error() string {
	if e.Message == "" {
		return ErrTurnTimedOut.Error()
	}
	return e.Message
}

func (e *UnknownTurnError) Is(target error) bool {
	return target == ErrTurnTimedOut
}

// TurnTimeout reports the per-turn budget. Runners and the control plane read
// the same value; tests shorten it.
func TurnTimeout() time.Duration { return turnTimeout }

// SetTurnTimeout replaces the per-turn budget and returns nothing; callers in
// tests restore the previous value.
func SetTurnTimeout(d time.Duration) { turnTimeout = d }
