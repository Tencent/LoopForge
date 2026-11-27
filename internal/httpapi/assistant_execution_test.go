package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

func TestOwnerResumesLegacyProjectConversationWithUnsharedRunner(t *testing.T) {
	h, owner := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"fixture reply","drafts":[]}`, nil
	}
	runner := domain.Runner{
		ID: "owner-runner", OwnerUserID: owner.ID, Status: domain.RunnerStatusOnline,
		RunnerVersion: domain.RunnerVersionConversationCLI,
	}
	if err := h.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: runner.ID}
	saved := domain.AssistantSession{
		ID: "legacy-conversation", ProjectID: "workbench", ExecutionTarget: &target,
		Messages: []assistantMessage{{Role: "user", Content: "before upgrading"}, {Role: "assistant", Content: "earlier reply"}},
	}
	if err := h.repo.PutAssistantSession(owner.ID, saved); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(assistantRequest{
		ProjectID: saved.ProjectID, SessionID: saved.ID,
		Messages: append(saved.Messages, assistantMessage{Role: "user", Content: "after upgrading"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/assistant/chat"
	c, rec := workbenchContext(owner, http.MethodPost, path, bytes.NewBuffer(body))
	h.chat(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume on own unshared machine: %d %s", rec.Code, rec.Body)
	}
	resumed, ok := h.repo.AssistantSession(owner.ID, saved.ID)
	if !ok || resumed.Execution() != target || len(resumed.Messages) != 4 {
		t.Fatalf("resumed conversation = %+v", resumed)
	}
}

func TestLockedConversationRejectsTurnsAfterRunnerAccessChanges(t *testing.T) {
	h, owner := workbenchFixture(t)
	member := domain.User{ID: owner.ID + 1, LoginName: "shared-conversation-member", CodeBuddyAPIKey: "test-only"}
	if err := h.repo.PutUser(member); err != nil {
		t.Fatal(err)
	}
	h.generate = func(context.Context, string, domain.User) (string, error) { return "fixture", nil }
	const projectID = "workbench"
	setRole := func(t *testing.T, role string) {
		t.Helper()
		project, ok := h.repo.Project(projectID)
		if !ok {
			t.Fatal("workbench project not found")
		}
		project.Members = []domain.ProjectMember{{UserID: member.ID, Role: role}}
		if err := h.repo.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	target := &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: "shared-locked"}
	if err := h.repo.PutRunner(domain.Runner{ID: target.RunnerID, OwnerUserID: owner.ID,
		Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationCLI}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/assistant/chat", "/api/assistant/generations"} {
		t.Run(path, func(t *testing.T) {
			setRole(t, domain.ProjectRoleMember)
			if err := h.repo.SetRunnerSharedProjects(target.RunnerID, owner.ID, []string{projectID}); err != nil {
				t.Fatal(err)
			}
			for _, revoke := range []string{"share", "membership"} {
				id := "locked-" + revoke + "-" + strings.TrimPrefix(path, "/api/assistant/")
				if err := h.repo.PutAssistantSession(member.ID, domain.AssistantSession{
					ID: id, ProjectID: projectID, ExecutionTarget: target, ExecutionTargetLocked: true,
				}); err != nil {
					t.Fatal(err)
				}
				if revoke == "share" {
					if err := h.repo.SetRunnerSharedProjects(target.RunnerID, owner.ID, nil); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := h.repo.SetRunnerSharedProjects(target.RunnerID, owner.ID, []string{projectID}); err != nil {
						t.Fatal(err)
					}
					setRole(t, domain.ProjectRoleViewer)
				}
				body, _ := json.Marshal(assistantRequest{SessionID: id, ProjectID: projectID,
					Messages: []assistantMessage{{Role: "user", Content: "fixture"}}})
				c, rec := workbenchContext(member, http.MethodPost, path, bytes.NewBuffer(body))
				if strings.HasSuffix(path, "/chat") {
					h.chat(c)
				} else {
					h.startGeneration(c)
				}
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "会话绑定的执行机器") {
					t.Errorf("%s: got %d %s, want 403 with a machine access error", revoke, rec.Code, rec.Body.String())
				}
				if _, ok := h.repo.AssistantGeneration(member.ID, id); ok {
					t.Errorf("%s: a generation was created after losing access", revoke)
				}
			}
		})
	}
}
