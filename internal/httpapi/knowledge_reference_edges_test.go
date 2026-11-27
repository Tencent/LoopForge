package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
)

func TestKnowledgeToolDescriptionAndCatalogPromptContext(t *testing.T) {
	gotDesc := knowledgeToolDescription("团队档案库", "weknora", "kc-1", "search_knowledge", "Search docs")
	for _, want := range []string{"[个人知识库：团队档案库 (weknora)]", "Search docs", "优先调用"} {
		if !strings.Contains(gotDesc, want) {
			t.Fatalf("knowledgeToolDescription() = %q, want containing %q", gotDesc, want)
		}
	}
	nonSearch := knowledgeToolDescription("团队档案库", "mcp", "kc-1", "wiki_tree", "List wiki tree")
	if strings.Contains(nonSearch, "优先调用") {
		t.Fatalf("non-search tool should not append search priority hint: %q", nonSearch)
	}

	var listDocsCalls atomic.Int32
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "notifications/initialized":
			w.WriteHeader(204)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "list_documents", "description": "List docs", "annotations": map[string]any{"readOnlyHint": true}},
					{"name": "search_knowledge", "description": "Search KB", "annotations": map[string]any{"readOnlyHint": true}},
				},
			}})
		case "tools/call":
			listDocsCalls.Add(1)
			<-gate
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": `{"documents":[{"file_name":"sf-doc.md","description":"文档"}]}`}},
			}})
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := knowledge.NewService(store, func() string { return "sf" })
	exec := knowledge.NewExecutor(svc)
	conn, err := svc.CreateConnector(context.Background(), 1, knowledge.ConnectorInput{
		Name:     "Singleflight KB",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: store, knowledgeSvc: svc, knowledgeExec: exec}
	refs := []domain.KnowledgeRef{{ConnectorID: conn.ID, Mode: "all"}}

	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = knowledgeReferencePromptContext(context.Background(), d, 1, refs)
		}(i)
	}
	close(gate)
	wg.Wait()

	if got := listDocsCalls.Load(); got != 1 {
		t.Fatalf("concurrent cold-start list_documents calls = %d, want 1", got)
	}
	ns := knowledgeToolNamespace(conn.ID)
	for i, res := range results {
		if !strings.Contains(res, "sf-doc.md") || !strings.Contains(res, ns+"__search_knowledge") {
			t.Fatalf("result[%d] missing expected catalog details: %q", i, res)
		}
	}
}

func TestKnowledgeProxyRPCHandleInitializeAndToolsList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpc)
		w.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "search_knowledge", "description": "Search KB", "annotations": map[string]any{"readOnlyHint": true}},
				},
			}})
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := knowledge.NewService(store, func() string { return "p" })
	exec := knowledge.NewExecutor(svc)
	conn, err := svc.CreateConnector(context.Background(), 1, knowledge.ConnectorInput{
		Name:     "Engineering KB",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	d := &deps{repo: store, knowledgeSvc: svc, knowledgeExec: exec}
	kh := &knowledgeHandlers{deps: d, svc: svc, exec: exec}
	claims := knowledge.ProxyClaims{ProjectID: "default", UserID: 1, Ref: &domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "all"}}

	initResp := kh.handleProxyRPC(context.Background(), claims, agentToolRPCRequest{ID: 1, Method: "initialize"})
	initRaw, _ := json.Marshal(initResp.Result)
	if !strings.Contains(string(initRaw), "Engineering KB") {
		t.Fatalf("unexpected initialize result: %s", initRaw)
	}
	listResp := kh.handleProxyRPC(context.Background(), claims, agentToolRPCRequest{ID: 2, Method: "tools/list"})
	listRaw, _ := json.Marshal(listResp.Result)
	for _, want := range []string{"[个人知识库：Engineering KB (mcp)]", "优先调用", `"readOnlyHint":true`} {
		if !strings.Contains(string(listRaw), want) {
			t.Fatalf("tools/list missing %q in: %s", want, listRaw)
		}
	}
}

func TestRunTelemetryAndClipRunTextKnowledgeEdges(t *testing.T) {
	ev := domain.TelemetryEvent{
		Name: "knowledge.call",
		Attributes: map[string]any{
			"knowledgeReadId": "kr-1",
			"attempt":         1,
			"query":           "端口",
			"targets":         []string{"部署规范.md"},
			"hits":            2,
			"origin":          domain.KnowledgeCallOriginModelTool,
			"largeSecretBlob": "should-be-dropped",
		},
	}
	slimmed := slimTelemetryEvent(ev)
	for _, keep := range []string{"knowledgeReadId", "attempt", "query", "targets", "hits", "origin"} {
		if _, ok := slimmed.Attributes[keep]; !ok {
			t.Fatalf("expected %q preserved on slim knowledge.call telemetry: %+v", keep, slimmed.Attributes)
		}
	}
	if _, leaked := slimmed.Attributes["largeSecretBlob"]; leaked {
		t.Fatalf("expected unlisted attribute dropped: %+v", slimmed.Attributes)
	}

	sample := strings.Repeat("中", 10) + " [[knowledge:ev_abc_1]] 后缀"
	clipped := clipRunText(sample, 20)
	if !utf8.ValidString(clipped) {
		t.Fatalf("clipped text invalid UTF-8: %q", clipped)
	}
	if strings.Contains(clipped, "[[knowledge:") && !strings.Contains(clipped, "]]") {
		t.Fatalf("clipRunText must not leave half-clipped citation marker: %q", clipped)
	}
}

func TestChatSessionKnowledgeForkPromptAndProxyGuardEdges(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "proxy-guard-secret")
	msgs := []assistantMessage{{
		Role:           "assistant",
		Content:        `{"message":"ok"}`,
		KnowledgeReads: []domain.KnowledgeRead{{ID: "kr-prompt", Status: domain.KnowledgeStatusHit}},
	}}
	forPrompt := assistantPromptMessages(msgs)
	if len(forPrompt[0].KnowledgeReads) != 0 {
		t.Fatalf("assistantPromptMessages must clear KnowledgeReads, got %+v", forPrompt[0].KnowledgeReads)
	}

	h, a, b, _, s := sharedWorkbenchFixture(t)
	wantRefs := []domain.KnowledgeRef{{ConnectorID: "wk-1", Mode: "allowlist", Sources: []string{"kb1"}}}
	s.Messages = append(s.Messages, domain.AssistantMessage{Role: "user", Content: "原始问题"})
	if err := h.repo.PutAssistantSession(a.ID, s); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetAssistantSessionKnowledge(a.ID, s.ID, wantRefs); err != nil {
		t.Fatal(err)
	}
	s, _ = h.repo.AssistantSession(a.ID, s.ID)
	body, _ := json.Marshal(gin.H{"messageIndex": 1, "content": "同人分叉问题", "updatedAt": s.UpdatedAt})
	cOwn, wOwn := workbenchContext(a, "POST", "/", bytes.NewBuffer(body))
	cOwn.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.forkSession(cOwn)
	if wOwn.Code != 201 {
		t.Fatalf("same-user forkSession HTTP %d: %s", wOwn.Code, wOwn.Body.String())
	}
	var ownBranch domain.AssistantSession
	if err := json.Unmarshal(wOwn.Body.Bytes(), &ownBranch); err != nil {
		t.Fatal(err)
	}
	if !ownBranch.KnowledgeRestricted || !reflect.DeepEqual(ownBranch.KnowledgeRefs, wantRefs) {
		t.Fatalf("same-user forked branch lost KB config: restricted=%v refs=%+v", ownBranch.KnowledgeRestricted, ownBranch.KnowledgeRefs)
	}

	// Cross-user fork must keep the privacy watermark without exposing or copying creator's personal KnowledgeRefs.
	crossBody, _ := json.Marshal(gin.H{"messageIndex": 1, "content": "跨人分叉问题", "updatedAt": s.UpdatedAt})
	c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(crossBody))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.forkSession(c)
	if w.Code != 201 {
		t.Fatalf("cross-user forkSession HTTP %d: %s", w.Code, w.Body.String())
	}
	var branch domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &branch); err != nil {
		t.Fatal(err)
	}
	if !branch.KnowledgeRestricted || len(branch.KnowledgeRefs) != 0 {
		t.Fatalf("cross-user forked branch leaked KB refs or dropped watermark: restricted=%v refs=%+v", branch.KnowledgeRestricted, branch.KnowledgeRefs)
	}

	g := domain.AssistantGeneration{
		ID:        "gen-stop",
		StartedAt: time.Now().UTC(),
		Error:     "生成已停止",
		Stream: &domain.AssistantStreamSnapshot{
			Answer: "半截回答 [[knowledge:ev_1]] 与 `[[knowledge:ev_code]]`",
			Activity: []domain.AssistantActivityEvent{
				{Kind: "text", Text: "步骤说明 [[knowledge:ev_2]]"},
			},
		},
	}
	stoppedMsg, ok := turnTranscriptMessage(g, "model-x", true)
	if !ok {
		t.Fatal("expected transcript message")
	}
	if strings.Contains(stoppedMsg.Content, "[[knowledge:ev_1]]") {
		t.Fatalf("expected unresolved citation stripped from stopped reply content: %s", stoppedMsg.Content)
	}
	if !strings.Contains(stoppedMsg.Content, "`[[knowledge:ev_code]]`") {
		t.Fatalf("expected inline code citation preserved: %s", stoppedMsg.Content)
	}
	if strings.Contains(stoppedMsg.Activity[0].Text, "[[knowledge:ev_2]]") {
		t.Fatalf("expected unresolved citation stripped from stopped narration: %s", stoppedMsg.Activity[0].Text)
	}

	// proxyMCP must enforce running-node & attempt guard when RunID+NodeID are claimed.
	_ = h.repo.PutRun(domain.Run{
		ID:               "run-proxy-guard",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "default", ResponsibleUserID: 7},
		Nodes:            map[string]*domain.NodeRun{"n1": {NodeID: "n1", Status: "succeeded", Attempt: 1}},
	})
	tok, err := knowledge.SignProxyToken(knowledge.ProxyClaims{
		RunID: "run-proxy-guard", NodeID: "n1", Attempt: 1, ProjectID: "default", UserID: 7,
		Ref: &domain.KnowledgeRef{ConnectorID: "wk-1", Mode: "all"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(rec)
	gc.Request = httptest.NewRequest(http.MethodPost, knowledge.ProxyPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	gc.Request.Header.Set("Authorization", "Bearer "+tok)
	kh := &knowledgeHandlers{deps: &deps{repo: h.repo}}
	kh.proxyMCP(gc)
	if rec.Code != 403 {
		t.Fatalf("expected non-running node proxy token rejected with 403, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestSessionUpdateAllowsDisabledOrDeletedHistoricalKnowledgeRefs(t *testing.T) {
	s, h, _, mcpURL := newKnowledgeTestServer(t)

	_ = s.PutUser(domain.User{ID: 401, LoginName: "alice"})
	_ = s.PutUser(domain.User{ID: 402, LoginName: "bob"})
	alice, _ := s.User(401)
	bob, _ := s.User(402)
	_ = s.PutProject(domain.Project{
		ID:      "p-receipt",
		Name:    "Receipt Project",
		OwnerID: alice.ID,
		Members: []domain.ProjectMember{{UserID: bob.ID, Role: "member"}},
	})

	createRec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/connectors", `{"name":"Alice Docs","type":"mcp","endpoint":"`+mcpURL+`"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create connector = %d (%s)", createRec.Code, createRec.Body.String())
	}
	var conn domain.KnowledgeConnector
	if err := json.Unmarshal(createRec.Body.Bytes(), &conn); err != nil {
		t.Fatal(err)
	}

	sessionID := "sess-receipt-kb"
	initSess := domain.AssistantSession{
		ID:        sessionID,
		UserID:    alice.ID,
		ProjectID: "p-receipt",
		Title:     "Receipt Chat",
		Messages: []domain.AssistantMessage{
			{Role: "user", Content: "创建需求", AuthorID: alice.ID, AuthorName: alice.LoginName},
			{Role: "assistant", Content: `{"message":"请确认","drafts":[{"tool":"issues_save","body":{"title":"t1"}}]}`},
		},
	}
	if err := s.PutAssistantSession(alice.ID, initSess); err != nil {
		t.Fatal(err)
	}
	bindRec := doJSONAsUser(t, h, alice, http.MethodPut, "/api/assistant/sessions/"+sessionID+"/knowledge", `{"knowledgeRefs":[{"connectorId":"`+conn.ID+`","mode":"all"}]}`)
	if bindRec.Code != http.StatusOK {
		t.Fatalf("bind knowledge = %d (%s)", bindRec.Code, bindRec.Body.String())
	}

	putReceipt := func(u domain.User, savedID string) *httptest.ResponseRecorder {
		t.Helper()
		cur, ok := s.AssistantSession(alice.ID, sessionID)
		if !ok {
			t.Fatal("session missing")
		}
		cur.Messages[1].Content = `{"message":"请确认","drafts":[{"tool":"issues_save","body":{"title":"t1"},"saved":true,"savedId":"` + savedID + `"}]}`
		raw, _ := json.Marshal(cur)
		return doJSONAsUser(t, h, u, http.MethodPut, "/api/assistant/sessions/"+sessionID, string(raw))
	}

	// 1. Connector enabled -> receipt save succeeds.
	if rec := putReceipt(alice, "iss-enabled"); rec.Code != http.StatusOK {
		t.Fatalf("receipt save while enabled = %d (%s)", rec.Code, rec.Body.String())
	}

	// 2. Disable connector -> receipt saves by creator and project member still succeed,
	//    while explicitly rebinding the disabled connector via /knowledge still fails (400).
	disableRec := doJSONAsUser(t, h, alice, http.MethodPut, "/api/knowledge/connectors/"+conn.ID, `{"name":"Alice Docs","type":"mcp","endpoint":"`+mcpURL+`","enabled":false}`)
	if disableRec.Code != http.StatusOK {
		t.Fatalf("disable connector = %d (%s)", disableRec.Code, disableRec.Body.String())
	}
	if rec := putReceipt(alice, "iss-disabled-owner"); rec.Code != http.StatusOK {
		t.Fatalf("owner receipt save after disable = %d (%s)", rec.Code, rec.Body.String())
	}
	memberRec := putReceipt(bob, "iss-disabled-member")
	if memberRec.Code != http.StatusOK {
		t.Fatalf("member receipt save after disable = %d (%s)", memberRec.Code, memberRec.Body.String())
	}
	var memberResp domain.AssistantSession
	if err := json.Unmarshal(memberRec.Body.Bytes(), &memberResp); err != nil {
		t.Fatal(err)
	}
	if len(memberResp.KnowledgeRefs) != 0 {
		t.Fatalf("member PUT response leaked creator knowledgeRefs: %+v", memberResp.KnowledgeRefs)
	}
	if rec := doJSONAsUser(t, h, alice, http.MethodPut, "/api/assistant/sessions/"+sessionID+"/knowledge", `{"knowledgeRefs":[{"connectorId":"`+conn.ID+`","mode":"all"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("rebinding disabled connector via /knowledge = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// 3. Delete connector -> existing session receipt save still succeeds,
	//    brand-new session referencing deleted connector still returns 400,
	//    and clearing via /knowledge with [] succeeds.
	if delRec := doJSONAsUser(t, h, alice, http.MethodDelete, "/api/knowledge/connectors/"+conn.ID, ""); delRec.Code != http.StatusOK {
		t.Fatalf("delete connector = %d (%s)", delRec.Code, delRec.Body.String())
	}
	if rec := putReceipt(alice, "iss-deleted-owner"); rec.Code != http.StatusOK {
		t.Fatalf("owner receipt save after delete = %d (%s)", rec.Code, rec.Body.String())
	}
	newBadRec := doJSONAsUser(t, h, alice, http.MethodPut, "/api/assistant/sessions/sess-new-deleted-kb", `{"id":"sess-new-deleted-kb","projectId":"p-receipt","knowledgeRefs":[{"connectorId":"`+conn.ID+`","mode":"all"}]}`)
	if newBadRec.Code != http.StatusBadRequest {
		t.Fatalf("new session referencing deleted connector = %d, want 400 (%s)", newBadRec.Code, newBadRec.Body.String())
	}
	if clearRec := doJSONAsUser(t, h, alice, http.MethodPut, "/api/assistant/sessions/"+sessionID+"/knowledge", `{"knowledgeRefs":[]}`); clearRec.Code != http.StatusOK {
		t.Fatalf("clearing deleted connector via /knowledge = %d (%s)", clearRec.Code, clearRec.Body.String())
	}
}

func TestProjectSharedSessionHidesCreatorKnowledgeRefsFromMembers(t *testing.T) {
	s, h, _, mcpURL := newKnowledgeTestServer(t)

	_ = s.PutUser(domain.User{ID: 501, LoginName: "alice"})
	_ = s.PutUser(domain.User{ID: 502, LoginName: "bob"})
	_ = s.PutUser(domain.User{ID: 503, LoginName: "outsider"})
	alice, _ := s.User(501)
	bob, _ := s.User(502)
	outsider, _ := s.User(503)

	_ = s.PutProject(domain.Project{
		ID:      "p-shared-kb",
		Name:    "Shared KB Project",
		OwnerID: alice.ID,
		Members: []domain.ProjectMember{{UserID: bob.ID, Role: "member"}},
	})
	_ = s.PutIssue(domain.Issue{ID: "iss-shared-1", ProjectID: "p-shared-kb", Title: "Shared Issue"})

	createRec := doJSONAsUser(t, h, alice, http.MethodPost, "/api/knowledge/connectors", `{"name":"Alice Secret KB","type":"mcp","endpoint":"`+mcpURL+`"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create connector = %d (%s)", createRec.Code, createRec.Body.String())
	}
	var conn domain.KnowledgeConnector
	if err := json.Unmarshal(createRec.Body.Bytes(), &conn); err != nil {
		t.Fatal(err)
	}

	sessionID := "sess-shared-kb"
	if err := s.PutAssistantSession(alice.ID, domain.AssistantSession{
		ID:        sessionID,
		UserID:    alice.ID,
		ProjectID: "p-shared-kb",
		Title:     "Shared Project Chat",
		Messages:  []domain.AssistantMessage{{Role: "user", Content: "讨论架构", AuthorID: alice.ID, AuthorName: alice.LoginName}},
	}); err != nil {
		t.Fatal(err)
	}
	wantRefs := []domain.KnowledgeRef{{ConnectorID: conn.ID, Mode: "allowlist", Sources: []string{"a"}}}
	bindRec := doJSONAsUser(t, h, alice, http.MethodPut, "/api/assistant/sessions/"+sessionID+"/knowledge", `{"knowledgeRefs":[{"connectorId":"`+conn.ID+`","mode":"allowlist","sources":["a"]}]}`)
	if bindRec.Code != http.StatusOK {
		t.Fatalf("bind knowledge = %d (%s)", bindRec.Code, bindRec.Body.String())
	}

	// Creator GET sees personal knowledgeRefs.
	ownerGet := doJSONAsUser(t, h, alice, http.MethodGet, "/api/assistant/sessions/"+sessionID, "")
	if ownerGet.Code != http.StatusOK {
		t.Fatalf("owner GET session = %d (%s)", ownerGet.Code, ownerGet.Body.String())
	}
	var ownerSess domain.AssistantSession
	if err := json.Unmarshal(ownerGet.Body.Bytes(), &ownerSess); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ownerSess.KnowledgeRefs, wantRefs) {
		t.Fatalf("owner GET lost knowledgeRefs: got %+v want %+v", ownerSess.KnowledgeRefs, wantRefs)
	}

	// Project member GET succeeds but must NOT receive creator's knowledgeRefs.
	memberGet := doJSONAsUser(t, h, bob, http.MethodGet, "/api/assistant/sessions/"+sessionID, "")
	if memberGet.Code != http.StatusOK {
		t.Fatalf("member GET session = %d (%s)", memberGet.Code, memberGet.Body.String())
	}
	var memberSess domain.AssistantSession
	if err := json.Unmarshal(memberGet.Body.Bytes(), &memberSess); err != nil {
		t.Fatal(err)
	}
	if len(memberSess.KnowledgeRefs) != 0 {
		t.Fatalf("member GET leaked creator knowledgeRefs: %+v", memberSess.KnowledgeRefs)
	}

	// Session list exposes knowledgeRefs to creator only, never to project members.
	findInList := func(u domain.User) domain.AssistantSession {
		t.Helper()
		listRec := doJSONAsUser(t, h, u, http.MethodGet, "/api/assistant/sessions?projectId=p-shared-kb", "")
		if listRec.Code != http.StatusOK {
			t.Fatalf("list sessions for %s = %d (%s)", u.LoginName, listRec.Code, listRec.Body.String())
		}
		var rows []domain.AssistantSession
		if err := json.Unmarshal(listRec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == sessionID {
				return row
			}
		}
		t.Fatalf("session %s missing from list for %s", sessionID, u.LoginName)
		return domain.AssistantSession{}
	}
	if got := findInList(alice).KnowledgeRefs; !reflect.DeepEqual(got, wantRefs) {
		t.Fatalf("owner list lost knowledgeRefs: %+v", got)
	}
	if got := findInList(bob).KnowledgeRefs; len(got) != 0 {
		t.Fatalf("member list leaked creator knowledgeRefs: %+v", got)
	}

	// Project member linking an issue to the shared session also receives sanitized session,
	// while stored bindings for creator remain intact.
	cur, _ := s.AssistantSession(alice.ID, sessionID)
	issueBody, _ := json.Marshal(gin.H{"issueId": "iss-shared-1", "updatedAt": cur.UpdatedAt})
	issueRec := doJSONAsUser(t, h, bob, http.MethodPut, "/api/assistant/sessions/"+sessionID+"/issue", string(issueBody))
	if issueRec.Code != http.StatusOK {
		t.Fatalf("member PUT issue = %d (%s)", issueRec.Code, issueRec.Body.String())
	}
	var issueResp domain.AssistantSession
	if err := json.Unmarshal(issueRec.Body.Bytes(), &issueResp); err != nil {
		t.Fatal(err)
	}
	if len(issueResp.KnowledgeRefs) != 0 {
		t.Fatalf("member PUT issue leaked creator knowledgeRefs: %+v", issueResp.KnowledgeRefs)
	}
	stored, _ := s.AssistantSession(alice.ID, sessionID)
	if !reflect.DeepEqual(stored.KnowledgeRefs, wantRefs) {
		t.Fatalf("stored creator knowledgeRefs were mutated: %+v", stored.KnowledgeRefs)
	}

	// Non-member link access remains blocked by KnowledgeRestricted.
	outsiderGet := doJSONAsUser(t, h, outsider, http.MethodGet, "/api/assistant/sessions/"+sessionID, "")
	if outsiderGet.Code != http.StatusForbidden {
		t.Fatalf("outsider GET restricted session = %d, want 403 (%s)", outsiderGet.Code, outsiderGet.Body.String())
	}
}
