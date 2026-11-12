package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// A direct chat may open its workspace before the first transcript is saved:
// the binding reserves the owner's private session so it can persist.
func TestPersonalWorkspaceBindReservesUnsavedDirectChat(t *testing.T) {
	h, owner := workbenchFixture(t)
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: "unsaved-direct-chat", ChatOwnerID: owner.ID}
	if _, ok := h.repo.AssistantSession(owner.ID, s.ChatSessionID); ok {
		t.Fatal("fixture already has the session")
	}
	bound, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "scratch"})
	if err != nil || bound.Run.ID == "" || bound.Run.ExecutionContext.ProjectID != "" || bound.Run.ExecutionContext.ResponsibleUserID != owner.ID {
		t.Fatalf("bind unsaved direct chat: %+v %v", bound, err)
	}
	saved, ok := h.repo.AssistantSession(owner.ID, s.ChatSessionID)
	if !ok || saved.WorkspaceRunID != bound.Run.ID {
		t.Fatalf("reserved session did not keep the binding: %+v %v", saved, ok)
	}
}

// Projectless bindings are private to the owner's own unshared, non-run,
// non-project chat; every other combination is refused before any run exists.
func TestPersonalWorkspaceBindRejectsForeignOrNonPrivateChats(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "bind-other"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("bind-other")
	for _, chat := range []domain.AssistantSession{
		{ID: "private-chat"},
		{ID: "project-chat", ProjectID: "workbench"},
		{ID: "shared-chat", Shared: true},
		{ID: "run-chat", RunID: "some-run"},
	} {
		if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
			t.Fatal(err)
		}
	}
	gateway := &agentToolGateway{deps: h.deps}
	for _, tc := range []struct {
		name string
		s    func() *AgentToolSession
	}{
		{"project chat", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: "project-chat", ChatOwnerID: owner.ID}
		}},
		{"shared chat", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: "shared-chat", ChatOwnerID: owner.ID}
		}},
		{"run chat", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: "run-chat", ChatOwnerID: owner.ID}
		}},
		{"another user's chat", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, UserID: other.ID, User: other, ChatSessionID: "private-chat", ChatOwnerID: owner.ID}
		}},
		{"caller mismatch", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: other, ChatSessionID: "private-chat", ChatOwnerID: owner.ID}
		}},
		{"anonymous unsaved", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, ChatSessionID: "anon-chat"}
		}},
		{"unsaved chat of another owner", func() *AgentToolSession {
			return &AgentToolSession{Mode: AgentToolAssistant, UserID: other.ID, User: other, ChatSessionID: "foreign-unsaved", ChatOwnerID: owner.ID}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := gateway.bindConversationWorkspace(context.Background(), tc.s(), workspaceOpenChoice{Goal: "scratch"}); !errors.Is(err, errAgentToolForbidden) {
				t.Fatalf("want forbidden, got %v", err)
			}
		})
	}
	// Refusals must not reserve sessions on someone else's behalf.
	for _, id := range []string{"anon-chat", "foreign-unsaved"} {
		for _, uid := range []int64{0, owner.ID, other.ID} {
			if _, ok := h.repo.AssistantSession(uid, id); ok {
				t.Fatalf("refused bind reserved %s for user %d", id, uid)
			}
		}
	}
	if runs := h.repo.Runs(); len(runs) != 0 {
		t.Fatalf("refused binds created runs: %+v", runs)
	}
}

// A personal workspace has no project, so project-only choices are rejected.
func TestPersonalWorkspaceBindRejectsProjectOnlyChoices(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "private-chat"}); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: owner.ID, User: owner, ChatSessionID: "private-chat", ChatOwnerID: owner.ID}
	for name, choice := range map[string]workspaceOpenChoice{
		"issue":               {IssueID: "ISSUE-1"},
		"repositories":        {Repositories: []string{"repo"}},
		"branch":              {Branch: "main"},
		"base branch":         {BaseBranch: "main"},
		"repository branches": {RepositoryBranches: map[string]workspaceBranchChoice{"repo": {}}},
		"requirement":         {Requirement: conversationRequirement{Title: "new feature"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gateway.bindConversationWorkspace(context.Background(), s, choice)
			if err == nil || !strings.Contains(err.Error(), "个人工作区没有项目仓库或需求") {
				t.Fatalf("want personal workspace refusal, got %v", err)
			}
		})
	}
	if runs := h.repo.Runs(); len(runs) != 0 {
		t.Fatalf("refused binds created runs: %+v", runs)
	}
}
