package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestConversationDraftMachineSelectionAndFirstTurnBinding(t *testing.T) {
	h, user := workbenchFixture(t)
	for _, runner := range []domain.Runner{
		{ID: "mine", OwnerUserID: user.ID, RunnerVersion: domain.RunnerVersionConversationCLI},
		{ID: "other", OwnerUserID: user.ID + 1, RunnerVersion: domain.RunnerVersionConversationCLI},
	} {
		if err := h.repo.PutRunner(runner); err != nil {
			t.Fatal(err)
		}
	}
	put := func(target *domain.AssistantExecutionTarget, want int) {
		draft, ok := h.repo.AssistantSession(user.ID, "machine-draft")
		if !ok {
			draft = domain.AssistantSession{ID: "machine-draft", ProjectID: "workbench"}
		}
		draft.ExecutionTarget = target
		body, _ := json.Marshal(draft)
		c, rec := workbenchContext(user, "PUT", "/api/assistant/sessions/machine-draft", bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: "machine-draft"}}
		h.session(c)
		if rec.Code != want {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
	}
	cloud := &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud}
	put(cloud, 200)
	put(&domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "other"}, 400)
	put(&domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "mine"}, 200)
	put(cloud, 200)
	old, _ := h.repo.AssistantSession(user.ID, "machine-draft")
	if old.ExecutionLocked() {
		t.Fatal("selection locked an empty conversation")
	}
	entered, finished := make(chan struct{}), make(chan struct{})
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		defer close(finished)
		<-ctx.Done()
		return "", ctx.Err()
	}
	body, _ := json.Marshal(assistantRequest{SessionID: old.ID, ProjectID: old.ProjectID, ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "mine"}, Messages: []assistantMessage{{Role: "user", Content: "hello"}}})
	c, rec := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	g, _ := h.repo.AssistantGeneration(user.ID, old.ID)
	t.Cleanup(func() { _ = h.repo.CancelAssistantGeneration(user.ID, old.ID, g.ID) })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	saved, _ := h.repo.AssistantSession(user.ID, old.ID)
	if !saved.ExecutionLocked() || saved.Execution().RunnerID != "mine" {
		t.Fatal("first turn did not bind the final draft choice")
	}
	_ = h.repo.CancelAssistantGeneration(user.ID, old.ID, g.ID)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not stop")
	}
	put(cloud, 409)
}

func TestDraftCloudAllocationIsServerOwned(t *testing.T) {
	old := domain.AssistantSession{ID: "draft", ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud, RunnerID: "allocated"}}
	for _, requested := range []*domain.AssistantExecutionTarget{nil, old.ExecutionTarget} {
		if got := draftExecutionTarget(old, requested); got.Pool != domain.ExecutionModeCloud || got.RunnerID != "" {
			t.Fatalf("saved allocation reused as client input: %+v", got)
		}
	}
	forged := &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud, RunnerID: "forged"}
	if draftExecutionTarget(old, forged).RunnerID != "forged" {
		t.Fatal("forged cloud runner silently accepted")
	}
}
