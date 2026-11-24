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
	body, _ := json.Marshal(gin.H{"messageIndex": 1, "content": "分叉问题", "updatedAt": s.UpdatedAt})
	c, w := workbenchContext(b, "POST", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.forkSession(c)
	if w.Code != 201 {
		t.Fatalf("forkSession HTTP %d: %s", w.Code, w.Body.String())
	}
	var branch domain.AssistantSession
	if err := json.Unmarshal(w.Body.Bytes(), &branch); err != nil {
		t.Fatal(err)
	}
	if !branch.KnowledgeRestricted || !reflect.DeepEqual(branch.KnowledgeRefs, wantRefs) {
		t.Fatalf("forked branch lost KB config: restricted=%v refs=%+v", branch.KnowledgeRestricted, branch.KnowledgeRefs)
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
