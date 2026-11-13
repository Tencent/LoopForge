package httpapi

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceCatalogAndCallRejectPlatformCapabilities(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, ProjectID: conv.ProjectID, RuntimeMode: domain.AssistantModeWorkspace, ExpiresAt: time.Now().Add(time.Minute), ctx: context.Background()}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := &agentToolGateway{deps: h.deps}
	list := g.handleRPC(withAgentToolToken(context.Background(), s.Token), s, agentToolRPCRequest{ID: 1, Method: "tools/list"})
	data, _ := json.Marshal(list)
	for _, name := range []string{"agents_save", "workflows_save", "submit_assistant_reply", "workbench_workspace_write", "workbench_workspace_command", "workbench_workspace_open"} {
		if strings.Contains(string(data), `"name":"`+name+`"`) {
			t.Fatalf("workspace leaked %s", name)
		}
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
		out := g.handleRPC(withAgentToolToken(context.Background(), s.Token), s, agentToolRPCRequest{ID: 2, Method: "tools/call", Params: params})
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"isError":true`) && out.Error == nil {
			t.Fatalf("legacy tool callable: %s", raw)
		}
	}
	for _, name := range []string{"workbench_platform_mode", "workbench_workspace_submit_mr"} {
		if !strings.Contains(string(data), `"name":"`+name+`"`) {
			t.Fatalf("missing integration: %s", name)
		}
	}
	old := s.Token
	if err := s.rotateMode(domain.AssistantModePlatform); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"tools/list", "tools/call", "agentflow/nativeApproval"} {
		params := json.RawMessage(`{"name":"agents_save","arguments":{}}`)
		out := g.handleRPC(withAgentToolToken(context.Background(), old), s, agentToolRPCRequest{ID: 3, Method: method, Params: params})
		raw, _ := json.Marshal(out)
		if out.Error == nil && !strings.Contains(string(raw), `"isError":true`) {
			t.Fatalf("old token accepted after rotate: %s %s", method, raw)
		}
	}
}

func TestEncodedCLIArgumentsPreserveGatewayRestrictions(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, ProjectID: conv.ProjectID, RuntimeMode: domain.AssistantModePlatform, ExpiresAt: time.Now().Add(time.Minute), ctx: context.Background()}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := &agentToolGateway{deps: h.deps}
	ctx := withAgentToolToken(context.Background(), s.Token)
	params := json.RawMessage(`{"name":"workbench_tool_schema","arguments":"{\"name\":\"issues_list\"}"}`)
	if result, err := g.callTool(ctx, s, params); err != nil || result == nil {
		t.Fatalf("encoded CLI argument object was not accepted: %v", err)
	}
	for _, invalid := range []string{`"[]"`, `"null"`, `"\"nested\""`, `"{malformed}"`} {
		raw := json.RawMessage(`{"name":"workbench_tool_schema","arguments":` + invalid + `}`)
		if _, err := g.callTool(ctx, s, raw); err == nil {
			t.Fatalf("invalid encoded argument accepted: %s", invalid)
		}
	}
	if err := s.rotateMode(domain.AssistantModeWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := g.callTool(ctx, s, params); err == nil {
		t.Fatal("old token accepted an encoded call after handoff")
	}
	ctx = withAgentToolToken(context.Background(), s.Token)
	if _, err := g.callTool(ctx, s, params); err == nil {
		t.Fatal("encoded argument bypassed workspace catalog restriction")
	}
}

func TestNativeApprovalAllowsPlatformReadsAndRejectsEffects(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	g := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModePlatform, ctx: context.Background()}
	if yes, _ := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"Read","input":{"path":"SKILL.md"}}`)); !yes {
		t.Fatal("platform skill read denied")
	}
	if yes, _ := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"Bash","input":{"command":"echo unsafe"}}`)); yes {
		t.Fatal("platform native shell permitted")
	}
}

func TestNativeApprovalRequiresExactInitiatingActorAndNewAction(t *testing.T) {
	h, a, b, _, conv := sharedWorkbenchFixture(t)
	g := &agentToolGateway{deps: h.deps}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &AgentToolSession{UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModeWorkspace, ctx: ctx}
	result := make(chan bool, 1)
	go func() {
		yes, _ := g.approveNativeTool(ctx, s, json.RawMessage(`{"name":"Write","input":{"path":"approved.txt","content":"exact"}}`))
		result <- yes
	}()
	var item domain.AssistantNativeApproval
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		items := h.repo.PendingNativeApprovals(a.ID, conv.ID)
		if len(items) > 0 {
			item = items[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if item.ID == "" {
		t.Fatal("native approval not persisted")
	}
	if err := h.repo.DecideNativeApproval(a.ID, b.ID, conv.ID, item.ID, "allow"); err == nil {
		t.Fatal("different actor approved")
	}
	if err := h.repo.DecideNativeApproval(a.ID, a.ID, conv.ID, item.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	select {
	case yes := <-result:
		if !yes {
			t.Fatal("approved exact action denied")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approval not delivered")
	}
	go func() {
		yes, _ := g.approveNativeTool(ctx, s, json.RawMessage(`{"name":"Write","input":{"path":"different.txt","content":"new"}}`))
		result <- yes
	}()
	cancel()
	select {
	case yes := <-result:
		if yes {
			t.Fatal("old receipt approved another action")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled approval blocked")
	}
}

func TestRuntimeModeHandoffPublishesToLiveGeneration(t *testing.T) {
	recorder := newAssistantGenerationRecorder(domain.AssistantGeneration{ID: "mode-stream", Status: "running"})
	defer recorder.close()
	recorder.sink("runtime_mode", map[string]string{"mode": domain.AssistantModeWorkspace})
	if got := recorder.snapshot(); got.RuntimeMode != domain.AssistantModeWorkspace {
		t.Fatal("handoff mode not published")
	}
	recorder.sink("runtime_mode", map[string]string{"mode": "invalid"})
	if got := recorder.snapshot(); got.RuntimeMode != domain.AssistantModeWorkspace {
		t.Fatal("invalid mode accepted")
	}
}

// Exercise the names recorded by real CLI hooks, including discovery before
// tools/call. A legitimate wrapper must reach the gateway without granting
// arbitrary native commands or another MCP server the same exception.
func TestNativeApprovalRecognizesCLIGatewayAliases(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	g := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, ProjectID: conv.ProjectID, RuntimeMode: domain.AssistantModePlatform, ExpiresAt: time.Now().Add(time.Minute), ctx: context.Background()}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, raw := range []string{
		`{"name":"mcp__agentflow_session__workbench_workspace_open","input":{}}`,
		`{"name":"agentflow_session_workbench_workspace_open","input":{}}`,
		`{"name":"call_mcp_tool","input":{"ServerName":"agentflow_session","ToolName":"workbench_workspace_open","Arguments":"{}"}}`,
		`{"name":"GetMcpTools","input":{"server":"agentflow_session"}}`,
		`{"name":"GetDynamicTools","input":{"namespace":"agentflow_session","toolName":"workbench_workspace_open"}}`,
		`{"name":"CallDynamicTool","input":{"namespace":"agentflow_session","toolName":"workbench_workspace_open","arguments":{}}}`,
		`{"name":"CallMcpTool","input":{"server":"agentflow_session","toolName":"workbench_workspace_open","arguments":{}}}`,
		`{"name":"MCP:workbench_workspace_open","input":{}}`,
	} {
		if allowed, reason := g.approveNativeTool(context.Background(), s, json.RawMessage(raw)); !allowed {
			t.Fatalf("gateway alias blocked: %s: %s", raw, reason)
		}
	}
	for _, raw := range []string{
		`{"name":"call_mcp_tool","input":{"ServerName":"foreign","ToolName":"Bash"}}`,
		`{"name":"call_mcp_tool","input":{"ServerName":"agentflow_session"}}`,
		`{"name":"CallDynamicTool","input":{"namespace":"foreign","toolName":"Bash"}}`,
		`{"name":"GetDynamicTools","input":{"namespace":"foreign"}}`,
		`{"name":"CallDynamicTool","input":{"namespace":"foreign","server":"agentflow_session","toolName":"Bash"}}`,
		`{"name":"MCP:Bash","input":{}}`,
		`{"name":"Bash","input":{"command":"touch effect.txt"}}`,
		`{"name":"run_command","input":{"command":"touch effect.txt"}}`,
		`{"name":"Write","input":{"path":"effect.txt","content":"unsafe"}}`,
	} {
		if allowed, _ := g.approveNativeTool(context.Background(), s, json.RawMessage(raw)); allowed {
			t.Fatalf("non-gateway action allowed: %s", raw)
		}
	}
	if err := s.rotateMode(domain.AssistantModeWorkspace); err != nil {
		t.Fatal(err)
	}
	// A persisted MCP alias can pass the native hook, but an old platform tool
	// must still be rejected by the current gateway at the actual call entry.
	if allowed, _ := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"agentflow_session_agents_save","input":{}}`)); !allowed {
		t.Fatal("gateway transport blocked")
	}
	if _, err := g.callTool(withAgentToolToken(context.Background(), s.Token), s, json.RawMessage(`{"name":"agents_save","arguments":{}}`)); err == nil {
		t.Fatal("native gateway exception bypassed workspace restrictions")
	}
}
