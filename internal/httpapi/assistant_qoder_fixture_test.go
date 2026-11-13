package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// A simulated CLI checks the chat invocation boundary and its session mapping;
// this does not exercise Qoder's own model or MCP implementation.
const qoderChatFixture = `#!/usr/bin/python3
import json, os, sys, urllib.request
args = sys.argv[1:]
def value(flag): return args[args.index(flag)+1]
assert os.environ['QODER_PERSONAL_ACCESS_TOKEN'] == 'fixture-pat'
assert value('--tools') == '' and '--strict-mcp-config' in args
server = json.load(open(value('--mcp-config')))['mcpServers']['probe']
assert server['type'] == 'http'
state = os.path.join(os.environ['QODER_CONFIG_DIR'], 'fixture-session.json')
if '--resume' in args:
    assert value('--resume') == 'native-session-1'
    answer = json.load(open(state))['answer']
else:
    data = json.dumps({'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':'read_secret','arguments':{}}}).encode()
    response = json.load(urllib.request.urlopen(urllib.request.Request(server['url'], data=data, headers={'Content-Type':'application/json'})))
    answer = response['result']['content'][0]['text']
    json.dump({'answer':answer}, open(state, 'w'))
for event in [
    {'type':'system','subtype':'init'},
    {'type':'assistant','message':{'role':'assistant','content':[{'type':'text','text':answer}]}},
    {'type':'result','subtype':'success','is_error':False,'result':answer},
]:
    event['session_id'] = 'native-session-1'
    print(json.dumps(event), flush=True)
`

func TestQoderSimulatedChatMCPAndResume(t *testing.T) {
	var calls atomic.Int32
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"fixture-answer-42"}]}}`))
	}))
	defer mcp.Close()
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "qodercli")
	if err := os.WriteFile(binary, []byte(qoderChatFixture), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_QODER_BIN", binary)
	config := filepath.Join(root, "mcp-input.json")
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"probe": map[string]any{"url": mcp.URL}}})
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	user := domain.User{ID: 1, AgentCLICredentials: map[string]domain.AgentCLICredential{"qoder": {APIKey: "fixture-pat"}}}
	req := assistantCLIRequest{Executor: "qoder", Model: "auto", Prompt: "Use the MCP tool", SessionID: "chat-1", UserID: 1, SessionRoot: root, Native: true, MCPConfig: config}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for turn := 0; turn < 2; turn++ {
		req.Resume = turn > 0
		out, session, err := runAssistantCLI(ctx, req, user, nil)
		if err != nil || !strings.Contains(out, "fixture-answer-42") || session != "chat-1" {
			t.Fatalf("turn %d: output=%q session=%q error=%v", turn, out, session, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one MCP call, got %d", calls.Load())
	}
}
