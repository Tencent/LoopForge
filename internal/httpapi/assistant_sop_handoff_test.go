package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func submitHandoffReport(t *testing.T, h *assistantHandlers, body string) {
	t.Helper()
	session := activeReportSession(t)
	gateway := &agentToolGateway{deps: h.deps}
	tools := gateway.toolList(context.Background(), session)
	if len(tools) != 1 || tools[0]["name"] != submitNotificationReportTool {
		t.Fatal("handoff has executable tools", tools)
	}
	raw, _ := json.Marshal(map[string]any{"name": submitNotificationReportTool, "arguments": map[string]any{"body": body}})
	if _, err := gateway.callTool(context.Background(), session, raw); err != nil {
		t.Fatal(err)
	}
}

func TestGroupSOPHandoffEndpointRunsMainBotBeforeChangingMode(t *testing.T) {
	h, assistant, user, run, _ := groupConversationFixture(t)
	h.assistant = assistant
	run.Status = "waiting"
	run.PauseRequested = true
	run.Nodes["dev-task"].Status = "succeeded"
	run.ExecutionContext.ResponsibleUserID = user.ID + 500 // must not borrow original actor's key
	run.WorkflowSnapshot.SOPCheckpoint = &domain.SOPCheckpoint{Workflow: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "build", Type: "agent"}}}, Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "succeeded", Attempt: 2}}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.engine.RunWorkspace(run.ID), 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	assistant.generate = func(ctx context.Context, prompt string, actor domain.User) (string, error) {
		called = true
		if actor.ID != user.ID {
			t.Fatal("borrowed another user's credentials")
		}
		current, _ := h.repo.RunState(run.ID)
		if current.WorkflowSnapshot.CollaborationMode != "group" {
			t.Fatal("switched before generating")
		}
		for _, want := range []string{"用户说只修界面", "角色已经读到文件", "成员建议", "验证与证据", "previousHandoff", "新的明确决定覆盖旧决定"} {
			if !strings.Contains(prompt, want) {
				t.Fatal("missing handoff source/instruction", want)
			}
		}
		submitHandoffReport(t, assistant, "## 最新要求\n只修界面\n## 验证\n尚未执行 test-only")
		return "", nil
	}
	c, rec := workbenchContext(user, "POST", "/api/runs/"+run.ID+"/group/sop", bytes.NewBufferString(`{"nodeId":"build"}`))
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	h.groupControl(c)
	if rec.Code != 202 || called {
		t.Fatal("request must only enqueue", rec.Code, rec.Body.String())
	}
	executeTestSOPHandoffJob(t, h, run.ID)
	if !called {
		t.Fatal(rec.Code, rec.Body.String())
	}
	got, _ := h.repo.RunState(run.ID)
	if got.WorkflowSnapshot.GroupSOPHandoff == nil || got.WorkflowSnapshot.CollaborationMode != "" || len(got.Artifacts) != 2 || got.PauseRequested || got.Status != "running" {
		t.Fatal("missing persisted handoff")
	}
	if strings.Contains(got.Artifacts[0].Content, "test-only") {
		t.Fatal("model echoed credential into handoff")
	}
}

func TestMainBotHandoffSummarizesEveryChunkWithPriorDecisions(t *testing.T) {
	h, user := workbenchFixture(t)
	calls := 0
	h.generate = func(ctx context.Context, prompt string, actor domain.User) (string, error) {
		calls++
		if !strings.Contains(prompt, `"resumeNode":"build"`) || !strings.Contains(prompt, "latest goal") {
			t.Fatal("chunk lost task/target")
		}
		if calls > 1 && !strings.Contains(prompt, fmt.Sprintf("summary-round-%d", calls-1)) {
			t.Fatal("dropped earlier summary")
		}
		submitHandoffReport(t, h, fmt.Sprintf("summary-round-%d", calls))
		return "", nil
	}
	transcript, _ := json.Marshal(map[string]any{"goal": "latest goal", "resumeNode": "build", "conversation": strings.Repeat("chat ", 14000)})
	got, err := h.generateGroupSOPHandoff(context.Background(), domain.Run{ID: "long-history", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: user.ID}}, domain.AssistantSession{}, string(transcript))
	if err != nil || calls < 2 || got != fmt.Sprintf("summary-round-%d", calls) {
		t.Fatal(calls, got, err)
	}
}

func TestMainBotHandoffMissingCredentialsKeepsGroup(t *testing.T) {
	h, assistant, user, run, _ := groupConversationFixture(t)
	h.assistant = assistant
	run.Status = "waiting"
	run.PauseRequested = true
	run.Nodes["dev-task"].Status = "succeeded"
	run.WorkflowSnapshot.SOPCheckpoint = &domain.SOPCheckpoint{Workflow: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "build"}}}, Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "pending"}}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.engine.RunWorkspace(run.ID), 0700); err != nil {
		t.Fatal(err)
	}
	user.CodeBuddyAPIKey = ""
	if err := h.repo.PutUser(user); err != nil {
		t.Fatal(err)
	}
	c, rec := workbenchContext(user, "POST", "/api/runs/"+run.ID+"/group/sop", bytes.NewBufferString(`{"nodeId":"build"}`))
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	h.groupControl(c)
	if rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	executeTestSOPHandoffJob(t, h, run.ID)
	job, _, _ := h.repo.SOPHandoffJob(run.ID)
	if job.Status != domain.SOPHandoffJobFailed {
		t.Fatal("expected persisted credential failure", job)
	}
	got, _ := h.repo.RunState(run.ID)
	if got.WorkflowSnapshot.CollaborationMode != "group" || len(got.Artifacts) != 0 {
		t.Fatal("lost group state")
	}
	// Explicit completion remains available without a model or workspace.
	got.WorkspaceCleanedAt = time.Now().UTC()
	if err := h.repo.PutRun(got); err != nil {
		t.Fatal(err)
	}
	c, rec = workbenchContext(user, "POST", "/api/runs/"+run.ID+"/group/sop", bytes.NewBufferString(`{"complete":true}`))
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	h.groupControl(c)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func executeTestSOPHandoffJob(t *testing.T, h *runsHandlers, runID string) domain.SOPHandoffJob {
	t.Helper()
	job, ok, err := h.repo.SOPHandoffJob(runID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	claimed, ok, err := h.repo.ClaimSOPHandoffJob(runID, job.ID, newAssistantSessionID(), time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatal("cannot claim handoff", err)
	}
	h.executeSOPHandoffJob(context.Background(), claimed)
	job, _, err = h.repo.SOPHandoffJob(runID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
