package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func personalWorkspaceGateway(t *testing.T, h *assistantHandlers) *agentToolGateway {
	t.Helper()
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")
	return gateway
}

func TestProjectWorkspaceBindRejectsMissingProject(t *testing.T) {
	h, owner := workbenchFixture(t)
	gateway := personalWorkspaceGateway(t, h)
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "gone", UserID: owner.ID, User: owner, ChatSessionID: "c", ChatOwnerID: owner.ID}
	if _, err := gateway.bindConversationWorkspace(context.Background(), s, workspaceOpenChoice{Goal: "x"}); err == nil || !strings.Contains(err.Error(), "项目不存在") {
		t.Fatalf("got %v, want missing project", err)
	}
}

// Only the responsible user controls a personal workspace browser; platform
// admin and project membership do not apply because there is no project.
func TestBrowserControlOfPersonalWorkspace(t *testing.T) {
	h, owner := workbenchFixture(t)
	browser := &browserHandlers{deps: h.deps}
	req := httptest.NewRequest("GET", "/", nil)
	personal := domain.Run{ID: "personal-browser", Kind: domain.RunKindWorkspace, ExecutionContext: domain.TelemetryContext{ResponsibleUserID: owner.ID}}
	for _, tc := range []struct {
		name string
		run  domain.Run
		user int64
		want bool
	}{
		{"owner", personal, owner.ID, true},
		{"other user", personal, owner.ID + 1, false},
		{"anonymous", personal, 0, false},
		{"unowned personal", domain.Run{ID: "orphan", Kind: domain.RunKindWorkspace}, 0, false},
	} {
		if got := browser.canControl(req, tc.run, tc.user); got != tc.want {
			t.Errorf("%s: canControl=%v want %v", tc.name, got, tc.want)
		}
	}
}
