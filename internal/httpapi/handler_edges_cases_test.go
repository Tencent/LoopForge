package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func cvAPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestWorkbenchImagesDecksAttachments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", root)
	const owner = int64(81001)
	workspace := filepath.Join(root, "image-workspace")
	imageDir := filepath.Join(workspace, "generated-images")
	if err := os.MkdirAll(imageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pngData := cvAPNG(t)
	if err := os.WriteFile(filepath.Join(imageDir, "fresh.png"), pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(imageDir, "not-image.png"), []byte("plain"), 0o600)
	_ = os.WriteFile(filepath.Join(imageDir, "ignore.txt"), []byte("plain"), 0o600)
	now := time.Now()
	images := workspaceImagesIn(workspace, owner, now.Add(-time.Minute), now.Add(time.Minute), "wb-p")
	if len(images) != 1 {
		t.Fatalf("images=%v", images)
	}
	record, path, err := loadWorkbenchAttachment(owner, images[0].ID)
	if err != nil || record.ProjectID != "wb-p" || !strings.HasSuffix(path, ".bin") {
		t.Fatalf("record=%+v path=%s err=%v", record, path, err)
	}
	opened, file, err := openWorkbenchAttachment(owner, images[0].ID)
	if err != nil || opened.Name != "fresh.png" {
		t.Fatalf("open=%+v %v", opened, err)
	}
	_ = file.Close()
	if _, _, err := loadWorkbenchAttachment(owner, "../bad"); err == nil {
		t.Fatal("invalid attachment id accepted")
	}
	longName := strings.Repeat("x", 250) + ".png"
	long, err := saveWorkbenchImage(owner, longName, "image/png", pngData, "wb-p")
	if err != nil || len(long.Name) != 240 {
		t.Fatalf("long attachment=%+v %v", long, err)
	}

	deckRoot := filepath.Join(root, "deck-root")
	deckData := cvAPPTX(t, map[string]string{"slide1.xml": `<a:p><a:r><a:t>Deck title</a:t></a:r></a:p>`})
	if err := os.MkdirAll(filepath.Join(deckRoot, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deckRoot, "ws", "deck.pptx"), deckData, 0o600); err != nil {
		t.Fatal(err)
	}
	deck := workspaceDecksIn(deckRoot, owner, now.Add(-time.Minute), now.Add(time.Minute))
	if len(deck.Files) != 1 || deck.Slides != "Deck title" {
		t.Fatalf("deck=%+v", deck)
	}
	if got := workspaceDecksIn(filepath.Join(root, "missing"), owner, now, now); len(got.Files) != 0 {
		t.Fatalf("missing deck=%+v", got)
	}

	sessionRoot := assistantSessionRoot(owner)
	if err := os.MkdirAll(filepath.Join(sessionRoot, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "ws", "talk.pptx"), deckData, 0o600); err != nil {
		t.Fatal(err)
	}
	message := assistantMessage{Role: "assistant", Content: `{"message":"PPT 已完成","drafts":[]}`, CreatedAt: time.Now()}
	if !attachWorkspaceDeck(owner, &message, map[string]bool{}) || len(message.Attachments) != 1 || !strings.Contains(message.Content, "```slides") {
		t.Fatalf("attached deck=%+v", message)
	}
	if attachWorkspaceDeck(owner, nil, map[string]bool{}) {
		t.Fatal("nil message changed")
	}
	already := assistantMessage{Content: "```slides\nx\n```"}
	if attachWorkspaceDeck(owner, &already, map[string]bool{}) {
		t.Fatal("existing slides changed")
	}
	autoImageDir := filepath.Join(assistantCLIWorkspaceForUser(owner), "generated-images")
	if err := os.MkdirAll(autoImageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(autoImageDir, "auto.png"), pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	conv := assistantConversation{ProjectID: "wb-p", Messages: []assistantMessage{{
		Role: "assistant", Content: `{"message":"saved /tmp/generated-images/auto.png","drafts":[]}`, CreatedAt: time.Now(),
	}}}
	if !materializeWorkspaceImages(owner, &conv) || len(conv.Messages[0].Attachments) == 0 || strings.Contains(conv.Messages[0].Content, "/tmp/generated-images") {
		t.Fatalf("materialized conversation=%+v", conv)
	}
	if materializeWorkspaceImages(owner, nil) {
		t.Fatal("nil conversation changed")
	}

	session := &AgentToolSession{
		UserID: owner, ProjectID: "wb-p",
		AttachmentIDs: map[string]bool{images[0].ID: true},
	}
	result, err := readWorkbenchAttachmentTool(session, json.RawMessage(`{"id":"`+images[0].ID+`"}`))
	if err != nil || result["isError"] != false {
		t.Fatalf("read image=%v %v", result, err)
	}
	text, err := saveWorkbenchImage(owner, "notes.txt", "text/plain", []byte("hello"), "wb-p")
	if err != nil {
		t.Fatal(err)
	}
	session.AttachmentIDs[text.ID] = true
	result, err = readWorkbenchAttachmentTool(session, json.RawMessage(`{"id":"`+text.ID+`"}`))
	if err != nil || !strings.Contains(string(cvAJSON(result)), "hello") {
		t.Fatalf("read text=%v %v", result, err)
	}
	binary, _ := saveWorkbenchImage(owner, "blob.bin", "application/octet-stream", []byte{0, 1, 2}, "wb-p")
	session.AttachmentIDs[binary.ID] = true
	result, err = readWorkbenchAttachmentTool(session, json.RawMessage(`{"id":"`+binary.ID+`"}`))
	if err != nil || !strings.Contains(string(cvAJSON(result)), "二进制") {
		t.Fatalf("read binary=%v %v", result, err)
	}
	session.SetTerminalOnly()
	if _, err := readWorkbenchAttachmentTool(session, json.RawMessage(`{"id":"`+images[0].ID+`"}`)); err == nil {
		t.Fatal("terminal-only attachment read accepted")
	}

	copied, err := copyForkAttachment(owner, owner+1, images[0].ID, "wb-p")
	if err != nil || copied.ID == images[0].ID {
		t.Fatalf("copy=%+v %v", copied, err)
	}
	if _, err := copyForkAttachment(owner, owner+1, images[0].ID, "other"); err == nil {
		t.Fatal("cross-project attachment copied")
	}
}

func cvAJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func TestWorkbenchReferencesAndUploads(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", root)
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "cva-wb-owner"})
	owner, _ := st.UserByLoginName("cva-wb-owner")
	_ = st.PutUser(domain.User{LoginName: "cva-wb-viewer"})
	viewer, _ := st.UserByLoginName("cva-wb-viewer")
	projectID := "cva-wb-p"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "Workbench P", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}})
	_ = st.PutIssue(domain.Issue{ID: "cva-wb-i", ProjectID: projectID, Title: "Issue", Description: "D"})
	_ = st.PutAgent(domain.AgentDefinition{ID: "cva-wb-a", ProjectID: projectID, Name: "Agent", Prompt: "Do"})
	_ = st.PutWorkflow(domain.WorkflowDefinition{ID: "cva-wb-w", ProjectID: projectID, Name: "Workflow", Status: "published"})
	_ = st.PutRepository(domain.Repository{ID: "cva-wb-r", ProjectID: projectID, Name: "Repo"})
	run := domain.Run{
		ID: "cva-wb-run", WorkflowID: "cva-wb-w", Status: "running",
		ExecutionContext: domain.TelemetryContext{ProjectID: projectID, IssueID: "cva-wb-i"},
		WorkflowSnapshot: domain.WorkflowDefinition{Name: "Workflow", Nodes: []domain.WorkflowNode{{ID: "n", AgentID: "cva-wb-a"}}},
		Nodes:            map[string]*domain.NodeRun{"n": {Status: "running", Output: "out"}},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "cva-wb-r", Name: "Repo"}},
	}
	_ = st.PutRun(run)
	h := &assistantHandlers{deps: &deps{repo: st}}
	c, _ := cvAContext(t, http.MethodGet, "/", "", &owner)
	for _, ref := range []domain.AssistantReference{
		{Type: "issue", ID: "cva-wb-i"}, {Type: "agent", ID: "cva-wb-a"},
		{Type: "sop", ID: "cva-wb-w"}, {Type: "repository", ID: "cva-wb-r"},
		{Type: "run", ID: "cva-wb-run"},
	} {
		canonical, text, err := h.workbenchReference(ref, projectID)
		if err != nil || canonical.Name == "" || text == "" {
			t.Errorf("reference %v = %+v %q %v", ref, canonical, text, err)
		}
		if _, _, err := h.accessibleWorkbenchReference(c, owner.ID, ref, projectID); err != nil {
			t.Errorf("accessible %v: %v", ref, err)
		}
	}
	if _, _, err := h.workbenchReference(domain.AssistantReference{Type: "issue", ID: "missing"}, projectID); err == nil {
		t.Fatal("missing reference accepted")
	}
	if workbenchProjectAllowsUser(domain.Project{}, 99, false) != true ||
		workbenchProjectAllowsUser(domain.Project{OwnerID: owner.ID}, viewer.ID, false) {
		t.Fatal("project access policy")
	}
	for kind, id := range map[string]string{"agent": "cva-wb-a", "run": run.ID, "sop": run.WorkflowID, "issue": "cva-wb-i", "repository": "cva-wb-r"} {
		if !taskWorkbenchReferenceAllowed(run, kind, id) {
			t.Errorf("%s reference rejected", kind)
		}
	}
	if taskWorkbenchReferenceAllowed(run, "agent", "other") || runNodeAgentID(run, "n") != "cva-wb-a" || runNodeAgentID(run, "missing") != "" {
		t.Fatal("task reference helpers")
	}

	c, rec := cvAContext(t, http.MethodGet, "/api/assistant/references?projectId="+projectID, "", &owner)
	h.workbenchReferences(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "cva-wb-a") {
		t.Fatalf("references=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/references?projectId="+projectID+"&scope=all&q=agent&type=agent&offset=-1", "", &owner)
	h.workbenchReferences(c)
	if rec.Code != 200 {
		t.Fatalf("global refs=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/references?projectId=missing", "", &viewer)
	h.workbenchReferences(c)
	if rec.Code != 403 {
		t.Fatalf("forbidden refs=%d", rec.Code)
	}

	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("projectId", projectID)
	part, _ := writer.CreateFormFile("file", `..\note.txt`)
	_, _ = part.Write([]byte("attachment body"))
	_ = writer.Close()
	c, rec = cvAContext(t, http.MethodPost, "/api/assistant/attachments", "", &owner)
	c.Request.Body = io.NopCloser(bytes.NewReader(form.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	h.uploadAttachment(c)
	if rec.Code != 201 {
		t.Fatalf("upload=%d %s", rec.Code, rec.Body.String())
	}
	var saved domain.AssistantAttachment
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/attachments/"+saved.ID, "", &owner)
	c.Params = gin.Params{{Key: "id", Value: saved.ID}}
	h.downloadAttachment(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "attachment body") {
		t.Fatalf("download=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/attachments/missing", "", &owner)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.downloadAttachment(c)
	if rec.Code != 404 {
		t.Fatalf("missing download=%d", rec.Code)
	}

	req := assistantRequest{
		ProjectID: projectID,
		Messages: []assistantMessage{{Role: "user", Content: "help", References: []domain.AssistantReference{
			{Type: "issue", ID: "cva-wb-i"}, {Type: "agent", ID: "cva-wb-a"},
			{Type: "repository", ID: "cva-wb-r"},
		}, Attachments: []domain.AssistantAttachment{saved}}},
	}
	c, _ = cvAContext(t, http.MethodPost, "/", "", &owner)
	if err := h.prepareWorkbenchContext(c, &req, owner); err != nil || len(req.roles) != 1 || req.contextText == "" || !req.attachmentIDs[saved.ID] {
		t.Fatalf("prepared request=%+v err=%v", req, err)
	}
	runReq := assistantRequest{
		ProjectID: projectID, RunID: run.ID,
		Messages: []assistantMessage{{Role: "user", Content: "run", References: []domain.AssistantReference{
			{Type: "run", ID: run.ID}, {Type: "issue", ID: "cva-wb-i"},
		}}},
	}
	if err := h.prepareWorkbenchContext(c, &runReq, owner); err != nil || !strings.Contains(runReq.contextText, "当前 SOP 运行") {
		t.Fatalf("run context err=%v text=%s", err, runReq.contextText)
	}
	longReq := assistantRequest{ProjectID: projectID, Messages: make([]assistantMessage, 201)}
	if err := h.prepareWorkbenchContext(c, &longReq, owner); err == nil {
		t.Fatal("overlong conversation accepted")
	}
	tooManyRefs := assistantRequest{ProjectID: projectID, Messages: []assistantMessage{{Role: "user", References: make([]domain.AssistantReference, 13)}}}
	if err := h.prepareWorkbenchContext(c, &tooManyRefs, owner); err == nil {
		t.Fatal("too many references accepted")
	}
}

func TestAgentToolRPCAndTerminalBranches(t *testing.T) {
	gateway := &agentToolGateway{}
	report := &AgentToolSession{Mode: AgentToolReport, Final: make(chan AgentTerminalResult, 1)}
	for _, request := range []agentToolRPCRequest{
		{ID: 1, Method: "initialize", Params: json.RawMessage(`{"protocolVersion":"2024-11-05"}`)},
		{ID: 2, Method: "ping"},
		{ID: 3, Method: "tools/list"},
		{ID: 4, Method: "unknown"},
	} {
		if response := gateway.handleRPC(context.Background(), report, request); response == nil {
			t.Fatalf("nil response for %s", request.Method)
		}
	}
	if response := gateway.handleRPC(context.Background(), report, agentToolRPCRequest{Method: "notifications/initialized"}); response != nil {
		t.Fatal("notification returned response")
	}
	for _, mode := range []AgentToolMode{AgentToolReport, AgentToolClarify, AgentToolAssistant, AgentToolScheduled, AgentToolMode("other")} {
		if agentToolInstructions(mode) == "" {
			t.Fatalf("empty instructions for %s", mode)
		}
	}
	if agentToolProtocolVersion(json.RawMessage(`{}`)) != "2025-06-18" {
		t.Fatal("protocol fallback")
	}
	result, err := gateway.callTerminal(report, submitNotificationReportTool, json.RawMessage(`{"title":"T","body":"Body"}`))
	if err != nil || result["isError"] == true {
		t.Fatalf("report terminal=%v %v", result, err)
	}
	if _, err := gateway.callTerminal(report, submitNotificationReportTool, json.RawMessage(`{"body":"again"}`)); err == nil {
		t.Fatal("second report terminal accepted")
	}
	clarify := &AgentToolSession{Mode: AgentToolClarify, Final: make(chan AgentTerminalResult, 1)}
	result, err = gateway.callTerminal(clarify, submitClarificationTool, json.RawMessage(`{"summary":"","ready":false,"questions":[{"id":"q","question":"Pick","options":["A","B"]}]}`))
	if err != nil || result["isError"] == true {
		t.Fatalf("clarify terminal=%v %v", result, err)
	}
	assistant := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", Final: make(chan AgentTerminalResult, 1)}
	result, err = gateway.callTerminal(assistant, submitAssistantReplyTool, json.RawMessage(`{"message":"progress","drafts":[]}`))
	if err != nil || result["isError"] != true {
		t.Fatalf("empty draft terminal=%v %v", result, err)
	}
	if _, err := gateway.callTerminal(assistant, submitNotificationReportTool, json.RawMessage(`{}`)); err == nil {
		t.Fatal("wrong terminal allowed")
	}

	session := &AgentToolSession{Mode: AgentToolClarify, MCPServers: []domain.MCPServerDefinition{{ID: "tapd-id", Name: "Tapd", MarketID: "70"}}}
	if gateway.serverIDForKind(session, "tapd") != "tapd-id" {
		t.Fatal("server kind lookup")
	}
	if _, ok := gateway.lookupServer(session, "TAPD"); !ok {
		t.Fatal("server name lookup")
	}
	if _, err := gateway.dispatch(context.Background(), session, agentToolDescriptor{Kind: "agentflow", UpstreamName: "issues_save"}, nil); err == nil {
		t.Fatal("clarify platform write allowed")
	}
	if _, err := gateway.dispatch(context.Background(), session, agentToolDescriptor{Kind: "tapd", UpstreamName: "proxy_execute_tool", ServerID: "tapd-id"}, map[string]any{"tool_name": "stories_update"}); err == nil {
		t.Fatal("tapd write allowed")
	}
}

func TestAssistantApprovalDraftAndReviewHelpers(t *testing.T) {
	for input, want := range map[string]string{"AUTO": AssistantApprovalAuto, " assisted ": AssistantApprovalAssisted, "bad": AssistantApprovalConfirm} {
		if got := normalizeAssistantApprovalMode(input); got != want {
			t.Fatalf("approval mode %q=%q", input, got)
		}
	}
	for tool, want := range map[string]bool{"issues_delete": true, "sync_start": true, "agents_save": false} {
		if assistantRiskyToolName(tool) != want || assistantDraftRisky(assistantDraft{Tool: tool}) != want {
			t.Fatalf("risk %s", tool)
		}
	}
	if !(assistantApprovalReview{Outcome: "allow", RiskLevel: "high", UserAuthorization: "medium"}).allows() {
		t.Fatal("authorized high risk denied")
	}
	if (assistantApprovalReview{Outcome: "allow", RiskLevel: "critical", UserAuthorization: "high"}).allows() {
		t.Fatal("critical risk allowed")
	}
	ctx := withAssistantApprovalReviewer(context.Background(), func(context.Context, string, string, map[string]any) assistantApprovalReview {
		return assistantApprovalReview{Outcome: "allow", RiskLevel: "low", UserAuthorization: "high", Rationale: "ok"}
	})
	if !reviewAssistantAction(ctx, "x", "x", nil).allows() || reviewAssistantAction(context.Background(), "x", "x", nil).Outcome != "unavailable" {
		t.Fatal("review context")
	}
	catalog := assistantModelCatalog{Models: []assistantModel{
		{ID: "bad", Credits: "n/a"}, {ID: "free", Credits: "0x"}, {ID: "cheap", Credits: "0.2×"}, {ID: "expensive", Credits: "2"},
	}}
	if assistantApprovalReviewModel(catalog) != "free" || cheapestAssistantReviewModel(catalog, true) != "cheap" {
		t.Fatalf("review models=%v", assistantApprovalReviewModels(catalog))
	}
	if got := assistantApprovalReviewModels(catalog); len(got) != 2 || got[0] != "free" || got[1] != "cheap" {
		t.Fatalf("models=%v", got)
	}
	for _, raw := range []string{
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"ok"}`,
		`prefix {"outcome":"deny","risk_level":"critical","user_authorization":"unknown","rationale":"unsafe"} suffix`,
	} {
		if _, err := decodeAssistantApprovalReview(raw); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"outcome":"allow","risk_level":"critical","user_authorization":"high","rationale":"no"}`,
		`{"outcome":"yes","risk_level":"low","user_authorization":"high","rationale":"no"}`,
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"ok","extra":1}`,
	} {
		if _, err := decodeAssistantApprovalReview(raw); err == nil {
			t.Fatalf("invalid review accepted: %s", raw)
		}
	}
	if !strings.Contains(assistantReviewRefusal("delete", assistantApprovalReview{Rationale: "unsafe"}).Error(), "unsafe") {
		t.Fatal("review refusal")
	}
	if _, err := runAssistantApprovalReview(context.Background(), assistantApprovalInput{}, domain.User{}, ""); err == nil {
		t.Fatal("empty review config accepted")
	}

	body := json.RawMessage(`{"id":"x","gitToken":"g","apiKey":"a","nested":{"password":"p"},"headers":{"Authorization":"Bearer x","X":"ok"}}`)
	clean := stripAssistantDraftSecrets("repositories_save", body)
	if strings.Contains(string(clean), "gitToken") || strings.Contains(string(clean), "apiKey") || strings.Contains(string(clean), "password") || strings.Contains(string(clean), "Bearer") {
		t.Fatalf("secrets retained: %s", clean)
	}
	if got := normalizeAssistantIssueBranchDraft("issues_save", json.RawMessage(`{"baseBranch":"main","repositoryBranches":{"a":"master","b":"dev"}}`)); strings.Contains(string(got), "main") || strings.Contains(string(got), "master") {
		t.Fatalf("branches not normalized: %s", got)
	}
	injected := injectAssistantDraftProjectID("agents_save", json.RawMessage(`{"id":"agent"}`), "p")
	if !strings.Contains(string(injected), `"projectId":"p"`) || !strings.Contains(string(injected), `"name":"agent"`) {
		t.Fatalf("project not injected: %s", injected)
	}
	// A new project is top-level: drafted inside another project's session it
	// must not carry that project, or the confirmation card shows it there.
	for _, tool := range []string{"projects_save", "business_domains_save", "sync_config_save"} {
		for _, body := range []string{`{"id":"okr","name":"OKR系统"}`, `{"id":"okr","name":"OKR系统","projectId":"skillhub"}`} {
			if got := injectAssistantDraftProjectID(tool, json.RawMessage(body), "skillhub"); strings.Contains(string(got), "projectId") || !strings.Contains(string(got), `"id":"okr"`) {
				t.Fatalf("%s %s: %s", tool, body, got)
			}
		}
	}
	st := cvAStore(t)
	h := &assistantHandlers{deps: &deps{repo: st}}
	drafts := h.resolveAssistantDrafts(context.Background(), domain.User{}, "p", []assistantDraft{{Tool: "agents_delete", Body: json.RawMessage(`{"id":"missing"}`)}}, AssistantApprovalAuto)
	if len(drafts) != 1 || len(drafts[0].Warnings) == 0 || drafts[0].Auto {
		t.Fatalf("warning draft auto-applied: %+v", drafts)
	}
}

func TestMarketAndWorkspaceMRHelpers(t *testing.T) {
	if len(mcpMarketItems) == 0 {
		t.Fatal("empty MCP market")
	}
	item := mcpMarketItems[0]
	if !strings.Contains(mcpMarketSearchText(item), strings.ToLower(item.Name)) ||
		len(filterMCPMarket(mcpMarketItems, item.ID)) == 0 ||
		len(filterMCPMarket(mcpMarketItems, "definitely-no-match")) != 0 {
		t.Fatal("market search")
	}
	if _, ok := marketItem("missing"); ok || !sameMarketItem(item.ID, item.ID) {
		t.Fatal("market identity")
	}
	visible := visibleMCPServersForUser([]domain.MCPServerDefinition{
		{ID: "project", Scope: "project"}, {ID: "mine", Scope: "personal", OwnerUserID: 7}, {ID: "other", Scope: "personal", OwnerUserID: 8},
	}, 7)
	if len(visible) != 2 {
		t.Fatalf("visible=%v", visible)
	}
	_ = withMarketIcons([]domain.MCPServerDefinition{{MarketID: item.ID}})
	st := cvAStore(t)
	projectID := "cva-market-p"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "P"})
	if _, _, err := installMarketMCP(nil, item.ID, projectID); err == nil {
		t.Fatal("nil store install succeeded")
	}
	server, created, err := installMarketMCP(st, item.ID, projectID)
	if err != nil || !created {
		t.Fatalf("install=%+v %v %v", server, created, err)
	}
	if _, created, err = installMarketMCP(st, item.ID, projectID); err != nil || created {
		t.Fatalf("duplicate install created=%v err=%v", created, err)
	}
	h := &marketHandlers{deps: &deps{repo: st}}
	c, rec := cvAContext(t, http.MethodGet, "/api/mcp-market?q="+item.ID, "", nil)
	h.mcpMarket(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), item.ID) {
		t.Fatalf("market=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/mcp-market/install", `{"marketId":"missing","scope":"project","projectId":"`+projectID+`"}`, nil)
	h.mcpMarketInstall(c)
	if rec.Code != 404 {
		t.Fatalf("missing market install=%d", rec.Code)
	}

	for _, tc := range []struct {
		rc   domain.RepositoryContext
		want string
	}{
		{domain.RepositoryContext{CloneURL: "git@git.example.com:group/repo.git"}, "group/repo"},
		{domain.RepositoryContext{WebURL: "https://git.example.com/team/project/"}, "team/project"},
		{domain.RepositoryContext{CloneURL: "https://github.com/a/b.git"}, ""},
	} {
		if got := gongfengProjectPath(tc.rc); got != tc.want {
			t.Fatalf("project path=%q want=%q", got, tc.want)
		}
	}
	for status, part := range map[int]string{401: "Token", 403: "权限", 404: "找不到", 500: "HTTP 500"} {
		if got := gongfengMRError(status, []byte(`{"message":"detail"}`)).Error(); !strings.Contains(got, part) {
			t.Fatalf("MR error %d=%q", status, got)
		}
	}
	if firstNonNil(nil, "x", "y") != "x" || firstNonNil(nil) != nil {
		t.Fatal("first nonnil")
	}
	desc := workspaceSubmitMRDescriptor()
	if desc.Name != "workbench_workspace_submit_mr" {
		t.Fatalf("descriptor=%+v", desc)
	}

	var auth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization")+"|"+r.Header.Get("PRIVATE-TOKEN"))
		switch {
		case strings.Contains(r.URL.Path, "merge_request/9"):
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "merge_requests"):
			_ = json.NewEncoder(w).Encode([]gongfengOpenMR{{ID: 9, IID: 3, SourceBranch: "work", TargetBranch: "main"}})
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()
	client := upstream.Client()
	if open, ok := findOpenGongfengMR(context.Background(), client, "token", upstream.URL, "work", "main"); !ok || open.IID != 3 {
		t.Fatalf("open MR=%+v %v", open, ok)
	}
	if err := updateGongfengMR(context.Background(), client, "token", upstream.URL, 9, "title", "body"); err != nil {
		t.Fatal(err)
	}
	if err := updateGongfengMR(context.Background(), client, "token", upstream.URL, 0, "title", "body"); err == nil {
		t.Fatal("zero MR id updated")
	}
	retry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") == "Bearer token" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer retry.Close()
	resp, _, err := gongfengPostOrGet(context.Background(), retry.Client(), http.MethodGet, retry.URL, "token", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("auth retry=%v %v", resp, err)
	}
}

func TestWorkspaceSubmitMRValidationBranches(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "cva-mr-no-token"})
	noToken, _ := st.UserByLoginName("cva-mr-no-token")
	_ = st.PutUser(domain.User{LoginName: "cva-mr-token", GitToken: "test-token"})
	withToken, _ := st.UserByLoginName("cva-mr-token")
	runID := "cva-mr-run"
	run := domain.Run{ID: runID, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "sop"}}
	_ = st.PutRun(run)
	gateway := &agentToolGateway{deps: &deps{repo: st}}
	session := &AgentToolSession{User: noToken, UserID: noToken.ID}
	if _, err := gateway.workspaceSubmitMR(context.Background(), session, map[string]any{"title": "x"}); err == nil {
		t.Fatal("submit without workspace accepted")
	}
	session.workspaceRunID = runID
	if _, err := gateway.workspaceSubmitMR(context.Background(), session, map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "对话工作区") {
		t.Fatalf("SOP submit err=%v", err)
	}
	run.Kind = domain.RunKindWorkspace
	run.WorkflowSnapshot.CollaborationMode = ""
	run.Repositories = []domain.RepositoryContext{{RepositoryID: "repo", Name: "app"}}
	_ = st.PutRun(run)
	for _, args := range []map[string]any{
		{},
		{"title": strings.Repeat("x", 201)},
		{"title": "x", "description": strings.Repeat("d", 20001)},
		{"title": "x", "targetBranch": "../bad"},
	} {
		if _, err := gateway.workspaceSubmitMR(context.Background(), session, args); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	if _, err := gateway.workspaceSubmitMR(context.Background(), session, map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "Git Token") {
		t.Fatalf("missing token err=%v", err)
	}
	session.User, session.UserID = withToken, withToken.ID
	if _, err := gateway.workspaceSubmitMR(context.Background(), session, map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "执行引擎") {
		t.Fatalf("missing engine err=%v", err)
	}
	session.User = domain.User{ID: 999999}
	session.UserID = 999999
	if _, err := gateway.workspaceSubmitMR(context.Background(), session, map[string]any{"title": "x"}); err == nil {
		t.Fatal("missing requester accepted")
	}
}

func TestMCPImportFilesAndServerBranches(t *testing.T) {
	st := cvAStore(t)
	projectID := "cva-r3-mcp-p"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "P"})
	h := &mcpHandlers{deps: &deps{repo: st}}

	c, rec := cvAContext(t, http.MethodPost, "/api/mcp/import", `{`, nil)
	h.mcpImport(c)
	if rec.Code != 400 {
		t.Fatalf("bad import=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/mcp/import", `{"mcpServers":{"bad":{"transport":"invalid"}}}`, nil)
	h.mcpImport(c)
	if rec.Code != 400 {
		t.Fatalf("invalid import=%d %s", rec.Code, rec.Body.String())
	}
	body := `{"mcpServers":{"cva-r3-import":{"name":"Imported","transport":"http","url":"https://example.test/mcp","scope":"project","projectId":"` + projectID + `"}}}`
	c, rec = cvAContext(t, http.MethodPost, "/api/mcp/import", body, nil)
	h.mcpImport(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"imported":1`) {
		t.Fatalf("import=%d %s", rec.Code, rec.Body.String())
	}

	custom := domain.MCPServerDefinition{ID: "cva-r3-files", Name: "Files", Scope: "project", ProjectID: projectID, Transport: "http", URL: "https://example.test/files"}
	if err := st.PutMCPServer(custom); err != nil {
		t.Fatal(err)
	}
	upload := func(paths []string, names []string) (*gin.Context, *httptest.ResponseRecorder) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		for _, path := range paths {
			_ = writer.WriteField("paths", path)
		}
		for i, name := range names {
			part, _ := writer.CreateFormFile("files", name)
			_, _ = part.Write([]byte("content-" + string(rune('a'+i))))
		}
		_ = writer.Close()
		c, rec := cvAContext(t, http.MethodPost, "/api/mcp/cva-r3-files/files", "", nil)
		c.Params = gin.Params{{Key: "id", Value: custom.ID}}
		c.Request.Body = http.NoBody
		c.Request = httptest.NewRequest(http.MethodPost, "/api/mcp/cva-r3-files/files", bytes.NewReader(buf.Bytes()))
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return c, rec
	}
	c, rec = upload([]string{"src/a.txt"}, []string{"a.txt"})
	h.mcpFiles(c)
	if rec.Code != 200 {
		t.Fatalf("file upload=%d %s", rec.Code, rec.Body.String())
	}
	if content, ok := st.MCPFileContent(custom.ID, "a.txt"); !ok || string(content) != "content-a" {
		t.Fatalf("stored file=%q %v", content, ok)
	}
	c, rec = upload([]string{"one", "two"}, []string{"a.txt"})
	h.mcpFiles(c)
	if rec.Code != 400 {
		t.Fatalf("path mismatch=%d", rec.Code)
	}
	c, rec = upload([]string{"same.txt", "same.txt"}, []string{"a.txt", "b.txt"})
	h.mcpFiles(c)
	if rec.Code != 409 {
		t.Fatalf("duplicate paths=%d %s", rec.Code, rec.Body.String())
	}

	c, rec = cvAContext(t, http.MethodGet, "/api/mcp-servers", "", nil)
	h.mcpServers(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), custom.ID) {
		t.Fatalf("servers=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodDelete, "/api/mcp-servers?id=missing", "", nil)
	h.mcpServers(c)
	if rec.Code != 404 {
		t.Fatalf("delete missing=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPut, "/api/mcp-servers", `{"id":"cva-r3-files","name":"Updated","transport":"http","url":"https://example.test/new"}`, nil)
	h.mcpServers(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Updated") {
		t.Fatalf("update=%d %s", rec.Code, rec.Body.String())
	}
}

func TestBrowserStreamEndpointAndLoops(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "cva-r3-browser"})
	user, _ := st.UserByLoginName("cva-r3-browser")
	projectID := "cva-r3-browser-p"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "P", OwnerID: user.ID})
	run := domain.Run{ID: "cva-r3-browser-run", Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: projectID}, Nodes: map[string]*domain.NodeRun{}}
	_ = st.PutRun(run)
	en := engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := &browserHandlers{deps: &deps{repo: st, engine: en}}

	router := gin.New()
	router.GET("/stream/:id", func(c *gin.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestUserContextKey{}, domainUserToAuth(user)))
		h.stream(c)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/stream/"+run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	kind, raw, err := ws.ReadMessage()
	if err != nil || kind != websocket.TextMessage || !strings.Contains(string(raw), `"type":"state"`) {
		t.Fatalf("initial stream kind=%d body=%s err=%v", kind, raw, err)
	}
	_ = ws.Close()

	c, rec := cvAContext(t, http.MethodGet, "/stream/missing", "", &user)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.stream(c)
	if rec.Code != 404 {
		t.Fatalf("missing stream=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodGet, "/stream/"+run.ID, "", nil)
	c.Params = gin.Params{{Key: "id", Value: run.ID}}
	h.stream(c)
	if rec.Code != 401 {
		t.Fatalf("anonymous stream=%d", rec.Code)
	}
}

func TestAssistantTaskGenerationAndWriterBranches(t *testing.T) {
	st := cvAStore(t)
	projectID := "cva-r3-task-p"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "P"})
	_ = st.PutUser(domain.User{LoginName: "cva-r3-task", CodeBuddyAPIKey: "key"})
	user, _ := st.UserByLoginName("cva-r3-task")
	h := &assistantHandlers{deps: &deps{repo: st}}

	task := assistantTask{ID: "cva-r3-empty", Name: "Empty", Prompt: "P", ProjectID: projectID, Enabled: true, IntervalSec: 60}
	h.generate = func(context.Context, string, domain.User) (string, error) { return "   ", nil }
	if err := h.executeAssistantTask(context.Background(), user, &task); err == nil || task.LastStatus != "error" || task.LastError == "" {
		t.Fatalf("empty result=%+v %v", task, err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	task.ID = "cva-r3-cancel"
	h.generate = func(context.Context, string, domain.User) (string, error) { return "unused", nil }
	if err := h.executeAssistantTask(cancelCtx, user, &task); err == nil || !strings.Contains(task.LastError, "停止") {
		t.Fatalf("cancelled=%+v %v", task, err)
	}
	task.ID, task.LastRunAt = "cva-r3-due", time.Now().Add(-2*time.Minute)
	task.LastStatus = "idle"
	if err := saveAssistantTask(st, user.ID, task); err != nil {
		t.Fatal(err)
	}
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return "summary\n\n<!-- notification-result:{\"title\":\"T\",\"summary\":\"S\",\"body\":\"B\"} -->", nil
	}
	h.tickAssistantTasks()
	due, _ := loadAssistantTask(st, user.ID, task.ID)
	if due.LastStatus != "ok" || due.NotificationResult == "" {
		t.Fatalf("due task=%+v", due)
	}
	h.runStoredAssistantTask(user, task)

	writer := &generationHTTPWriter{header: http.Header{}}
	writer.Header().Set("X-Test", "1")
	writer.WriteHeader(202)
	large := bytes.Repeat([]byte("x"), 20<<10)
	n, err := writer.Write(large)
	writer.Flush()
	if err != nil || n != len(large) || writer.status != 202 || writer.body.Len() != len(large) {
		t.Fatalf("writer status=%d n=%d body=%d err=%v", writer.status, n, writer.body.Len(), err)
	}
	_, _ = writer.Write([]byte("ignored"))
	if writer.body.Len() != len(large) {
		t.Fatal("writer exceeded cap behavior")
	}
}

func TestClarifyFailureAndQueueBranches(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "cva-r3-clarify-p", Name: "P"})
	_ = st.PutIssue(domain.Issue{ID: "cva-r3-clarify-i", ProjectID: "cva-r3-clarify-p", Title: "I"})
	h := &streamHandlers{deps: &deps{repo: st}}
	h.jobs = newClarifyHub(h)
	h.jobs.exec = func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		return nil, "", nil, errors.New("runner failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &clarifyJob{issueID: "missing", done: make(chan struct{}), cancel: cancel, subs: map[chan clarifyJobEvent]struct{}{}}
	h.runClarifyJob(ctx, job, "missing", chatClarifyRequest{})
	if job.err != "issue not found" {
		t.Fatalf("missing issue err=%q", job.err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	job = &clarifyJob{issueID: "cva-r3-clarify-i", done: make(chan struct{}), cancel: cancel, subs: map[chan clarifyJobEvent]struct{}{}}
	h.runClarifyJob(ctx, job, job.issueID, chatClarifyRequest{})
	if job.err != "runner failed" {
		t.Fatalf("runner err=%q", job.err)
	}

	h.jobs.tokens <- struct{}{}
	queueCtx, queueCancel := context.WithCancel(context.Background())
	queueCancel()
	composer := &clarifyJob{issueID: "composer:cancel", done: make(chan struct{}), cancel: func() {}, subs: map[chan clarifyJobEvent]struct{}{}}
	h.runComposerClarifyJob(queueCtx, composer, domain.Issue{}, chatClarifyRequest{})
	<-h.jobs.tokens
	if composer.err != "分析已取消" {
		t.Fatalf("composer cancel=%q", composer.err)
	}
	if h.cancelClarifyJob("missing") {
		t.Fatal("missing clarify job canceled")
	}
}

func TestAssistantRecoveryBrowserAndBotAccess(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		ok   bool
		text string
	}{
		{`{"message":"hello","drafts":[]}`, true, "hello"},
		{"prefix {\"message\":\"embedded\",\"drafts\":[]} suffix", true, "embedded"},
		{`{"drafts":[]}`, false, ""},
		{`{"message":"mcp__agentflow_session__submit_assistant_reply 参数如下","drafts":[]}`, false, ""},
		{"plain answer", true, "plain answer"},
		{"", false, ""},
	} {
		reply, ok := recoverNativeAssistantReply(tc.raw, "p")
		if ok != tc.ok || ok && !strings.Contains(reply.Message, tc.text) {
			t.Errorf("recover %q = %+v %v", tc.raw, reply, ok)
		}
	}
	if !assistantProgressOnly("稍等，我先检查", nil) || assistantProgressOnly("已完成检查", nil) {
		t.Fatal("progress classification")
	}
	if lastAssistantUserContent([]assistantMessage{{Role: "user", Content: "one"}, {Role: "assistant", Content: "a"}, {Role: "user", Content: "two"}}) != "two" {
		t.Fatal("last user content")
	}

	gateway := &agentToolGateway{}
	if _, err := gateway.callMainBotBrowser(context.Background(), &AgentToolSession{}, "workbench_browser_snapshot", nil); !errors.Is(err, errAgentToolForbidden) {
		t.Fatalf("browser without workspace=%v", err)
	}

	st := cvAStore(t)
	h := &assistantHandlers{deps: &deps{repo: st}}
	bot := domain.NotificationBot{ID: "cva-r3-bot", BotID: "bot", ProjectID: "p", Enabled: true, InteractionEnabled: true}
	if _, err := h.botExecutionUser(bot, "sender"); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("missing bot err=%v", err)
	}
	if err := st.SaveNotificationBot(bot, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.botExecutionUser(bot, "sender"); err == nil || !strings.Contains(err.Error(), "绑定") {
		t.Fatalf("unbound err=%v", err)
	}
}

func TestGenerationLifecycleAndValidation(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st := cvAStore(t)
	project := domain.Project{ID: "cva-r3-gen-p", Name: "P"}
	_ = st.PutProject(project)
	_ = st.PutUser(domain.User{LoginName: "cva-r3-gen", CodeBuddyAPIKey: "key"})
	user, _ := st.UserByLoginName("cva-r3-gen")
	project.OwnerID = user.ID
	_ = st.PutProject(project)
	h := &assistantHandlers{deps: &deps{repo: st}}
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return "generated response", nil
	}
	cases := []struct {
		name string
		body string
		code int
	}{
		{"anonymous", `{"messages":[{"role":"user","content":"x"}]}`, 401},
		{"invalid json", `{`, 400},
		{"empty", `{}`, 400},
		{"last assistant", `{"messages":[{"role":"assistant","content":"x"}]}`, 400},
		{"empty content", `{"messages":[{"role":"user","content":" "}]}`, 400},
		{"invalid role", `{"messages":[{"role":"system","content":"x"},{"role":"user","content":"x"}]}`, 400},
		{"fake trigger", `{"messages":[{"role":"user","content":"x","trigger":"fake"}]}`, 400},
		{"invalid session", `{"sessionId":"../bad","messages":[{"role":"user","content":"x"}]}`, 400},
		{"bad effort", `{"model":"claude-sonnet-4-5","effort":"impossible","messages":[{"role":"user","content":"x"}]}`, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var who *domain.User
			if tc.name != "anonymous" {
				who = &user
			}
			c, rec := cvAContext(t, http.MethodPost, "/api/assistant/generations", tc.body, who)
			h.startGeneration(c)
			if rec.Code != tc.code {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	sessionID := "cva-r3-generation"
	c, rec := cvAContext(t, http.MethodPost, "/api/assistant/generations", `{"sessionId":"`+sessionID+`","projectId":"`+project.ID+`","messages":[{"role":"user","content":"generate now"}]}`, &user)
	h.startGeneration(c)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		g, ok := st.AssistantGeneration(user.ID, sessionID)
		if ok && g.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/generations/"+sessionID+"?after=0", "", &user)
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	h.generation(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "completed") {
		t.Fatalf("get generation=%d %s", rec.Code, rec.Body.String())
	}
	c, rec = cvAContext(t, http.MethodGet, "/api/assistant/generations/missing", "", &user)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.generation(c)
	if rec.Code != 404 {
		t.Fatalf("missing generation=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodPost, "/api/assistant/generations/"+sessionID+"?generationId=wrong", "", &user)
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	h.generation(c)
	if rec.Code != 409 {
		t.Fatalf("cancel completed=%d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentToolHTTPProtocol(t *testing.T) {
	st := cvAStore(t)
	h := &agentToolGateway{deps: &deps{repo: st}}
	session := &AgentToolSession{
		Mode:      AgentToolAssistant,
		ProjectID: "cva-r3-tools",
		ExpiresAt: time.Now().Add(time.Minute),
		Final:     make(chan AgentTerminalResult, 1),
	}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(method, token, body string) *httptest.ResponseRecorder {
		c, rec := cvAContext(t, method, "/api/agent-tools/"+token, body, nil)
		c.Params = gin.Params{{Key: "token", Value: token}}
		h.ServeHTTP(c)
		return rec
	}
	if rec := call(http.MethodPost, "missing", `{}`); rec.Code != 401 {
		t.Fatalf("missing token=%d", rec.Code)
	}
	if rec := call(http.MethodGet, session.Token, ""); rec.Code != 405 || rec.Header().Get("Allow") == "" {
		t.Fatalf("method=%d", rec.Code)
	}
	if rec := call(http.MethodPost, session.Token, `{`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "parse error") {
		t.Fatalf("parse=%d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"unknown"}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"unknown_tool","arguments":{}}}`,
	} {
		rec := call(http.MethodPost, session.Token, body)
		if rec.Code != 200 {
			t.Fatalf("rpc=%d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := call(http.MethodPost, session.Token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); rec.Code != 200 && rec.Code != 202 {
		t.Fatalf("notification=%d", rec.Code)
	}
	if rec := call(http.MethodDelete, session.Token, ""); rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("delete=%d", rec.Code)
	}
}

func TestRemoteWorkspaceEditing(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const runnerID = "cvb-r2-edit-runner"
	if err := f.s.PutRunner(domain.Runner{ID: runnerID, RunnerVersion: "9.0.0", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "cvb-r2-remote-edit", Status: "succeeded", Nodes: map[string]*domain.NodeRun{},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ResponsibleUserID: f.user.ID,
			ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: runnerID}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcquireWorkspaceEditor(run.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}

	hub := newRunnerControlHub()
	conn := &runnerControlConn{runnerID: runnerID, send: make(chan runnerControlMessage, 20), done: make(chan struct{})}
	hub.conns[runnerID] = conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for message := range conn.send {
			req := message.WorkspaceRequest
			if req.Path == "remote-error.txt" {
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Error: "remote rejected"})
				continue
			}
			if req.Path == "invalid-response.txt" {
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Payload: []byte(`{`)})
				continue
			}
			if req.Terminal != nil && req.Terminal.Data == "fail" {
				hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Error: "terminal rejected"})
				continue
			}
			payload := []byte(`{"sha256":"remote-sha"}`)
			if req.Operation == domain.RunnerWorkspaceOperationTerminal {
				payload = []byte(`{"id":"terminal-one","ok":true}`)
			}
			hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Payload: payload})
		}
	}()
	t.Cleanup(func() {
		close(conn.send)
		<-done
	})

	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerControls: hub}
	router := gin.New()
	router.PUT("/api/runs/:id/workspace/file", h.workspaceWrite)
	router.POST("/api/runs/:id/workspace/terminal", h.workspaceTerminal)
	router.POST("/api/runs/:id/workspace/release", h.workspaceRelease)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := authedRequest(t, httptest.NewRequest(method, path, bytes.NewReader(raw)), f.user)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodPut, "/api/runs/"+run.ID+"/workspace/file",
		map[string]any{"path": "remote.txt", "content": "body"}); rec.Code != http.StatusOK {
		t.Fatalf("remote write = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, "/api/runs/"+run.ID+"/workspace/file",
		map[string]any{"path": "remote-error.txt", "content": "body"}); rec.Code != http.StatusConflict {
		t.Fatalf("remote write error = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPut, "/api/runs/"+run.ID+"/workspace/file",
		map[string]any{"path": "invalid-response.txt", "content": "body"}); rec.Code != http.StatusConflict {
		t.Fatalf("invalid remote response = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, "/api/runs/"+run.ID+"/workspace/terminal",
		map[string]any{"operation": "input", "id": "terminal-one", "data": "fail"}); rec.Code != http.StatusConflict {
		t.Fatalf("remote terminal error = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, "/api/runs/"+run.ID+"/workspace/terminal",
		map[string]any{"operation": "input", "id": "terminal-one", "data": "echo hi\n"}); rec.Code != http.StatusOK {
		t.Fatalf("remote terminal = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPost, "/api/runs/"+run.ID+"/workspace/release", nil); rec.Code != http.StatusOK {
		t.Fatalf("remote release = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRoomRunSynchronization(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-r2-sync-room", ProjectID: "platform", Name: "Sync room"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "cvb-r2-sync-agent", Name: "Named Agent"}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{Name: "Sync flow", Nodes: []domain.WorkflowNode{
		{ID: "failed", Name: "Failure", Type: "agent", AgentID: "cvb-r2-sync-agent"},
		{ID: "approval", Name: "Approval", Type: "approval", RejectTo: "failed"},
	}}
	run := domain.Run{ID: "cvb-r2-sync-finished", WorkflowID: "flow", WorkflowSnapshot: wf, Status: "failed",
		Nodes: map[string]*domain.NodeRun{"failed": {Status: "failed", Error: "boom"}, "approval": {Status: "succeeded"}}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	dispatch := domain.ChatMessage{ID: "cvb-r2-sync-dispatch", RoomID: room.ID, Kind: domain.ChatKindDispatch,
		RunID: run.ID, WorkflowName: wf.Name, CreatedAt: time.Now().Add(-time.Minute)}
	missingApproval := domain.ChatMessage{ID: "cvb-r2-missing-approval", RoomID: room.ID, Kind: domain.ChatKindApproval,
		RunID: "missing-run", NodeID: "approval", Attempt: 1}
	for _, msg := range []domain.ChatMessage{dispatch, missingApproval} {
		if err := f.s.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	h := &roomsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	got := h.syncRoomRuns(room, f.s.Messages(room.ID, 200))
	if len(got) < 4 {
		t.Fatalf("synchronized messages: %#v", got)
	}

	waiting := domain.Run{ID: "cvb-r2-sync-waiting", WorkflowSnapshot: wf, Status: "waiting",
		Nodes: map[string]*domain.NodeRun{"approval": {Status: "waiting_approval", Attempt: 2}}}
	if err := f.s.PutRun(waiting); err != nil {
		t.Fatal(err)
	}
	approval := domain.ChatMessage{ID: "cvb-r2-live-approval", RoomID: room.ID, Kind: domain.ChatKindApproval,
		RunID: waiting.ID, NodeID: "approval", Attempt: 2}
	if err := f.s.PutMessage(approval); err != nil {
		t.Fatal(err)
	}
	got = h.syncRoomRuns(room, f.s.Messages(room.ID, 200))
	found := false
	for _, msg := range got {
		if msg.ID == approval.ID && msg.CanReject && !msg.Resolved {
			found = true
		}
	}
	if !found {
		t.Fatalf("live approval not projected: %#v", got)
	}
	_ = h.syncRoomRuns(room, got) // exercise revision-cache hits.
}

func TestSpaceAIStartPaths(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	room := domain.ChatRoom{ID: "cvb-r2-ask-room", ProjectID: "platform", Name: "Ask"}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	rooms := &roomsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	call := func(status int, response string) domain.ChatMessage {
		api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(response))
		})
		h := &spacesHandlers{deps: &deps{repo: f.s, engine: f.e, api: api}, rooms: rooms}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = authedRequest(t, httptest.NewRequest(http.MethodPost, "/ask", nil), f.user)
		return h.askAI(c, room, domain.RequirementSpace{ID: room.ID}, domain.ChatMessage{
			ID: "question", UserID: f.user.ID, Author: "User", Body: "Help", CreatedAt: time.Now(),
		}, "model", "ask", "")
	}
	if reply := call(http.StatusServiceUnavailable, `{"error":"model unavailable"}`); reply.AIStatus != domain.AIStatusFailed ||
		!strings.Contains(reply.AIError, "unavailable") {
		t.Fatalf("failed AI reply: %+v", reply)
	}
	if reply := call(http.StatusAccepted, `{}`); reply.AIStatus != domain.AIStatusRunning || reply.AISessionID == "" {
		t.Fatalf("accepted AI reply: %+v", reply)
	}
}

func TestRunnerAdditionalSuccessAndDecodePaths(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	runnerID, session := registerTestRunner(t, f.h)
	auth := map[string]string{"Authorization": "Bearer " + session}
	runner, _ := f.s.Runner(runnerID)
	runner.OwnerUserID = f.user.ID
	if err := f.s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodGet, "/api/runners?mine=1", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("mine = %d: %s", rec.Code, rec.Body.String())
	}
	reregister := requestWithHeaders(t, f.h, http.MethodPost, "/runners/register", map[string]any{
		"runnerId": runnerID, "name": "updated", "maxConcurrency": 0,
		"runtimes": []domain.RunnerRuntime{{Provider: "codebuddy", Status: domain.RuntimeStatusOnline}},
	}, map[string]string{"X-Bootstrap-Token": "boot-secret"})
	if reregister.Code != http.StatusOK {
		t.Fatalf("reregister = %d: %s", reregister.Code, reregister.Body.String())
	}
	var rotated struct {
		SessionToken string `json:"sessionToken"`
	}
	if json.Unmarshal(reregister.Body.Bytes(), &rotated) != nil || rotated.SessionToken == "" {
		t.Fatalf("rotated session: %s", reregister.Body.String())
	}
	auth = map[string]string{"Authorization": "Bearer " + rotated.SessionToken}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/previews/anything/complete", "ignored", auth); rec.Code != http.StatusOK {
		t.Fatalf("preview complete = %d: %s", rec.Code, rec.Body.String())
	}
	reqMalformed := httptest.NewRequest(http.MethodPost, "/runners/tasks/anything/complete", strings.NewReader("{"))
	reqMalformed.Header.Set("Content-Type", "application/json")
	reqMalformed.Header.Set("Authorization", "Bearer "+rotated.SessionToken)
	recMalformed := httptest.NewRecorder()
	f.h.ServeHTTP(recMalformed, reqMalformed)
	if recMalformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed completion = %d", recMalformed.Code)
	}

	for _, item := range []struct{ method, path string }{
		{http.MethodPut, "/api/runners/" + runnerID + "/run-limit"},
		{http.MethodPut, "/api/runners/default"},
		{http.MethodPut, "/api/runners/runtimes"},
		{http.MethodPost, "/runners/heartbeat"},
		{http.MethodPost, "/runners/tasks/claim"},
		{http.MethodPost, "/api/runners/tasks"},
		{http.MethodPost, "/runners/register"},
	} {
		headers := auth
		if item.path == "/runners/register" {
			headers = map[string]string{"X-Bootstrap-Token": "boot-secret"}
		}
		req := httptest.NewRequest(item.method, item.path, strings.NewReader("{"))
		req.Header.Set("Content-Type", "application/json")
		if strings.HasPrefix(item.path, "/api/") {
			req = authedRequest(t, req, f.user)
		}
		for key, value := range headers {
			if strings.EqualFold(key, "Authorization") && strings.HasPrefix(item.path, "/api/") {
				continue
			}
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("malformed request unexpectedly succeeded: %s", item.path)
		}
	}

	created := cvBRequest(t, f, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock", "prompt": "complete successfully", "runnerId": runnerID,
	}, true)
	var task domain.RunnerTask
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &task) != nil {
		t.Fatalf("create task = %d: %s", created.Code, created.Body.String())
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/claim",
		map[string]any{"availableRuntimes": []string{"mock"}, "noWait": true}, auth); rec.Code != http.StatusOK {
		t.Fatalf("claim task = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/start", nil, auth); rec.Code != http.StatusOK {
		t.Fatalf("start task = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/events",
		map[string]any{"events": []any{}}, auth); rec.Code != http.StatusOK {
		t.Fatalf("empty events = %d: %s", rec.Code, rec.Body.String())
	}
	exit := 0
	if rec := requestWithHeaders(t, f.h, http.MethodPost, "/runners/tasks/"+task.ID+"/complete",
		map[string]any{"status": domain.RunnerTaskSuccess, "exitCode": exit, "result": "done"}, auth); rec.Code != http.StatusOK {
		t.Fatalf("complete task = %d: %s", rec.Code, rec.Body.String())
	}
	longTask := cvBRequest(t, f, http.MethodPost, "/api/runners/tasks", map[string]any{
		"provider": "mock", "prompt": strings.Repeat("p", 600), "runnerId": runnerID,
	}, true)
	if longTask.Code != http.StatusCreated {
		t.Fatalf("create long task = %d", longTask.Code)
	}
	if listed := cvBRequest(t, f, http.MethodGet, "/api/runners/tasks", nil, true); listed.Code != http.StatusOK {
		t.Fatalf("list long task = %d", listed.Code)
	}
}

func TestCatalogWorkflowOperations(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const sandboxID = "cvb-r2-ops-sandbox"
	if err := f.s.PutSandbox(domain.SandboxProfile{ID: sandboxID, Name: "Ops sandbox", ProjectID: "platform", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	makeWorkflow := func(id string) domain.WorkflowDefinition {
		wf := domain.WorkflowDefinition{ID: id, Name: id, ProjectID: "platform", SandboxProfileID: sandboxID, Version: 1}
		if err := f.s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
		stored, _ := f.s.Workflow(id)
		stored.Name += " updated"
		if err := f.s.PutWorkflow(stored); err != nil {
			t.Fatal(err)
		}
		stored, _ = f.s.Workflow(id)
		return stored
	}
	wf := makeWorkflow("cvb-r2-ops")
	revisions := f.s.Revisions("workflow", wf.ID)
	if len(revisions) == 0 {
		t.Fatal("workflow revision missing")
	}
	cases := []struct {
		method, path string
		body         any
		want         int
	}{
		{http.MethodGet, "/api/workflows/" + wf.ID + "/revisions/" + revisions[0].ID, nil, http.StatusOK},
		{http.MethodGet, "/api/workflows/" + wf.ID + "/revisions/missing", nil, http.StatusNotFound},
		{http.MethodPost, "/api/workflows/" + wf.ID + "/clone", map[string]any{}, http.StatusBadRequest},
		{http.MethodPost, "/api/workflows/" + wf.ID + "/clone", map[string]any{"id": "cvb-r2-clone", "name": "Clone"}, http.StatusCreated},
		{http.MethodPost, "/api/workflows/" + wf.ID + "/publish", nil, http.StatusOK},
	}
	for _, tc := range cases {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, true)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	stale := domain.WorkflowDefinition{ID: "cvb-r2-deleted-stale", Name: "stale", ProjectID: "platform",
		SandboxProfileID: sandboxID, Version: 1, Revision: 3}
	if rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", stale, true); rec.Code != http.StatusConflict {
		t.Fatalf("stale resurrection = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCloseRemoteWorkspaceResponseErrors(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	run := domain.Run{ID: "cvb-r2-close-errors", Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ExecutionMode: domain.ExecutionModeRunner, ExecutionRunnerID: "cvb-close-runner"}}
	hub := newRunnerControlHub()
	conn := &runnerControlConn{runnerID: "cvb-close-runner", send: make(chan runnerControlMessage, 2), done: make(chan struct{})}
	hub.conns[conn.runnerID] = conn
	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerControls: hub}
	go func() {
		message := <-conn.send
		hub.resolveWorkspace(message.RequestID, domain.RunnerWorkspaceResponse{Error: "cannot close"})
	}()
	if err := h.closeWorkspaceTerminals(context.Background(), run, f.user.ID); err == nil {
		t.Fatal("runner close response error accepted")
	}
}

func TestRunnerWorkspaceRequestCancellation(t *testing.T) {
	for _, closeConnection := range []bool{false, true} {
		hub := newRunnerControlHub()
		conn := &runnerControlConn{runnerID: "runner", send: make(chan runnerControlMessage), done: make(chan struct{})}
		hub.conns[conn.runnerID] = conn
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{Operation: "info"})
			result <- err
		}()
		<-conn.send // let requestWorkspace enter its response wait.
		if closeConnection {
			close(conn.done)
		} else {
			cancel()
		}
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("workspace request unexpectedly succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("workspace request did not cancel")
		}
		cancel()
	}
}

func TestRunnerAdditionalInternalBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	h := &runnersHandlers{deps: &deps{repo: f.s, engine: f.e}, runnerWaiters: newRunnerWaiterRegistry(),
		runnerControls: newRunnerControlHub(), runnerBindCodes: map[string]runnerBindCode{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/runners?mine=1", nil)
	h.runners(c)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous personal runners = %d", rec.Code)
	}
	if userID, ok := h.consumeRunnerBindCode("invalid"); ok || userID != 0 {
		t.Fatal("invalid bind code accepted")
	}
	h.runnerBindCodes["bind-expired"] = runnerBindCode{userID: 1, expiresAt: time.Now().Add(-time.Minute)}
	if _, ok := h.consumeRunnerBindCode("bind-expired"); ok {
		t.Fatal("expired bind code accepted")
	}
	h.runnerBindCodes["bind-valid"] = runnerBindCode{userID: f.user.ID, expiresAt: time.Now().Add(time.Minute)}
	if userID, ok := h.consumeRunnerBindCode("bind-valid"); !ok || userID != f.user.ID {
		t.Fatal("valid bind code rejected")
	}
	req := httptest.NewRequest(http.MethodPost, "/runners/register", nil)
	if _, ok := h.resolveRunnerRegistrant(req); ok {
		t.Fatal("credential-free registration accepted")
	}
	t.Setenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN", "bootstrap")
	req.Header.Set("X-Bootstrap-Token", "bootstrap")
	if owner, ok := h.resolveRunnerRegistrant(req); !ok || owner != 0 {
		t.Fatal("bootstrap registration rejected")
	}
	if _, _, err := newRunnerSessionToken(); err != nil {
		t.Fatal(err)
	}
	bearer := httptest.NewRequest(http.MethodPost, "/runners/register", nil)
	bearer.Header.Set("Authorization", "Bearer bootstrap")
	if owner, ok := h.resolveRunnerRegistrant(bearer); !ok || owner != 0 {
		t.Fatal("bearer bootstrap registration rejected")
	}
	h.runnerBindCodes["bind-cleanup"] = runnerBindCode{userID: 1, expiresAt: time.Now().Add(-time.Minute)}
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = authedRequest(t, httptest.NewRequest(http.MethodPost, "/bind", nil), f.user)
	h.issueRunnerBindCode(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("bind code issue = %d", rec.Code)
	}
}

func TestWorkspaceRemoteUnavailableAndRestoreErrors(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const runnerID = "cvb-r2-old-runner"
	if err := f.s.PutRunner(domain.Runner{ID: runnerID, RunnerVersion: "0.1.0", Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "cvb-r2-unavailable-edit", Status: "succeeded", Nodes: map[string]*domain.NodeRun{},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform", ExecutionMode: domain.ExecutionModeRunner,
			ExecutionRunnerID: runnerID}}
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := f.s.AcquireWorkspaceEditor(run.ID, f.user.ID); err != nil {
		t.Fatal(err)
	}
	h := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	router := gin.New()
	router.PUT("/api/runs/:id/workspace/file", h.workspaceWrite)
	router.POST("/api/runs/:id/workspace/terminal", h.workspaceTerminal)
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := authedRequest(t, httptest.NewRequest(method, path, bytes.NewReader(raw)), f.user)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodPut, "/api/runs/"+run.ID+"/workspace/file",
		map[string]any{"path": "x.txt", "content": "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("disconnected write = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, "/api/runs/"+run.ID+"/workspace/terminal",
		map[string]any{"operation": "input", "id": "x", "data": "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("old runner terminal = %d: %s", rec.Code, rec.Body.String())
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/restore", nil)
	_ = h.prepareRunActivationWorkspace(c, "missing")
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/restore", nil)
	c.Request.Header.Set("Prefer", "respond-async")
	_ = h.prepareRunActivationWorkspace(c, "missing")

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	if err := h.withWorkspaceWriter(c, "missing", f.user.ID, func(domain.Run) error { return nil }); err == nil {
		t.Fatal("missing workspace writer succeeded")
	}
}

func TestRunnerLongPollClientCancellation(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNNER_LONG_POLL_TIMEOUT", "5s")
	t.Setenv("AGENTFLOW_RUNNER_DB_POLL_INTERVAL", "1s")
	f := cvBNewHTTPFixture(t)
	_, session := registerTestRunner(t, f.h)
	raw, _ := json.Marshal(map[string]any{"availableRuntimes": []string{"never-available"}})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/runners/tasks/claim", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+session)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		done <- rec
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled runner poll did not return")
	}
}
