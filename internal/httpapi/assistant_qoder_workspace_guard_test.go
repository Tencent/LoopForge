package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// qoderGatewayProbeFixture is a simulated qodercli: it reads the agentflow
// session token from the MCP config it was given, reports it to the test
// gateway, and then answers like a successful stream-json turn.
const qoderGatewayProbeFixture = `#!%s
import json, sys, urllib.request
args = sys.argv[1:]
config = json.load(open(args[args.index('--mcp-config') + 1]))
url = config['mcpServers']['agentflow_session']['url']
token = url.split('/internal/agent-tools/')[1].split('/')[0]
urllib.request.urlopen('%s/internal/agent-tools/' + token + '/probe', timeout=5).read()
for event in [
    {'type':'system','subtype':'init'},
    {'type':'assistant','message':{'role':'assistant','content':[{'type':'text','text':'probe done'}]}},
    {'type':'result','subtype':'success','is_error':False,'result':'probe done'},
]:
    event['session_id'] = 'qoder-probe-session'
    print(json.dumps(event), flush=True)
`

// TestQoderChatSessionCarriesExecutorForWorkspaceOpenGuard drives a platform
// (non-workspace) Qoder chat through h.chat and checks that the tool session
// the CLI talks to is tagged with the Qoder executor, so a workspace_open
// call made during that turn is rejected by openConversationWorkspace. The
// guard there only works if chat copies req.Executor into the session.
func TestQoderChatSessionCarriesExecutorForWorkspaceOpenGuard(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the fake CLI")
	}
	h, actor := workbenchFixture(t)
	if _, err := h.repo.UpdateUserCredentials(actor.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{"qoder": {APIKey: "test-pat"}}}); err != nil {
		t.Fatal(err)
	}
	type probe struct {
		executor, runtimeMode string
		openErr               error
	}
	probes := make(chan probe, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.Split(strings.TrimPrefix(r.URL.Path, "/internal/agent-tools/"), "/")[0]
		session, ok := agentToolSessions.get(token)
		if !ok {
			http.Error(w, "missing session", 500)
			return
		}
		_, openErr := (&agentToolGateway{}).openConversationWorkspace(context.Background(), session, map[string]any{"goal": "修改代码"})
		select {
		case probes <- probe{executor: session.Executor, runtimeMode: session.RuntimeMode, openErr: openErr}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	previous := config.Current()
	endpoints := previous
	endpoints.AgentToolGateway = server.URL
	config.SetCurrent(endpoints)
	defer config.SetCurrent(previous)
	binary := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(binary, []byte(strings.ReplaceAll(strings.Replace(qoderGatewayProbeFixture, "%s", python, 1), "%s", server.URL)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_QODER_BIN", binary)

	body, _ := json.Marshal(assistantRequest{SessionID: "qoder-platform-chat", Executor: "qoder", Model: "qoder/auto", Messages: []assistantMessage{{Role: "user", Content: "帮我看看"}}})
	c, response := workbenchContext(actor, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if response.Code != http.StatusOK {
		t.Fatalf("platform-mode Qoder chat must succeed: %d %s", response.Code, response.Body.String())
	}
	select {
	case got := <-probes:
		if got.executor != runtime.AgentCLIQoder {
			t.Fatalf("tool session executor = %q, want %q (runtime mode %q)", got.executor, runtime.AgentCLIQoder, got.runtimeMode)
		}
		if got.runtimeMode == domain.AssistantModeWorkspace {
			t.Fatalf("fixture expected a platform-mode chat, got %q", got.runtimeMode)
		}
		if got.openErr == nil || !strings.Contains(got.openErr.Error(), "Qoder 暂不支持") {
			t.Fatalf("workspace_open during a Qoder turn must be refused, got %v", got.openErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("fake qodercli never reached the tool gateway: %d %s", response.Code, response.Body.String())
	}
}
