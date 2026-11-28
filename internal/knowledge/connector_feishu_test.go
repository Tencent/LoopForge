package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
)

func TestFeishuFactoryValidateAndNew(t *testing.T) {
	f := newFeishuFactory()
	if meta := f.Meta(); meta.Type != domain.ConnectorTypeFeishu || !meta.SupportsSubSources {
		t.Fatalf("unexpected feishu meta: %+v", meta)
	}

	// Blank endpoint defaults to https://open.feishu.cn; appSecret can live in Secret.Headers["appSecret"].
	ep, cfg, sec, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{
		TimeoutSec: 20,
		Extra:      map[string]string{"appId": " cli_123 "},
	}, domain.KnowledgeConnectorSecret{
		Headers: map[string]string{"appSecret": "sec-in-header"},
	}, false)
	if err != nil || ep != feishuDefaultBaseURL || cfg.Extra["appId"] != "cli_123" || sec.Headers["appSecret"] != "sec-in-header" {
		t.Fatalf("unexpected normalized feishu config: ep=%q cfg=%+v sec=%+v err=%v", ep, cfg, sec, err)
	}

	if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{TimeoutSec: 301, Extra: map[string]string{"appId": "cli_1"}}, domain.KnowledgeConnectorSecret{Token: "sec"}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for timeout >300, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{Token: "sec"}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for missing appId, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{Extra: map[string]string{"appId": "cli_1"}}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for missing appSecret, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{Extra: map[string]string{"appId": "cli_1"}}, domain.KnowledgeConnectorSecret{}, true); err != nil {
		t.Fatalf("expected existing secret accepted, got %v", err)
	}

	if _, err := f.New(ConnectorSpec{Endpoint: feishuDefaultBaseURL}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when New lacks appId, got %v", err)
	}
	if _, err := f.New(ConnectorSpec{Endpoint: feishuDefaultBaseURL, Config: domain.KnowledgeConnectorConfig{Extra: map[string]string{"appId": "cli_1"}}}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when New lacks appSecret, got %v", err)
	}
	conn, err := f.New(ConnectorSpec{
		ID:       "fs-1",
		Endpoint: feishuDefaultBaseURL,
		Config:   domain.KnowledgeConnectorConfig{TimeoutSec: 25, Extra: map[string]string{"appId": "cli_1"}},
		Secret:   domain.KnowledgeConnectorSecret{Headers: map[string]string{"appSecret": "sec-1"}},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	fc := conn.(*feishuConnector)
	if fc.Type() != domain.ConnectorTypeFeishu || fc.http.Timeout != 25*time.Second {
		t.Fatalf("unexpected feishu connector state: type=%s timeout=%v", fc.Type(), fc.http.Timeout)
	}
	if tools, err := fc.Tools(context.Background()); err != nil || len(tools) != 4 {
		t.Fatalf("expected 4 tools, got len=%d err=%v", len(tools), err)
	}
}

func TestFeishuTATAndAPIErrorHandling(t *testing.T) {
	const appSecret = "fs-secret-leak-88"
	const tatTok = "tat-secret-leak-77"
	var tatMode atomic.Int32
	var apiMode atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			switch tatMode.Load() {
			case 1:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("boom " + appSecret))
			case 2:
				_, _ = w.Write([]byte("{bad-tat-json"))
			case 3:
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 10003, "msg": "bad secret " + appSecret})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": tatTok, "expire": 0})
			}
			return
		}
		switch apiMode.Load() {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "always expired"})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 131006, "msg": "no permission"})
		case 3:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("gateway fail " + tatTok))
		case 4:
			_, _ = w.Write([]byte("{bad-api-json"))
		case 5:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 999, "msg": "biz error " + appSecret})
		case 6:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": "not-a-struct"})
		}
	}))
	defer srv.Close()

	conn, err := newFeishuFactory().New(ConnectorSpec{
		ID:       "fs-err",
		Endpoint: srv.URL,
		Config:   domain.KnowledgeConnectorConfig{Extra: map[string]string{"appId": "cli_err"}},
		Secret:   domain.KnowledgeConnectorSecret{Token: appSecret},
	})
	if err != nil {
		t.Fatal(err)
	}
	fc := conn.(*feishuConnector)
	ctx := context.Background()

	// TAT error paths redact appSecret.
	for _, m := range []int32{1, 2, 3} {
		tatMode.Store(m)
		if _, err := fc.getTAT(ctx, true); err == nil || strings.Contains(err.Error(), appSecret) {
			t.Fatalf("tatMode %d expected error without secret leak, got %v", m, err)
		}
	}
	tatMode.Store(0)

	// Expire <= 0 defaults to 7200s and populates tat cache.
	tok, err := fc.getTAT(ctx, true)
	if err != nil || tok != tatTok || time.Until(fc.tatExpiresAt) < time.Hour {
		t.Fatalf("expected valid default-TTL TAT, got tok=%q err=%v exp=%v", tok, err, fc.tatExpiresAt)
	}

	// API error modes.
	apiMode.Store(1)
	if err := fc.doAPI(ctx, http.MethodGet, "/open-apis/wiki/v2/spaces", nil); err == nil || !strings.Contains(err.Error(), "code=99991663") {
		t.Fatalf("expected code=99991663 error when retry still fails, got %v", err)
	}
	apiMode.Store(2)
	if err := fc.doAPI(ctx, http.MethodGet, "/open-apis/wiki/v2/spaces", nil); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden on code 131006, got %v", err)
	}
	apiMode.Store(3)
	if chk := fc.Check(ctx); chk.OK || strings.Contains(chk.Error, tatTok) {
		t.Fatalf("expected Check failure with TAT redacted, got %+v", chk)
	}
	apiMode.Store(4)
	if err := fc.doAPI(ctx, http.MethodGet, "/open-apis/wiki/v2/spaces", nil); err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("expected parse error, got %v", err)
	}
	apiMode.Store(5)
	if err := fc.doAPI(ctx, http.MethodGet, "/open-apis/wiki/v2/spaces", nil); err == nil || !strings.Contains(err.Error(), "code=999") || strings.Contains(err.Error(), appSecret) {
		t.Fatalf("expected biz error without secret leak, got %v", err)
	}
	apiMode.Store(6)
	var out struct {
		Items []string `json:"items"`
	}
	if err := fc.doAPI(ctx, http.MethodGet, "/open-apis/wiki/v2/spaces", &out); err == nil || !strings.Contains(err.Error(), "数据解析失败") {
		t.Fatalf("expected data unmarshal error, got %v", err)
	}
}

func TestFeishuWikiPaginationBFSTreeSearchAndReadDoc(t *testing.T) {
	const appSecret = "fs-wiki-secret"
	var spaceNodesCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "tenant_access_token": "tat-ok", "expire": 7200})
		case "/open-apis/wiki/v2/spaces":
			pt := r.URL.Query().Get("page_token")
			if pt == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"items": []map[string]any{
							{"space_id": ""}, // skipped
							{"space_id": "sp-1", "name": "Architecture", "description": "Core"},
						},
						"has_more":   true,
						"page_token": "p2",
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"items":    []map[string]any{{"space_id": "sp-2", "name": ""}},
					"has_more": false,
				},
			})
		case "/open-apis/wiki/v2/spaces/sp-1/nodes":
			spaceNodesCalls.Add(1)
			parent := r.URL.Query().Get("parent_node_token")
			pt := r.URL.Query().Get("page_token")
			switch {
			case parent == "" && pt == "":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"items": []map[string]any{
							{"node_token": "n-root", "obj_token": "d-root", "obj_type": "docx", "title": "Platform", "has_child": true, "obj_edit_time": "200"},
						},
						"has_more":   true,
						"page_token": "np2",
					},
				})
			case parent == "" && pt == "np2":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"items": []map[string]any{
							{"node_token": "n-root", "obj_token": "d-root", "obj_type": "docx", "title": "Duplicate"}, // deduplicated
							{"node_token": "n-sheet", "obj_token": "s-1", "obj_type": "sheet", "title": "Sheet Metrics", "has_child": false, "obj_edit_time": "100"},
						},
						"has_more": false,
					},
				})
			case parent == "n-root":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"items": []map[string]any{
							{"node_token": "n-child", "obj_token": "d-child", "obj_type": "docx", "title": "Gateway", "has_child": false, "obj_edit_time": "300"},
						},
						"has_more": false,
					},
				})
			}
		case "/open-apis/wiki/v2/spaces/sp-2/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"items": []any{}, "has_more": false},
			})
		case "/open-apis/docx/v1/documents/d-root/raw_content":
			w.WriteHeader(http.StatusInternalServerError) // exercise title-match fallback in search
		case "/open-apis/docx/v1/documents/d-child/raw_content":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"content": "Zero trust gateway routing guide."},
			})
		case "/open-apis/docx/v1/documents/d-direct/raw_content":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"content": "Direct docx content."},
			})
		case "/open-apis/wiki/v2/spaces/get_node":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := newFeishuFactory().New(ConnectorSpec{
		ID:       "fs-wiki",
		Endpoint: srv.URL,
		Config:   domain.KnowledgeConnectorConfig{Extra: map[string]string{"appId": "cli_wiki"}},
		Secret:   domain.KnowledgeConnectorSecret{Token: appSecret},
	})
	if err != nil {
		t.Fatal(err)
	}
	fc := conn.(*feishuConnector)
	ctx := context.Background()
	scopeSP1 := SourceScope{Mode: "allowlist", Sources: []string{"sp-1"}}
	allScope := SourceScope{Mode: "all"}

	// 1. ListSources paginates and defaults empty space name to space_id.
	kbsRes, err := fc.CallTool(ctx, WeKnoraToolListKnowledgeBases, nil, allScope)
	if err != nil || kbsRes.Hits != 2 || !strings.Contains(kbsRes.Text, "sp-2") {
		t.Fatalf("unexpected list_knowledge_bases: res=%+v err=%v", kbsRes, err)
	}

	// 2. list_documents: validation, scope, BFS PathTitle, keyword filter, pagination, cache reuse.
	if _, err := fc.CallTool(ctx, WeKnoraToolListDocuments, nil, scopeSP1); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for missing kbID, got %v", err)
	}
	if _, err := fc.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "sp-2"}, scopeSP1); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope space, got %v", err)
	}
	docsRes, err := fc.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{
		"knowledge_base_id": "sp-1",
		"keyword":           "platform / gateway",
	}, scopeSP1)
	if err != nil || docsRes.Hits != 1 || !strings.Contains(docsRes.Text, "Platform / Gateway") {
		t.Fatalf("expected BFS PathTitle match, got %+v err=%v", docsRes, err)
	}
	callsBeforeCache := spaceNodesCalls.Load()
	emptyDocs, err := fc.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{
		"knowledge_base_id": "sp-1",
		"keyword":           "nope",
	}, scopeSP1)
	if err != nil || emptyDocs.Status != domain.KnowledgeStatusEmpty || spaceNodesCalls.Load() != callsBeforeCache {
		t.Fatalf("expected cached empty list_documents, got %+v err=%v", emptyDocs, err)
	}

	// Expired cache entries are ignored by lookupCachedNode.
	fc.cacheMu.Lock()
	fc.spaceNodes["expired-sp"] = feishuSpaceNodesCache{
		nodes:     []feishuWikiNode{{NodeToken: "expired-tok"}},
		expiresAt: time.Now().Add(-time.Minute),
	}
	fc.cacheMu.Unlock()
	if _, ok := fc.lookupCachedNode("expired-tok"); ok {
		t.Fatal("expired cache node should not be returned")
	}

	// 3. search_knowledge: empty query, out-of-scope, empty spaces, body + title-fallback scoring.
	if _, err := fc.CallTool(ctx, WeKnoraToolSearchKnowledge, nil, scopeSP1); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty query, got %v", err)
	}
	if _, err := fc.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{"query": "x", "knowledge_base_ids": []string{"sp-2"}}, scopeSP1); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope space, got %v", err)
	}
	emptySearch, err := fc.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{"query": "x"}, SourceScope{Mode: "allowlist", Sources: []string{"sp-missing"}})
	if err != nil || emptySearch.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty when no target spaces match, got %+v err=%v", emptySearch, err)
	}
	searchRes, err := fc.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{"query": "Platform", "limit": 2}, scopeSP1)
	if err != nil || searchRes.Hits != 2 || !strings.Contains(searchRes.Text, "Zero trust gateway routing guide.") {
		t.Fatalf("unexpected search result: %+v err=%v", searchRes, err)
	}

	// 4. read_document: empty ID, space/obj_token shorthand (hit & scope errors), direct obj_token in all-mode, unsupported tool.
	if _, err := fc.CallTool(ctx, WeKnoraToolReadDocument, nil, scopeSP1); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty docID, got %v", err)
	}
	shortHit, err := fc.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "sp-1/d-child"}, scopeSP1)
	if err != nil || !strings.Contains(shortHit.Text, "Zero trust gateway routing guide.") {
		t.Fatalf("expected shorthand read to succeed, got %+v err=%v", shortHit, err)
	}
	if _, err := fc.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "sp-2/d-child"}, scopeSP1); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for wrong space in shorthand, got %v", err)
	}
	if _, err := fc.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "sp-1/d-unknown"}, scopeSP1); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for unknown doc in allowlisted space, got %v", err)
	}
	directHit, err := fc.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "d-direct"}, allScope)
	if err != nil || !strings.Contains(directHit.Text, "Direct docx content.") {
		t.Fatalf("expected all-mode direct docx read to succeed, got %+v err=%v", directHit, err)
	}
	if _, err := fc.CallTool(ctx, "bad_tool", nil, allScope); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for unsupported tool, got %v", err)
	}
}
