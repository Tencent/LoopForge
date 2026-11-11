package engine

import (
	"context"
	"sync"

	"agentflow/internal/domain"
)

// Parallel SOP branches run as independent executeNode goroutines. Nothing
// serializes a whole run any more; what must not overlap is locked by a
// narrower key:
//
//   - one execution per (run, node): a paused-then-resumed run, a rework or a
//     lease takeover may start a node again while its previous execution is
//     still winding down; the new one waits instead of interleaving;
//   - one CLI invocation per (workspace owner, agent): an Agent's control
//     directories (.agentflow/<agent>, config/<agent>, private home, gate MCP
//     config) are keyed by its ID, so two nodes of the same role in one
//     checkout (including a child SOP in its parent's checkout) take turns;
//   - one pool binding per run, so concurrent first nodes pick one machine.
//
// Everything else (preparation, checkout, input materialization) stays under
// runEnvironment.nodeMu, which a node now holds only while preparing.

type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until key is free and returns its release. Entries are
// reference counted so finished runs do not leave mutexes behind.
func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	entry := k.locks[key]
	if entry == nil {
		entry = &keyedLock{}
		k.locks[key] = entry
	}
	entry.refs++
	k.mu.Unlock()
	entry.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Unlock()
			k.mu.Lock()
			entry.refs--
			if entry.refs == 0 {
				delete(k.locks, key)
			}
			k.mu.Unlock()
		})
	}
}

func (e *Engine) lockNodeExec(runID, nodeID string) func() {
	return e.execLocks.lock("node\x00" + runID + "\x00" + nodeID)
}

func (e *Engine) lockAgentInvocation(workspaceOwner, agentID string) func() {
	return e.execLocks.lock("agent\x00" + workspaceOwner + "\x00" + agentID)
}

func (e *Engine) lockPoolBinding(runID string) func() {
	return e.execLocks.lock("pool\x00" + runID)
}

// nodeExecution is a started node attempt this instance is running. Its
// cancel stops only that node, so a rework that resets a running sibling
// does not wait for it to finish on its own.
type nodeExecution struct {
	attempt int
	cancel  context.CancelFunc
}

func (e *Engine) registerNodeExecution(runID, nodeID string, attempt int, cancel context.CancelFunc) func() {
	e.nodeExecMu.Lock()
	if e.nodeExecs == nil {
		e.nodeExecs = map[string]map[string]nodeExecution{}
	}
	if e.nodeExecs[runID] == nil {
		e.nodeExecs[runID] = map[string]nodeExecution{}
	}
	e.nodeExecs[runID][nodeID] = nodeExecution{attempt: attempt, cancel: cancel}
	e.nodeExecMu.Unlock()
	return func() {
		e.nodeExecMu.Lock()
		if current, ok := e.nodeExecs[runID][nodeID]; ok && current.attempt == attempt {
			delete(e.nodeExecs[runID], nodeID)
			if len(e.nodeExecs[runID]) == 0 {
				delete(e.nodeExecs, runID)
			}
		}
		e.nodeExecMu.Unlock()
	}
}

// nodeExecutionLive reports whether the persisted node still belongs to the
// execution that started it: same attempt and still in flight.
func nodeExecutionLive(nr *domain.NodeRun, attempt int) bool {
	return nr != nil && nr.Attempt == attempt && (nr.Status == "running" || nr.Status == "pending_input")
}

// reconcileNodeExecutions cancels executions whose node was reset or
// re-attempted by a control decision (rework, retry, refresh), possibly made
// on another instance. Their outcome would be discarded anyway; cancelling
// frees the node lock so the new attempt can start.
//
// The registry is copied before the run is read: an execution registers only
// after committing its running status, so a state read that follows the copy
// is never older than any copied registration. Reading first would let a node
// that started in between look stale and be cancelled with status "running"
// left behind.
func (e *Engine) reconcileNodeExecutions(runID string) {
	e.nodeExecMu.Lock()
	registered := make(map[string]nodeExecution, len(e.nodeExecs[runID]))
	for nodeID, execution := range e.nodeExecs[runID] {
		registered[nodeID] = execution
	}
	e.nodeExecMu.Unlock()
	if len(registered) == 0 {
		return
	}
	r, ok := e.repo.RunState(runID)
	if !ok {
		return
	}
	for nodeID, execution := range registered {
		if !nodeExecutionLive(r.Nodes[nodeID], execution.attempt) {
			execution.cancel()
		}
	}
}

// cancelNodeExecutions stops this instance's executions of the given nodes.
func (e *Engine) cancelNodeExecutions(runID string, nodeIDs map[string]bool) {
	e.nodeExecMu.Lock()
	var stale []context.CancelFunc
	for nodeID, execution := range e.nodeExecs[runID] {
		if nodeIDs[nodeID] {
			stale = append(stale, execution.cancel)
		}
	}
	e.nodeExecMu.Unlock()
	for _, cancel := range stale {
		cancel()
	}
}
