package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

func TestMainBotBrowserToolsFollowWorkspaceAccess(t *testing.T) {
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p"}
	if len(mainBotBrowserTools(s)) != 0 {
		t.Fatal("browser tools without a task run")
	}
	s.RunID = "run-1"
	tools := mainBotBrowserTools(s)
	if len(tools) != 7 || tools[0].Name != "workbench_browser_navigate" || tools[0].Kind != "browser" {
		t.Fatalf("tools=%+v", tools)
	}
}

func TestMainBotBrowserRequestMapping(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want domain.BrowserRequest
	}{
		{"navigate", map[string]any{"url": "baidu.com"}, domain.BrowserRequest{Operation: "navigate", URL: "baidu.com", WithSnapshot: true, Ensure: true}},
		{"navigate", map[string]any{"action": "back"}, domain.BrowserRequest{Operation: "back", WithSnapshot: true, Ensure: true}},
		{"type", map[string]any{"ref": "e3", "text": "hi", "submit": true}, domain.BrowserRequest{Operation: "type", Ref: "e3", Text: "hi", Submit: true, Ensure: true}},
		{"tabs", map[string]any{"action": "switch", "tabId": "T1"}, domain.BrowserRequest{Operation: "activate", TargetID: "T1", WithSnapshot: true, Ensure: true}},
		{"tabs", map[string]any{"action": "list"}, domain.BrowserRequest{Operation: "status", Ensure: true}},
	}
	for _, tc := range cases {
		got, err := mainBotBrowserRequest(mainBotBrowserPrefix+tc.name, tc.args)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("%s %v: got %+v err=%v", tc.name, tc.args, got, err)
		}
	}
	for _, bad := range []struct {
		name string
		args map[string]any
	}{{"navigate", map[string]any{}}, {"navigate", map[string]any{"action": "rm"}}, {"type", map[string]any{"ref": "e1"}}, {"tabs", map[string]any{"action": "switch"}}} {
		if _, err := mainBotBrowserRequest(mainBotBrowserPrefix+bad.name, bad.args); err == nil {
			t.Fatalf("%s %v accepted", bad.name, bad.args)
		}
	}
}

func TestFormatBrowserStateListsRefsBeforeText(t *testing.T) {
	text := formatBrowserState(domain.BrowserState{URL: "https://a.test", Title: "A", Tabs: []domain.BrowserTab{{ID: "T1", Title: "A", Active: true}, {ID: "T2", Title: "B"}},
		Snapshot: &domain.BrowserSnapshot{Text: "正文", Truncated: true, Elements: []domain.BrowserElement{{Ref: "e1", Role: "text", Name: "搜索", Value: "x"}, {Ref: "e2", Role: "checkbox", Name: "记住", Checked: true}}}}, "")
	for _, want := range []string{"URL: https://a.test", "* T1", `[e1] text "搜索" value="x"`, `[e2] checkbox "记住" checked`, "正文", "已截断"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Index(text, "[e1]") > strings.Index(text, "正文") {
		t.Fatal("elements must precede page text")
	}
}

// TestMainBotBrowserStartsAndDrivesRunBrowser goes through the gateway, the
// browser API and the engine, starting the run browser on first use.
func TestMainBotBrowserStartsAndDrivesRunBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := runtime.FindChromium(); err != nil {
		t.Skip("chromium not installed")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<title>测试页</title><h1>Main Bot 看到了</h1><button onclick="document.body.append('已点击')">点我</button>`)
	}))
	defer page.Close()
	a, owner, peer, _, _ := sharedWorkbenchFixture(t)
	project, _ := a.repo.Project("workbench")
	project.Members = []domain.ProjectMember{{UserID: peer.ID, Role: "viewer"}}
	if err := a.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	en := engine.New(a.repo, runtime.NewManager(root))
	a.deps.engine = en
	h := &agentToolGateway{deps: a.deps}
	h.api = New(a.repo, en, tracker.NewService(), "", "")
	r, _ := a.repo.RunState("shared-run")
	r.ID, r.Status, r.Nodes, r.WorkspaceCleanedAt = "browser-run-"+t.Name(), "succeeded", map[string]*domain.NodeRun{}, time.Time{}
	if err := a.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(en.RunWorkspace(r.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := en.DeleteRun(r.ID); err != nil {
			t.Error(err)
		}
	}()
	s := &AgentToolSession{Mode: AgentToolAssistant, RunID: r.ID, ProjectID: r.ExecutionContext.ProjectID, UserID: owner.ID, User: owner}
	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		result, err := h.callMainBotBrowser(context.Background(), s, mainBotBrowserPrefix+name, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		content := result["content"].([]map[string]any)
		return content[0]["text"].(string), result["isError"] == true
	}
	text, failed := call("navigate", map[string]any{"url": page.URL})
	if failed || !strings.Contains(text, "Main Bot 看到了") || !strings.Contains(text, `button "点我"`) {
		t.Fatalf("navigate failed=%v:\n%q", failed, text)
	}
	ref := text[strings.Index(text, "[e")+1 : strings.Index(text, `] button "点我"`)]
	if text, failed = call("click", map[string]any{"ref": ref}); failed || !strings.Contains(text, "已点击") {
		t.Fatalf("click failed=%v:\n%s", failed, text)
	}
	shot, err := h.callMainBotBrowser(context.Background(), s, mainBotBrowserPrefix+"screenshot", nil)
	if err != nil || len(shot["content"].([]map[string]any)) != 2 || shot["content"].([]map[string]any)[1]["type"] != "image" {
		t.Fatalf("screenshot=%v err=%v", shot, err)
	}
	if replyFiles := s.takeReplyFiles(); len(replyFiles) != 1 || !replyFiles[0].Screenshot || !strings.HasPrefix(replyFiles[0].MimeType, "image/") || len(replyFiles[0].Data) == 0 {
		t.Fatalf("screenshot was not kept for the reply: %+v", replyFiles)
	}
	s.User, s.UserID = peer, peer.ID
	if text, failed = call("navigate", map[string]any{"url": page.URL}); failed || !strings.Contains(text, "Main Bot 看到了") {
		t.Fatalf("project participant could not drive the shared browser:\n%s", text)
	}
}

// What Main Bot produced for the user during a turn — screenshots and attached
// workspace files — ends up on the stored reply and downloads like any
// other attachment of the conversation.
func TestAssistantReplyCarriesTurnScreenshotsAndFiles(t *testing.T) {
	h, u := workbenchFixture(t)
	const sessionID = "reply-files"
	shot := cvAPNG(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		agentToolSessions.mu.Lock()
		var turn *AgentToolSession
		for _, s := range agentToolSessions.sessions {
			if s.ChatSessionID == sessionID {
				turn = s
			}
		}
		agentToolSessions.mu.Unlock()
		if turn == nil {
			return "", fmt.Errorf("tool session not registered")
		}
		turn.addReplyScreenshot("image/png", shot)
		if err := turn.attachReplyFile("sidebar-demo.html", "text/html", []byte("<h1>Demo</h1>")); err != nil {
			return "", err
		}
		return "两张图在下面", nil
	}
	c, w := workbenchContext(u, "POST", "/api/assistant/chat", bytes.NewBufferString(`{"sessionId":"`+sessionID+`","projectId":"workbench","messages":[{"role":"user","content":"做个侧栏 demo"}]}`))
	h.chat(c)
	if w.Code != http.StatusOK {
		t.Fatalf("chat %d: %s", w.Code, w.Body.String())
	}
	saved, ok := h.repo.AssistantSession(u.ID, sessionID)
	if !ok || len(saved.Messages) != 2 {
		t.Fatalf("saved=%+v", saved)
	}
	files := saved.Messages[1].Attachments
	if len(files) != 2 || files[0].MimeType != "image/png" || files[1].Name != "sidebar-demo.html" || files[1].MimeType != "text/html" {
		t.Fatalf("reply attachments=%+v", files)
	}
	for _, f := range files {
		c, w := workbenchContext(u, "GET", "/api/assistant/attachments/"+f.ID+"?sessionId="+sessionID, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: f.ID}}
		h.downloadAttachment(c)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != f.MimeType || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
			t.Fatalf("download %s: %d %v", f.Name, w.Code, w.Header())
		}
	}
}
