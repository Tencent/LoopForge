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

func TestKnowledgeClientMethods(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		result := any(map[string]any{})
		switch req.Method {
		case "initialize":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": ToolSearch}}}
		case "tools/call":
			text := `{}`
			switch req.Params.Name {
			case ToolListSources:
				text = `{"sources":[{"id":"s","name":"Source"}]}`
			case ToolSearch:
				text = `{"query":"q","results":[{"source":"s","path":"a.md"}]}`
			case ToolFetch:
				text = `{"source":"s","path":"a.md","lines":[{"number":1,"text":"x"}]}`
			case ToolWrite:
				text = `{"source":"s","path":"a.md","commit":"abc"}`
			case "invalid":
				text = `not-json`
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	client, err := NewClient(domain.KnowledgeSettings{URL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if sources, err := client.ListSources(ctx); err != nil || len(sources) != 1 {
		t.Fatalf("sources = %#v, %v", sources, err)
	}
	if result, err := client.Search(ctx, "q", []string{"s"}); err != nil || len(result.Results) != 1 {
		t.Fatalf("search = %#v, %v", result, err)
	}
	if doc, err := client.Fetch(ctx, "s", "a.md", 1, 2); err != nil || len(doc.Lines) != 1 {
		t.Fatalf("fetch = %#v, %v", doc, err)
	}
	if result, err := client.WriteDocument(ctx, "s", "a.md", "x"); err != nil || result.Commit != "abc" {
		t.Fatalf("write = %#v, %v", result, err)
	}
	if tools, err := client.Tools(ctx); err != nil || len(tools) != 1 {
		t.Fatalf("tools = %#v, %v", tools, err)
	}
	var out map[string]any
	if err := client.callJSON(ctx, "invalid", nil, &out); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestKnowledgeClientHelpers(t *testing.T) {
	if _, err := NewClient(domain.KnowledgeSettings{}); err != ErrNotConfigured {
		t.Fatalf("not configured = %v", err)
	}
	for _, name := range ReadOnlyTools {
		if !IsReadOnlyTool(name) {
			t.Fatal(name)
		}
	}
	if IsReadOnlyTool("write") {
		t.Fatal("write is read-only")
	}
	if got, err := ScopeSources([]string{"a", " b "}, nil); err != nil || len(got) != 2 {
		t.Fatalf("scope = %#v, %v", got, err)
	}
	if _, err := ScopeSources([]string{"a"}, []string{"b"}); err == nil {
		t.Fatal("unauthorized source accepted")
	}
	if !SourceAllowed(nil, "x") || SourceAllowed([]string{"a"}, "x") {
		t.Fatal("source allowed")
	}
	if got := cleanSources([]string{" a ", "a", ""}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("clean = %#v", got)
	}
	if got := clip(strings.Repeat("界", 205), 200); !strings.HasSuffix(got, "…") {
		t.Fatal("clip")
	}
	if stripBearer("Bearer token") != "token" || len(appendUnique([]string{"a"}, "a")) != 1 || firstNonEmpty("", " x ") != " x " {
		t.Fatal("service helpers")
	}
}
