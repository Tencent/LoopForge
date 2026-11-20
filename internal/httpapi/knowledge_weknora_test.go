package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
	"agentflow/internal/repo"
)

func TestKnowledgeProxy_WeKnoraAllowlistAndAntiBypass(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var searchedKBs []string
	wk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "kb-allowed", "name": "允许库", "knowledge_count": 1},
					{"id": "kb-secret", "name": "保密库", "knowledge_count": 1},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-allowed/hybrid-search":
			searchedKBs = append(searchedKBs, "kb-allowed")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    []map[string]any{{"id": "c1", "knowledge_id": "doc-a", "knowledge_title": "公开文档", "content": "ok", "score": 0.9}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-secret/hybrid-search":
			searchedKBs = append(searchedKBs, "kb-secret")
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-secret":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-secret", "knowledge_base_id": "kb-secret", "title": "机密"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-a", "knowledge_base_id": "kb-allowed", "title": "公开"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/chunks/doc-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"total":   1,
				"data":    []map[string]any{{"id": "c1", "content": "公开正文", "chunk_index": 0}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wk.Close()

	settings := domain.KnowledgeSettings{
		Provider:       domain.KnowledgeProviderWeKnora,
		Enabled:        true,
		URL:            wk.URL,
		Token:          "wk-secret-key",
		ProjectSources: map[string][]string{"p1": {"kb-allowed"}},
	}
	if err := store.PutKnowledgeSettings(settings); err != nil {
		t.Fatal(err)
	}

	h := &knowledgeHandlers{
		deps:     &deps{repo: store},
		svc:      knowledge.NewService(store, func() string { return "id-1" }),
		activity: &knowledgeActivityLog{},
	}

	callRPC := func(method string, params any) map[string]any {
		resp := h.handleProxyRPC(t.Context(), settings, knowledge.ProxyClaims{ProjectID: "p1", UserID: 1, AgentID: "dev"}, agentToolRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  method,
			Params:  mustMarshalJSON(params),
		})
		raw, _ := json.Marshal(resp)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}

	// 1. tools/list only returns WeKnora read-only tools
	listResp := callRPC("tools/list", map[string]any{})
	listRaw, _ := json.Marshal(listResp)
	if !strings.Contains(string(listRaw), "search_knowledge") || strings.Contains(string(listRaw), "kb_search") {
		t.Fatalf("unexpected tools/list: %s", listRaw)
	}

	// 2. list_knowledge_bases filters out kb-secret
	kbsResp := callRPC("tools/call", map[string]any{"name": "list_knowledge_bases", "arguments": map[string]any{}})
	kbsRaw, _ := json.Marshal(kbsResp)
	if !strings.Contains(string(kbsRaw), "kb-allowed") || strings.Contains(string(kbsRaw), "kb-secret") {
		t.Fatalf("list_knowledge_bases leaked unauthorized KB: %s", kbsRaw)
	}

	// 3. search_knowledge without kb_ids forces kb-allowed only
	searchedKBs = nil
	searchResp := callRPC("tools/call", map[string]any{"name": "search_knowledge", "arguments": map[string]any{"query": "规范"}})
	searchRaw, _ := json.Marshal(searchResp)
	if len(searchedKBs) != 1 || searchedKBs[0] != "kb-allowed" || !strings.Contains(string(searchRaw), "公开文档") {
		t.Fatalf("unexpected scoped search: kbs=%v resp=%s", searchedKBs, searchRaw)
	}

	// 4. search_knowledge requesting kb-secret is rejected
	bypassSearch := callRPC("tools/call", map[string]any{
		"name":      "search_knowledge",
		"arguments": map[string]any{"query": "机密", "knowledge_base_ids": []string{"kb-secret"}},
	})
	bypassRaw, _ := json.Marshal(bypassSearch)
	if !strings.Contains(string(bypassRaw), `"isError":true`) {
		t.Fatalf("expected error for unauthorized kb in search_knowledge: %s", bypassRaw)
	}

	// 5. read_document on doc-secret (belongs to kb-secret) is rejected
	bypassRead := callRPC("tools/call", map[string]any{
		"name":      "read_document",
		"arguments": map[string]any{"knowledge_id": "doc-secret"},
	})
	bypassReadRaw, _ := json.Marshal(bypassRead)
	if !strings.Contains(string(bypassReadRaw), `"isError":true`) {
		t.Fatalf("expected error for cross-KB read_document: %s", bypassReadRaw)
	}

	// 6. read_document on doc-a succeeds
	okRead := callRPC("tools/call", map[string]any{
		"name":      "read_document",
		"arguments": map[string]any{"knowledge_id": "doc-a"},
	})
	okReadRaw, _ := json.Marshal(okRead)
	if !strings.Contains(string(okReadRaw), "公开正文") {
		t.Fatalf("expected doc-a content: %s", okReadRaw)
	}
}

func TestKnowledgeProxy_MixedProvidersAndDeletedRefUnavailable(t *testing.T) {
	lcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"lc","version":"1.0"}}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"kb_search","description":"search"}]}}`))
		case "tools/call":
			payload := `{"query":"q","mode":"hybrid","results":[{"source":"lc-kb","path":"lc.md","title":"LC-Result","snippet":"from lc","score":0.92}]}`
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": payload}}},
			})
			_, _ = w.Write(resp)
		}
	}))
	defer lcSrv.Close()

	wkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/knowledge-bases":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    []map[string]any{{"id": "wk-kb", "name": "WK KB"}},
			})
		case strings.HasSuffix(r.URL.Path, "/hybrid-search"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    []map[string]any{{"id": "c1", "knowledge_id": "d1", "knowledge_title": "WK-Result", "content": "from wk", "score": 0.88}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wkSrv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	settings := domain.KnowledgeSettings{
		Enabled: true,
		Connections: []domain.KnowledgeConnection{
			{ID: "lc-1", Name: "LightCloud", Provider: domain.KnowledgeProviderLightCloud, URL: lcSrv.URL, Token: "t1", Enabled: true},
			{ID: "wk-1", Name: "WeKnora", Provider: domain.KnowledgeProviderWeKnora, URL: wkSrv.URL, Token: "t2", Enabled: true},
		},
	}
	if err := store.PutKnowledgeSettings(settings); err != nil {
		t.Fatal(err)
	}

	h := &knowledgeHandlers{
		deps:     &deps{repo: store},
		svc:      knowledge.NewService(store, func() string { return "id-1" }),
		activity: &knowledgeActivityLog{},
	}

	callProxy := func(claims knowledge.ProxyClaims, method string, params any) map[string]any {
		resp := h.handleProxyRPC(t.Context(), settings, claims, agentToolRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  method,
			Params:  mustMarshalJSON(params),
		})
		raw, _ := json.Marshal(resp)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}

	// 1. SOP proxy (no Ref) exposes search_knowledge and recalls from both LightCloud and WeKnora
	searchResp := callProxy(
		knowledge.ProxyClaims{RunID: "run-mix", NodeID: "n1", ProjectID: "proj-mix", AgentID: "a1", UserID: 1},
		"tools/call",
		map[string]any{"name": "search_knowledge", "arguments": map[string]any{"query": "q"}},
	)
	searchRaw, _ := json.Marshal(searchResp)
	if !strings.Contains(string(searchRaw), "LC-Result") || !strings.Contains(string(searchRaw), "WK-Result") {
		t.Fatalf("expected mixed results from both LightCloud and WeKnora: %s", searchRaw)
	}

	// 2. Bound ref pointing to deleted connection on a project with NO allowlist must return isError (unavailable), NOT {"results":[]}
	delResp := callProxy(
		knowledge.ProxyClaims{
			RunID: "run-del", NodeID: "n1", ProjectID: "proj-mix", AgentID: "a1", UserID: 1,
			Ref: &domain.KnowledgeRef{
				ServerID: domain.KnowledgeMCPServerID,
				Mode:     "allowlist",
				Sources:  []string{"conn-deleted::kb-1"},
			},
		},
		"tools/call",
		map[string]any{"name": "search_knowledge", "arguments": map[string]any{"query": "q"}},
	)
	delRaw, _ := json.Marshal(delResp)
	if !strings.Contains(string(delRaw), `"isError":true`) {
		t.Fatalf("expected isError:true for deleted connection ref, got: %s", delRaw)
	}
}

func mustMarshalJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
