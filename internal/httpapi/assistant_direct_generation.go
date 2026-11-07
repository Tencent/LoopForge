package httpapi

import (
	"context"
	"time"

	"agentflow/internal/domain"
)

// Synchronous shared chat must occupy the same durable slot as /generations.
// Otherwise a second process can save a pending turn while the first CLI writes
// the shared transcript. A local file lock additionally fences stale writers.
func (h *assistantHandlers) claimDirectSharedGeneration(ctx context.Context, req assistantRequest, ownerID int64, actor domain.User, cancel context.CancelFunc) (domain.AssistantGeneration, func(bool), error) {
	g := domain.AssistantGeneration{ID: newAssistantSessionID(), SessionID: req.SessionID, UserID: ownerID, ActorID: actor.ID, ActorName: actor.LoginName, ProjectID: req.ProjectID, Status: "running", StartedAt: time.Now().UTC()}
	pending := assistantConversation{ID: req.SessionID, Shared: true, ProjectID: req.ProjectID, Model: req.Model, Effort: req.Effort, RunID: req.RunID, Messages: req.Messages}
	if err := h.repo.BeginAssistantGeneration(g, pending); err != nil {
		return g, nil, err
	}
	untrack := inflightTasks.Add()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, ok := h.repo.AssistantGeneration(ownerID, req.SessionID)
				if !ok || current.ID != g.ID || current.Status != "running" || current.CancelRequested {
					cancel()
					return
				}
				if err := h.repo.CheckpointAssistantGeneration(g); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return g, func(completed bool) {
		defer untrack()
		close(stop)
		<-done
		g.Status = "failed"
		if completed {
			g.Status = "completed"
		} else {
			current, ok := h.repo.AssistantGeneration(ownerID, req.SessionID)
			if ctx.Err() != nil || ok && current.ID == g.ID && current.CancelRequested {
				g.Status = "cancelled"
			}
		}
		_ = h.repo.CheckpointAssistantGeneration(g)
	}, nil
}
