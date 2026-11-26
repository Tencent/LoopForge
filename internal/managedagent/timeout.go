package managedagent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// turnTimeout bounds one managed Agent turn. A variable so tests can shorten it.
var turnTimeout = 5 * time.Minute

// turnTimeoutError names the turn budget when it, rather than the caller's
// context (run cancellation) or a single request, ended the turn.
func turnTimeoutError(provider string, turn, parent context.Context, err error) error {
	if err == nil || !errors.Is(turn.Err(), context.DeadlineExceeded) || parent.Err() != nil {
		return err
	}
	return fmt.Errorf("%s Agent 未在 %s 内完成本轮，已请求取消：%w", provider, turnTimeout, err)
}
