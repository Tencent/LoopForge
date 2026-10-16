package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"github.com/gin-gonic/gin"
)

func workbenchFixture(t *testing.T) (*assistantHandlers, domain.User) {
	t.Helper()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err = st.PutUser(domain.User{LoginName: "workbench-owner", CodeBuddyAPIKey: "test-only"}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName("workbench-owner")
	if err = st.PutProject(domain.Project{ID: "workbench", Name: "Workbench", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	return &assistantHandlers{deps: &deps{repo: st}}, u
}
func workbenchContext(u domain.User, method, path string, body *bytes.Buffer) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, path, body)
	c.Request = req.WithContext(context.WithValue(req.Context(), requestUserContextKey{}, domainUserToAuth(u)))
	return c, rec
}

func TestViewerCannotExecuteAgentsThroughRunConversation(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "read-only-viewer", CodeBuddyAPIKey: "test-only"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := h.repo.UserByLoginName("read-only-viewer")
	project, _ := h.repo.Project("workbench")
	project.Members = []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutRun(domain.Run{ID: "viewer-run", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: project.ID}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	c, _ := workbenchContext(viewer, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	if !h.workbenchProjectAllowed(c, project.ID, viewer.ID) {
		t.Fatal("viewer must retain read access")
	}
	request := assistantRequest{ProjectID: project.ID, RunID: "viewer-run", Messages: []assistantMessage{{Role: "user", Content: "modify the workspace"}}}
	if err := h.prepareWorkbenchContext(c, &request, viewer); err == nil {
		t.Fatal("viewer obtained an agent execution context")
	}
	c, _ = workbenchContext(owner, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &request, owner); err != nil {
		t.Fatal("owner lost execution access:", err)
	}
}

func TestWorkbenchArchiveValidatesStateAndOwner(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "archive-http"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body     string
		owner    int64
		status   int
		archived bool
	}{
		{`{}`, u.ID, 400, false},
		{`{"archived":true}`, u.ID + 100, 404, false},
		{`{"archived":true}`, u.ID, 200, true},
		{`{"archived":false}`, u.ID, 200, false},
	} {
		user := u
		user.ID = tc.owner
		c, rec := workbenchContext(user, "POST", "/", bytes.NewBufferString(tc.body))
		c.Params = gin.Params{{Key: "id", Value: "archive-http"}}
		h.archiveSession(c)
		if rec.Code != tc.status {
			t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
		}
		saved, _ := h.repo.AssistantSession(u.ID, "archive-http")
		if saved.Archived != tc.archived {
			t.Fatal("unexpected archive state")
		}
	}
}

func TestWorkbenchPlainAnswerDoesNotRetrySubmit(t *testing.T) {
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAssisted, AssistantApprovalAuto} {
		t.Run(mode, func(t *testing.T) {
			h, user := workbenchFixture(t)
			calls := 0
			h.generate = func(_ context.Context, prompt string, _ domain.User) (string, error) {
				calls++
				return "你好！", nil
			}
			body, _ := json.Marshal(map[string]any{"approvalMode": mode, "messages": []assistantMessage{{Role: "user", Content: "你好"}}})
			c, rec := workbenchContext(user, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
			h.chat(c)
			var reply assistantReply
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.Message != "你好！" || calls != 1 || len(reply.Drafts) != 0 {
				t.Fatalf("plain answer: status=%d calls=%d body=%s", rec.Code, calls, rec.Body)
			}
			prompt := assistantSystemPromptForMode(mode)
			if strings.Contains(prompt, "本轮结束必须调用 submit_assistant_reply") {
				t.Fatal("prompt forces conversational submit")
			}
		})
	}
}
func TestWorkbenchReferencesAreScopedAndConsultationCannotWrite(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "workbench", Instructions: "分析设计"})
	h.repo.PutProject(domain.Project{ID: "private", Name: "Private", OwnerID: 999})
	h.repo.PutIssue(domain.Issue{ID: "foreign", Title: "Private", ProjectID: "private"})
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: "workbench", ApprovalMode: "auto", Messages: []assistantMessage{{Role: "user", Content: "分析", References: []domain.AssistantReference{{Type: "agent", ID: "dev", Name: "伪造名称"}}}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if !req.consultation || !strings.Contains(req.contextText, "分析设计") || req.Messages[0].References[0].Name != "开发" {
		t.Fatalf("role context missing: %+v", req)
	}
	mode := workbenchExecMode("auto", req.consultation)
	if assistantPlatformToolAllowed(mode, "issues_save") || !assistantPlatformToolAllowed(mode, "issues_list") {
		t.Fatal("role must only have read access")
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", ExecMode: mode, Final: make(chan AgentTerminalResult, 1)}
	result, err := (&agentToolGateway{deps: h.deps}).callTerminal(s, submitAssistantReplyTool, json.RawMessage(`{"message":"改需求","drafts":[{"tool":"issues_save","body":{"id":"a","title":"需求","projectId":"workbench"}}]}`))
	if err != nil || result["isError"] != true || s.finished() {
		t.Fatalf("consultation accepted a draft: %v %v", result, err)
	}
	req.Messages[0].References = []domain.AssistantReference{{Type: "issue", ID: "foreign"}}
	if err := h.prepareWorkbenchContext(c, &req, u); err == nil {
		t.Fatal("cross-project reference allowed")
	}
	req.ProjectID = "private"
	if err := h.prepareWorkbenchContext(c, &req, u); err == nil {
		t.Fatal("non-member allowed")
	}
}
func TestWorkbenchAttachmentsReachModelAndRemainPrivate(t *testing.T) {
	h, u := workbenchFixture(t)
	var picture bytes.Buffer
	png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("projectId", "workbench")
	f, _ := w.CreateFormFile("file", "../../design.png")
	f.Write(picture.Bytes())
	w.Close()
	c, rec := workbenchContext(u, "POST", "/api/assistant/attachments", &body)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	h.uploadAttachment(c)
	if rec.Code != 201 {
		t.Fatalf("upload: %s", rec.Body.String())
	}
	var attachment domain.AssistantAttachment
	if err := json.Unmarshal(rec.Body.Bytes(), &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Name != "design.png" || attachment.MimeType != "image/png" {
		t.Fatalf("bad metadata: %+v", attachment)
	}
	session := &AgentToolSession{UserID: u.ID, ProjectID: "workbench", Mode: AgentToolAssistant, AttachmentIDs: map[string]bool{attachment.ID: true}}
	raw, _ := json.Marshal(map[string]string{"id": attachment.ID})
	result, err := readWorkbenchAttachmentTool(session, raw)
	if err != nil {
		t.Fatal(err)
	}
	content := result["content"].([]map[string]any)
	if len(content) != 2 || content[1]["type"] != "image" || content[1]["mimeType"] != "image/png" {
		t.Fatalf("image not passed to model: %v", content)
	}
	session.UserID = u.ID + 1
	if _, err = readWorkbenchAttachmentTool(session, raw); err == nil {
		t.Fatal("another user's attachment was readable")
	}
	session.UserID = u.ID
	session.AttachmentIDs = nil
	if _, err = readWorkbenchAttachmentTool(session, raw); err == nil {
		t.Fatal("unreferenced attachment allowed")
	}
	if _, _, err = loadWorkbenchAttachment(u.ID, "../../etc/passwd"); err == nil {
		t.Fatal("attachment traversal allowed")
	}
}
func TestWorkbenchRunContextIsNotChangedByMention(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutRun(domain.Run{ID: "one", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, Nodes: map[string]*domain.NodeRun{}})
	h.repo.PutRun(domain.Run{ID: "two", Status: "waiting", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"}, Nodes: map[string]*domain.NodeRun{}})
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: "workbench", RunID: "one", Messages: []assistantMessage{{Role: "user", Content: "对照", References: []domain.AssistantReference{{Type: "run", ID: "two"}}}}}
	if err := h.prepareWorkbenchContext(c, &req, u); err == nil {
		t.Fatal("unrelated task reference accepted")
	}
	req.Messages[0].References[0].ID = "one"
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if req.RunID != "one" || !strings.Contains(req.contextText, `"id":"one"`) {
		t.Fatal("mention silently changed task")
	}
	if err := saveAssistantConversation(h.repo, u.ID, assistantConversation{ID: "bound", ProjectID: "workbench", RunID: "one", Messages: req.Messages}); err != nil {
		t.Fatal(err)
	}
	saved, err := loadAssistantConversation(h.repo, u.ID, "bound")
	if err != nil || saved.RunID != "one" || len(saved.Messages[0].References) != 1 {
		t.Fatalf("context not persisted: %v %v", saved, err)
	}
}
