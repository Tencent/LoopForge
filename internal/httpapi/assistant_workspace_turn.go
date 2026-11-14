package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"agentflow/internal/engine"
	"agentflow/internal/repo"
)

var errAssistantTurnClosed = errors.New("本轮对话已结束")

// The lease lasts 15 minutes and is renewed every 30 seconds. A storage error
// is retried on the next tick while the lease is still comfortably valid;
// only a lease taken by someone else, or one about to lapse, stops the turn.
const assistantWorkspaceRenewGrace = 5 * time.Minute

// Ordinary conversations discover their workspace during the turn. Hold its
// lease across tools, just as task conversations hold their generation lease.
// Only switching away or finishing the turn captures a checkpoint.
type assistantWorkspaceTurn struct {
	operations  sync.RWMutex
	preparation sync.Mutex
	mu          sync.Mutex
	engine      assistantWorkspaceLeaseEngine
	cancel      context.CancelFunc
	runID       string
	token       string
	renewedAt   time.Time
	prepared    bool
	closed      bool
	renewError  error
	saveErrors  map[string]error
	// onLease records each foreground lease on the turn's generation so a
	// reader that reaps a dead turn can release exactly that lease.
	onLease func(runID, token string)
	stop    chan struct{}
	done    chan struct{}
}

type assistantWorkspaceLeaseEngine interface {
	BeginConversationWorkspaceUse(string) (string, error)
	MaterializeConversationWorkspace(context.Context, string) error
	EnsureConversationWorkspace(context.Context, string, string) error
	RenewConversationWorkspaceUse(string, string) error
	EndConversationWorkspaceUse(context.Context, string, string) error
	EndConversationWorkspaceUseAsync(context.Context, string, string) (<-chan error, error)
}

func newAssistantWorkspaceTurn(en assistantWorkspaceLeaseEngine, cancel context.CancelFunc) *assistantWorkspaceTurn {
	t := &assistantWorkspaceTurn{engine: en, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(t.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-t.stop:
				return
			case <-ticker.C:
				if err := t.renew(); err != nil {
					return
				}
			}
		}
	}()
	return t
}

// recordLeases sets where each lease this turn takes is recorded.
func (t *assistantWorkspaceTurn) recordLeases(fn func(runID, token string)) {
	t.mu.Lock()
	t.onLease = fn
	t.mu.Unlock()
}

// Tools may run in parallel, but a binding switch and the final checkpoint
// must wait for all tools using the old workspace to return.
func (t *assistantWorkspaceTurn) lockOperation() func() {
	if t == nil {
		return func() {}
	}
	t.operations.RLock()
	return t.operations.RUnlock
}

// usable reports why this turn can no longer touch a workspace. Callers check
// it before creating anything the turn would then be unable to use.
func (t *assistantWorkspaceTurn) usable() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usableLocked()
}

func (t *assistantWorkspaceTurn) usableLocked() error {
	if t.closed {
		return errAssistantTurnClosed
	}
	return t.renewError
}

func (t *assistantWorkspaceTurn) lease(ctx context.Context, runID string) (context.Context, error) {
	t.mu.Lock()
	if err := t.usableLocked(); err != nil {
		t.mu.Unlock()
		return ctx, err
	}
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()
		return ctx, err
	}
	if t.token != "" && t.runID != runID {
		t.mu.Unlock()
		return ctx, errors.New("工作区已切换，请重新调用工具")
	}
	if t.token == "" {
		token, err := t.engine.BeginConversationWorkspaceUse(runID)
		if err != nil {
			t.mu.Unlock()
			return ctx, err
		}
		t.runID, t.token, t.renewedAt = runID, token, time.Now()
		if t.onLease != nil {
			t.onLease(runID, token)
		}
	}
	ctx = engine.WithConversationWorkspaceToken(ctx, t.token, runID)
	t.mu.Unlock()
	// Checkout can be slow. Serialize its first preparation without blocking
	// renewal of the lease that protects it.
	t.preparation.Lock()
	defer t.preparation.Unlock()
	t.mu.Lock()
	prepared := t.prepared
	t.mu.Unlock()
	if !prepared {
		if err := t.engine.MaterializeConversationWorkspace(ctx, runID); err != nil {
			return ctx, err
		}
		// A session can remain in workspace mode after idle reclamation. Restore
		// its snapshot before the native CLI can mount or recreate that path.
		if err := t.engine.EnsureConversationWorkspace(ctx, runID, engine.ConversationWorkspaceToken(ctx, runID)); err != nil {
			return ctx, err
		}
		t.mu.Lock()
		t.prepared = true
		t.mu.Unlock()
	}
	return ctx, nil
}

// Run control requests originate in fresh MCP HTTP requests too. Reattach the
// lease only for the workspace this turn actually owns.
func (t *assistantWorkspaceTurn) withLease(ctx context.Context, runID string) context.Context {
	if t == nil {
		return ctx
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.runID == runID && !t.closed {
		return engine.WithConversationWorkspaceToken(ctx, t.token, runID)
	}
	return ctx
}

func (t *assistantWorkspaceTurn) renew() error {
	t.mu.Lock()
	if t.closed || t.token == "" {
		t.mu.Unlock()
		return nil
	}
	err := t.engine.RenewConversationWorkspaceUse(t.runID, t.token)
	if err == nil {
		t.renewedAt = time.Now()
		t.mu.Unlock()
		return nil
	}
	if !errors.Is(err, repo.ErrWorkspaceInUse) && time.Since(t.renewedAt) < assistantWorkspaceRenewGrace {
		log.Printf("assistant workspace %s: lease renewal failed, retrying: %v", t.runID, err)
		t.mu.Unlock()
		return nil
	}
	t.renewError = fmt.Errorf("工作区 %s 租约续期失败：%w", t.runID, err)
	err = t.renewError
	t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	return err
}

// checkpoint is called with operations exclusively locked by the binding
// switch, before it changes the session's workspace or releases the old one.
func (t *assistantWorkspaceTurn) checkpoint(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errAssistantTurnClosed
	}
	return t.finishLocked(ctx)
}

func (t *assistantWorkspaceTurn) finishLocked(ctx context.Context) error {
	if t.token == "" {
		return nil
	}
	// Only hand off the lease here. The background archive is interruptible by
	// the next turn and must never delay a stop or stream completion.
	done, err := t.engine.EndConversationWorkspaceUseAsync(ctx, t.runID, t.token)
	if err != nil {
		err = fmt.Errorf("工作区 %s：%w", t.runID, err)
		if t.saveErrors == nil {
			t.saveErrors = map[string]error{}
		}
		t.saveErrors[t.runID] = err
	} else {
		delete(t.saveErrors, t.runID)
		trackAssistantWorkspaceSave(t.runID, done)
	}
	t.runID, t.token = "", ""
	t.prepared = false
	return err
}

func (t *assistantWorkspaceTurn) close(ctx context.Context) error {
	t.operations.Lock()
	defer t.operations.Unlock()
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.stop)
		_ = t.finishLocked(ctx)
	}
	err := t.renewError
	for _, saveErr := range t.saveErrors {
		err = errors.Join(err, saveErr)
	}
	t.mu.Unlock()
	<-t.done
	return err
}

// Background failures are durable workspace metadata. Never write a late save
// result into a completed generation or a subsequent turn's stream.
func trackAssistantWorkspaceSave(runID string, result <-chan error) {
	done := inflightTasks.Add()
	go func() {
		defer done()
		if err := <-result; err != nil {
			log.Printf("assistant background workspace save run=%s: %v", runID, err)
		}
	}()
}
