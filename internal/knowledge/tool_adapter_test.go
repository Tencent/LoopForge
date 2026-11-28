package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestTencentDocsSearchAndGetContentCorrelation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(res any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": res})
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}})
		case "notifications/initialized":
			reply(map[string]any{})
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{
				{"name": "manage.search_file", "description": "Search Tencent Docs", "inputSchema": map[string]any{"type": "object"}},
				{"name": "get_content", "description": "Get Tencent Doc body", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			name, _ := req.Params["name"].(string)
			switch name {
			case "manage.search_file":
				reply(map[string]any{"content": []map[string]any{{
					"type": "text",
					"text": `{"file_list":[{"file_id":"doc_td_123","file_name":"研发发布流程规范.docx"},{"file_id":"doc_td_456","file_name":"值班手册.md"}]}`,
				}}})
			case "get_content":
				// Plain unstructured text without title metadata
				reply(map[string]any{"content": []map[string]any{{
					"type": "text",
					"text": "1. 发布前必须完成灰度验证。\n2. 严禁周五晚间全量发布。",
				}}})
			}
		}
	}))
	defer srv.Close()

	svc := NewService(newMemoryConnectorStore(), func() string { return "conn_td" })
	conn, err := svc.CreateConnector(context.Background(), 1, ConnectorInput{
		Name:     "腾讯文档",
		Type:     domain.ConnectorTypeMCP,
		Endpoint: srv.URL,
		Config: &domain.KnowledgeConnectorConfig{
			AllowedTools: []string{"manage.search_file", "get_content"},
		},
	})
	if err != nil {
		t.Fatalf("Create connector: %v", err)
	}

	exec := NewExecutor(svc)
	ref := domain.KnowledgeRef{ConnectorID: conn.ID, Mode: "all"}

	// Step 1: manage.search_file returns file_list
	searchRead, _, err := exec.ExecuteReferenceToolWithHistory(
		context.Background(), 1, ref, "manage.search_file",
		map[string]any{"query": "发布流程"},
		domain.KnowledgeCallOriginModelTool, nil,
	)
	if err != nil {
		t.Fatalf("manage.search_file err: %v", err)
	}
	if len(searchRead.Evidences) != 2 {
		t.Fatalf("expected 2 search evidences, got %+v", searchRead.Evidences)
	}
	if searchRead.Evidences[0].DocumentID != "doc_td_123" || searchRead.Evidences[0].FileName != "研发发布流程规范.docx" {
		t.Fatalf("unexpected first search evidence: %+v", searchRead.Evidences[0])
	}

	// Step 2: get_content returns plain text without title; should correlate doc_td_123 -> 研发发布流程规范.docx
	contentRead, contentText, err := exec.ExecuteReferenceToolWithHistory(
		context.Background(), 1, ref, "get_content",
		map[string]any{"file_id": "doc_td_123"},
		domain.KnowledgeCallOriginModelTool,
		[]domain.KnowledgeRead{searchRead},
	)
	if err != nil {
		t.Fatalf("get_content err: %v", err)
	}
	if !contentRead.ReadFull {
		t.Fatalf("expected get_content ReadFull=true")
	}
	if contentRead.Status != domain.KnowledgeStatusHit {
		t.Fatalf("expected status=%q, got %q", domain.KnowledgeStatusHit, contentRead.Status)
	}
	if len(contentRead.Evidences) != 1 {
		t.Fatalf("expected 1 correlated evidence, got %+v", contentRead.Evidences)
	}
	ev := contentRead.Evidences[0]
	if ev.DocumentID != "doc_td_123" || ev.FileName != "研发发布流程规范.docx" {
		t.Fatalf("expected correlated doc metadata, got %+v", ev)
	}
	if !strings.Contains(contentText, "[[knowledge:"+ev.ID+"]]") || !strings.Contains(contentText, "研发发布流程规范.docx") {
		t.Fatalf("expected citation suffix with evidence ID and doc name, got:\n%s", contentText)
	}
}

func TestEnrichReadEvidencesFallbacksAndCustomToolRules(t *testing.T) {
	// Level 3: Markdown heading fallback when no prior search exists
	read := domain.KnowledgeRead{
		ID:       "r1",
		ServerID: "conn_1",
		Tool:     "get_content",
		Status:   domain.KnowledgeStatusUnstructured,
	}
	rule := resolveKnowledgeToolRule(nil, "get_content")
	enrichReadEvidences(&read, "# 架构设计评审\n正文内容...", map[string]any{"file_id": "abc_99"}, rule, nil, "conn_1")
	if len(read.Evidences) != 1 || read.Evidences[0].Title != "架构设计评审" || read.Evidences[0].DocumentID != "abc_99" {
		t.Fatalf("expected markdown heading fallback, got %+v", read.Evidences)
	}

	// Custom Extra["tool_rules"] override for a new third-party MCP provider
	extra := map[string]string{
		"tool_rules": `[{"tool":"custom_read","role":"read","docIdArgs":["page_key"]},{"tool":"custom_search","role":"search","listKeys":["entries"],"docIdKeys":["page_key"],"titleKeys":["page_title"]}]`,
	}
	customReadRule := resolveKnowledgeToolRule(extra, "custom_read")
	if customReadRule.Role != ToolRoleRead {
		t.Fatalf("expected custom_read role=read, got %+v", customReadRule)
	}
	customSearchRule := resolveKnowledgeToolRule(extra, "custom_search")
	_, evs, isJSON, count := ExtractEvidencesWithRule(`{"entries":[{"page_key":"pk_1","page_title":"自定义知识库文档"}]}`, "conn_custom", &customSearchRule)
	if !isJSON || count != 1 || len(evs) != 1 || evs[0].DocumentID != "pk_1" || evs[0].Title != "自定义知识库文档" {
		t.Fatalf("unexpected custom rule extraction: isJSON=%v count=%d evs=%+v", isJSON, count, evs)
	}
}
