package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestProjectParticipantsKeepConversationToolsAndWorkspace(t *testing.T) {
	for _, role := range []string{"member", "viewer"} {
		t.Run(role, func(t *testing.T) {
			h, owner, peer, outsider, _ := sharedWorkbenchFixture(t)
			project, _ := h.repo.Project("workbench")
			project.Members = []domain.ProjectMember{{UserID: peer.ID, Role: role}}
			if err := h.repo.PutProject(project); err != nil {
				t.Fatal(err)
			}
			run := domain.Run{ID: "conversation-workspace", Kind: domain.RunKindWorkspace, Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: project.ID}, WorkflowSnapshot: domain.WorkflowDefinition{SandboxProfileID: "business"}, Nodes: map[string]*domain.NodeRun{}}
			if err := h.repo.PutRun(run); err != nil {
				t.Fatal(err)
			}
			chat := domain.AssistantSession{ID: "project-workspace-chat", ProjectID: project.ID, WorkspaceRunID: run.ID, OwnWorkspaceRunIDs: []string{run.ID}, SandboxProfileID: "business", Messages: []domain.AssistantMessage{{Role: "user", AuthorID: owner.ID, Content: "start"}, {Role: "assistant", Content: "started"}}}
			if err := h.repo.PutAssistantSession(owner.ID, chat); err != nil {
				t.Fatal(err)
			}
			content := "before"
			h.generate = func(ctx context.Context, prompt string, actor domain.User) (string, error) {
				if actor.ID != peer.ID || !strings.Contains(prompt, run.ID) {
					return "", fmt.Errorf("participant lost identity or workspace context: actor=%d", actor.ID)
				}
				var session *AgentToolSession
				agentToolSessions.mu.Lock()
				for _, candidate := range agentToolSessions.sessions {
					if candidate.ChatSessionID == chat.ID && candidate.UserID == actor.ID {
						session = candidate
						break
					}
				}
				agentToolSessions.mu.Unlock()
				if session == nil || session.ChatOwnerID != owner.ID || session.workspaceRun() != run.ID || session.SandboxProfileID != "business" {
					return "", fmt.Errorf("participant lost shared workspace binding or environment")
				}
				gateway := &agentToolGateway{deps: h.deps}
				catalog := gateway.buildCatalog(ctx, session)
				for _, name := range []string{"workbench_workspace_open", "workbench_workspace_tree", "workbench_workspace_read", "workbench_workspace_write", "workbench_workspace_command", "workbench_workspace_submit_mr", "workbench_workspace_sop", "workbench_browser_navigate"} {
					if _, ok := lookupAgentTool(catalog, name); !ok {
						return "", fmt.Errorf("participant lost tool %s", name)
					}
				}
				gateway.api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					user, ok := h.requestUser(r, true)
					if !ok || user.ID != peer.ID || mainBotWorkspaceRun(r.Context()) != run.ID || r.URL.Path != "/api/runs/"+run.ID+"/workspace/file" {
						t.Errorf("workspace operation lost caller or conversation: %s", r.URL.Path)
						http.Error(w, "wrong scope", http.StatusForbidden)
						return
					}
					if r.Method == http.MethodPut {
						var body struct{ Content string }
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						content = body.Content
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"content": content, "sha256": runtime.WorkspaceContentSHA(content)})
				})
				if _, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_write", map[string]any{"path": "README.md", "content": "peer edit", "expectedSha256": runtime.WorkspaceContentSHA(content)}); err != nil {
					return "", err
				}
				read, err := gateway.callMainBotWorkspace(ctx, session, "workbench_workspace_read", map[string]any{"path": "README.md"})
				if err != nil || !strings.Contains(read, "peer edit") {
					return "", fmt.Errorf("participant cannot read its edit: %s %v", read, err)
				}
				return "edited", nil
			}
			stored, _ := h.repo.AssistantSession(owner.ID, chat.ID)
			request := assistantRequest{ProjectID: project.ID, SessionID: chat.ID, Messages: append(stored.Messages, assistantMessage{Role: "user", Content: "continue"})}
			body, _ := json.Marshal(request)
			c, rec := workbenchContext(outsider, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
			h.chat(c)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("non-member entered conversation: %d %s", rec.Code, rec.Body.String())
			}
			c, rec = workbenchContext(peer, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(body))
			h.chat(c)
			if rec.Code != http.StatusOK {
				t.Fatalf("participant cannot continue conversation: %d %s", rec.Code, rec.Body.String())
			}
			stored, _ = h.repo.AssistantSession(owner.ID, chat.ID)
			if content != "peer edit" || stored.WorkspaceRunID != run.ID || stored.UserID != owner.ID || stored.Messages[len(stored.Messages)-2].AuthorID != peer.ID {
				t.Fatalf("participant turn changed shared workspace ownership or lost attribution: %+v", stored)
			}
		})
	}
}

func TestConversationWorkspaceInheritsProjectAccessForRunOperations(t *testing.T) {
	h, _, peer, _, _ := sharedWorkbenchFixture(t)
	project, _ := h.repo.Project("workbench")
	project.Members = []domain.ProjectMember{{UserID: peer.ID, Role: "viewer"}}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	run, _ := h.repo.RunState("shared-run")
	run.Status = "succeeded"
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	runs := &runsHandlers{deps: h.deps}
	for _, trustedRun := range []string{"", "another-run", run.ID} {
		c, rec := workbenchContext(peer, http.MethodPut, "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: run.ID}}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), mainBotWorkspaceKey{}, trustedRun))
		_, actor, allowed := runs.workspaceEditActor(c)
		want := trustedRun == run.ID
		if allowed != want || allowed && actor != peer.ID || !allowed && rec.Code != http.StatusForbidden {
			t.Errorf("run %q: allowed=%v actor=%d status=%d", trustedRun, allowed, actor, rec.Code)
		}
		if got := runs.workspaceWritable(c, run); got != want {
			t.Errorf("run %q: workspace writable=%v, want %v", trustedRun, got, want)
		}
		browser := &browserHandlers{deps: h.deps}
		if got := browser.canControl(c.Request, run, peer.ID); got != want {
			t.Errorf("run %q: browser controllable=%v, want %v", trustedRun, got, want)
		}
	}
}

func TestProjectParticipantOpensAndSwitchesSharedWorkspace(t *testing.T) {
	h, owner, session := chatSandboxFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "workspace-peer"}); err != nil {
		t.Fatal(err)
	}
	peer, _ := h.repo.UserByLoginName("workspace-peer")
	project, _ := h.repo.Project(session.ProjectID)
	project.Members = []domain.ProjectMember{{UserID: peer.ID, Role: "viewer"}}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	session.UserID, session.User = peer.ID, peer
	gateway := &agentToolGateway{deps: h.deps}
	first, err := gateway.bindConversationWorkspace(session, workspaceOpenChoice{Goal: "first shared workspace"})
	if err != nil || !first.Created || first.Run.ExecutionContext.ResponsibleUserID != peer.ID {
		t.Fatalf("participant cannot open workspace with its own execution identity: %+v %v", first, err)
	}
	second, err := gateway.bindConversationWorkspace(session, workspaceOpenChoice{Goal: "second shared workspace", New: true})
	if err != nil || !second.Created || second.Previous != first.Run.ID {
		t.Fatalf("participant cannot switch workspace: %+v %v", second, err)
	}
	stored, _ := h.repo.AssistantSession(owner.ID, session.ChatSessionID)
	if stored.WorkspaceRunID != second.Run.ID || len(stored.OwnWorkspaceRunIDs) != 2 || stored.UserID != owner.ID {
		t.Fatalf("shared workspace binding was not saved to original conversation: %+v", stored)
	}
	request := &assistantRequest{SessionID: stored.ID, ProjectID: stored.ProjectID}
	if note := h.workspaceTurnContext(request, peer); !strings.Contains(note, first.Run.ID) || !strings.Contains(note, second.Run.ID) {
		t.Fatalf("participant cannot see current and previous workspaces: %s", note)
	}

	issue := domain.Issue{ID: "shared-workspace-issue", ProjectID: project.ID, Title: "Shared issue"}
	if err := h.repo.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	request.Messages = []assistantMessage{{Role: "user", Content: "use this issue", References: []domain.AssistantReference{{Type: "issue", ID: issue.ID}}}}
	if note := h.linkMentionedIssue(request, peer); note == "" {
		t.Fatal("participant cannot link an issue to the shared conversation")
	}
	stored, _ = h.repo.AssistantSession(owner.ID, stored.ID)
	if stored.IssueID != issue.ID {
		t.Fatalf("issue link was not saved to original conversation: %+v", stored)
	}
	dispatch := domain.AssistantSession{ID: "shared-dispatch-chat", ProjectID: project.ID}
	if err := h.repo.PutAssistantSession(owner.ID, dispatch); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(withConversationSession(context.Background(), dispatch.ID), http.MethodPost, "/", nil)
	linkDispatchingConversation(h.deps, req, peer.ID, issue)
	stored, _ = h.repo.AssistantSession(owner.ID, dispatch.ID)
	if stored.IssueID != issue.ID {
		t.Fatal("participant dispatch did not link the shared conversation")
	}
}
