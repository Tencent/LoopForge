package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestConversationLifecycle(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "space-test-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "space-fe", Name: "前端研发", Version: 1, ProjectID: "platform", Status: "published", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "review", Name: "方案评审", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	owner, _ := s.User(1)

	space := requestJSONAuthed[spaceView](t, h, owner, http.MethodPost, "/api/spaces", map[string]any{
		"projectId": "platform", "title": "积分商城兑换改版", "summary": "兑换流程从 4 步缩到 2 步", "creatorTitle": "产品",
	}, http.StatusCreated)
	if space.ID == "" || space.Requirement != nil || space.IssueID != "" || len(space.Members) != 1 || space.Members[0].Title != "产品" {
		t.Fatalf("new space: %+v", space)
	}
	list := requestJSONAuthed[[]spaceListItem](t, h, owner, http.MethodGet, "/api/spaces?projectId=platform", nil, http.StatusOK)
	if len(list) != 1 || list[0].Title != "积分商城兑换改版" {
		t.Fatalf("list: %+v", list)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, "/api/spaces/"+space.ID+"/members", map[string]any{"userId": 987654}, http.StatusBadRequest)

	base := "/api/spaces/" + space.ID
	doc := requestJSONAuthed[domain.SpaceArtifact](t, h, owner, http.MethodPost, base+"/artifacts", map[string]any{
		"kind": "doc", "title": "需求说明", "format": "markdown", "body": "# 目标\n两步兑换", "announce": true,
	}, http.StatusCreated)
	if doc.Version != 1 {
		t.Fatalf("doc: %+v", doc)
	}
	v2 := requestJSONAuthed[domain.SpaceArtifact](t, h, owner, http.MethodPut, base+"/artifacts/"+doc.ID, map[string]any{
		"title": "需求说明", "format": "markdown", "body": "# 目标\n两步兑换\n\n## 验收\n- 库存不足有提示", "baseVersion": 1, "announce": true,
	}, http.StatusOK)
	if v2.Version != 2 || v2.Kind != "doc" {
		t.Fatalf("v2: %+v", v2)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPut, base+"/artifacts/"+doc.ID, map[string]any{
		"title": "需求说明", "format": "markdown", "body": "stale", "baseVersion": 1,
	}, http.StatusConflict)
	revs := requestJSONAuthed[[]domain.SpaceArtifact](t, h, owner, http.MethodGet, base+"/artifacts/"+doc.ID+"/revisions", nil, http.StatusOK)
	if len(revs) != 2 || revs[0].Version != 2 || revs[1].Body != "" {
		t.Fatalf("revisions: %+v", revs)
	}
	first := requestJSONAuthed[domain.SpaceArtifact](t, h, owner, http.MethodGet, base+"/artifacts/"+doc.ID+"/revisions/1", nil, http.StatusOK)
	if first.Body != "# 目标\n两步兑换" {
		t.Fatalf("revision 1 must keep its body: %+v", first)
	}

	proto := requestJSONAuthed[domain.SpaceArtifact](t, h, owner, http.MethodPost, base+"/artifacts", map[string]any{
		"kind": "prototype", "title": "兑换页", "format": "html", "body": "<button onclick=\"alert(1)\">兑换</button>",
	}, http.StatusCreated)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, base+"/artifacts/"+proto.ID+"/raw", nil)
	h.ServeHTTP(rec, authedRequest(t, req, owner))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || strings.Contains(rec.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatalf("prototype must render in an opaque sandbox: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}

	thread := requestJSONAuthed[domain.SpaceThread](t, h, owner, http.MethodPost, base+"/threads", map[string]any{"title": "库存口径"}, http.StatusOK)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/messages", map[string]any{"body": "库存按 SKU 还是 SPU？", "threadId": thread.ID}, http.StatusCreated)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/messages", map[string]any{"body": "主线程消息"}, http.StatusCreated)
	mainMsg := requestJSONAuthed[struct {
		Message domain.ChatMessage `json:"message"`
	}](t, h, owner, http.MethodPost, base+"/messages", map[string]any{"body": "兑换失败要不要退积分？"}, http.StatusCreated)
	anchored := requestJSONAuthed[domain.SpaceThread](t, h, owner, http.MethodPost, base+"/threads", map[string]any{"messageId": mainMsg.Message.ID}, http.StatusOK)
	again := requestJSONAuthed[domain.SpaceThread](t, h, owner, http.MethodPost, base+"/threads", map[string]any{"messageId": mainMsg.Message.ID}, http.StatusOK)
	if anchored.MessageID != mainMsg.Message.ID || anchored.Title == "" || again.ID != anchored.ID {
		t.Fatalf("a message opens exactly one thread: %+v %+v", anchored, again)
	}
	inThread := requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, base+"/messages?threadId="+thread.ID, nil, http.StatusOK)
	if len(inThread) != 1 || inThread[0].ThreadID != thread.ID {
		t.Fatalf("thread messages: %+v", inThread)
	}
	for _, m := range requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, base+"/messages", nil, http.StatusOK) {
		if m.ThreadID != "" {
			t.Fatalf("main timeline must not include thread replies: %+v", m)
		}
	}

	// The test user has no CodeBuddy key: the reply must say so instead of
	// pretending to think.
	// Sent like a browser (gzip accepted) so the forwarded assistant call's
	// error body must still be readable.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, base+"/messages", strings.NewReader(`{"body":"帮我总结一下","toAi":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, authedRequest(t, req, owner))
	var asked struct {
		Reply domain.ChatMessage `json:"reply"`
	}
	gz, err := gzip.NewReader(rec.Body)
	if rec.Code != http.StatusCreated || err != nil || json.NewDecoder(gz).Decode(&asked) != nil {
		t.Fatalf("ask ai: %d %v", rec.Code, err)
	}
	if asked.Reply.Kind != domain.ChatKindAI || asked.Reply.AIStatus != domain.AIStatusFailed || !strings.Contains(asked.Reply.AIError, "API Key") {
		t.Fatalf("ai reply should surface the missing key: %+v", asked.Reply)
	}

	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/requirement", map[string]any{"title": " "}, http.StatusBadRequest)
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/requirement", map[string]any{"title": "积分兑换", "priority": "p0"}, http.StatusBadRequest)
	linked := requestJSONAuthed[spaceView](t, h, owner, http.MethodPost, base+"/requirement", map[string]any{
		"title": "积分商城兑换改版", "description": "兑换流程从 4 步缩到 2 步", "priority": "high",
	}, http.StatusCreated)
	if linked.Requirement == nil || linked.IssueID != linked.Requirement.ID || linked.Requirement.Number == 0 || linked.Requirement.Priority != "high" || linked.Requirement.Status != "todo" {
		t.Fatalf("requirement: %+v", linked)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/requirement", map[string]any{"title": "再建一个"}, http.StatusConflict)
	reopened := requestJSONAuthed[spaceView](t, h, owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": "platform", "title": "x", "issueId": linked.IssueID}, http.StatusOK)
	if reopened.ID != space.ID {
		t.Fatalf("a requirement keeps one conversation: %s vs %s", reopened.ID, space.ID)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": "platform", "title": "x", "issueId": "iss-missing"}, http.StatusBadRequest)
	if list := requestJSONAuthed[[]spaceListItem](t, h, owner, http.MethodGet, "/api/spaces?projectId=platform", nil, http.StatusOK); list[0].Requirement == nil || list[0].Requirement.ID != linked.IssueID {
		t.Fatalf("list should carry the requirement: %+v", list[0])
	}

	started := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, base+"/sops", map[string]any{
		"workflowIds": []string{"space-fe"}, "inputs": []string{doc.ID},
	}, http.StatusCreated)
	if started.Task == nil || started.Task.RunID == "" || started.Issue.ID != linked.IssueID {
		t.Fatalf("SOPs must run on the linked requirement: %+v", started)
	}
	if desc := started.Issue.Description; !strings.HasPrefix(desc, "兑换流程从 4 步缩到 2 步") || strings.Count(desc, "## 输入产物") != 1 || !strings.Contains(desc, "需求说明 v2") {
		t.Fatalf("inputs should be appended to the requirement once: %q", desc)
	}
	runID := started.Task.RunID
	var lanes []spaceLane
	deadline := time.Now().Add(3 * time.Second)
	for {
		lanes = requestJSONAuthed[[]spaceLane](t, h, owner, http.MethodGet, base+"/lanes", nil, http.StatusOK)
		if len(lanes) == 1 && len(lanes[0].Nodes) == 1 && lanes[0].Nodes[0].Status == "waiting_approval" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lanes: %+v", lanes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lanes[0].ThreadID == "" || len(lanes[0].Inputs) != 1 || lanes[0].Inputs[0].Version != 2 {
		t.Fatalf("lane should own a thread and pin its inputs: %+v", lanes[0])
	}
	requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, base+"/messages", nil, http.StatusOK)
	requestJSONAuthed[domain.ChatMessage](t, h, owner, http.MethodPost, base+"/decisions", map[string]any{"runId": runID, "nodeId": "review", "action": "approve"}, http.StatusOK)
	detail := requestJSONAuthed[spaceLaneDetail](t, h, owner, http.MethodGet, base+"/lanes/"+runID, nil, http.StatusOK)
	if detail.RunID != runID || len(detail.Events) == 0 {
		t.Fatalf("lane detail: %+v", detail)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodGet, base+"/lanes/run-elsewhere", nil, http.StatusNotFound)

	flow := requestJSONAuthed[struct {
		Artifact domain.SpaceArtifact `json:"artifact"`
		Workflow struct {
			ID string `json:"id"`
		} `json:"workflow"`
	}](t, h, owner, http.MethodPost, base+"/flows", map[string]any{
		"name": "联调 SOP", "sandboxProfileId": "local",
		"nodes": []map[string]any{{"id": "dev", "name": "开发", "type": "agent", "agentId": "developer"}, {"id": "check", "name": "验收", "type": "approval"}},
		"edges": []map[string]any{{"source": "dev", "target": "check"}, {"source": "check", "target": "dev", "when": "rework"}},
	}, http.StatusOK)
	wf, ok := s.Workflow(flow.Workflow.ID)
	if !ok || wf.ProjectID != "platform" || len(wf.Nodes) != 2 || wf.Nodes[1].RejectTo != "dev" || len(wf.Edges) != 1 || flow.Artifact.WorkflowID != wf.ID {
		t.Fatalf("flow should be saved as a real SOP: %+v %+v", wf, flow)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/flows", map[string]any{
		"name": "缺角色", "nodes": []map[string]any{{"id": "x", "name": "开发", "type": "agent", "agentId": "nobody"}},
	}, http.StatusBadRequest)

	usage := requestJSONAuthed[map[string]any](t, h, owner, http.MethodGet, base+"/usage", nil, http.StatusOK)
	if lanes, _ := usage["lanes"].([]any); len(lanes) != 1 {
		t.Fatalf("usage: %+v", usage)
	}
	catalog := requestJSONAuthed[map[string][]map[string]any](t, h, owner, http.MethodGet, base+"/catalog", nil, http.StatusOK)
	if len(catalog["workflows"]) == 0 || len(catalog["agents"]) == 0 || len(catalog["sandboxes"]) == 0 {
		t.Fatalf("catalog: %+v", catalog)
	}
}

func authedRequest(t *testing.T, req *http.Request, user domain.User) *http.Request {
	t.Helper()
	token, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: user.ID, LoginName: user.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	// Browsers send Origin on writes; in-process forwarding must keep it same-origin.
	req.Header.Set("Origin", "http://"+req.Host)
	return req
}

func TestConversationRequirementIsExplicit(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "space-test-session-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{
		ID: "space-be", Name: "后端研发", Version: 1, ProjectID: "platform", Status: "published", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "review", Name: "方案评审", Type: "approval", MaxAttempts: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	owner, _ := s.User(1)
	issuesBefore := len(s.Issues())

	chat := requestJSONAuthed[spaceView](t, h, owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": "platform", "title": "随便聊聊缓存方案"}, http.StatusCreated)
	if chat.Requirement != nil || chat.IssueID != "" || len(s.Issues()) != issuesBefore {
		t.Fatalf("a new conversation must not create a requirement: %+v", chat.RequirementSpace)
	}
	base := "/api/spaces/" + chat.ID
	if msgs := requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, base+"/messages", nil, http.StatusOK); len(msgs) != 0 {
		t.Fatalf("plain conversations open empty: %+v", msgs)
	}

	// Starting an SOP from a plain conversation creates the requirement the
	// user confirmed in the dialog and links it, so later SOPs share it.
	first := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, base+"/sops", map[string]any{
		"title": "缓存击穿治理", "workflowIds": []string{"space-be"},
	}, http.StatusCreated)
	view := requestJSONAuthed[spaceView](t, h, owner, http.MethodGet, base, nil, http.StatusOK)
	if view.Requirement == nil || view.Requirement.ID != first.Issue.ID || view.Requirement.Title != "缓存击穿治理" {
		t.Fatalf("first SOP should link its requirement: %+v", view.Requirement)
	}
	second := requestJSONAuthed[roomIssueResult](t, h, owner, http.MethodPost, base+"/sops", map[string]any{
		"title": "ignored", "workflowIds": []string{"space-be"},
	}, http.StatusCreated)
	if second.Issue.ID != first.Issue.ID || len(s.Issues()) != issuesBefore+1 {
		t.Fatalf("later SOPs must reuse the requirement: %s vs %s", second.Issue.ID, first.Issue.ID)
	}
	if lanes := requestJSONAuthed[[]spaceLane](t, h, owner, http.MethodGet, base+"/lanes", nil, http.StatusOK); len(lanes) != 2 {
		t.Fatalf("both lanes stay on the conversation: %+v", lanes)
	}

	// From the requirements page: an issue without a conversation gets one.
	if err := s.PutIssue(domain.Issue{ID: "iss-page", ProjectID: "platform", Title: "导出改异步", Status: "todo", Kind: "issue", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	opened := requestJSONAuthed[spaceView](t, h, owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": "platform", "title": "导出改异步", "issueId": "iss-page"}, http.StatusCreated)
	if opened.Requirement == nil || opened.Requirement.ID != "iss-page" {
		t.Fatalf("conversation should carry the requirement: %+v", opened.Requirement)
	}
	msgs := requestJSONAuthed[[]domain.ChatMessage](t, h, owner, http.MethodGet, "/api/spaces/"+opened.ID+"/messages", nil, http.StatusOK)
	if len(msgs) != 1 || msgs[0].IssueID != "iss-page" {
		t.Fatalf("opening note: %+v", msgs)
	}
}

func TestConversationMessageAttachments(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "space-test-session-secret")
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	owner, _ := s.User(1)
	space := requestJSONAuthed[spaceView](t, h, owner, http.MethodPost, "/api/spaces", map[string]any{"projectId": "platform", "title": "截图讨论"}, http.StatusCreated)
	base := "/api/spaces/" + space.ID

	var picture bytes.Buffer
	png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var form bytes.Buffer
	w := multipart.NewWriter(&form)
	w.WriteField("projectId", "platform")
	f, _ := w.CreateFormFile("file", "screen.png")
	f.Write(picture.Bytes())
	w.Close()
	up := httptest.NewRequest(http.MethodPost, "/api/assistant/attachments", &form)
	up.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedRequest(t, up, owner))
	var attachment domain.AssistantAttachment
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &attachment) != nil {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	posted := requestJSONAuthed[struct {
		Message domain.ChatMessage `json:"message"`
	}](t, h, owner, http.MethodPost, base+"/messages", map[string]any{"body": "", "attachments": []map[string]string{{"id": attachment.ID}}}, http.StatusCreated)
	if len(posted.Message.Attachments) != 1 || posted.Message.Attachments[0].Name != "screen.png" || posted.Message.Attachments[0].MimeType != "image/png" {
		t.Fatalf("message attachments: %+v", posted.Message)
	}
	requestJSONAuthed[map[string]any](t, h, owner, http.MethodPost, base+"/messages", map[string]any{"body": "x", "attachments": []map[string]string{{"id": strings.Repeat("ab", 16)}}}, http.StatusBadRequest)

	get := func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedRequest(t, httptest.NewRequest(http.MethodGet, base+"/attachments/"+id, nil), owner))
		return rec
	}
	if got := get(attachment.ID); got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(got.Header().Get("Content-Disposition"), "inline") || !bytes.Equal(got.Body.Bytes(), picture.Bytes()) {
		t.Fatalf("download: %d %v", got.Code, got.Header())
	}
	if got := get(strings.Repeat("cd", 16)); got.Code != http.StatusNotFound {
		t.Fatalf("attachment outside the space must not be served: %d", got.Code)
	}
}
