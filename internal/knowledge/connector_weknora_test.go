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

func TestWeKnoraValidateURLFactoryAndRedirect(t *testing.T) {
	f := newWeKnoraFactory()
	if meta := f.Meta(); meta.Type != domain.ConnectorTypeWeKnora || !meta.SupportsSubSources {
		t.Fatalf("unexpected weknora meta: %+v", meta)
	}

	norm, err := ValidateOutboundWeKnoraURL(" https://weknora.example.com/api/v1/#frag ")
	if err != nil || norm != "https://weknora.example.com" {
		t.Fatalf("expected stripped /api/v1 URL, got %q err=%v", norm, err)
	}

	badURLs := []string{
		"",
		"http://weknora\n.example.com",
		"ftp://weknora.example.com",
		"http://",
		"http://user:pass@weknora.example.com",
		"http://weknora.example.com?debug=1",
		"http://weknora.example.com?",
		"http://169.254.169.254",
		"http://100.64.0.1",
	}
	for _, raw := range badURLs {
		if _, err := ValidateOutboundWeKnoraURL(raw); !isAppErrKind(err, apperr.Invalid) {
			t.Fatalf("expected Invalid for URL %q, got %v", raw, err)
		}
	}

	if _, _, _, err := f.ValidateAndNormalize("https://wk.example.com", domain.KnowledgeConnectorConfig{TimeoutSec: 301}, domain.KnowledgeConnectorSecret{Token: "k"}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for timeout >300, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("https://wk.example.com", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, false); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for missing key, got %v", err)
	}
	if _, _, _, err := f.ValidateAndNormalize("https://wk.example.com", domain.KnowledgeConnectorConfig{}, domain.KnowledgeConnectorSecret{}, true); err != nil {
		t.Fatalf("expected existing secret allowed, got %v", err)
	}

	if _, err := f.New(ConnectorSpec{Endpoint: "https://wk.example.com"}); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid when New lacks credential, got %v", err)
	}
	conn, err := f.New(ConnectorSpec{
		ID:       "wk-1",
		Endpoint: "https://wk.example.com/api/v1",
		Config:   domain.KnowledgeConnectorConfig{TimeoutSec: 18},
		Secret:   domain.KnowledgeConnectorSecret{Token: "Bearer api-key-1"},
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	wc := conn.(*weknoraConnector)
	if wc.Type() != domain.ConnectorTypeWeKnora || wc.http.Timeout != 18*time.Second {
		t.Fatalf("unexpected connector state: type=%s timeout=%v", wc.Type(), wc.http.Timeout)
	}
	if tools, err := wc.Tools(context.Background()); err != nil || len(tools) != 4 {
		t.Fatalf("expected 4 tools, got len=%d err=%v", len(tools), err)
	}

	// checkWeKnoraRedirect rules: empty via ok, same-origin ok, cross-origin rejected, >5 rejected.
	sameReq, _ := http.NewRequest(http.MethodGet, "https://wk.example.com/b", nil)
	originReq, _ := http.NewRequest(http.MethodGet, "https://wk.example.com/a", nil)
	if err := checkWeKnoraRedirect(sameReq, nil); err != nil {
		t.Fatalf("empty via should be allowed: %v", err)
	}
	if err := checkWeKnoraRedirect(sameReq, []*http.Request{originReq}); err != nil {
		t.Fatalf("same-origin redirect should be allowed: %v", err)
	}
	crossReq, _ := http.NewRequest(http.MethodGet, "https://evil.example.com/steal", nil)
	if err := checkWeKnoraRedirect(crossReq, []*http.Request{originReq}); err == nil || !strings.Contains(err.Error(), "跨源重定向") {
		t.Fatalf("expected cross-origin redirect rejection, got %v", err)
	}
	tooMany := make([]*http.Request, weknoraMaxRedirects+1)
	for i := range tooMany {
		tooMany[i] = originReq
	}
	if err := checkWeKnoraRedirect(sameReq, tooMany); err == nil {
		t.Fatal("expected redirect hop limit error")
	}
}

func TestWeKnoraToolsSearchPartialFailurePaginationAndReadDoc(t *testing.T) {
	const apiKey = "wk-secret-key-99"
	var failKBs atomic.Bool
	var failKB2Search atomic.Bool
	var failAllSearch atomic.Bool
	var failDocs atomic.Bool
	var metaCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != apiKey || r.Header.Get("X-Tenant-ID") != "tenant-7" || r.Header.Get("X-Cfg") != "cfg-v" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases":
			if failKBs.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"down ` + apiKey + `"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "kb-1", "name": "KB One", "knowledge_count": 2},
					{"id": "kb-2", "name": "KB Two", "knowledge_count": 3},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-1/hybrid-search":
			if failAllSearch.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "c1", "content": "chunk one", "knowledge_id": "doc-1", "chunk_index": 2, "score": 0.7},
					{"id": "c2", "content": "chunk zero", "knowledge_id": "doc-1", "chunk_index": 1, "score": 0.9},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge-bases/kb-2/hybrid-search":
			if failAllSearch.Load() || failKB2Search.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"kb-2 error"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data": []map[string]any{
					{"id": "c3", "content": "kb2 hit", "knowledge_id": "doc-2", "chunk_index": 0, "score": 0.8},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge-bases/kb-1/knowledge":
			if failDocs.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if r.URL.Query().Get("keyword") == "none" {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "total": 0, "data": nil})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"total":   1,
				"data":    []map[string]any{{"id": "doc-1", "knowledge_base_id": "kb-1", "title": "Spec", "file_name": "spec.md"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/knowledge/doc-1":
			metaCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"data":    map[string]any{"id": "doc-1", "knowledge_base_id": "kb-1", "title": "Spec", "file_name": "spec.md"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/chunks/doc-1":
			page := r.URL.Query().Get("page")
			switch page {
			case "1":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"total":   3,
					"data": []map[string]any{
						{"id": "c0", "content": "Alpha intro", "chunk_index": 0},
						{"id": "c1", "content": "Beta Go Auth", "chunk_index": 1},
					},
				})
			case "2":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"total":   3,
					"data": []map[string]any{
						{"id": "c2", "content": "Gamma Go End", "chunk_index": 2},
					},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "total": 3, "data": []any{}})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := newWeKnoraFactory().New(ConnectorSpec{
		ID:       "kc-wk",
		Endpoint: srv.URL,
		Config: domain.KnowledgeConnectorConfig{
			Headers: map[string]string{"X-Cfg": "cfg-v"},
			Extra:   map[string]string{"tenantId": "tenant-7"},
		},
		Secret: domain.KnowledgeConnectorSecret{Token: apiKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	allScope := SourceScope{Mode: "all"}

	// 1. Check failure redacts API key; Check success lists sources.
	failKBs.Store(true)
	if chk := conn.Check(ctx); chk.OK || strings.Contains(chk.Error, apiKey) {
		t.Fatalf("expected Check failure with secret redacted, got %+v", chk)
	}
	if _, err := conn.CallTool(ctx, WeKnoraToolListKnowledgeBases, nil, allScope); err == nil {
		t.Fatal("expected error from list_knowledge_bases when upstream 500")
	}
	failKBs.Store(false)
	if chk := conn.Check(ctx); !chk.OK || len(chk.Sources) != 2 {
		t.Fatalf("expected Check OK, got %+v", chk)
	}
	if emptyKB, err := conn.CallTool(ctx, WeKnoraToolListKnowledgeBases, nil, SourceScope{Mode: "allowlist", Sources: []string{"missing"}}); err != nil || emptyKB.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty for unmatched allowlist, got %+v err=%v", emptyKB, err)
	}

	// 2. search_knowledge: empty query, arg aliases, deterministic sorting, partial failure, full failure.
	if _, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{}, allScope); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty query, got %v", err)
	}
	searchRes, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{
		"query":             "chunk",
		"knowledge_base_id": "kb-1",
		"limit":             1,
	}, allScope)
	if err != nil || searchRes.Hits != 1 || !strings.Contains(searchRes.Text, "chunk zero") {
		t.Fatalf("expected top-scored chunk zero with limit=1, got %+v err=%v", searchRes, err)
	}

	// Partial failure: kb-1 succeeds, kb-2 fails -> KnowledgeStatusPartial with warning.
	failKB2Search.Store(true)
	partial, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{
		"query":   "chunk",
		"sources": []string{"kb-1", "kb-2"},
	}, allScope)
	if err != nil || partial.Status != domain.KnowledgeStatusPartial || partial.Warning == "" || partial.Hits != 2 {
		t.Fatalf("expected partial status with warning, got %+v err=%v", partial, err)
	}
	failKB2Search.Store(false)

	// All KBs fail -> apperr.Upstream.
	failAllSearch.Store(true)
	if _, err := conn.CallTool(ctx, WeKnoraToolSearchKnowledge, map[string]any{"query": "chunk"}, allScope); !isAppErrKind(err, apperr.Upstream) {
		t.Fatalf("expected Upstream when all KBs fail, got %v", err)
	}
	failAllSearch.Store(false)

	// 3. list_documents: missing kbID, out-of-scope, empty, upstream error, normal defaults.
	if _, err := conn.CallTool(ctx, WeKnoraToolListDocuments, nil, allScope); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for missing kbID, got %v", err)
	}
	if _, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "kb-2"}, SourceScope{Mode: "allowlist", Sources: []string{"kb-1"}}); !isAppErrKind(err, apperr.Forbidden) {
		t.Fatalf("expected Forbidden for out-of-scope kb, got %v", err)
	}
	if emptyDocs, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "kb-1", "keyword": "none", "page": -1, "page_size": 999}, allScope); err != nil || emptyDocs.Status != domain.KnowledgeStatusEmpty {
		t.Fatalf("expected Empty docs, got %+v err=%v", emptyDocs, err)
	}
	failDocs.Store(true)
	if _, err := conn.CallTool(ctx, WeKnoraToolListDocuments, map[string]any{"knowledge_base_id": "kb-1"}, allScope); err == nil {
		t.Fatal("expected error on upstream list_documents failure")
	}
	failDocs.Store(false)

	// 4. read_document: missing ID, cross-page chunk stitching, query filter, meta cache, range summary.
	if _, err := conn.CallTool(ctx, WeKnoraToolReadDocument, nil, allScope); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for empty doc ID, got %v", err)
	}
	readRes, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{
		"path":   "doc-1",
		"offset": 1,
		"limit":  2,
		"query":  "go",
	}, SourceScope{Mode: "allowlist", Sources: []string{"kb-1"}})
	if err != nil || readRes.Hits != 2 || len(readRes.Evidences) != 1 || readRes.Evidences[0].Locator != "#chunk-1..#chunk-2" {
		t.Fatalf("unexpected stitched read_document result: %+v err=%v", readRes, err)
	}
	// Second read reuses cached docMeta (metaCalls stays 1) and query miss yields Empty.
	emptyRead, err := conn.CallTool(ctx, WeKnoraToolReadDocument, map[string]any{
		"knowledge_id": "doc-1",
		"offset":       -1,
		"limit":        500,
		"query":        "no-such-word",
	}, allScope)
	if err != nil || emptyRead.Status != domain.KnowledgeStatusEmpty || metaCalls.Load() != 1 {
		t.Fatalf("unexpected cached empty read: res=%+v err=%v metaCalls=%d", emptyRead, err, metaCalls.Load())
	}

	if got := summarizeWeKnoraChunkRange(nil); got != "" {
		t.Fatalf("expected empty range, got %q", got)
	}
	if got := summarizeWeKnoraChunkRange([]weKnoraChunk{{ChunkIndex: 5}}); got != "#chunk-5" {
		t.Fatalf("expected #chunk-5, got %q", got)
	}
	if _, err := conn.CallTool(ctx, "unknown", nil, allScope); !isAppErrKind(err, apperr.Invalid) {
		t.Fatalf("expected Invalid for unsupported tool, got %v", err)
	}
}

func TestWeKnoraErrorMessageParsing(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", "Bad Gateway"},
		{"error string", `{"error":"bad api key"}`, "bad api key"},
		{"error object", `{"error":{"message":"nested msg"}}`, "nested msg"},
		{"top message", `{"message":"top msg"}`, "top msg"},
		{"raw text", "upstream gateway error", "upstream gateway error"},
	}
	for _, tc := range cases {
		if got := parseWeKnoraErrorMessage(http.StatusBadGateway, []byte(tc.raw)); got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{invalid-json"))
	}))
	defer srv.Close()
	conn, err := newWeKnoraFactory().New(ConnectorSpec{
		ID:       "wk-parse",
		Endpoint: srv.URL,
		Secret:   domain.KnowledgeConnectorSecret{Token: "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ListSources(context.Background()); err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("expected JSON parse error, got %v", err)
	}
}
