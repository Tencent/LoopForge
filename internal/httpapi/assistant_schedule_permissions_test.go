package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func TestScheduledAssistantCanDispatchSOP(t *testing.T) {
	h, user := workbenchFixture(t)
	st := h.repo
	wf := domain.WorkflowDefinition{ID: "scheduled-approval", ProjectID: "workbench", Name: "Scheduled approval", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "scheduled-dispatch", ProjectID: "workbench", Title: "Scheduled dispatch", Status: "todo", WorkflowID: wf.ID}); err != nil {
		t.Fatal(err)
	}
	en := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h.api = New(st, en, tracker.NewService(), "", "")
	var runID string
	t.Cleanup(func() {
		if runID != "" {
			_ = en.Cancel(runID)
		}
	})
	h.generate = func(ctx context.Context, _ string, actor domain.User) (string, error) {
		text, err := h.callAssistantPlatform(ctx, actor, "workbench", assistantMCPCall{Tool: "issues_dispatch", Path: map[string]any{"id": "scheduled-dispatch"}})
		if err != nil {
			return "", err
		}
		var task domain.Task
		if err := json.Unmarshal([]byte(text), &task); err != nil {
			return "", err
		}
		runID = task.RunID
		return "已启动 SOP", nil
	}
	if _, err := h.prepareScheduleJob(domain.ScheduleJob{ID: "ai-dispatch", ProjectID: "workbench", OwnerUserID: user.ID, Name: "Dispatch", AssistantPrompt: "启动需求的 SOP", SkipDispatch: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if run, ok := st.RunState(runID); ok && run.WorkflowID == wf.ID && run.Status == "waiting" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("AI schedule did not reach the SOP approval gate: id=%q", runID)
}

func TestScheduledAssistantNativeGatewayKeepsWorkspaceAndTerminalReply(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the fake CLI")
	}
	h, user := workbenchFixture(t)
	h.engine = engine.New(h.repo, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h.api = New(h.repo, h.engine, tracker.NewService(), "", "")
	if err := h.repo.PutIssue(domain.Issue{ID: "native-scheduled-issue", ProjectID: "workbench", Title: "Original", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	gateway := &agentToolGateway{deps: h.deps}
	sessionIDs := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.Split(strings.TrimPrefix(r.URL.Path, "/internal/agent-tools/"), "/")[0]
		session, ok := agentToolSessions.get(token)
		if !ok {
			http.Error(w, "missing session", 500)
			return
		}
		sessionID := session.ChatSessionID
		sessionIDs <- sessionID
		conv, exists := h.repo.AssistantSession(user.ID, sessionID)
		if !exists || conv.Source != domain.AssistantSourceScheduled || session.workspaceTurn == nil || session.ExecMode != assistantExecModeForApproval(AssistantApprovalAuto) {
			t.Error("scheduled gateway lacks its persisted conversation or full-access workspace lifecycle")
		}
		// A binding written during execution must survive the final transcript save.
		if err := h.repo.SetAssistantSessionWorkspaceRun(user.ID, sessionID, "scheduled-workspace", true); err != nil {
			t.Error(err)
		}
		if err := session.attachReplyFile("report.md", "text/markdown", []byte("# Scheduled report")); err != nil {
			t.Error(err)
		}
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		c.Params = gin.Params{{Key: "token", Value: token}}
		gateway.ServeHTTP(c)
	}))
	defer server.Close()
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	defer config.SetCurrent(previous)
	bin := filepath.Join(t.TempDir(), "codebuddy")
	// Exercise the real CLI-to-gateway protocol using only a loopback server.
	script := "#!" + python + `
import json, sys, urllib.request
args = sys.argv[1:]
config = json.load(open(args[args.index('--mcp-config') + 1]))
url = config['mcpServers']['agentflow_session']['url']
payload = {'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'submit_assistant_reply','arguments':{'message':'已完成定时巡检并生成报告。','drafts':[{'tool':'issues_save','body':{'id':'native-scheduled-issue','projectId':'workbench','title':'Scheduled update'}}]}}}
req = urllib.request.Request(url, json.dumps(payload).encode(), {'Content-Type':'application/json'})
try:
    urllib.request.urlopen(req, timeout=5).read()
except Exception:
    pass
# Terminal submission cancels this process; it must still count as success.
raise SystemExit(1)
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	task := assistantTask{ID: "native-scheduled", Name: "Native schedule", ProjectID: "workbench", Prompt: "巡检并生成报告"}
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatal(err)
	}
	if task.LastStatus != "ok" || !strings.Contains(task.LastMessage, "已完成定时巡检") {
		t.Fatalf("terminal reply was lost: %+v", task)
	}
	sessionID := <-sessionIDs
	issue, _ := h.repo.Issue("native-scheduled-issue")
	if issue.Title != "Scheduled update" {
		t.Fatalf("terminal draft was not executed with full access: %+v", issue)
	}
	conv, ok := h.repo.AssistantSession(user.ID, task.SessionID)
	if !ok || task.SessionID != sessionID || conv.WorkspaceRunID != "scheduled-workspace" || !slices.Contains(conv.OwnWorkspaceRunIDs, "scheduled-workspace") {
		t.Fatalf("workspace binding was lost: %+v", conv)
	}
	// Reply files are attachments on the stored reply, as for a chat turn.
	if files := conv.Messages[len(conv.Messages)-1].Attachments; len(files) != 1 || files[0].Name != "report.md" {
		t.Fatalf("scheduled report attachment was lost: %+v", files)
	}
}
