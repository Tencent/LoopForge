package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sharedWorkbenchFixture(t *testing.T) (*assistantHandlers, domain.User, domain.User, domain.User, domain.AssistantSession) {
	t.Helper()
	h, a := workbenchFixture(t)
	for _, name := range []string{"peer", "outsider"} {
		if err := h.repo.PutUser(domain.User{LoginName: name, CodeBuddyAPIKey: name + "-test-key"}); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := h.repo.UserByLoginName("peer")
	out, _ := h.repo.UserByLoginName("outsider")
	p, _ := h.repo.Project("workbench")
	p.Members = append(p.Members, domain.ProjectMember{UserID: b.ID, Role: "member"})
	if err := h.repo.PutProject(p); err != nil {
		t.Fatal(err)
	}
	r := domain.Run{ID: "shared-run", WorkflowID: "wf", WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "开发流程", Nodes: []domain.WorkflowNode{{ID: "develop", Type: "agent", AgentID: "dev"}}}, ExecutionContext: domain.TelemetryContext{ProjectID: p.ID}, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	if err := h.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(a.ID, domain.AssistantSession{ID: "legacy-discussion", RunID: r.ID, ProjectID: p.ID, Messages: []domain.AssistantMessage{{Role: "user", Content: "历史咨询"}}}); err != nil {
		t.Fatal(err)
	}
	c, w := workbenchContext(a, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: r.ID}}
	h.runConversation(c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session domain.AssistantSession
	json.Unmarshal(w.Body.Bytes(), &session)
	return h, a, b, out, session
}
func TestSharedRunConversationVisibilityAndAttribution(t *testing.T) {
	h, a, b, out, s := sharedWorkbenchFixture(t)
	if !s.Shared || s.ID != repo.RunAssistantSessionID("shared-run") || len(s.Messages) != 1 || s.Messages[0].AuthorID != a.ID {
		t.Fatal("shared history/author missing")
	}
	for _, tc := range []struct {
		u      domain.User
		status int
	}{{b, 200}, {out, 403}} {
		c, w := workbenchContext(tc.u, "GET", "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: s.ID}}
		h.session(c)
		if w.Code != tc.status {
			t.Fatalf("user %d status %d", tc.u.ID, w.Code)
		}
	}
	c, w := workbenchContext(b, "GET", "/", bytes.NewBuffer(nil))
	h.listSessions(c)
	var list []domain.AssistantSession
	json.Unmarshal(w.Body.Bytes(), &list)
	found := false
	for _, item := range list {
		if item.ID == s.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("peer cannot discover shared discussion")
	}
	actor := int64(0)
	h.generate = func(_ context.Context, prompt string, u domain.User) (string, error) {
		actor = u.ID
		if u.CodeBuddyAPIKey != b.CodeBuddyAPIKey {
			t.Error("creator credentials reused")
		}
		return "角色回复", nil
	}
	h.repo.PutAgent(domain.AgentDefinition{ID: "dev", ProjectID: "workbench", Name: "实现工程师", Instructions: "分析开发方案"})
	messages := append(s.Messages, assistantMessage{Role: "user", Content: "你做了什么", AuthorID: a.ID, AuthorName: "forged", References: []domain.AssistantReference{{Type: "agent", ID: "dev"}}})
	body, _ := json.Marshal(assistantRequest{ProjectID: "workbench", RunID: "shared-run", SessionID: s.ID, Messages: messages})
	c, w = workbenchContext(b, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, _ := h.repo.AssistantSession(a.ID, s.ID)
	last := saved.Messages[len(saved.Messages)-2]
	reply := saved.Messages[len(saved.Messages)-1]
	if actor != b.ID || last.AuthorID != b.ID || last.AuthorName != b.LoginName || reply.SpeakerName != "实现工程师" {
		t.Fatalf("wrong actor/reply: %+v %+v", last, reply)
	}
	for _, method := range []string{"DELETE", "PUT"} {
		payload := bytes.NewBufferString(`{"messages":[]}`)
		c, w = workbenchContext(b, method, "/", payload)
		c.Params = gin.Params{{Key: "id", Value: s.ID}}
		h.session(c)
		if w.Code < 400 {
			t.Fatal("shared transcript mutation allowed")
		}
	}
	c, w = workbenchContext(b, "POST", "/", bytes.NewBufferString(`{"archived":true}`))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.archiveSession(c)
	if w.Code != 403 {
		t.Fatal("shared archive allowed")
	}
}
func TestSharedGenerationUsesOneSlotAndOnlyActorCanCancel(t *testing.T) {
	h, a, b, out, s := sharedWorkbenchFixture(t)
	g := domain.AssistantGeneration{ID: "job", SessionID: s.ID, UserID: a.ID, ActorID: b.ID, ProjectID: s.ProjectID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "second", SessionID: s.ID, UserID: a.ID, ActorID: a.ID}); err == nil {
		t.Fatal("two shared generations allowed")
	}
	for _, tc := range []struct {
		u      domain.User
		method string
		code   int
	}{{a, "GET", 200}, {out, "GET", 403}, {a, "POST", 403}, {b, "POST", 200}} {
		c, w := workbenchContext(tc.u, tc.method, "/?generationId=job", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: s.ID}}
		h.generation(c)
		if w.Code != tc.code {
			t.Fatalf("%s user %d: %d %s", tc.method, tc.u.ID, w.Code, w.Body.String())
		}
	}
	if _, shared, err := h.conversationScope(func() *gin.Context { c, _ := workbenchContext(b, "GET", "/", bytes.NewBuffer(nil)); return c }(), "private", "shared-run", s.ProjectID, b.ID); err == nil || shared {
		t.Fatal("private SOP slot accepted")
	}
}
func TestRoleConsultationHasIndependentPromptAndReadOnlyTools(t *testing.T) {
	req := assistantRequest{consultationRole: &domain.AgentDefinition{ID: "dev", Name: "实现工程师", Instructions: "分析代码设计"}}
	prompt := assistantSystemPromptForRequest(req, AssistantApprovalAuto)
	if !bytes.Contains([]byte(prompt), []byte("分析代码设计")) || bytes.Contains([]byte(prompt), []byte("你是 AgentFlow")) || !bytes.Contains([]byte(prompt), []byte("禁止写入")) {
		t.Fatal("role invocation does not have independent instructions")
	}
}

func TestSharedRoleConsultationRunsFreshCLI(t *testing.T) {
	h, a, b, _, session := sharedWorkbenchFixture(t)
	role := domain.AgentDefinition{ID: "real-dev", ProjectID: "workbench", Name: "实现工程师", Instructions: "先分析开发方案，再给出测试依据"}
	if err := h.repo.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	r, _ := h.repo.RunState("shared-run")
	r.WorkflowSnapshot.Nodes[0].AgentID = role.ID
	if err := h.repo.PutRun(r); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codebuddy")
	argsFile := filepath.Join(root, "args")
	script := `#!/bin/sh
for arg in "$@"; do printf '%s\n' "$arg"; done > '` + argsFile + `'
printf '%s\n' '{"type":"result","result":"工程师咨询回复"}'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	messages := append(session.Messages, assistantMessage{Role: "user", Content: "你如何实现", References: []domain.AssistantReference{{Type: "agent", ID: role.ID}}})
	body, _ := json.Marshal(assistantRequest{ProjectID: "workbench", RunID: "shared-run", SessionID: session.ID, Messages: messages})
	c, w := workbenchContext(b, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(args), "\n")
	values := map[string]string{}
	for i, v := range lines {
		if strings.HasPrefix(v, "--") && i+1 < len(lines) {
			values[v] = lines[i+1]
		}
	}
	if !strings.Contains(values["--system-prompt"], "你是 实现工程师") || !strings.Contains(string(args), role.Instructions) || strings.Contains(string(args), "\n--resume\n") {
		t.Fatal("consultation reused PM invocation")
	}
	if values["--session-id"] == session.ID {
		t.Fatal("consultation shares writer CLI session")
	}
	if !strings.Contains(string(args), "Bash,Write,Edit") {
		t.Fatal("builtin writers not fenced")
	}
	saved, _ := h.repo.AssistantSession(a.ID, session.ID)
	if saved.Messages[len(saved.Messages)-1].SpeakerAgentID != role.ID {
		t.Fatal("role response not attributed")
	}
}

func TestSharedAttachmentAccessRequiresRecordedReference(t *testing.T) {
	h, a, b, out, s := sharedWorkbenchFixture(t)
	id := strings.Repeat("a", 32)
	other := strings.Repeat("b", 32)
	dir := workbenchAttachmentDir(a.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{id, other} {
		record := workbenchAttachmentRecord{ProjectID: s.ProjectID, AssistantAttachment: domain.AssistantAttachment{ID: key, Name: "note.txt", MimeType: "text/plain", Size: 5}}
		raw, _ := json.Marshal(record)
		if err := os.WriteFile(filepath.Join(dir, key+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, key+".bin"), []byte("hello"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.Messages[0].Attachments = []domain.AssistantAttachment{{ID: id, Name: "note.txt", MimeType: "text/plain", Size: 5}}
	if err := h.repo.PutAssistantSession(a.ID, s); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		u    domain.User
		id   string
		code int
	}{{b, id, 200}, {b, other, 404}, {out, id, 403}} {
		c, w := workbenchContext(tc.u, "GET", "/?sessionId="+s.ID, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: tc.id}}
		h.downloadAttachment(c)
		if w.Code != tc.code {
			t.Fatalf("attachment user %d status %d", tc.u.ID, w.Code)
		}
	}
	saved, _ := h.repo.AssistantSession(a.ID, s.ID)
	req := assistantRequest{ProjectID: s.ProjectID, SessionID: s.ID, RunID: s.RunID, Messages: append(saved.Messages, assistantMessage{Role: "user", Content: "看看附件"})}
	c, _ := workbenchContext(b, "POST", "/", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &req, b); err != nil {
		t.Fatal(err)
	}
	session := &AgentToolSession{UserID: b.ID, ProjectID: s.ProjectID, AttachmentIDs: req.attachmentIDs, AttachmentOwners: req.attachmentOwners}
	result, err := readWorkbenchAttachmentTool(session, json.RawMessage(`{"id":"`+id+`"}`))
	if err != nil || result["isError"] != false {
		t.Fatal("peer cannot read referenced attachment", err)
	}
}

func TestTaskReferenceSearchAndSubmissionScope(t *testing.T) {
	h, a, _, _, s := sharedWorkbenchFixture(t)
	h.repo.PutAgent(domain.AgentDefinition{ID: "dev", ProjectID: s.ProjectID, Name: "当前开发"})
	h.repo.PutAgent(domain.AgentDefinition{ID: "other", ProjectID: s.ProjectID, Name: "其他角色"})
	c, w := workbenchContext(a, "GET", "/api/assistant/references?projectId=workbench&scope=all&runId=shared-run&type=agent", bytes.NewBuffer(nil))
	h.workbenchReferences(c)
	var rows struct {
		Items []domain.AssistantReference `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows.Items) != 1 || rows.Items[0].ID != "dev" {
		t.Fatalf("task role search: %d %s", w.Code, w.Body.String())
	}
	c, _ = workbenchContext(a, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: s.ProjectID, RunID: s.RunID, SessionID: s.ID, Messages: append(s.Messages, assistantMessage{Role: "user", Content: "咨询", References: []domain.AssistantReference{{Type: "agent", ID: "other"}}})}
	if err := h.prepareWorkbenchContext(c, &req, a); err == nil {
		t.Fatal("unrelated role bypassed task scope")
	}
	req.RunID = ""
	req.SessionID = ""
	if err := h.prepareWorkbenchContext(c, &req, a); err != nil {
		t.Fatal("ordinary conversation lost unrestricted reference search", err)
	}
}
