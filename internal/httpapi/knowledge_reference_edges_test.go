package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
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

func TestKnowledgeCallErrorRedactsUpstream(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "超时"},
		{"403", errors.New("gateway HTTP 403: secret upstream body"), "拒绝访问"},
		{"401", errors.New("gateway HTTP 401: Bearer leaked-token"), "拒绝访问"},
		{"generic", errors.New("gateway HTTP 500: internal detail https://secret.host"), "不可用"},
		{"plain", errors.New("connection reset by peer"), "不可用"},
		{"mcp tool validation", errors.New("mcp tool search_knowledge: knowledge base \"foo\" was not found"), "was not found"},
		{"mcp tool with url redacted", errors.New("mcp tool search_knowledge: failed to reach https://secret.host"), "不可用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := knowledgeCallError(tc.err).Error()
			if !strings.Contains(got, tc.want) {
				t.Fatalf("knowledgeCallError(%v) = %q, want containing %q", tc.err, got, tc.want)
			}
			// Must never echo upstream bodies, tokens or hosts.
			for _, leak := range []string{"secret upstream body", "leaked-token", "secret.host", "internal detail"} {
				if strings.Contains(got, leak) {
					t.Fatalf("knowledgeCallError leaked %q into %q", leak, got)
				}
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (timeoutError) Unwrap() error   { return nil }

func TestKnowledgeCallErrorNetTimeout(t *testing.T) {
	var nerr net.Error = timeoutError{}
	got := knowledgeCallError(nerr).Error()
	if !strings.Contains(got, "超时") {
		t.Fatalf("net timeout error = %q, want 超时", got)
	}
}

func TestKnowledgeReadResultStates(t *testing.T) {
	cases := []struct {
		name       string
		serverID   string
		tool       string
		text       string
		hits       int
		err        error
		wantStatus string
		wantCite   bool
	}{
		{"success with text", domain.KnowledgeMCPServerID, knowledge.ToolSearch, "some answer", 1, nil, domain.KnowledgeStatusHit, true},
		{"blank text", domain.KnowledgeMCPServerID, knowledge.ToolFetch, "   ", 0, nil, domain.KnowledgeStatusEmpty, false},
		{"search zero hits", domain.KnowledgeMCPServerID, knowledge.ToolSearch, `{"results":[]}`, 0, nil, domain.KnowledgeStatusEmpty, false},
		{"external empty json results", "handbook", knowledge.ToolSearch, `{"results":[]}`, 0, nil, domain.KnowledgeStatusEmpty, false},
		{"external plain text unstructured", "handbook", knowledge.ToolSearch, "raw", 0, nil, domain.KnowledgeStatusUnstructured, true},
		{"call failure", domain.KnowledgeMCPServerID, knowledge.ToolSearch, "", 0, errors.New("boom"), domain.KnowledgeStatusUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read, text := knowledgeReadResult(tc.serverID, tc.tool, tc.text, tc.hits, tc.err)
			if read.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", read.Status, tc.wantStatus)
			}
			if read.ID == "" {
				t.Fatal("knowledge read id must not be empty")
			}
			if read.ServerID != tc.serverID || read.Tool != tc.tool {
				t.Fatalf("read = %+v, unexpected server/tool", read)
			}
			cited := strings.Contains(text, "[[knowledge:"+read.ID+"]]")
			if cited != tc.wantCite {
				t.Fatalf("citation marker present = %v, want %v (text=%q)", cited, tc.wantCite, text)
			}
		})
	}
}

func TestStringList(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"string slice", []string{"a", "b"}, []string{"a", "b"}},
		{"any slice", []any{"a", "b"}, []string{"a", "b"}},
		{"any slice skip non-string", []any{"a", 1, "b"}, []string{"a", "b"}},
		{"comma string", "a,b,c", []string{"a", "b", "c"}},
		{"single string", "a", []string{"a"}},
		{"blank string", "  ", nil},
		{"other type", 42, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stringList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("stringList(%v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestValueOr(t *testing.T) {
	if got := valueOr(nil, "fallback"); got != "fallback" {
		t.Fatalf("valueOr(nil) = %v", got)
	}
	if got := valueOr("value", "fallback"); got != "value" {
		t.Fatalf("valueOr(value) = %v", got)
	}
	if got := valueOr(0, 99); got != 0 {
		t.Fatalf("valueOr(0) must keep zero value, got %v", got)
	}
}

func TestKnowledgeToolDescription(t *testing.T) {
	got := knowledgeToolDescription("团队档案库", "包含成员技术背景与排障手册", "weknora-1", "search_knowledge", "Search docs")
	for _, want := range []string{"[用户已绑定知识库：团队档案库（包含成员技术背景与排障手册）]", "Search docs", "优先调用"} {
		if !strings.Contains(got, want) {
			t.Fatalf("knowledgeToolDescription() = %q, want containing %q", got, want)
		}
	}
	nonSearch := knowledgeToolDescription("团队档案库", "", "weknora-1", "wiki_tree", "List wiki tree")
	if strings.Contains(nonSearch, "优先调用") {
		t.Fatalf("non-search tool should not append search priority hint: %q", nonSearch)
	}
}

func TestExtractCatalogTitles(t *testing.T) {
	raw := `{"documents":[{"title":"whoami","description":"个人技术档案"},{"title":"vpc-dns-architecture"},{"title":"whoami"}]}`
	got := extractCatalogTitles(raw, 10)
	want := []string{"whoami (个人技术档案)", "vpc-dns-architecture", "whoami"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractCatalogTitles(json) = %#v, want %#v", got, want)
	}
	plain := extractCatalogTitles("- doc-a\n- doc-b\n- doc-a\n", 2)
	if !reflect.DeepEqual(plain, []string{"doc-a", "doc-b"}) {
		t.Fatalf("extractCatalogTitles(plain) = %#v, want [doc-a doc-b]", plain)
	}
}

func TestKnowledgeReferencePromptContextAndCache(t *testing.T) {
	resetKnowledgeCatalogCacheForTest()
	defer resetKnowledgeCatalogCacheForTest()

	var listDocsCalls int
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
					{"name": "search_knowledge", "description": "Search KB", "annotations": map[string]any{"readOnlyHint": true}},
					{"name": "list_documents", "description": "List docs", "annotations": map[string]any{"readOnlyHint": true}},
				},
			}})
		case "tools/call":
			if rpc.Params.Name == "list_documents" {
				listDocsCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": `{"documents":[{"title":"whoami","description":"工程师技术档案"},{"title":"vpc-dns-architecture"}]}`}},
				}})
			}
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutMCPServer(domain.MCPServerDefinition{
		ID:          "weknora-1",
		Name:        "Engineering KB",
		Description: "工程团队文档与成员档案",
		Scope:       "project",
		ProjectID:   "default",
		Transport:   "http",
		URL:         srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: store}
	refs := []domain.KnowledgeRef{{ServerID: "weknora-1", Mode: "all"}}

	got := knowledgeReferencePromptContext(context.Background(), d, "default", 1, refs)
	ns := knowledgeToolNamespace("weknora-1")
	for _, want := range []string{
		domain.KnowledgeLookupInstruction,
		"<bound_knowledge_bases>",
		`name="Engineering KB"`,
		ns + "__search_knowledge",
		"whoami (工程师技术档案)",
		"vpc-dns-architecture",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("knowledgeReferencePromptContext() missing %q in:\n%s", want, got)
		}
	}
	if listDocsCalls != 1 {
		t.Fatalf("list_documents calls = %d, want 1", listDocsCalls)
	}
	// Second call within TTL must hit cache without calling list_documents again.
	_ = knowledgeReferencePromptContext(context.Background(), d, "default", 1, refs)
	if listDocsCalls != 1 {
		t.Fatalf("second call should use cache; list_documents calls = %d, want 1", listDocsCalls)
	}
}

func TestKnowledgeReferencePromptContextWeKnoraFlow(t *testing.T) {
	resetKnowledgeCatalogCacheForTest()
	defer resetKnowledgeCatalogCacheForTest()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
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
					{"name": "list_knowledge_bases", "description": "List KBs", "annotations": map[string]any{"readOnlyHint": true}},
					{"name": "list_documents", "description": "List docs", "annotations": map[string]any{"readOnlyHint": true}},
					{"name": "search_knowledge", "description": "Search KB", "annotations": map[string]any{"readOnlyHint": true}},
				},
			}})
		case "tools/call":
			switch rpc.Params.Name {
			case "list_knowledge_bases":
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": `{"knowledge_bases":[{"id":"kb-net","name":"网络排障库","description":"VPC DNS 排障"}]}`}},
				}})
			case "list_documents":
				if rpc.Params.Arguments["knowledge_base_id"] != "kb-net" {
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "knowledge_base_id required"}}}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": `{"documents":[{"title":"vpcdns-test.md"}]}`}},
				}})
			}
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutMCPServer(domain.MCPServerDefinition{
		ID:        "weknora-2",
		Name:      "WeKnora",
		Scope:     "project",
		ProjectID: "default",
		Transport: "http",
		URL:       srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: store}
	got := knowledgeReferencePromptContext(context.Background(), d, "default", 1, []domain.KnowledgeRef{{ServerID: "weknora-2", Mode: "all"}})
	for _, want := range []string{"vpcdns-test.md", "<knowledge_base_ids>kb-net</knowledge_base_ids>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in L0 catalog, got:\n%s", want, got)
		}
	}
	sopGot := buildSOPKnowledgeCatalogXML(context.Background(), d, "default", 1, []domain.KnowledgeRef{{ServerID: "weknora-2", Mode: "all"}})
	for _, want := range []string{
		"mcp__" + domain.KnowledgeMCPServerID + "-weknora-2__search_knowledge",
		"vpcdns-test.md",
	} {
		if !strings.Contains(sopGot, want) {
			t.Fatalf("buildSOPKnowledgeCatalogXML() missing %q in:\n%s", want, sopGot)
		}
	}

	// Verify /runners/knowledge/mcp proxy initialize + tools/list for external claims.Ref.
	kh := &knowledgeHandlers{deps: d}
	claims := knowledge.ProxyClaims{ProjectID: "default", UserID: 1, Ref: &domain.KnowledgeRef{ServerID: "weknora-2", Mode: "all"}}
	initResp := kh.handleProxyRPC(context.Background(), domain.KnowledgeSettings{}, claims, agentToolRPCRequest{ID: 1, Method: "initialize"})
	initRaw, _ := json.Marshal(initResp.Result)
	if strings.Contains(string(initRaw), "kb_search") || !strings.Contains(string(initRaw), "WeKnora") {
		t.Fatalf("unexpected external initialize result: %s", initRaw)
	}
	listResp := kh.handleProxyRPC(context.Background(), domain.KnowledgeSettings{}, claims, agentToolRPCRequest{ID: 2, Method: "tools/list"})
	listRaw, _ := json.Marshal(listResp.Result)
	for _, want := range []string{"[用户已绑定知识库：WeKnora]", "优先调用", `"readOnlyHint":true`} {
		if !strings.Contains(string(listRaw), want) {
			t.Fatalf("external tools/list missing %q in: %s", want, listRaw)
		}
	}
}

func TestKnowledgeCallQuerySummary(t *testing.T) {
	if got := knowledgeCallQuerySummary(nil); got != "" {
		t.Fatalf("nil args = %q, want empty", got)
	}
	if got := knowledgeCallQuerySummary(map[string]any{
		"query":        "  vpc   dns  ",
		"knowledge_id": "kn-1",
	}); got != "vpc dns" {
		t.Fatalf("query priority = %q, want %q", got, "vpc dns")
	}
	if got := knowledgeCallQuerySummary(map[string]any{
		"knowledge_id": "kn-1",
		"document_id":  "doc-1",
	}); got != "kn-1" {
		t.Fatalf("knowledge_id fallback = %q, want %q", got, "kn-1")
	}
	if got := knowledgeCallQuerySummary(map[string]any{
		"document_id": "doc-1",
		"path":        "a/b.md",
	}); got != "doc-1" {
		t.Fatalf("document_id fallback = %q, want %q", got, "doc-1")
	}
	if got := knowledgeCallQuerySummary(map[string]any{
		"path":    "playbooks/dns.md",
		"keyword": "dns",
	}); got != "playbooks/dns.md" {
		t.Fatalf("path fallback = %q, want %q", got, "playbooks/dns.md")
	}
	if got := knowledgeCallQuerySummary(map[string]any{
		"keyword": "dns-timeout",
	}); got != "dns-timeout" {
		t.Fatalf("keyword fallback = %q, want %q", got, "dns-timeout")
	}
	longQuery := strings.Repeat("a", 150)
	if got := knowledgeCallQuerySummary(map[string]any{"query": longQuery}); len([]rune(got)) > 120 {
		t.Fatalf("long query not truncated to <=120 runes: len=%d", len([]rune(got)))
	}
}

func TestExtractKnowledgeResultMeta(t *testing.T) {
	// Built-in kb_search JSON.
	builtinSearch := `{"query":"vpcdns","results":[{"source":"net","path":"playbooks/vpcdns.md","title":"vpcdns-test.md"},{"source":"net","path":"playbooks/vpcdns.md","title":"vpcdns-test.md"},{"source":"net","path":"playbooks/coredns.md"}]}`
	targets, hits := extractKnowledgeResultMeta(builtinSearch)
	if hits != 3 {
		t.Fatalf("builtin search hits = %d, want 3", hits)
	}
	if !reflect.DeepEqual(targets, []string{"vpcdns-test.md", "playbooks/coredns.md"}) {
		t.Fatalf("builtin search targets = %#v", targets)
	}

	// Built-in kb_fetch JSON.
	builtinFetch := `{"source":"net","path":"playbooks/vpcdns.md","lines":[{"number":1,"text":"# title"}]}`
	targets, hits = extractKnowledgeResultMeta(builtinFetch)
	if hits != 1 || !reflect.DeepEqual(targets, []string{"playbooks/vpcdns.md"}) {
		t.Fatalf("builtin fetch targets=%#v hits=%d", targets, hits)
	}

	// WeKnora JSON with >5 items and duplicate titles.
	weknoraJSON := `{"results":[
		{"knowledge_title":"doc-1.md","knowledge_id":"k1"},
		{"knowledge_title":"doc-2.md","knowledge_id":"k2"},
		{"knowledge_title":"doc-1.md","knowledge_id":"k1"},
		{"title":"doc-3.md"},
		{"title":"doc-4.md"},
		{"title":"doc-5.md"},
		{"title":"doc-6.md"}
	]}`
	targets, hits = extractKnowledgeResultMeta(weknoraJSON)
	if hits != 7 {
		t.Fatalf("weknora hits = %d, want 7", hits)
	}
	wantTop5 := []string{"doc-1.md", "doc-2.md", "doc-3.md", "doc-4.md", "doc-5.md"}
	if !reflect.DeepEqual(targets, wantTop5) {
		t.Fatalf("weknora targets = %#v, want %#v", targets, wantTop5)
	}

	// WeKnora single document data wrapper.
	weknoraSingle := `{"success":true,"data":{"id":"kn-42","title":"vpcdns-test.md","content":"hello"}}`
	targets, hits = extractKnowledgeResultMeta(weknoraSingle)
	if hits != 1 || !reflect.DeepEqual(targets, []string{"vpcdns-test.md"}) {
		t.Fatalf("weknora single targets=%#v hits=%d", targets, hits)
	}

	// Markdown text output.
	markdown := "### 1. vpcdns-test.md\n相关段落内容...\n- **标题**: coredns-guide.md\n### 2. vpcdns-test.md\n"
	targets, hits = extractKnowledgeResultMeta(markdown)
	if hits != 3 || !reflect.DeepEqual(targets, []string{"vpcdns-test.md", "coredns-guide.md"}) {
		t.Fatalf("markdown targets=%#v hits=%d", targets, hits)
	}
}

func TestCallKnowledgeReferenceExtractsHitsAndTargets(t *testing.T) {
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
					{"name": "search_knowledge", "description": "Search KB", "annotations": map[string]any{"readOnlyHint": true}},
				},
			}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": `{"results":[{"knowledge_title":"vpcdns-test.md"},{"knowledge_title":"dns-faq.md"}]}`}},
			}})
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutMCPServer(domain.MCPServerDefinition{
		ID:        "weknora-obs",
		Name:      "WeKnora Obs",
		Scope:     "project",
		ProjectID: "default",
		Transport: "http",
		URL:       srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: store}
	ref := domain.KnowledgeRef{ServerID: "weknora-obs", Mode: "all"}
	args := map[string]any{"query": "vpcdns"}
	text, hits, err := callKnowledgeReference(context.Background(), d, "default", 1, ref, "search_knowledge", args)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2", hits)
	}
	read, _ := knowledgeReadResult(ref.ServerID, "search_knowledge", text, hits, err)
	read.Query = knowledgeCallQuerySummary(args)
	if read.Query != "vpcdns" {
		t.Fatalf("read.Query = %q, want vpcdns", read.Query)
	}
	if !reflect.DeepEqual(read.Targets, []string{"vpcdns-test.md", "dns-faq.md"}) {
		t.Fatalf("read.Targets = %#v", read.Targets)
	}
}

func TestKnowledgeReadResultEvidencesAndFiveStates(t *testing.T) {
	// 1. External MCP empty JSON array -> 无匹配 with empty evidences.
	emptyRead, _ := knowledgeReadResult("ext-1", "search_knowledge", `{"results":[]}`, 0, nil)
	if emptyRead.Status != domain.KnowledgeStatusEmpty || len(emptyRead.Evidences) != 0 {
		t.Fatalf("empty external JSON = %+v, want 无匹配 & 0 evidences", emptyRead)
	}

	// 2. External MCP plain text -> 未知结构 and raw text preserved.
	unstructuredRead, unstructuredText := knowledgeReadResult("ext-1", "search_knowledge", "raw", 0, nil)
	if unstructuredRead.Status != domain.KnowledgeStatusUnstructured {
		t.Fatalf("unstructured status = %q, want 未知结构", unstructuredRead.Status)
	}
	if !strings.Contains(unstructuredText, "raw") || !strings.Contains(unstructuredText, "[[knowledge:"+unstructuredRead.ID+"]]") {
		t.Fatalf("unstructured text missing raw or marker: %q", unstructuredText)
	}

	// 3. 10 documents returned, only [[knowledge:ev_2]] cited -> only Evidences[1].Cited == true.
	var items []map[string]any
	for i := 1; i <= 10; i++ {
		items = append(items, map[string]any{
			"document_id": "doc-" + string(rune('0'+i%10)),
			"title":       "Doc Title " + string(rune('A'+i-1)),
			"source":      "conn-1::kb-1",
		})
	}
	rawJSON, _ := json.Marshal(map[string]any{"results": items})
	hitRead, hitText := knowledgeReadResult(domain.KnowledgeMCPServerID, "search_knowledge", string(rawJSON), 10, nil)
	if hitRead.Status != domain.KnowledgeStatusHit {
		t.Fatalf("hit status = %q, want 命中", hitRead.Status)
	}
	if len(hitRead.Evidences) != 10 {
		t.Fatalf("len(Evidences) = %d, want 10", len(hitRead.Evidences))
	}
	evID2 := hitRead.Evidences[1].ID
	if !strings.HasPrefix(evID2, "ev_") || !strings.HasSuffix(evID2, "_2") {
		t.Fatalf("unexpected evidence[1].ID = %q", evID2)
	}
	if !strings.Contains(hitText, "[[knowledge:"+evID2+"]]") {
		t.Fatalf("expected [[knowledge:%s]] marker in output text:\n%s", evID2, hitText)
	}
	domain.MarkKnowledgeReadCited(&hitRead, "结论参考 [[knowledge:"+evID2+"]]")
	if !hitRead.Cited {
		t.Fatal("expected read.Cited = true")
	}
	for i, ev := range hitRead.Evidences {
		want := i == 1
		if ev.Cited != want {
			t.Fatalf("evidence[%d] (%s) Cited = %v, want %v", i, ev.ID, ev.Cited, want)
		}
	}

	// 4. Partial status propagation.
	partialRead, _ := knowledgeReadResultWithMeta(domain.KnowledgeMCPServerID, "search_knowledge", string(rawJSON), 10, domain.KnowledgeStatusPartial, "连接 B 超时", domain.KnowledgeCallOriginModelTool, nil)
	if partialRead.Status != domain.KnowledgeStatusPartial || partialRead.Warning != "连接 B 超时" {
		t.Fatalf("partial read = %+v", partialRead)
	}
}

func TestKnowledgeCatalogCacheInvalidationAndSingleflight(t *testing.T) {
	resetKnowledgeCatalogCacheForTest()
	defer resetKnowledgeCatalogCacheForTest()

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
				},
			}})
		case "tools/call":
			listDocsCalls.Add(1)
			<-gate
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": `{"documents":[{"title":"sf-doc.md"}]}`}},
			}})
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutMCPServer(domain.MCPServerDefinition{
		ID:        "sf-kb",
		Name:      "Singleflight KB",
		Scope:     "project",
		ProjectID: "default",
		Transport: "http",
		URL:       srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: store}
	refs := []domain.KnowledgeRef{{ServerID: "sf-kb", Mode: "all"}}

	// Concurrent cold-start calls must coalesce via singleflight into 1 upstream probe.
	var wg sync.WaitGroup
	results := make([]string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = knowledgeReferencePromptContext(context.Background(), d, "default", 1, refs)
		}(i)
	}
	close(gate)
	wg.Wait()

	if got := listDocsCalls.Load(); got != 1 {
		t.Fatalf("concurrent cold-start list_documents calls = %d, want 1", got)
	}
	for i, res := range results {
		if !strings.Contains(res, "sf-doc.md") {
			t.Fatalf("result[%d] missing sf-doc.md: %q", i, res)
		}
	}

	// Built-in knowledge cache key must invalidate immediately when project scope or connection state changes.
	keyBefore := knowledgeCatalogCacheKey(d, "chat", "default", 1, []domain.KnowledgeRef{{ServerID: domain.KnowledgeMCPServerID, Mode: "all"}})
	if err := store.PutKnowledgeSettings(domain.KnowledgeSettings{
		Enabled:        true,
		DefaultSources: []string{"c1::kb1"},
		ProjectSources: map[string][]string{"default": {"c1::kb1"}},
		Connections: []domain.KnowledgeConnection{{
			ID: "c1", Name: "C1", Provider: domain.KnowledgeProviderWeKnora, URL: "http://kb.invalid", Token: "t1", Enabled: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	keyScoped := knowledgeCatalogCacheKey(d, "chat", "default", 1, []domain.KnowledgeRef{{ServerID: domain.KnowledgeMCPServerID, Mode: "all"}})
	if keyScoped == keyBefore {
		t.Fatal("expected cache key to change after enabling connection and setting project scope")
	}
	if err := store.PutKnowledgeSettings(domain.KnowledgeSettings{
		Enabled:        true,
		DefaultSources: []string{"c1::kb1"},
		ProjectSources: map[string][]string{"default": {}},
		Connections: []domain.KnowledgeConnection{{
			ID: "c1", Name: "C1", Provider: domain.KnowledgeProviderWeKnora, URL: "http://kb.invalid", Token: "t1", Enabled: false,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	keyRevoked := knowledgeCatalogCacheKey(d, "chat", "default", 1, []domain.KnowledgeRef{{ServerID: domain.KnowledgeMCPServerID, Mode: "all"}})
	if keyRevoked == keyScoped {
		t.Fatal("expected cache key to change after revoking project scope / disabling connection")
	}
}

func TestKnowledgeReadResultUniqueEvidenceIDsAndMetadata(t *testing.T) {
	// 1. Two sequential calls in one turn must produce distinct evidence IDs so citing
	//    call 2 does not mark call 1 as cited.
	rawSearch1, _ := json.Marshal(map[string]any{
		"results": []map[string]any{{
			"source":      "conn1::kb1",
			"source_name": "架构知识库",
			"path":        "conn1::doc-a",
			"title":       "架构规范.md",
			"chunk_index": 3,
			"start_line":  0,
			"end_line":    0,
		}},
	})
	read1, _ := knowledgeReadResult(domain.KnowledgeMCPServerID, "search_knowledge", string(rawSearch1), 1, nil)
	if len(read1.Evidences) != 1 {
		t.Fatalf("len(read1.Evidences) = %d, want 1", len(read1.Evidences))
	}
	if read1.Evidences[0].SourceName != "架构知识库" || read1.Evidences[0].Locator != "#chunk-3" {
		t.Fatalf("unexpected search evidence metadata: %+v", read1.Evidences[0])
	}

	rawDoc2, _ := json.Marshal(map[string]any{
		"knowledge_id":        "conn1::doc-b",
		"title":               "部署手册.md",
		"knowledge_base_id":   "conn1::kb2",
		"knowledge_base_name": "运维手册库",
		"chunks": []map[string]any{
			{"id": "chunk-uuid-1", "chunk_index": 2, "content": "step 1"},
			{"id": "chunk-uuid-2", "chunk_index": 3, "content": "step 2"},
			{"id": "chunk-uuid-3", "chunk_index": 4, "content": "step 3"},
		},
	})
	read2, text2 := knowledgeReadResult(domain.KnowledgeMCPServerID, "read_document", string(rawDoc2), 3, nil)
	if len(read2.Evidences) != 1 {
		t.Fatalf("read_document chunks must aggregate into 1 doc evidence, got %+v", read2.Evidences)
	}
	ev2 := read2.Evidences[0]
	if ev2.DocumentID != "conn1::doc-b" || ev2.Title != "部署手册.md" || ev2.SourceID != "conn1::kb2" || ev2.SourceName != "运维手册库" || ev2.Locator != "#chunk-2..#chunk-4" {
		t.Fatalf("unexpected read_document evidence: %+v", ev2)
	}
	if ev2.ID == read1.Evidences[0].ID {
		t.Fatalf("evidence IDs collided across calls: %q", ev2.ID)
	}
	if !strings.Contains(text2, "知识库「运维手册库」 / 部署手册.md (#chunk-2..#chunk-4)") {
		t.Fatalf("expected readable source + title + chunk range in tool text:\n%s", text2)
	}

	answer := "按部署手册执行 [[knowledge:" + ev2.ID + "]]"
	domain.MarkKnowledgeReadCited(&read1, answer)
	domain.MarkKnowledgeReadCited(&read2, answer)
	if read1.Cited || read1.Evidences[0].Cited {
		t.Fatalf("read1 must not be marked cited when only read2 was cited: %+v", read1)
	}
	if !read2.Cited || !read2.Evidences[0].Cited {
		t.Fatalf("read2 must be marked cited: %+v", read2)
	}

	// 2. Catalog tools must not produce citable evidences or [[knowledge:...]] prompts.
	for _, tool := range []string{"list_knowledge_bases", "list_documents", knowledge.ToolListSources} {
		rawCat, _ := json.Marshal(map[string]any{"documents": []map[string]any{{"id": "d1", "title": "不应作为证据"}}})
		catRead, catText := knowledgeReadResult(domain.KnowledgeMCPServerID, tool, string(rawCat), 1, nil)
		if len(catRead.Evidences) != 0 {
			t.Fatalf("catalog tool %s must not attach Evidences, got %+v", tool, catRead.Evidences)
		}
		if strings.Contains(catText, "[[knowledge:") {
			t.Fatalf("catalog tool %s must not append citation prompt: %q", tool, catText)
		}
	}
}

func TestKnowledgeReadResultExternalUntruncatedEvidenceAndUnavailable(t *testing.T) {
	padding := strings.Repeat("a", 33000)
	longJSON, _ := json.Marshal(map[string]any{
		"results": []map[string]any{{
			"document_id": "ext-doc-long",
			"title":       "超长外部文档.md",
			"snippet":     padding,
		}},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
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
				"tools": []map[string]any{{"name": "search_ext", "annotations": map[string]any{"readOnlyHint": true}}},
			}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": string(longJSON)}},
			}})
		}
	}))
	defer srv.Close()

	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PutMCPServer(domain.MCPServerDefinition{
		ID: "ext-long", Name: "Long KB", Scope: "project", ProjectID: "default", Transport: "http", URL: srv.URL,
	}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: store}
	ref := domain.KnowledgeRef{ServerID: "ext-long", Mode: "all"}
	res, err := callKnowledgeReferenceWithOrigin(context.Background(), d, "default", 1, ref, "search_ext", map[string]any{"query": "test"}, domain.KnowledgeCallOriginModelTool)
	if err != nil {
		t.Fatalf("callKnowledgeReferenceWithOrigin: %v", err)
	}
	if utf8.RuneCountInString(res.Text) <= 32000 {
		t.Fatalf("expected raw model-tool result to stay untruncated for JSON parsing, len=%d", utf8.RuneCountInString(res.Text))
	}
	read, promptText := knowledgeReadResultWithMeta("ext-long", "search_ext", res.Text, res.Hits, res.Status, res.Warning, domain.KnowledgeCallOriginModelTool, nil)
	if read.Status != domain.KnowledgeStatusHit || len(read.Evidences) != 1 || read.Evidences[0].Title != "超长外部文档.md" {
		t.Fatalf("expected full-JSON evidence extraction before truncation, got %+v", read)
	}
	if utf8.RuneCountInString(promptText) > 32500 {
		t.Fatalf("expected truncated promptText for external server, got runes=%d", utf8.RuneCountInString(promptText))
	}
	if !strings.Contains(promptText, "勿放入反引号") {
		t.Fatalf("expected promptText to forbid backticks: %q", promptText)
	}

	unavailRead, _ := knowledgeReadResultWithMeta("ext-long", "search_ext", `{"results":[{"document_id":"d1","title":"X"}]}`, 1, domain.KnowledgeStatusUnavailable, "down", domain.KnowledgeCallOriginModelTool, nil)
	if unavailRead.Status != domain.KnowledgeStatusUnavailable || len(unavailRead.Evidences) != 0 {
		t.Fatalf("expected unavailable status to clear evidences, got %+v", unavailRead)
	}
}

func TestExtractKnowledgeEvidencesMultiDocVsSingleDocChunks(t *testing.T) {
	multiDocChunks, _ := json.Marshal(map[string]any{
		"id":   "resp-wrapper",
		"name": "wrapper",
		"chunks": []map[string]any{
			{"document_id": "doc-1", "title": "文档一.md", "chunk_index": 0},
			{"document_id": "doc-2", "title": "文档二.md", "chunk_index": 1},
		},
	})
	_, evs, _, _ := extractKnowledgeEvidences(string(multiDocChunks), "")
	if len(evs) != 2 || evs[0].ID != "ev_1" || evs[1].ID != "ev_2" || evs[0].Title != "文档一.md" || evs[1].Title != "文档二.md" {
		t.Fatalf("expected multi-doc chunks split into 2 evidences with fallback ev_ prefix, got %+v", evs)
	}

	// Even if parent has knowledge_id, chunks that carry explicit doc identity must not collapse.
	mixedChunks, _ := json.Marshal(map[string]any{
		"knowledge_id": "parent-k",
		"chunks": []map[string]any{
			{"document_id": "doc-a", "title": "A", "chunk_index": 0},
			{"document_id": "doc-b", "title": "B", "chunk_index": 1},
		},
	})
	_, mixedEvs, _, _ := extractKnowledgeEvidences(string(mixedChunks), "ev_mix")
	if len(mixedEvs) != 2 {
		t.Fatalf("expected chunks carrying doc identity to emit 2 evidences, got %+v", mixedEvs)
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

func TestChatSessionKnowledgeForkPromptAndUnfinishedEdges(t *testing.T) {
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
	wantRefs := []domain.KnowledgeRef{{ServerID: "wk-1", Mode: "allowlist", Sources: []string{"wk-1::kb1"}}}
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

	// proxyMCP must enforce running-node & attempt guard when RunID+NodeID are claimed even with Ref == nil.
	settings := domain.KnowledgeSettings{Enabled: true, URL: "https://kb.invalid/mcp", Token: "proxy-secret"}
	_ = h.repo.PutKnowledgeSettings(settings)
	_ = h.repo.PutRun(domain.Run{
		ID:               "run-proxy-guard",
		Status:           "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "default", ResponsibleUserID: 7},
		Nodes:            map[string]*domain.NodeRun{"n1": {NodeID: "n1", Status: "succeeded", Attempt: 1}},
	})
	tok, err := knowledge.SignProxyToken(settings, knowledge.ProxyClaims{
		RunID: "run-proxy-guard", NodeID: "n1", Attempt: 1, ProjectID: "default", UserID: 7,
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
		t.Fatalf("expected non-running node role proxy token rejected with 403, got %d (%s)", rec.Code, rec.Body.String())
	}
}
