package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"
)

func TestKnowledgeProxyToolName(t *testing.T) {
	cases := map[string]string{
		"kb_search":                             "kb_search",
		"  kb_fetch ":                           "kb_fetch",
		"mcp__agentflow-knowledge__kb_search":   "kb_search",
		" agentflow-knowledge__kb_list_sources": "kb_list_sources",
		"":                                      "",
	}
	for in, want := range cases {
		if got := knowledgeProxyToolName(in); got != want {
			t.Errorf("knowledgeProxyToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func newProxyToolTestHandlers(t *testing.T) (*knowledgeHandlers, *fakeKB, domain.KnowledgeSettings) {
	t.Helper()
	kb := &fakeKB{}
	upstream := httptest.NewServer(kb)
	t.Cleanup(upstream.Close)
	settings := domain.KnowledgeSettings{
		Enabled: true, URL: upstream.URL + "/mcp", Token: "upstream-secret",
		ProjectSources: map[string][]string{"p1": {"a"}},
	}
	return &knowledgeHandlers{activity: &knowledgeActivityLog{}}, kb, settings
}

func proxyToolResultText(t *testing.T, result any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode result: %v (%s)", err, raw)
	}
	if len(decoded.Content) == 0 {
		return "", decoded.IsError
	}
	return decoded.Content[0].Text, decoded.IsError
}

func TestCallProxyToolRejectsMalformedParams(t *testing.T) {
	h, _, settings := newProxyToolTestHandlers(t)
	result, rpcErr := h.callProxyTool(context.Background(), settings, knowledge.ProxyClaims{ProjectID: "p1"}, json.RawMessage(`{"name":1}`))
	if result != nil || rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("malformed params: result=%v err=%+v", result, rpcErr)
	}
	if len(h.activity.recent()) != 0 {
		t.Fatal("malformed call must not be recorded")
	}
}

func TestCallProxyToolRejectsWriteToolBeforeUpstream(t *testing.T) {
	h, kb, settings := newProxyToolTestHandlers(t)
	result, rpcErr := h.callProxyTool(context.Background(), settings, knowledge.ProxyClaims{ProjectID: "p1"},
		json.RawMessage(`{"name":"mcp__agentflow-knowledge__kb_write_document","arguments":{"source":"a"}}`))
	if result != nil || rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("write tool: result=%v err=%+v", result, rpcErr)
	}
	if !strings.Contains(rpcErr.Message, "mcp__agentflow-knowledge__kb_write_document") || !strings.Contains(rpcErr.Message, "只读") {
		t.Fatalf("message should name the requested tool and read-only rule: %q", rpcErr.Message)
	}
	if kb.lastCall("kb_write_document") != nil || len(h.activity.recent()) != 0 {
		t.Fatal("rejected write must not reach upstream or be recorded")
	}
}

func TestCallProxyToolRecordsScopedCalls(t *testing.T) {
	h, kb, settings := newProxyToolTestHandlers(t)
	claims := knowledge.ProxyClaims{ProjectID: "p1", AgentID: "dev", UserID: 7}

	result, rpcErr := h.callProxyTool(context.Background(), settings, claims,
		json.RawMessage(`{"name":"mcp__agentflow-knowledge__kb_search","arguments":{"query":"帕鲁"}}`))
	if rpcErr != nil {
		t.Fatalf("search: %+v", rpcErr)
	}
	if text, isErr := proxyToolResultText(t, result); isErr || !strings.Contains(text, "a@c1:x.md") {
		t.Fatalf("search result = %q isError=%t", text, isErr)
	}
	if got := kb.lastCall("kb_search"); got == nil || strings.Join(stringList(got["sources"]), ",") != "a" {
		t.Fatalf("search not narrowed to project sources: %#v", got)
	}

	result, rpcErr = h.callProxyTool(context.Background(), settings, claims,
		json.RawMessage(`{"name":"kb_fetch","arguments":{"source":"b","path":"x.md"}}`))
	if rpcErr != nil {
		t.Fatalf("out-of-scope fetch should be a tool error, not an rpc error: %+v", rpcErr)
	}
	if text, isErr := proxyToolResultText(t, result); !isErr || !strings.Contains(text, "授权范围") {
		t.Fatalf("out-of-scope fetch result = %q isError=%t", text, isErr)
	}

	activity := h.activity.recent()
	if len(activity) != 2 {
		t.Fatalf("activity = %#v", activity)
	}
	failed, ok := activity[0], activity[1]
	if failed.Tool != "kb_fetch" || failed.OK || !strings.Contains(failed.Error, "授权范围") {
		t.Fatalf("failed activity = %#v", failed)
	}
	if ok.Tool != "kb_search" || !ok.OK || ok.Query != "帕鲁" || ok.Hits != 1 ||
		ok.ProjectID != "p1" || ok.AgentID != "dev" || ok.UserID != 7 || ok.Error != "" {
		t.Fatalf("search activity = %#v", ok)
	}
}
