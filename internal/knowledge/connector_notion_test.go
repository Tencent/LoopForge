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

func TestNotionFactoryValidateAndNew(t *testing.T) {
	f := newNotionFactory()
	meta := f.Meta()
	if meta.Type != domain.ConnectorTypeNotion || !meta.SupportsSubSources {
		t.Fatalf("unexpected notion meta: %+v", meta)
	}

	// Empty endpoint defaults to official Notion API URL and strips "Bearer " prefix.
	ep, cfg, sec, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{
		TimeoutSec:     15,
		Headers:        map[string]string{" X-Trace ": " 1 ", "": "drop"},
		DefaultSources: []string{" page-a ", "page-a", ""},
		Extra:          map[string]string{" k ": " v ", "empty": "   "},
	}, domain.KnowledgeConnectorSecret{Token: "Bearer ntn_secret_1"}, false)
	if err != nil {
		t.Fatalf("expected valid default config, got %v", err)
	}
	if ep != notionDefaultBaseURL || sec.Token != "ntn_secret_1" || sec.AuthType != domain.ConnectorAuthBearer {
		t.Fatalf("unexpected normalized output: ep=%q sec=%+v", ep, sec)
	}
	if len(cfg.DefaultSources) != 1 || cfg.Headers["X-Trace"] != "1" || cfg.Extra["k"] != "v" {
		t.Fatalf("unexpected normalized config: %+v", cfg)
	}

	// Missing token allowed only when hasExistingSecret=true.
	if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for missing token, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, true); err != nil {
		t.Fatalf("expected existing secret to be accepted, got %v", err)
	}

	// Invalid endpoints and timeout bounds.
	badEndpoints := []string{
		"ftp://api.notion.com",
		"https://api.notion\n.com",
		"https://user:pass@api.notion.com",
		"https://api.notion.com?foo=bar",
		"http://169.254.169.254",
		"http://100.100.100.200",
	}
	for _, raw := range badEndpoints {
		if _, _, _, err := f.ValidateAndNormalize(raw, domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{Token: "ntn_1"}, false); !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for endpoint %q, got %v", raw, err)
		}
	}
	for _, badTimeout := range []int{-1, 301} {
		if _, _, _, err := f.ValidateAndNormalize("", domain.KnowledgeConnectorConfig{TimeoutSec: badTimeout}, domain.KnowledgeConnectorSecret{Token: "ntn_1"}, false); !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for timeout %d, got %v", badTimeout, err)
		}
	}

	// New() validation and timeout propagation.
	if _, err := f.New(ConnectorSpec{Endpoint: "https://api.notion.com"}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when New lacks token, got %v", err)
	}
	if _, err := f.New(ConnectorSpec{Endpoint: "ftp://bad", Secret: domain.KnowledgeConnectorSecret{Token: "ntn_1"}}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when New gets bad endpoint, got %v", err)
	}
	conn, err := f.New(ConnectorSpec{
		ID:       "notion-1",
		Endpoint: "https://api.notion.com/",
		Config:   domain.KnowledgeConnectorConfig{TimeoutSec: 12},
		Secret:   domain.KnowledgeConnectorSecret{Token: "Bearer ntn_abc"},
	})
	if err != nil {
		t.Fatalf("unexpected New error: %v", err)
	}
	nc := conn.(*notionConnector)
	if nc.Type() != domain.ConnectorTypeNotion || nc.baseURL != notionDefaultBaseURL || nc.http.Timeout != 12*time.Second {
		t.Fatalf("unexpected connector state: type=%s base=%s timeout=%v", nc.Type(), nc.baseURL, nc.http.Timeout)
	}
	tools, err := nc.Tools(context.Background())
	if err != nil || len(tools) != 4 {
		t.Fatalf("expected 4 native read-only tools, got len=%d err=%v", len(tools), err)
	}
}

func TestNotionScopeAndIDNormalization(t *testing.T) {
	if got := normalizeNotionID(" 11111111-2222-3333-4444-555555555555 "); got != "11111111222233334444555555555555" {
		t.Fatalf("unexpected normalized notion id: %q", got)
	}

	allScope := SourceScope{Mode: "all"}
	if !notionScopeAllows(allScope, "any-id") {
		t.Fatal("mode=all should allow all ids")
	}
	ids, err := notionFilterScopeIDs(allScope, []string{"AA-BB", "cc-dd"})
	if err != nil || len(ids) != 2 || ids[0] != "aabb" || ids[1] != "ccdd" {
		t.Fatalf("unexpected all-mode filter result: ids=%v err=%v", ids, err)
	}

	emptyAllowlist := SourceScope{Mode: "allowlist"}
	if notionScopeAllows(emptyAllowlist, "") || notionScopeAllows(emptyAllowlist, "aa") {
		t.Fatal("empty allowlist must reject")
	}
	if _, err := notionFilterScopeIDs(emptyAllowlist, nil); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for empty allowlist, got %v", err)
	}

	allow := SourceScope{Mode: "allowlist", Sources: []string{"11111111-1111-1111-1111-111111111111"}}
	if !notionScopeAllows(allow, "11111111111111111111111111111111") {
		t.Fatal("expected hyphenated/compact UUID equivalence")
	}
	if notionScopeAllows(allow, "") || notionScopeAllows(allow, "22222222222222222222222222222222") {
		t.Fatal("unexpected allow for empty or foreign ID")
	}
	if got, err := notionFilterScopeIDs(allow, nil); err != nil || len(got) != 1 || got[0] != "11111111111111111111111111111111" {
		t.Fatalf("expected default allowlist expansion, got %v err=%v", got, err)
	}
	if _, err := notionFilterScopeIDs(allow, []string{"22222222-2222-2222-2222-222222222222"}); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope requested ID, got %v", err)
	}
}

func TestNotionBlocksToMarkdownAllTypes(t *testing.T) {
	var invalid notionBlock
	if err := json.Unmarshal([]byte(`"not-an-object"`), &invalid); err == nil {
		t.Fatal("expected error unmarshaling non-object block")
	}

	rawBlocks := `[
		{"id":"b1","type":"heading_1","heading_1":{"rich_text":[{"plain_text":"H1"}]}},
		{"id":"b2","type":"heading_2","heading_2":{"rich_text":[{"plain_text":"H2"}]}},
		{"id":"b3","type":"heading_3","heading_3":{"rich_text":[{"plain_text":"H3"}]}},
		{"id":"b3e","type":"heading_3","heading_3":{"rich_text":[]}},
		{"id":"b4","type":"paragraph","paragraph":{"rich_text":[{"plain_text":"Body text"}]}},
		{"id":"b4e","type":"paragraph","paragraph":{"rich_text":[]}},
		{"id":"b5","type":"bulleted_list_item","bulleted_list_item":{"rich_text":[{"plain_text":"Bullet"}]}},
		{"id":"b6","type":"numbered_list_item","numbered_list_item":{"rich_text":[{"plain_text":"Step 1"}]}},
		{"id":"b7","type":"to_do","to_do":{"checked":true,"rich_text":[{"plain_text":"Done"}]}},
		{"id":"b8","type":"to_do","to_do":{"checked":false,"rich_text":[{"plain_text":"Todo"}]}},
		{"id":"b8bad","type":"to_do","to_do":"bad"},
		{"id":"b9","type":"quote","quote":{"rich_text":[{"plain_text":"Quoted"}]}},
		{"id":"b10","type":"callout","callout":{"rich_text":[{"plain_text":"Note"}]}},
		{"id":"b11","type":"code","code":{"language":"plain text","rich_text":[{"plain_text":"echo hi"}]}},
		{"id":"b11bad","type":"code","code":"bad"},
		{"id":"b12","type":"table_row","table_row":{"cells":[[{"plain_text":"A|B"}],[{"plain_text":"C"}]]}},
		{"id":"b12e","type":"table_row","table_row":{"cells":[]}},
		{"id":"b13","type":"divider","divider":{}},
		{"id":"b14","type":"toggle","toggle":{"rich_text":[{"plain_text":"Details"}]}},
		{"id":"b15","type":"child_page","child_page":{"title":"Sub Page"}},
		{"id":"b16","type":"child_database","child_database":{"title":"Tasks DB"}},
		{"id":"b16e","type":"child_database","child_database":{"title":""}},
		{"id":"b17","type":"bookmark","bookmark":{"rich_text":[{"plain_text":"https://example.com"}]}}
	]`
	var blocks []notionBlock
	if err := json.Unmarshal([]byte(rawBlocks), &blocks); err != nil {
		t.Fatalf("unmarshal blocks: %v", err)
	}
	// Attach child block to toggle (index 18) to verify nested indentation.
	var childBlk notionBlock
	if err := json.Unmarshal([]byte(`{"id":"c1","type":"paragraph","paragraph":{"rich_text":[{"plain_text":"Inner detail"}]}}`), &childBlk); err != nil {
		t.Fatal(err)
	}
	for i := range blocks {
		if blocks[i].ID == "b14" {
			blocks[i].Children = []notionBlock{childBlk}
		}
	}

	md := notionBlocksToMarkdown(blocks, 0)
	wants := []string{
		"# H1",
		"## H2",
		"### H3",
		"Body text",
		"- Bullet",
		"1. Step 1",
		"- [x] Done",
		"- [ ] Todo",
		"> Quoted",
		"> Note",
		"```\necho hi\n```",
		"| A\\|B | C |",
		"---",
		"- Details\nInner detail",
		"- Sub Page",
		"- Tasks DB",
		"https://example.com",
	}
	for _, want := range wants {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered markdown missing %q:\n%s", want, md)
		}
	}
}

func TestNotionHierarchyListSearchReadAndPagination(t *testing.T) {
	const token = "ntn_hier_secret"
	var searchCalls atomic.Int32
	var failPage2 atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/users/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "user", "id": "bot"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/search":
			searchCalls.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			q, _ := body["query"].(string)
			if q == "nothing" {
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{
						"object": "page",
						"id":     "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
						"url":    "https://notion.so/root",
						"parent": map[string]any{"type": "workspace", "workspace": true},
						"properties": map[string]any{
							"Name": map[string]any{"type": "title", "title": []map[string]any{{"plain_text": "Root Engineering"}}},
						},
					},
					{
						"object": "page",
						"id":     "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
						"url":    "https://notion.so/child",
						"parent": map[string]any{"type": "page_id", "page_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
						"properties": map[string]any{
							"Name": map[string]any{"type": "title", "title": []map[string]any{{"plain_text": "Child Guide"}}},
						},
					},
					{
						"object": "database",
						"id":     "cccccccc-cccc-cccc-cccc-cccccccccccc",
						"url":    "https://notion.so/db",
						"parent": map[string]any{"type": "data_source_id", "data_source_id": "dddddddd-dddd-dddd-dddd-dddddddddddd"},
						"title":  []map[string]any{{"plain_text": "Orphan DB"}},
					},
					{
						"object":   "page",
						"id":       "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
						"archived": true,
						"parent":   map[string]any{"type": "workspace", "workspace": true},
					},
					{
						"object":   "page",
						"id":       "ffffffff-ffff-ffff-ffff-ffffffffffff",
						"in_trash": true,
						"parent":   map[string]any{"type": "workspace", "workspace": true},
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/pages/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "page",
				"id":     "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
				"url":    "https://notion.so/child",
				"parent": map[string]any{"type": "page_id", "page_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
				"properties": map[string]any{
					"Name": map[string]any{"type": "title", "title": []map[string]any{{"plain_text": "Child Guide"}}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/pages/archived-page":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object":   "page",
				"id":       "archived-page",
				"archived": true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/blocks/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb/children":
			cur := r.URL.Query().Get("start_cursor")
			switch cur {
			case "":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": []map[string]any{
						{"id": "p1", "type": "paragraph", "has_children": true, "paragraph": map[string]any{"rich_text": []map[string]any{{"plain_text": "Page1 keyword"}}}},
						{"id": "cp", "type": "child_page", "has_children": true, "child_page": map[string]any{"title": "Nested Page"}},
					},
					"has_more":    true,
					"next_cursor": "cur-2",
				})
			case "cur-2":
				if failPage2.Load() {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"page 2 failed"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": []map[string]any{
						{"id": "p2", "type": "paragraph", "paragraph": map[string]any{"rich_text": []map[string]any{{"plain_text": "Page2 end"}}}},
					},
					"has_more": false,
				})
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/blocks/p1/children":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"id": "p1-sub", "type": "bulleted_list_item", "bulleted_list_item": map[string]any{"rich_text": []map[string]any{{"plain_text": "Indented child"}}}},
				},
				"has_more": false,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := newNotionFactory().New(ConnectorSpec{
		ID:       "kc-notion",
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: token},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rootID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	scopeRoot := SourceScope{Mode: "allowlist", Sources: []string{rootID}}

	// 1. list_knowledge_bases builds hierarchy cache, respects allowlist, and reuses cache.
	res, err := conn.CallTool(ctx, WeKnoraToolListKnowledgeBases, nil, scopeRoot)
	if err != nil || res.Status != domain.KnowledgeStatusHit || res.Hits != 1 {
		t.Fatalf("list_knowledge_bases failed: res=%+v err=%v", res, err)
	}
	if !strings.Contains(res.Text, "Root Engineering") || strings.Contains(res.Text, "Orphan DB") {
		t.Fatalf("allowlist filtering failed: %s", res.Text)
	}
	callsAfterFirst := searchCalls.Load()
	emptyRes, err := conn.CallTool(ctx, WeKnoraToolListKnowledgeBases, nil, SourceScope{Mode: "allowlist", Sources: []string{"0000"}})
	if err != nil || emptyRes.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty for non-matching allowlist, got res=%+v err=%v", emptyRes, err)
	}
	if searchCalls.Load() != callsAfterFirst {
		t.Fatalf("expected hierarchy cache hit, got searchCalls=%d -> %d", callsAfterFirst, searchCalls.Load())
	}

	// 2. list_documents validation, scope, keyword filter, pagination, empty status.
	if _, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{}, scopeRoot); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty knowledge_base_id, got %v", err)
	}
	if _, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "cccccccccccccccccccccccccccccccc"}, scopeRoot); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope knowledge_base_id, got %v", err)
	}
	docsRes, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{
		"knowledge_base_id": rootID,
		"keyword":           "child",
		"page":              1,
		"page_size":         5,
	}, scopeRoot)
	if err != nil || docsRes.Hits != 1 || !strings.Contains(docsRes.Text, "Child Guide") {
		t.Fatalf("unexpected list_documents result: res=%+v err=%v", docsRes, err)
	}
	docsEmpty, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{
		"knowledge_base_id": rootID,
		"keyword":           "nonexistent",
	}, scopeRoot)
	if err != nil || docsEmpty.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty list_documents, got res=%+v err=%v", docsEmpty, err)
	}

	// 3. search_knowledge validation, scope filtering, preview snippet, empty results.
	if _, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{}, scopeRoot); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty query, got %v", err)
	}
	if _, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{
		"query":              "guide",
		"knowledge_base_ids": []string{"cccccccccccccccccccccccccccccccc"},
	}, scopeRoot); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope search kb, got %v", err)
	}
	searchRes, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{
		"query": "keyword",
		"limit": 100,
	}, SourceScope{Mode: "all"})
	if err != nil || searchRes.Hits != 3 || !strings.Contains(searchRes.Text, "Page1 keyword") {
		t.Fatalf("unexpected search result: res=%+v err=%v", searchRes, err)
	}
	searchNothing, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{"query": "nothing"}, scopeRoot)
	if err != nil || searchNothing.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty search result, got res=%+v err=%v", searchNothing, err)
	}

	// 4. read_document: validation, archived -> NotFound, scope -> Forbidden, pagination + child expansion.
	if _, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{}, scopeRoot); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty knowledge_id, got %v", err)
	}
	if _, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "archived-page"}, scopeRoot); !isAppErrKind(err, apperr.NotFound) {
		t.Fatalf("expected NotFound for archived page, got %v", err)
	}
	if _, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, SourceScope{Mode: "allowlist", Sources: []string{"cccccccccccccccccccccccccccccccc"}}); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope page, got %v", err)
	}
	readRes, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, scopeRoot)
	if err != nil || !strings.Contains(readRes.Text, "Indented child") || !strings.Contains(readRes.Text, "Page2 end") {
		t.Fatalf("unexpected read_document result: res=%+v err=%v", readRes, err)
	}

	// Page-2 failure degrades gracefully keeping Page 1 content.
	failPage2.Store(true)
	readPartial, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{"knowledge_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, scopeRoot)
	if err != nil || !strings.Contains(readPartial.Text, "Page1 keyword") {
		t.Fatalf("expected graceful partial read on page-2 error, got res=%+v err=%v", readPartial, err)
	}

	// 5. Unsupported tool returns Invalid.
	if _, err := conn.CallTool(ctx, "unknown_tool", nil, scopeRoot); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for unknown tool, got %v", err)
	}
}

func TestNotionErrorMappingAndRateLimit(t *testing.T) {
	if got := parseRetryAfter("2"); got != 2*time.Second {
		t.Fatalf("expected 2s, got %v", got)
	}
	if got := parseRetryAfter("99"); got != notionMaxRetryAfter {
		t.Fatalf("expected capped %v, got %v", notionMaxRetryAfter, got)
	}
	if got := parseRetryAfter("bad"); got != time.Second {
		t.Fatalf("expected fallback 1s, got %v", got)
	}

	const secretTok = "ntn_leak_guard_999"
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad token ` + secretTok + `"}`))
		case 2:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"forbidden"}`))
		case 3:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("plain error containing " + secretTok))
		case 4:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{bad-json`))
		case 5:
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"slow down"}`))
		case 6:
			if r.URL.Path == "/v1/users/me" {
				_, _ = w.Write([]byte(`{"id":"bot"}`))
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"search broken ` + secretTok + `"}`))
		}
	}))
	defer srv.Close()

	conn, err := newNotionFactory().New(ConnectorSpec{
		ID:       "kc-err",
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: secretTok},
	})
	if err != nil {
		t.Fatal(err)
	}
	nc := conn.(*notionConnector)
	ctx := context.Background()

	// 401 -> apperr.Upstream ("Token 无效")
	mode.Store(1)
	if err := nc.doJSON(ctx, http.MethodGet, "/v1/users/me", nil, nil); !isAppErrKind(err, apperr.Upstream) || !strings.Contains(err.Error(), "Token 无效") {
		t.Fatalf("expected Upstream 401 message, got %v", err)
	}
	if chk := nc.Check(ctx); chk.OK || strings.Contains(chk.Error, secretTok) {
		t.Fatalf("expected Check failure without secret leak, got %+v", chk)
	}

	// 403 -> apperr.Upstream ("Connections")
	mode.Store(2)
	if err := nc.doJSON(ctx, http.MethodGet, "/v1/users/me", nil, nil); !isAppErrKind(err, apperr.Upstream) || !strings.Contains(err.Error(), "Connections") {
		t.Fatalf("expected Upstream 403 message, got %v", err)
	}

	// 500 -> plain text error with secret redacted
	mode.Store(3)
	err = nc.doJSON(ctx, http.MethodGet, "/v1/users/me", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "Notion HTTP 500") || strings.Contains(err.Error(), secretTok) {
		t.Fatalf("unexpected 500 error: %v", err)
	}

	// 200 invalid JSON -> parse error
	mode.Store(4)
	var out map[string]any
	if err := nc.doJSON(ctx, http.MethodGet, "/v1/users/me", nil, &out); err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("expected parse error, got %v", err)
	}

	// 429 retry wait honors context cancellation promptly
	mode.Store(5)
	cctx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if err := nc.doJSON(cctx, http.MethodGet, "/v1/users/me", nil, nil); err == nil {
		t.Fatal("expected context deadline/cancel error during 429 wait")
	}

	// Check fails when /v1/users/me succeeds but ListSources (/v1/search) fails, redacting secrets.
	mode.Store(6)
	chk := nc.Check(ctx)
	if chk.OK || strings.Contains(chk.Error, secretTok) {
		t.Fatalf("expected Check failure on ListSources without secret leak, got %+v", chk)
	}
}
