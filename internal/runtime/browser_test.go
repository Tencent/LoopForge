package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/domain"
)

func TestMaterializeInvocationInjectsBrowserMCP(t *testing.T) {
	workspace := t.TempDir()
	browser := domain.MCPServerDefinition{ID: BrowserMCPServerID, Name: "Agent 浏览器", Transport: "http", URL: "http://127.0.0.1:8931/mcp"}
	compiled, err := materializeInvocation(workspace, "invoke-browser", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.AgentDefinition{ID: "dev", Name: "Dev"}, domain.AgentAssetBundle{}, InvocationOptions{BrowserMCP: &browser})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(compiled.AgentRoot), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), BrowserMCPServerID) || !strings.Contains(string(raw), "http://127.0.0.1:8931/mcp") {
		t.Fatalf("mcp config missing browser server:\n%s", raw)
	}
	hinted := false
	_ = filepath.WalkDir(workspace, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if body, readErr := os.ReadFile(path); readErr == nil && strings.Contains(string(body), "mcp__"+BrowserMCPServerID+"__") {
				hinted = true
			}
		}
		return nil
	})
	if !hinted {
		t.Fatal("agent instructions do not explain the browser MCP")
	}
}

func TestBrowserWantedForExecutor(t *testing.T) {
	t.Setenv("AGENTFLOW_BROWSER", "")
	if BrowserWantedForExecutor("mock") || BrowserWantedForExecutor("codebuddy") {
		t.Fatal("default under go test: no executor launches a browser")
	}
	t.Setenv("AGENTFLOW_BROWSER", "always")
	if !BrowserWantedForExecutor("mock") || !BrowserWantedForExecutor("codebuddy") {
		t.Fatal("always forces every executor")
	}
	t.Setenv("AGENTFLOW_BROWSER", "off")
	if BrowserWantedForExecutor("codebuddy") {
		t.Fatal("off disables browsers")
	}
}

func TestEnsureSandboxBrowserUnwrapsAndSkipsUnknownSandboxes(t *testing.T) {
	if _, ok := unwrapSandbox(WithSandboxCredentials(&dockerSandbox{id: "x"}, RunCredentials{})).(*dockerSandbox); !ok {
		t.Fatal("managed sandbox must unwrap to its docker sandbox")
	}
	sb := &capturingSandbox{workspace: t.TempDir()}
	var events []domain.TelemetryEvent
	if got := EnsureSandboxBrowser(context.Background(), sb, sb.workspace, "run-1", func(e domain.TelemetryEvent) { events = append(events, e) }); got != nil || len(events) != 0 {
		t.Fatalf("unknown sandbox got browser=%v events=%v", got, events)
	}
	if got := EnsureSandboxBrowser(context.Background(), sb, sb.workspace, "", nil); got != nil {
		t.Fatal("a browser needs a run to belong to")
	}
}

func TestHandleBrowserReportsMissingBrowser(t *testing.T) {
	state, err := HandleBrowser(context.Background(), "no-such-run-"+t.Name(), domain.BrowserRequest{Operation: "status"})
	if err != nil || state.Available || !strings.Contains(state.Reason, "尚未启动") {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

type fakeCDPTransport struct {
	mu      sync.Mutex
	sent    []cdpMessage
	replies chan []byte
	closed  chan struct{}
	once    sync.Once
}

func newFakeCDPTransport() *fakeCDPTransport {
	return &fakeCDPTransport{replies: make(chan []byte, 64), closed: make(chan struct{})}
}

func (f *fakeCDPTransport) read() ([]byte, error) {
	select {
	case msg := <-f.replies:
		return msg, nil
	case <-f.closed:
		return nil, errors.New("closed")
	}
}

func (f *fakeCDPTransport) write(body []byte) error {
	var msg cdpMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return err
	}
	f.mu.Lock()
	f.sent = append(f.sent, msg)
	f.mu.Unlock()
	reply, _ := json.Marshal(cdpMessage{ID: msg.ID, Result: json.RawMessage(`{}`)})
	f.replies <- reply
	return nil
}

func (f *fakeCDPTransport) close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func TestBrowserViewInputMapsToCDP(t *testing.T) {
	transport := newFakeCDPTransport()
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1", width: 1000, height: 500}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()
	err := view.input(context.Background(), []domain.BrowserInputEvent{
		{Type: "mouse", Action: "down", X: 0.5, Y: 0.25, Button: "left", Buttons: 1, ClickCount: 1},
		{Type: "wheel", X: 2, Y: -1, DeltaY: 120},
		{Type: "key", Action: "down", Key: "a", Code: "KeyA", KeyCode: 65, Text: "a"},
		{Type: "key", Action: "down", Key: "Backspace", Code: "Backspace", KeyCode: 8},
		{Type: "text", Text: "你好"},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.sent) != 5 {
		t.Fatalf("sent %d messages: %+v", len(transport.sent), transport.sent)
	}
	params := func(i int) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(transport.sent[i].Params, &out)
		return out
	}
	for _, msg := range transport.sent {
		if msg.SessionID != "page-1" {
			t.Fatalf("input must target the attached page session: %+v", msg)
		}
	}
	if p := params(0); transport.sent[0].Method != "Input.dispatchMouseEvent" || p["type"] != "mousePressed" || p["x"] != 500.0 || p["y"] != 125.0 {
		t.Fatalf("mouse: %s %v", transport.sent[0].Method, p)
	}
	if p := params(1); p["type"] != "mouseWheel" || p["x"] != 1000.0 || p["y"] != 0.0 || p["deltaY"] != 120.0 {
		t.Fatalf("wheel coordinates must clamp to the frame: %v", p)
	}
	if p := params(2); p["type"] != "keyDown" || p["text"] != "a" {
		t.Fatalf("printable key: %v", p)
	}
	if p := params(3); p["type"] != "rawKeyDown" || p["text"] != nil {
		t.Fatalf("control key: %v", p)
	}
	if p := params(4); transport.sent[4].Method != "Input.insertText" || p["text"] != "你好" {
		t.Fatalf("text: %s %v", transport.sent[4].Method, p)
	}
}
