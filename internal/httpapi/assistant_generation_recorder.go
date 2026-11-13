package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"agentflow/internal/domain"
)

// assistantGenerationRecorder accumulates a running turn's stream events into
// its generation record and publishes them to live SSE readers. Chat turns and
// scheduled runs share it so both stream to the conversation page the same way.
type assistantGenerationRecorder struct {
	mu          sync.Mutex
	g           domain.AssistantGeneration
	sequence    int
	eventBytes  int
	projection  assistantStreamProjection
	live        *assistantLiveGeneration
	diagnostics *assistantDiagnosticsCollector
}

// newAssistantGenerationRecorder registers g as live; call close when the
// turn's final state has been checkpointed.
func newAssistantGenerationRecorder(g domain.AssistantGeneration, cancel ...context.CancelFunc) *assistantGenerationRecorder {
	r := &assistantGenerationRecorder{g: g, live: &assistantLiveGeneration{changed: make(chan struct{})}, diagnostics: newAssistantDiagnosticsCollector(g)}
	r.live.read = r.snapshot
	r.live.diagnostics = func() domain.AssistantDiagnostics { return r.diagnostics.snapshot(r.snapshot()) }
	if len(cancel) > 0 {
		r.live.cancel = func() {
			r.mu.Lock()
			running := r.g.Status == "running"
			if running {
				r.g.CancelRequested = true
			}
			r.mu.Unlock()
			if running {
				cancel[0]()
				r.live.publish()
			}
		}
	}
	assistantLiveGenerations.Store(g.ID, r.live)
	return r
}

func (r *assistantGenerationRecorder) close() { assistantLiveGenerations.Delete(r.g.ID) }

// snapshotLocked copies the generation with its current stream projection.
func (r *assistantGenerationRecorder) snapshotLocked() domain.AssistantGeneration {
	copy := r.g
	copy.Events = append([]domain.AssistantGenerationEvent(nil), r.g.Events...)
	copy.Stream = r.projection.snapshot(r.sequence)
	return copy
}

func (r *assistantGenerationRecorder) snapshot() domain.AssistantGeneration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked()
}

// sink records one chat-protocol event ("thought", "result", "error",
// "workspace_warning") and wakes live readers.
func (r *assistantGenerationRecorder) sink(kind string, value any) {
	r.mu.Lock()
	defer func() { r.mu.Unlock(); r.live.publish() }()
	raw, _ := json.Marshal(value)
	switch kind {
	case "runtime_mode":
		var update struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(raw, &update) == nil && (update.Mode == domain.AssistantModePlatform || update.Mode == domain.AssistantModeWorkspace) {
			r.g.RuntimeMode = update.Mode
		}
	case "thought":
		if len(raw) > 256<<10 {
			return
		}
		r.sequence++
		r.projection.add(raw)
		r.eventBytes += len(raw)
		r.g.Events = append(r.g.Events, domain.AssistantGenerationEvent{Sequence: r.sequence, Data: raw})
		for len(r.g.Events) > 500 || r.eventBytes > 2<<20 {
			r.eventBytes -= len(r.g.Events[0].Data)
			r.g.Events = r.g.Events[1:]
		}
	case "result":
		r.g.Reply = raw
	case "workspace_warning":
		r.g.WorkspaceWarning = fmt.Sprint(value)
	case "error":
		var e struct {
			Error   string                   `json:"error"`
			Failure *domain.AssistantFailure `json:"failure"`
		}
		_ = json.Unmarshal(raw, &e)
		r.g.Error = e.Error
		r.g.Failure = e.Failure
	}
}

// activity is the turn's tool/thinking log for the stored transcript.
func (r *assistantGenerationRecorder) activity() []domain.AssistantActivityEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.projection.activity.snapshot()
}

// checkpointRunningGeneration renews a running turn's lease every tick but
// rewrites its diagnostic payload only when the evidence changed or the
// rewrite interval elapsed.
func (h *assistantHandlers) checkpointRunningGeneration(g domain.AssistantGeneration, d *assistantDiagnosticsCollector) error {
	if d == nil {
		return h.repo.CheckpointAssistantGeneration(g)
	}
	report, write := d.checkpointSnapshot(g)
	if !write {
		return h.repo.HeartbeatAssistantGeneration(g)
	}
	return h.repo.CheckpointAssistantGeneration(g, report)
}
