package httpapi

import (
	"archive/zip"
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
	"agentflow/internal/knowledge"
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
	// Title summaries run beside a new chat and would otherwise call the same
	// CodeBuddy binary, stealing scripted replies and inflight slots.
	h := &assistantHandlers{deps: &deps{repo: st}, titleSummary: func(context.Context, domain.User, string) (string, error) {
		return "", context.Canceled
	}}
	return h, u
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
func TestWorkbenchReferencesAreScopedAndRolesBecomeSubagents(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutAgent(domain.AgentDefinition{ID: "dev", Name: "开发", ProjectID: "workbench", Prompt: "分析设计"})
	h.repo.PutAgent(domain.AgentDefinition{ID: "qa", Name: "测试", ProjectID: "workbench", Prompt: "补测试"})
	h.repo.PutProject(domain.Project{ID: "private", Name: "Private", OwnerID: 999})
	h.repo.PutIssue(domain.Issue{ID: "foreign", Title: "Private", ProjectID: "private"})
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{ProjectID: "workbench", ApprovalMode: "auto", Messages: []assistantMessage{
		{Role: "user", Content: "先测", References: []domain.AssistantReference{{Type: "agent", ID: "qa"}}},
		{Role: "assistant", Content: "好"},
		{Role: "user", Content: "分析", References: []domain.AssistantReference{{Type: "agent", ID: "dev", Name: "伪造名称"}, {Type: "agent", ID: "qa"}}},
	}}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if len(req.roles) != 2 || !strings.Contains(req.contextText, "subagent_type=role-dev") || !strings.Contains(req.contextText, "subagent_type=role-qa") || req.Messages[2].References[0].Name != "开发" {
		t.Fatalf("role subagents missing: %+v", req.roles)
	}
	req.Messages = req.Messages[2:]
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
	if _, _, err := loadWorkbenchAttachment(u.ID+1, attachment.ID); err == nil {
		t.Fatal("another user's attachment was readable")
	}
	if _, _, err := loadWorkbenchAttachment(u.ID, "../../etc/passwd"); err == nil {
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

func TestPPTXBecomesInChatSlides(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		t.Helper()
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write("ppt/slides/slide2.xml", `<p:sld><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>第二页</a:t></a:r></a:p><a:p><a:r><a:t>要点</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`)
	write("ppt/slides/slide1.xml", `<p:sld><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>封面</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	slides := pptxSlides(buf.Bytes())
	if slides != "封面\n---\n第二页\n要点" {
		t.Fatalf("slides = %q", slides)
	}
	got := previewSlidesMessage("PPT 已上传到你的资料库：[点击打开](https://www.workbuddy.cn/space/d/abc)", slides)
	if strings.Contains(got, "workbuddy") || !strings.Contains(got, "```slides") || !strings.Contains(got, "封面") {
		t.Fatalf("preview = %q", got)
	}
}

func TestStripLocalMediaPathsKeepsTheSentence(t *testing.T) {
	in := "图片已经好了。\n**文件位置**：`/root/.agentflow/assistant/9101/ws/generated-images/A_modern_personal_website_home_2026-10-26T06-54-47.png`\n需要我改配色吗？"
	got := stripLocalMediaPaths(in)
	if strings.Contains(got, "/root/") || strings.Contains(got, "文件位置") || !strings.Contains(got, "需要我改配色吗") {
		t.Fatalf("path still visible: %q", got)
	}
	if strings.Contains(stripLocalMediaPaths("看这张 https://cdn.example.com/a.png"), "https") == false {
		t.Fatal("remote image url was removed")
	}
}

func TestWorkbenchMentionedSOPInjectsDispatchGuidanceOnlyOutsideRun(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID: "wf-kb-audit", Name: "知识库验收 SOP", ProjectID: "workbench", SandboxProfileID: repo.DefaultSandboxProfileID,
	}); err != nil {
		t.Fatal(err)
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{
		ProjectID: "workbench",
		Messages: []assistantMessage{{
			Role:       "user",
			Content:    "按 @知识库验收 SOP 执行一次验收",
			References: []domain.AssistantReference{{Type: "sop", ID: "wf-kb-audit"}},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"用户本轮 @ 了 SOP", "wf-kb-audit", "runs_start", "严禁在当前对话内直接代跑 SOP"} {
		if !strings.Contains(req.contextText, want) {
			t.Fatalf("expected contextText to contain %q, got:\n%s", want, req.contextText)
		}
	}

	if err := h.repo.PutRun(domain.Run{
		ID: "run-active", WorkflowID: "wf-kb-audit", Status: "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench"},
		Nodes:            map[string]*domain.NodeRun{},
	}); err != nil {
		t.Fatal(err)
	}
	runReq := assistantRequest{
		ProjectID: "workbench",
		RunID:     "run-active",
		Messages: []assistantMessage{{
			Role:       "user",
			Content:    "解释下 @知识库验收 SOP",
			References: []domain.AssistantReference{{Type: "sop", ID: "wf-kb-audit"}},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &runReq, u); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runReq.contextText, "严禁在当前对话内直接代跑 SOP") {
		t.Fatalf("active run conversation should not inject new-SOP dispatch guidance:\n%s", runReq.contextText)
	}
}

func TestAssistantCatalogIncludesKnowledgeConnectorsList(t *testing.T) {
	found := false
	for _, name := range agentflowAssistantReadToolNames() {
		if name == "knowledge_connectors_list" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected knowledge_connectors_list in assistant read-only tool catalog")
	}
	if !assistantPlatformToolAllowed(assistantExecMode{}, "knowledge_connectors_list") {
		t.Fatal("expected knowledge_connectors_list to be allowed in confirm mode")
	}
}

func TestResolveMentionedKnowledgeRefsForAgentAndSOP(t *testing.T) {
	h, u := workbenchFixture(t)
	svc := knowledge.NewService(h.repo, randomID)
	h.deps.knowledgeSvc = svc
	h.deps.knowledgeExec = knowledge.NewExecutor(svc)

	connA1, err := svc.CreateConnector(context.Background(), u.ID, knowledge.ConnectorInput{
		Type: domain.ConnectorTypeWeKnora, Name: "OwnerKB1", Enabled: new(true),
		Endpoint: "https://kb1.invalid", Secret: domain.KnowledgeConnectorSecret{Token: "tok1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connA2, err := svc.CreateConnector(context.Background(), u.ID, knowledge.ConnectorInput{
		Type: domain.ConnectorTypeWeKnora, Name: "OwnerKB2", Enabled: new(true),
		Endpoint: "https://kb2.invalid", Secret: domain.KnowledgeConnectorSecret{Token: "tok2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connForeign, err := svc.CreateConnector(context.Background(), u.ID+999, knowledge.ConnectorInput{
		Type: domain.ConnectorTypeWeKnora, Name: "ForeignKB", Enabled: new(true),
		Endpoint: "https://foreign.invalid", Secret: domain.KnowledgeConnectorSecret{Token: "tok-foreign"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.repo.PutAgent(domain.AgentDefinition{
		ID: "agent-kb", Name: "架构师", ProjectID: "workbench", KnowledgeBase: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID: "wf-kb-union", Name: "知识库巡检 SOP", ProjectID: "workbench", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{
			{
				ID: "n1", Name: "架构节点", Type: "agent", AgentID: "agent-kb",
				KnowledgeRefs: []domain.KnowledgeRef{
					{ConnectorID: connA1.ID, Mode: "allowlist", Sources: []string{"arch"}},
					{ConnectorID: connForeign.ID, Mode: "all"},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	sess := assistantConversation{ID: "sess-1", ProjectID: "workbench"}
	if err := h.repo.PutAssistantSession(u.ID, sess); err != nil {
		t.Fatal(err)
	}

	// 1. Plain message without @角色/@SOP does not mount knowledge even though project has agent-kb.
	refsPlain, changedPlain := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", sess, []assistantMessage{
		{Role: "user", Content: "普通消息不带引用"},
	})
	if changedPlain || len(refsPlain) != 0 {
		t.Fatalf("expected plain message not to mount knowledge refs, got changed=%v refs=%+v", changedPlain, refsPlain)
	}

	// 2. Mentioning @SOP unions node's connA1(allowlist:["arch"]) with node role's connA2(all) and drops foreign connector.
	refsSOP, changedSOP := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", sess, []assistantMessage{
		{
			Role:       "user",
			Content:    "读一下 @知识库巡检 SOP 的知识库做个概述",
			References: []domain.AssistantReference{{Type: "sop", ID: "wf-kb-union"}},
		},
	})
	if !changedSOP || len(refsSOP) != 2 {
		t.Fatalf("expected 2 unioned knowledge refs from @SOP, got changed=%v refs=%+v", changedSOP, refsSOP)
	}
	if refsSOP[0].ConnectorID != connA1.ID || refsSOP[0].Mode != "allowlist" || len(refsSOP[0].Sources) != 1 || refsSOP[0].Sources[0] != "arch" {
		t.Fatalf("expected first ref to preserve SOP node allowlist [arch], got %+v", refsSOP[0])
	}
	if refsSOP[1].ConnectorID != connA2.ID || refsSOP[1].Mode != "all" {
		t.Fatalf("expected second ref to append connA2(all) from role baseline, got %+v", refsSOP[1])
	}

	// Persist and verify KnowledgeRestricted privacy watermark is set.
	if err := h.repo.SetAssistantSessionKnowledge(u.ID, sess.ID, refsSOP); err != nil {
		t.Fatal(err)
	}
	savedAfter, ok := h.repo.AssistantSession(u.ID, sess.ID)
	if !ok || !savedAfter.KnowledgeRestricted || len(savedAfter.KnowledgeRefs) != 2 {
		t.Fatalf("expected KnowledgeRestricted=true and 2 saved refs, got %+v", savedAfter)
	}

	// 3. Explicit manual clear (KnowledgeRestricted=true, KnowledgeRefs=empty) stays cleared on plain message,
	// and direct @Agent mention mounts all enabled owner connectors.
	if err := h.repo.SetAssistantSessionKnowledge(u.ID, sess.ID, []domain.KnowledgeRef{}); err != nil {
		t.Fatal(err)
	}
	clearedSess, _ := h.repo.AssistantSession(u.ID, sess.ID)
	refsAfterClear, changedAfterClear := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", clearedSess, []assistantMessage{
		{Role: "user", Content: "我已经手动清空了知识库，这轮不带引用"},
	})
	if changedAfterClear || len(refsAfterClear) != 0 {
		t.Fatalf("expected manual clear to stay empty on plain message, got changed=%v refs=%+v", changedAfterClear, refsAfterClear)
	}
	refsAgent, changedAgent := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", clearedSess, []assistantMessage{
		{
			Role:       "user",
			Content:    "请 @架构师 检索知识库",
			References: []domain.AssistantReference{{Type: "agent", ID: "agent-kb"}},
		},
	})
	if !changedAgent || len(refsAgent) != 2 {
		t.Fatalf("expected direct @Agent mention to mount 2 owner connectors, got changed=%v refs=%+v", changedAgent, refsAgent)
	}

	// 4. workbenchReference metadata exposes knowledgeBase on agent and roleKnowledgeBase on sop.
	_, agentBlock, err := h.workbenchReference(domain.AssistantReference{Type: "agent", ID: "agent-kb"}, "workbench")
	if err != nil || !strings.Contains(agentBlock, `"knowledgeBase":true`) {
		t.Fatalf("expected agent workbenchReference to expose knowledgeBase:true, got err=%v block=%s", err, agentBlock)
	}
	_, sopBlock, err := h.workbenchReference(domain.AssistantReference{Type: "sop", ID: "wf-kb-union"}, "workbench")
	if err != nil || !strings.Contains(sopBlock, `"roleKnowledgeBase"`) {
		t.Fatalf("expected sop workbenchReference to expose roleKnowledgeBase, got err=%v block=%s", err, sopBlock)
	}
}

func TestResolveMentionedKnowledgeRefsForIssueBoundSOP(t *testing.T) {
	h, u := workbenchFixture(t)
	svc := knowledge.NewService(h.repo, randomID)
	h.deps.knowledgeSvc = svc
	h.deps.knowledgeExec = knowledge.NewExecutor(svc)

	conn, err := svc.CreateConnector(context.Background(), u.ID, knowledge.ConnectorInput{
		Type: domain.ConnectorTypeWeKnora, Name: "IssueKB", Enabled: new(true),
		Endpoint: "https://issue-kb.invalid", Secret: domain.KnowledgeConnectorSecret{Token: "tok-issue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAgent(domain.AgentDefinition{
		ID: "agent-issue-kb", Name: "知识库验收员", ProjectID: "workbench",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID: "wf-issue-bound", Name: "知识库验收sop", ProjectID: "workbench", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{
			{
				ID: "n1", Name: "验收节点", Type: "agent", AgentID: "agent-issue-kb",
				KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: conn.ID, Mode: "all"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, issue := range []domain.Issue{
		{ID: "issue-bound", ProjectID: "workbench", Title: "上线检查清单", Status: "backlog", WorkflowID: "wf-issue-bound"},
		{ID: "issue-unbound", ProjectID: "workbench", Title: "未绑定SOP的需求", Status: "backlog"},
		{ID: "issue-foreign", ProjectID: "other-project", Title: "外项目需求", Status: "backlog", WorkflowID: "wf-issue-bound"},
	} {
		if err := h.repo.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	sess := assistantConversation{ID: "sess-issue", ProjectID: "workbench"}
	if err := h.repo.PutAssistantSession(u.ID, sess); err != nil {
		t.Fatal(err)
	}

	// 1. @需求 with a bound SOP mounts the SOP node's knowledge refs.
	refs, changed := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", sess, []assistantMessage{
		{Role: "user", Content: "@上线检查清单", References: []domain.AssistantReference{{Type: "issue", ID: "issue-bound"}}},
	})
	if !changed || len(refs) != 1 || refs[0].ConnectorID != conn.ID || refs[0].Mode != "all" {
		t.Fatalf("expected @issue to mount bound SOP knowledge ref, got changed=%v refs=%+v", changed, refs)
	}

	// 2. @需求 without WorkflowID and foreign-project @需求 mount nothing.
	for _, id := range []string{"issue-unbound", "issue-foreign"} {
		refs, changed := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", sess, []assistantMessage{
			{Role: "user", Content: "@需求", References: []domain.AssistantReference{{Type: "issue", ID: id}}},
		})
		if changed || len(refs) != 0 {
			t.Fatalf("expected %s not to mount knowledge refs, got changed=%v refs=%+v", id, changed, refs)
		}
	}

	// 3. workbenchReference exposes the bound SOP id/name on issue references.
	_, issueBlock, err := h.workbenchReference(domain.AssistantReference{Type: "issue", ID: "issue-bound"}, "workbench")
	if err != nil || !strings.Contains(issueBlock, `"workflowId":"wf-issue-bound"`) || !strings.Contains(issueBlock, `"workflowName":"知识库验收sop"`) {
		t.Fatalf("expected issue workbenchReference to expose workflowId/workflowName, got err=%v block=%s", err, issueBlock)
	}

	// 4. prepareWorkbenchContext injects issues_dispatch + knowledge guidance for @需求 bound SOP.
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	req := assistantRequest{
		ProjectID: "workbench",
		Messages: []assistantMessage{{
			Role:       "user",
			Content:    "@上线检查清单 有什么需要注意的地方",
			References: []domain.AssistantReference{{Type: "issue", ID: "issue-bound"}},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"用户本轮 @ 的需求已绑定 SOP", "wf-issue-bound", "issues_dispatch", "严禁在当前对话内直接代跑 SOP"} {
		if !strings.Contains(req.contextText, want) {
			t.Fatalf("expected contextText to contain %q, got:\n%s", want, req.contextText)
		}
	}

	// 5. When the same SOP is also directly @-mentioned, the issue guidance is deduplicated.
	dupReq := assistantRequest{
		ProjectID: "workbench",
		Messages: []assistantMessage{{
			Role:    "user",
			Content: "@上线检查清单 @知识库验收sop",
			References: []domain.AssistantReference{
				{Type: "issue", ID: "issue-bound"},
				{Type: "sop", ID: "wf-issue-bound"},
			},
		}},
	}
	if err := h.prepareWorkbenchContext(c, &dupReq, u); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dupReq.contextText, "用户本轮 @ 了 SOP") || strings.Contains(dupReq.contextText, "用户本轮 @ 的需求已绑定 SOP") {
		t.Fatalf("expected deduplicated SOP guidance, got:\n%s", dupReq.contextText)
	}
}

func TestIssueBoundSOPVisibilityAndGuidanceEdges(t *testing.T) {
	h, u := workbenchFixture(t)
	svc := knowledge.NewService(h.repo, randomID)
	h.deps.knowledgeSvc = svc
	h.deps.knowledgeExec = knowledge.NewExecutor(svc)

	connA, err := svc.CreateConnector(context.Background(), u.ID, knowledge.ConnectorInput{
		Type: domain.ConnectorTypeWeKnora, Name: "EdgeKB1", Enabled: new(true),
		Endpoint: "https://edge-kb1.invalid", Secret: domain.KnowledgeConnectorSecret{Token: "tok-edge1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connB, err := svc.CreateConnector(context.Background(), u.ID, knowledge.ConnectorInput{
		Type: domain.ConnectorTypeWeKnora, Name: "EdgeKB2", Enabled: new(true),
		Endpoint: "https://edge-kb2.invalid", Secret: domain.KnowledgeConnectorSecret{Token: "tok-edge2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ag := range []domain.AgentDefinition{
		{ID: "agent-edge-plain", Name: "普通验收员", ProjectID: "workbench"},
		{ID: "agent-edge-toggle", Name: "全量验收员", ProjectID: "workbench", KnowledgeBase: true},
	} {
		if err := h.repo.PutAgent(ag); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID: "wf-edge-visible", Name: "可见sop", ProjectID: "workbench", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{{
			ID: "n1", Name: "验收", Type: "agent", AgentID: "agent-edge-plain",
			KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: connA.ID, Mode: "all"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID: "wf-edge-foreign-private", Name: "外项目私有sop", ProjectID: "other-project", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{{
			ID: "n1", Name: "验收", Type: "agent", AgentID: "agent-edge-plain",
			KnowledgeRefs: []domain.KnowledgeRef{{ConnectorID: connA.ID, Mode: "all"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutWorkflow(domain.WorkflowDefinition{
		ID: "wf-edge-role-toggle", Name: "角色全量sop", ProjectID: "workbench", SandboxProfileID: repo.DefaultSandboxProfileID,
		Nodes: []domain.WorkflowNode{{ID: "n1", Name: "验收", Type: "agent", AgentID: "agent-edge-toggle"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, issue := range []domain.Issue{
		{ID: "issue-edge-foreign-sop", ProjectID: "workbench", Title: "绑定外项目私有SOP", Status: "backlog", WorkflowID: "wf-edge-foreign-private"},
		{ID: "issue-edge-a", ProjectID: "workbench", Title: "需求A", Status: "backlog", WorkflowID: "wf-edge-visible"},
		{ID: "issue-edge-b", ProjectID: "workbench", Title: "需求B", Status: "backlog", WorkflowID: "wf-edge-visible"},
		{ID: "issue-edge-toggle", ProjectID: "workbench", Title: "角色全量需求", Status: "backlog", WorkflowID: "wf-edge-role-toggle"},
	} {
		if err := h.repo.PutIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	sess := assistantConversation{ID: "sess-edge", ProjectID: "workbench"}
	if err := h.repo.PutAssistantSession(u.ID, sess); err != nil {
		t.Fatal(err)
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))

	// 1. Issue bound to a foreign private SOP: no mount, no workflow metadata, no guidance.
	refs, changed := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", sess, []assistantMessage{
		{Role: "user", Content: "@绑定外项目私有SOP", References: []domain.AssistantReference{{Type: "issue", ID: "issue-edge-foreign-sop"}}},
	})
	if changed || len(refs) != 0 {
		t.Fatalf("foreign private SOP must not mount, got changed=%v refs=%+v", changed, refs)
	}
	_, block, err := h.workbenchReference(domain.AssistantReference{Type: "issue", ID: "issue-edge-foreign-sop"}, "workbench")
	if err != nil || strings.Contains(block, "workflowId") {
		t.Fatalf("foreign private SOP metadata must not leak, got err=%v block=%s", err, block)
	}
	foreignReq := assistantRequest{
		ProjectID: "workbench",
		Messages:  []assistantMessage{{Role: "user", Content: "@绑定外项目私有SOP", References: []domain.AssistantReference{{Type: "issue", ID: "issue-edge-foreign-sop"}}}},
	}
	if err := h.prepareWorkbenchContext(c, &foreignReq, u); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(foreignReq.contextText, "用户本轮 @ 的需求已绑定 SOP") {
		t.Fatalf("foreign private SOP must not inject guidance:\n%s", foreignReq.contextText)
	}

	// 2. Two issues bound to the same SOP: guidance lists the SOP exactly once.
	dupReq := assistantRequest{
		ProjectID: "workbench",
		Messages: []assistantMessage{{Role: "user", Content: "@需求A @需求B", References: []domain.AssistantReference{
			{Type: "issue", ID: "issue-edge-a"}, {Type: "issue", ID: "issue-edge-b"},
		}}},
	}
	if err := h.prepareWorkbenchContext(c, &dupReq, u); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dupReq.contextText, "用户本轮 @ 的需求已绑定 SOP") || strings.Count(dupReq.contextText, "(id=wf-edge-visible)") != 1 {
		t.Fatalf("same-SOP issues must dedupe guidance, got:\n%s", dupReq.contextText)
	}

	// 3. Run conversation: issue guidance is not injected.
	if err := h.repo.PutRun(domain.Run{
		ID: "run-edge", WorkflowID: "wf-edge-visible", Status: "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", IssueID: "issue-edge-a"},
		Nodes:            map[string]*domain.NodeRun{},
	}); err != nil {
		t.Fatal(err)
	}
	runReq := assistantRequest{
		ProjectID: "workbench",
		RunID:     "run-edge",
		Messages:  []assistantMessage{{Role: "user", Content: "@需求A 进度如何", References: []domain.AssistantReference{{Type: "issue", ID: "issue-edge-a"}}}},
	}
	if err := h.prepareWorkbenchContext(c, &runReq, u); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runReq.contextText, "用户本轮 @ 的需求已绑定 SOP") {
		t.Fatalf("run conversation must not inject issue guidance:\n%s", runReq.contextText)
	}

	// 4. Issue whose SOP node role has KnowledgeBase=true mounts all enabled owner connectors.
	refsToggle, changedToggle := h.resolveMentionedKnowledgeRefs(u.ID, "workbench", sess, []assistantMessage{
		{Role: "user", Content: "@角色全量需求", References: []domain.AssistantReference{{Type: "issue", ID: "issue-edge-toggle"}}},
	})
	if !changedToggle || len(refsToggle) != 2 {
		t.Fatalf("expected role-toggle issue to mount 2 connectors, got changed=%v refs=%+v", changedToggle, refsToggle)
	}
	got := map[string]bool{}
	for _, ref := range refsToggle {
		if ref.Mode != "all" {
			t.Fatalf("expected mode=all, got %+v", ref)
		}
		got[ref.ConnectorID] = true
	}
	if !got[connA.ID] || !got[connB.ID] {
		t.Fatalf("expected both owner connectors mounted, got %+v", refsToggle)
	}

	// 5. Personal conversation (projectId=""): no issue guidance, matching the mount rule.
	personalReq := assistantRequest{
		Messages: []assistantMessage{{Role: "user", Content: "@需求A", References: []domain.AssistantReference{{Type: "issue", ID: "issue-edge-a"}}}},
	}
	if err := h.prepareWorkbenchContext(c, &personalReq, u); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(personalReq.contextText, "用户本轮 @ 的需求已绑定 SOP") {
		t.Fatalf("personal conversation must not inject issue guidance:\n%s", personalReq.contextText)
	}
}
