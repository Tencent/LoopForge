package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

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
		{"success with text", domain.KnowledgeMCPServerID, knowledge.ToolSearch, "some answer", 1, nil, "已返回", true},
		{"blank text", domain.KnowledgeMCPServerID, knowledge.ToolFetch, "   ", 0, nil, "无匹配", false},
		{"search zero hits", domain.KnowledgeMCPServerID, knowledge.ToolSearch, `{"results":[]}`, 0, nil, "无匹配", false},
		{"external zero hits not forced no-match", "handbook", knowledge.ToolSearch, "raw", 0, nil, "已返回", true},
		{"call failure", domain.KnowledgeMCPServerID, knowledge.ToolSearch, "", 0, errors.New("boom"), "调用失败", false},
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

func TestFilterSourcesText(t *testing.T) {
	payload := `{"sources":[{"id":"dns","name":"DNS"},{"id":"api","name":"API"},{"name":"no-id"}]}`
	out, n, err := filterSourcesText(payload, []string{"dns"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("kept count = %d, want 1", n)
	}
	var parsed struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if len(parsed.Sources) != 1 || parsed.Sources[0].ID != "dns" {
		t.Fatalf("filtered sources = %+v, want only dns", parsed.Sources)
	}
	if _, _, err := filterSourcesText("not json", []string{"dns"}); err == nil {
		t.Fatal("invalid JSON must fail")
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
