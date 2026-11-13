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
	"time"

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
	// results answers methods with a canned result instead of {}.
	results map[string]string
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
	result := `{}`
	if r := f.results[msg.Method]; r != "" {
		result = r
	}
	reply, _ := json.Marshal(cdpMessage{ID: msg.ID, Result: json.RawMessage(result)})
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
	// The wheel also switches the screencast to light frames; only input
	// events are checked here.
	var sent []cdpMessage
	for _, msg := range transport.sent {
		if strings.HasPrefix(msg.Method, "Input.") {
			sent = append(sent, msg)
		}
	}
	if len(sent) != 5 {
		t.Fatalf("sent %d input messages: %+v", len(sent), transport.sent)
	}
	params := func(i int) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(sent[i].Params, &out)
		return out
	}
	for _, msg := range sent {
		if msg.SessionID != "page-1" {
			t.Fatalf("input must target the attached page session: %+v", msg)
		}
	}
	if p := params(0); sent[0].Method != "Input.dispatchMouseEvent" || p["type"] != "mousePressed" || p["x"] != 500.0 || p["y"] != 125.0 {
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
	if p := params(4); sent[4].Method != "Input.insertText" || p["text"] != "你好" {
		t.Fatalf("text: %s %v", sent[4].Method, p)
	}
}

func TestBrowserViewReportsScrollAndContentSize(t *testing.T) {
	transport := newFakeCDPTransport()
	transport.results = map[string]string{"Page.getLayoutMetrics": `{"cssContentSize":{"width":1000,"height":3200}}`}
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1"}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()
	frame, _ := json.Marshal(map[string]any{"data": "AAAA", "sessionId": 7, "metadata": map[string]any{"deviceWidth": 1000, "deviceHeight": 800, "scrollOffsetX": 0, "scrollOffsetY": 640}})
	view.onEvent(cdpMessage{Method: "Page.screencastFrame", SessionID: "page-1", Params: frame})
	deadline := time.Now().Add(2 * time.Second)
	for {
		state := view.state(0)
		if state.ContentHeight == 3200 {
			if state.ScrollY != 640 || state.Height != 800 || state.ContentWidth != 1000 {
				t.Fatalf("state %+v", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("content size never arrived: %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A second frame within the refresh interval does not read metrics again.
	view.onEvent(cdpMessage{Method: "Page.screencastFrame", SessionID: "page-1", Params: frame})
	time.Sleep(50 * time.Millisecond)
	transport.mu.Lock()
	defer transport.mu.Unlock()
	reads := 0
	for _, msg := range transport.sent {
		if msg.Method == "Page.getLayoutMetrics" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("layout metric reads: %d", reads)
	}
}

func TestBrowserDeviceScale(t *testing.T) {
	for raw, want := range map[string]string{"": "2", "1.5": "1.5", "3": "3", "0.5": "2", "x": "2", "4": "2"} {
		t.Setenv("AGENTFLOW_BROWSER_SCALE", raw)
		if got := BrowserDeviceScale(); got != want {
			t.Fatalf("%q: %s", raw, got)
		}
	}
}

func TestAgentScreenshotsStayInCSSPixels(t *testing.T) {
	transport := newFakeCDPTransport()
	transport.results = map[string]string{
		"Page.getLayoutMetrics":  `{"cssVisualViewport":{"pageX":0,"pageY":300,"clientWidth":1280,"clientHeight":800}}`,
		"Runtime.evaluate":       `{"result":{"value":2}}`,
		"Page.captureScreenshot": `{"data":"AAAA"}`,
	}
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1"}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()
	if data, err := view.screenshot(context.Background()); err != nil || data != "AAAA" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	last := transport.sent[len(transport.sent)-1]
	var params struct {
		Clip map[string]float64 `json:"clip"`
	}
	_ = json.Unmarshal(last.Params, &params)
	if last.Method != "Page.captureScreenshot" || params.Clip["scale"] != 0.5 || params.Clip["y"] != 300 || params.Clip["width"] != 1280 {
		t.Fatalf("%s %s", last.Method, last.Params)
	}
}

func TestBrowserViewCapturesLighterFramesWhileScrolling(t *testing.T) {
	settle := browserMotionSettle
	browserMotionSettle = 60 * time.Millisecond
	defer func() { browserMotionSettle = settle }()
	transport := newFakeCDPTransport()
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1", width: 1000, height: 500, frameWidth: 1600}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()
	starts := func() []map[string]any {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		var out []map[string]any
		for _, msg := range transport.sent {
			if msg.Method == "Page.startScreencast" {
				var p map[string]any
				_ = json.Unmarshal(msg.Params, &p)
				out = append(out, p)
			}
		}
		return out
	}
	wheel := []domain.BrowserInputEvent{{Type: "wheel", X: 0.5, Y: 0.5, DeltaY: 100}}
	ctx := context.Background()

	// Hovering and clicking keep sharp frames.
	if err := view.input(ctx, []domain.BrowserInputEvent{{Type: "mouse", Action: "move", X: 0.5, Y: 0.5}, {Type: "mouse", Action: "down", X: 0.5, Y: 0.5, Buttons: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := starts(); len(got) != 0 {
		t.Fatalf("hover restarted the screencast: %v", got)
	}
	// A scroll switches to light frames once, however many follow.
	for range 3 {
		if err := view.input(ctx, wheel); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := starts()
	if len(got) != 1 || got[0]["maxWidth"] != 800.0 || got[0]["quality"] != 60.0 {
		t.Fatalf("scroll frames: %v", got)
	}
	// Quiet input returns to sharp frames at the panel width.
	deadline := time.Now().Add(2 * time.Second)
	for len(starts()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got = starts()
	if len(got) != 2 || got[1]["maxWidth"] != 1600.0 || got[1]["quality"] != 80.0 {
		t.Fatalf("settled frames: %v", got)
	}
	// Dragging counts as motion too.
	if err := view.input(ctx, []domain.BrowserInputEvent{{Type: "mouse", Action: "move", X: 0.4, Y: 0.5, Buttons: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := starts(); len(got) != 3 || got[2]["quality"] != 60.0 {
		t.Fatalf("drag frames: %v", got)
	}
}

func TestBrowserViewCapturesASharpFrameWhenTheSettledScreencastSendsNone(t *testing.T) {
	settle, wait := browserMotionSettle, browserSettledFrameWait
	browserMotionSettle, browserSettledFrameWait = 30*time.Millisecond, 60*time.Millisecond
	defer func() { browserMotionSettle, browserSettledFrameWait = settle, wait }()
	wheel := []domain.BrowserInputEvent{{Type: "wheel", X: 0.5, Y: 0.5, DeltaY: 100}}
	screenshots := func(transport *fakeCDPTransport) int {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		n := 0
		for _, msg := range transport.sent {
			if msg.Method == "Page.captureScreenshot" {
				n++
			}
		}
		return n
	}
	newView := func() (*browserView, *fakeCDPTransport) {
		transport := newFakeCDPTransport()
		transport.results = map[string]string{"Page.captureScreenshot": `{"data":"SHARP"}`}
		view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "page-1", active: "t1", width: 1000, height: 500, frameWidth: 1600, frame: "LIGHT", seq: 5}
		view.client = newCDPClient(transport, view.onEvent)
		return view, transport
	}

	// Chromium sends nothing after the restart: the view captures the page.
	view, transport := newView()
	defer view.client.close()
	if err := view.input(context.Background(), wheel); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for view.state(5).Frame != "SHARP" {
		if time.Now().After(deadline) {
			t.Fatalf("no sharp frame after the scroll settled: %+v", view.state(0))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := screenshots(transport); n != 1 {
		t.Fatalf("screenshots: %d", n)
	}

	// A frame that arrives in time is kept and no screenshot is taken.
	view, transport = newView()
	defer view.client.close()
	if err := view.input(context.Background(), wheel); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		transport.mu.Lock()
		restarts := 0
		for _, msg := range transport.sent {
			if msg.Method == "Page.startScreencast" {
				restarts++
			}
		}
		transport.mu.Unlock()
		if restarts == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the screencast never returned to sharp frames")
		}
	}
	time.Sleep(browserSettledFrameWait / 4)
	frame, _ := json.Marshal(map[string]any{"data": "RESENT", "sessionId": 1, "metadata": map[string]any{"deviceWidth": 1000, "deviceHeight": 500}})
	view.onEvent(cdpMessage{Method: "Page.screencastFrame", SessionID: "page-1", Params: frame})
	time.Sleep(browserSettledFrameWait * 2)
	if got := view.state(0).Frame; got != "RESENT" || screenshots(transport) != 0 {
		t.Fatalf("frame %q, screenshots %d", got, screenshots(transport))
	}
}
