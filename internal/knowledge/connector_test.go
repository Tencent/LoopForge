package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

type memoryConnectorStore struct {
	mu    sync.Mutex
	items map[string]domain.KnowledgeConnector
}

func newMemoryConnectorStore() *memoryConnectorStore {
	return &memoryConnectorStore{items: map[string]domain.KnowledgeConnector{}}
}

func (m *memoryConnectorStore) ListKnowledgeConnectors(ownerUserID int64) []domain.KnowledgeConnector {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KnowledgeConnector
	for _, c := range m.items {
		if c.OwnerUserID == ownerUserID {
			out = append(out, c)
		}
	}
	return out
}

func (m *memoryConnectorStore) GetKnowledgeConnector(ownerUserID int64, id string) (domain.KnowledgeConnector, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok || c.OwnerUserID != ownerUserID {
		return domain.KnowledgeConnector{}, false
	}
	return c, true
}

func (m *memoryConnectorStore) PutKnowledgeConnector(conn domain.KnowledgeConnector) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.items[conn.ID]; ok && existing.OwnerUserID != conn.OwnerUserID {
		return errors.New("connector owned by another user")
	}
	if conn.UpdatedAt.IsZero() {
		conn.UpdatedAt = time.Now().UTC()
	}
	m.items[conn.ID] = conn
	return nil
}

func (m *memoryConnectorStore) DeleteKnowledgeConnector(ownerUserID int64, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.items[id]
	if !ok || c.OwnerUserID != ownerUserID {
		return false, nil
	}
	delete(m.items, id)
	return true, nil
}

// customPlugFactory proves adding a new knowledge connector type (e.g. Feishu/Notion)
// works end-to-end across Registry, Service, Executor, and BuildCatalogXML.
type customPlugFactory struct{}

func (customPlugFactory) Meta() domain.ConnectorTypeMeta {
	return domain.ConnectorTypeMeta{
		Type:               "feishu_wiki",
		Name:               "飞书知识库",
		Description:        "自定义扩展连接器测试",
		SupportsSubSources: true,
		DefaultAuthType:    domain.ConnectorAuthOAuth,
		Fields: []domain.ConnectorFieldSpec{
			{Key: "endpoint", Label: "空间地址", Kind: "url", Required: true},
		},
	}
}

func (customPlugFactory) ValidateAndNormalize(endpoint string, cfg domain.KnowledgeConnectorConfig, secret domain.KnowledgeConnectorSecret, _ bool) (string, domain.KnowledgeConnectorConfig, domain.KnowledgeConnectorSecret, error) {
	return strings.TrimSpace(endpoint), cfg, secret, nil
}

func (customPlugFactory) New(spec ConnectorSpec) (Connector, error) {
	return &customPlugConnector{spec: spec}, nil
}

type customPlugConnector struct{ spec ConnectorSpec }

func (c *customPlugConnector) Type() string { return "feishu_wiki" }
func (c *customPlugConnector) Check(_ context.Context) CheckResult {
	return CheckResult{OK: true, ConnectorID: c.spec.ID, Tools: []string{"search_wiki"}}
}
func (c *customPlugConnector) ListSources(_ context.Context) ([]Source, error) {
	return []Source{{ID: "space-a", Name: "架构文档库"}}, nil
}
func (c *customPlugConnector) Tools(_ context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: "search_wiki", Description: "Search Feishu Wiki"}}, nil
}
func (c *customPlugConnector) CallTool(_ context.Context, tool string, args map[string]any, scope SourceScope) (CallResult, error) {
	if !scope.Allows("space-a") {
		return CallResult{Status: domain.KnowledgeStatusUnavailable}, errors.New("out of scope")
	}
	return CallResult{
		Status:   domain.KnowledgeStatusHit,
		Text:     `{"summary":"OK"}`,
		Hits:     1,
		Citeable: true,
		Targets:  []string{"架构总览.docx"},
		Evidences: []domain.KnowledgeEvidence{{
			SourceID:   "space-a",
			SourceName: "架构文档库",
			DocumentID: "doc-9",
			Title:      "架构总览",
			FileName:   "架构总览.docx",
			Locator:    "Section 2",
		}},
	}, nil
}

func TestCryptoSealOpenAndCrossUserIsolation(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "unit-test-master-secret")
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")

	secret := domain.KnowledgeConnectorSecret{
		AuthType: domain.ConnectorAuthAPIKey,
		Token:    "sk-secret-123",
		Headers:  map[string]string{"X-Custom": "val-999"},
	}
	sealed, err := SealSecret(11, "conn-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, connectorSecretPrefix) || strings.Contains(sealed, "sk-secret-123") {
		t.Fatalf("unexpected sealed blob: %q", sealed)
	}
	opened, err := OpenSecret(11, "conn-1", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Token != "sk-secret-123" || opened.Headers["X-Custom"] != "val-999" {
		t.Fatalf("opened mismatch: %+v", opened)
	}
	if _, err := OpenSecret(22, "conn-1", sealed); err == nil {
		t.Fatal("expected AAD mismatch when decrypting under another user")
	}
	if redacted := RedactSecrets("Bearer sk-secret-123 / val-999", secret); strings.Contains(redacted, "sk-secret-123") || strings.Contains(redacted, "val-999") {
		t.Fatalf("secrets leaked after redact: %q", redacted)
	}

	t.Setenv("AGENTFLOW_SESSION_SECRET", "")
	t.Setenv("LOOPFORGE_KNOWLEDGE_SECRET_KEY", "")
	t.Setenv("SESSION_SECRET", "")
	if _, err := SealSecret(11, "conn-1", secret); err == nil {
		t.Fatal("expected missing secret error when AGENTFLOW_AUTH_REQUIRED=true")
	}
}

func TestWeKnoraConnectorReadOnlyScopeAndEvidence(t *testing.T) {
	const expectedKey = "wk-user-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != expectedKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized ` + expectedKey + `"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"kb-1","name":"loopforge-knowledge","knowledge_count":2},{"id":"kb-2","name":"team-docs","knowledge_count":1}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-2/hybrid-search":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"c-1","content":"demo-user","knowledge_id":"doc-2","chunk_index":0,"knowledge_title":"我是谁","knowledge_filename":"我是谁.md","score":0.92}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-1":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"doc-1","knowledge_base_id":"kb-1","title":"我是谁","file_name":"我是谁.md"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-2":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"doc-2","knowledge_base_id":"kb-2","title":"我是谁","file_name":"我是谁.md"}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/chunks/doc-2"):
			_, _ = w.Write([]byte(`{"success":true,"total":1,"data":[{"id":"c-1","content":"demo-user","chunk_index":0}]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/knowledge-bases/kb-2/knowledge"):
			_, _ = w.Write([]byte(`{"success":true,"total":1,"data":[{"id":"doc-2","knowledge_base_id":"kb-2","title":"我是谁","file_name":"我是谁.md"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store := newMemoryConnectorStore()
	svc := NewService(store, func() string { return "1" })
	exec := NewExecutor(svc)
	ctx := context.Background()

	conn, err := svc.CreateConnector(ctx, 100, ConnectorInput{
		Name:     "我的 WeKnora",
		Type:     domain.ConnectorTypeWeKnora,
		Endpoint: srv.URL + "/api/v1",
		Secret:   domain.KnowledgeConnectorSecret{Token: expectedKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if conn.SecretEnc != "" || !conn.SecretConfigured || conn.Endpoint != srv.URL {
		t.Fatalf("unexpected public connector: %+v", conn)
	}

	check := svc.CheckConnector(ctx, 100, conn.ID, nil)
	if !check.OK || len(check.Sources) != 2 || len(check.Tools) != 4 {
		t.Fatalf("check failed: %+v", check)
	}

	// User 200 cannot see or check User 100's connector.
	if got := svc.ListConnectors(200); len(got) != 0 {
		t.Fatalf("user 200 saw connectors: %+v", got)
	}
	if checkOther := svc.CheckConnector(ctx, 200, conn.ID, nil); checkOther.OK {
		t.Fatal("user 200 must not check user 100 connector")
	}

	ref := domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "allowlist", Sources: []string{"kb-2"}}

	// Out-of-scope KB must be rejected.
	if _, _, err := exec.ExecuteReferenceTool(ctx, 100, ref, WeKnoraToolSearchKnowledge, map[string]any{
		"query":              "我是谁",
		"knowledge_base_ids": []string{"kb-1"},
	}, domain.KnowledgeCallOriginModelTool); err == nil {
		t.Fatal("expected search on out-of-scope kb-1 to be rejected")
	}
	if _, _, err := exec.ExecuteReferenceTool(ctx, 100, ref, WeKnoraToolReadDocument, map[string]any{
		"knowledge_id": "doc-1",
	}, domain.KnowledgeCallOriginModelTool); err == nil {
		t.Fatal("expected read_document on doc-1 (belonging to kb-1) to be rejected")
	}

	// Allowed KB search returns structured FileName, SourceName, Locator and citation suffix.
	read, text, err := exec.ExecuteReferenceTool(ctx, 100, ref, WeKnoraToolSearchKnowledge, map[string]any{
		"query": "我是谁",
	}, domain.KnowledgeCallOriginModelTool)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != domain.KnowledgeStatusHit || len(read.Evidences) != 1 {
		t.Fatalf("unexpected read result: %+v", read)
	}
	ev := read.Evidences[0]
	if ev.SourceName != "team-docs" || ev.FileName != "我是谁.md" || ev.Locator != "#chunk-0" || ev.ConnectionID != conn.ID {
		t.Fatalf("unexpected evidence metadata: %+v", ev)
	}
	if !strings.Contains(text, "知识库「team-docs」 / 我是谁.md (#chunk-0)") || !strings.Contains(text, "[[knowledge:"+ev.ID+"]]") {
		t.Fatalf("unexpected citation suffix: %s", text)
	}

	// L0 catalog XML only includes allowed kb-2 samples and tools.
	catalog := exec.BuildCatalogXML(ctx, 100, []domain.KnowledgeRef{ref}, "chat", func(c domain.KnowledgeConnector, tName string) string {
		return "kb_" + c.ID + "__" + tName
	})
	if !strings.Contains(catalog, "kb-2") || strings.Contains(catalog, "kb-1") || !strings.Contains(catalog, "我是谁.md") {
		t.Fatalf("unexpected catalog XML:\n%s", catalog)
	}
}

func TestMCPConnectorReadOnlyFilterAndCustomRegistryExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "kb_search", "description": "Search", "annotations": map[string]any{"readOnlyHint": true}},
						{"name": "kb_list_sources", "description": "List", "annotations": map[string]any{"readOnlyHint": true}},
						{"name": "kb_write_document", "description": "Write", "annotations": map[string]any{"destructiveHint": true, "readOnlyHint": false}},
						{"name": "arbitrary_mutation", "description": "Mutate"},
					},
				},
			})
		case "tools/call":
			name, _ := req.Params["name"].(string)
			payload := `{"sources":[{"id":"src-1","name":"研发规范"},{"id":"src-2","name":"私有草稿"}]}`
			if name == "kb_search" {
				payload = `{"results":[{"source":"src-1","source_name":"研发规范","path":"guide.md","title":"规范","file_name":"guide.md","start_line":1,"end_line":10}]}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": payload}}},
			})
		}
	}))
	defer srv.Close()

	reg := DefaultRegistry()
	reg.Register(customPlugFactory{})
	svc := NewServiceWithRegistry(newMemoryConnectorStore(), reg, func() string { return "ext" })
	exec := NewExecutor(svc)
	ctx := context.Background()

	// Registry exposes all built-in types plus the custom plugin cleanly.
	if types := svc.ConnectorTypes(); len(types) != 7 || types[len(types)-1].Type != "feishu_wiki" {
		t.Fatalf("unexpected registered connector types: %+v", types)
	}

	mcpConn, err := svc.CreateConnector(ctx, 5, ConnectorInput{
		Name:     "团队 MCP",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "mcp-tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, tools, err := exec.ReferenceTools(ctx, 5, domain.KnowledgeRef{ConnectorID: mcpConn.ID, Mode: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 non-explicitly-writable tools exposed, got %+v", tools)
	}
	if _, _, err := exec.ExecuteReferenceTool(ctx, 5, domain.KnowledgeRef{ConnectorID: mcpConn.ID, Mode: "all"}, "kb_write_document", nil, ""); err == nil {
		t.Fatal("write tool must be blocked")
	}

	// Custom plugin connector works identically through Service & Executor.
	customConn, err := svc.CreateConnector(ctx, 5, ConnectorInput{
		Name:     "我的飞书库",
		Type:     "feishu_wiki",
		Endpoint: "https://feishu.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := svc.ListConnectorSources(ctx, 5, customConn.ID)
	if err != nil || len(sources) != 1 || sources[0].ID != "space-a" {
		t.Fatalf("custom sources failed: %+v %v", sources, err)
	}
	read, text, err := exec.ExecuteReferenceTool(ctx, 5, domain.KnowledgeRef{ConnectorID: customConn.ID, Mode: "allowlist", Sources: []string{"space-a"}}, "search_wiki", nil, "")
	if err != nil || !strings.Contains(text, "架构总览.docx") || len(read.Evidences) != 1 {
		t.Fatalf("custom tool call failed: read=%+v text=%s err=%v", read, text, err)
	}
}

func TestProxyTokenSignVerify(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "token-test-secret")
	now := time.Unix(1700000000, 0)
	tok, err := SignProxyToken(ProxyClaims{
		RunID:     "run-1",
		NodeID:    "node-1",
		Attempt:   2,
		ProjectID: "proj-1",
		UserID:    88,
		AgentID:   "dev",
		Ref:       &domain.KnowledgeRef{ConnectorID: "kc-1", Mode: "allowlist", Sources: []string{"kb-a"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyProxyToken(tok, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 88 || claims.Attempt != 2 || claims.Ref == nil || claims.Ref.ConnectorID != "kc-1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if _, err := VerifyProxyToken(tok, now.Add(ProxyTokenTTL+time.Second)); err == nil {
		t.Fatal("expected expired token error")
	}
	if _, err := SignProxyToken(ProxyClaims{UserID: 88}, now); err == nil {
		t.Fatal("expected missing ref rejection")
	}
}

func TestMCPConnectorAllowedToolsAndVendorAdapters(t *testing.T) {
	var lastRetrievalArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						// Feishu remote returns readOnlyHint=false for fetch-doc/search-doc.
						{"name": "fetch-doc", "description": "Fetch Feishu doc", "annotations": map[string]any{"readOnlyHint": false}},
						{"name": "search-doc", "description": "Search Feishu doc", "annotations": map[string]any{"readOnlyHint": false}},
						{"name": "create-doc", "description": "Create doc", "annotations": map[string]any{"readOnlyHint": false}},
						// Tencent Docs unannotated tools (mix of read + write).
						{"name": "manage.search_file", "description": "Search Tencent Docs"},
						{"name": "doc.delete_file", "description": "Delete Tencent Docs"},
						// RAGFlow tools with dataset_ids schema.
						{"name": "ragflow_list_datasets", "description": "List RAGFlow datasets"},
						{
							"name":        "ragflow_retrieval",
							"description": "Retrieve chunks",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"question":    map[string]any{"type": "string"},
									"dataset_ids": map[string]any{"type": "array"},
								},
							},
						},
					},
				},
			})
		case "tools/call":
			name, _ := req.Params["name"].(string)
			args, _ := req.Params["arguments"].(map[string]any)
			var payload string
			switch name {
			case "ragflow_list_datasets":
				payload = `{"datasets":[{"id":"ds-1","name":"星云网关","document_count":3},{"id":"ds-2","name":"私有库","document_count":1}]}`
			case "ragflow_retrieval":
				lastRetrievalArgs = args
				payload = `{"chunks":[{"dataset_id":"ds-1","document_id":"doc-9","document_keyword":"01_星云网关_架构与规格.md","content":"管理端口为 18443"}]}`
			default:
				payload = `{"ok":true}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": payload}}},
			})
		}
	}))
	defer srv.Close()

	idSeq := 0
	ids := []string{"v1", "v2", "v3"}
	svc := NewService(newMemoryConnectorStore(), func() string {
		id := ids[idSeq]
		idSeq++
		return id
	})
	exec := NewExecutor(svc)
	ctx := context.Background()

	// 1. Explicit allowlist overrides Feishu readOnlyHint=false and blocks unannotated write tools (doc.delete_file).
	feishuConn, err := svc.CreateConnector(ctx, 9, ConnectorInput{
		Name:     "飞书远程",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
		Config: &domain.KnowledgeConnectorConfig{
			AllowedTools: []string{"fetch-doc", "search-doc", "manage.search_file"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, tools, err := exec.ReferenceTools(ctx, 9, domain.KnowledgeRef{ConnectorID: feishuConn.ID, Mode: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected exactly 3 allowlisted tools, got %+v", tools)
	}
	if _, _, err := exec.ExecuteReferenceTool(ctx, 9, domain.KnowledgeRef{ConnectorID: feishuConn.ID, Mode: "all"}, "doc.delete_file", nil, ""); err == nil {
		t.Fatal("unlisted tool doc.delete_file must be blocked by allowlist")
	}
	if _, _, err := exec.ExecuteReferenceTool(ctx, 9, domain.KnowledgeRef{ConnectorID: feishuConn.ID, Mode: "all"}, "create-doc", nil, ""); err == nil {
		t.Fatal("unlisted tool create-doc must be blocked by allowlist")
	}

	// 2. RAGFlow dataset listing, dataset_ids scope injection, and evidence extraction.
	ragflowConn, err := svc.CreateConnector(ctx, 9, ConnectorInput{
		Name:     "RAGFlow",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "ragflow-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := svc.ListConnectorSources(ctx, 9, ragflowConn.ID)
	if err != nil || len(sources) != 2 || sources[0].ID != "ds-1" || sources[0].DocCount != 3 {
		t.Fatalf("expected ragflow_list_datasets to populate sources, got sources=%+v err=%v", sources, err)
	}
	ref := domain.KnowledgeRef{ConnectorID: ragflowConn.ID, Mode: "allowlist", Sources: []string{"ds-1"}}
	read, _, err := exec.ExecuteReferenceTool(ctx, 9, ref, "ragflow_retrieval", map[string]any{"question": "管理端口"}, "")
	if err != nil {
		t.Fatal(err)
	}
	dsIDs, ok := lastRetrievalArgs["dataset_ids"].([]any)
	if !ok || len(dsIDs) != 1 || dsIDs[0] != "ds-1" {
		t.Fatalf("expected dataset_ids=[ds-1] injected by scope, got %+v", lastRetrievalArgs)
	}
	if len(read.Evidences) != 1 || read.Evidences[0].SourceID != "ds-1" || read.Evidences[0].FileName != "01_星云网关_架构与规格.md" {
		t.Fatalf("unexpected RAGFlow evidence: %+v", read.Evidences)
	}
}
