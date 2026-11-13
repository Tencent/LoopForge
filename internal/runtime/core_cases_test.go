package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestProtocolAdaptersCoverNativeFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ex    domain.ExecutorDefinition
		lines []string
	}{
		{"claude", domain.ExecutorDefinition{Kind: "claude-code", Protocol: "stream-json", Model: "default"}, []string{
			`{"type":"system","subtype":"ignored"}`,
			`{"type":"system","subtype":"init","session_id":"s1","model":"opus","permissionMode":"bypass"}`,
			`{"type":"stream_event","session_id":"s1","parent_tool_use_id":"p","event":{"type":"message_start","message":{"model":"opus"}}}`,
			`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"call1","name":"Read","input":{}}}}`,
			`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"x\"}"}}}`,
			`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_stop"}}`,
			`{"type":"stream_event","session_id":"s1","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"considering"}}}`,
			`{"type":"stream_event","session_id":"s1","event":{"type":"message_delta","usage":{"input_tokens":2,"output_tokens":3}}}`,
			`{"type":"stream_event","session_id":"s1","event":{"type":"message_stop"}}`,
			`{"type":"assistant","session_id":"s1","message":{"model":"opus","content":[{"type":"text","text":"answer"},{"type":"tool_use","id":"call2","name":"Write","input":{"path":"x"}}]}}`,
			`{"type":"user","session_id":"s1","message":{"content":[{"type":"tool_result","tool_use_id":"call2","is_error":false,"content":"done"}]}}`,
			`{"type":"result","session_id":"s1","result":"final answer","is_error":false,"duration_ms":4,"num_turns":1,"total_cost_usd":0.2,"usage":{"input_tokens":2,"output_tokens":3}}`,
		}},
		{"codex", domain.ExecutorDefinition{Kind: "codex", Protocol: "jsonl"}, []string{
			`{"type":"thread.started","thread_id":"th"}`,
			`{"type":"turn.started","threadId":"th"}`,
			`{"type":"item.started","thread_id":"th","item":{"type":"command_execution","id":"i1","command":"go test","status":"running"}}`,
			`{"type":"item.completed","thread_id":"th","item":{"type":"command_execution","id":"i1","command":"go test","status":"completed","exit_code":0,"aggregated_output":"ok"}}`,
			`{"type":"item.updated","thread_id":"th","item":{"type":"agent_message","id":"i2","text":"codex answer"}}`,
			`{"type":"item.completed","thread_id":"th","item":{"type":"reasoning","text":"reason"}}`,
			`{"type":"turn.completed","thread_id":"th","usage":{"input_tokens":4,"output_tokens":5}}`,
			`{"type":"turn.failed","thread_id":"th"}`,
			`{"type":"custom.tool","message":"fallback","phase":"start","tool_call_id":"z"}`,
		}},
		{"generic", domain.ExecutorDefinition{Kind: "custom", Protocol: "jsonl"}, []string{
			`{"type":"tool.started","message":"run","session_id":"g","tool_call_id":"1"}`,
			`{"type":"reasoning.delta","text":"think"}`,
			`{"type":"usage.update","usage":{"input_tokens":7}}`,
			`{"type":"usage.final","input_tokens":8}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []domain.TelemetryEvent
			o := NewProtocolObserver(tc.ex, func(e domain.TelemetryEvent) { events = append(events, e) })
			o.Handle(domain.TelemetryEvent{Name: "process.stderr", Message: "plain"})
			o.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: "not-json"})
			for _, line := range tc.lines {
				o.Handle(domain.TelemetryEvent{Name: "process.stdout", Message: line})
			}
			o.Finalize(true)
			if len(events) < 3 {
				t.Fatalf("too few events: %+v", events)
			}
			for _, event := range events {
				if event.SpecVersion == "" || event.Source.Adapter == "" {
					t.Fatalf("event not normalized: %+v", event)
				}
			}
			if tc.name == "claude" {
				if o.VisibleOutput() != "final answer" || !o.adapter.Finished() || o.adapter.Failed() {
					t.Fatalf("observer state output=%q finished=%v failed=%v", o.VisibleOutput(), o.adapter.Finished(), o.adapter.Failed())
				}
			}
		})
	}
	var nilObserver *ProtocolObserver
	nilObserver.Handle(domain.TelemetryEvent{})
	nilObserver.Finalize(false)
	if nilObserver.VisibleOutput() != "" {
		t.Fatal("nil observer has output")
	}
}

func TestProtocolHelpersAndMetadata(t *testing.T) {
	spec := ObservabilitySpec()
	if spec.Version == "" || len(spec.Adapters) != 4 {
		t.Fatalf("spec=%+v", spec)
	}
	metrics := usageMetrics(domain.TokenUsage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, ReasoningTokens: 5, CostUSD: 1.25})
	if len(metrics) != 6 || int64Value(json.Number("9")) != 9 || int64Value("bad") != 0 || float64Value(2.5) != 2.5 {
		t.Fatalf("helpers metrics=%+v", metrics)
	}
	if stringValue(3) != "" || sliceValue("x") != nil {
		t.Fatal("type helpers accepted wrong types")
	}
}

func TestBrowserViewOperationsWithFakeCDP(t *testing.T) {
	transport := newFakeCDPTransport()
	view := &browserView{
		runID: "cvR", endpoint: browserEndpoint{Kind: "host"}, targets: map[string]*browserTarget{
			"old": {ID: "old", Title: "Old", URL: "https://old", created: time.Unix(1, 0)},
			"new": {ID: "new", Title: "New", URL: "https://new", created: time.Unix(2, 0)},
		}, active: "new", session: "s", changed: make(chan struct{}), frame: "jpeg", seq: 2, width: 1000, height: 700,
	}
	view.client = newCDPClient(transport, view.onEvent)
	defer view.client.close()
	ctx := context.Background()
	if view.latestTarget() != "new" || view.activeID() != "new" {
		t.Fatal("target selection")
	}
	state := view.state(0)
	if !state.Available || state.Frame != "jpeg" || len(state.Tabs) != 2 || !state.Tabs[1].Active {
		t.Fatalf("state=%+v", state)
	}
	for _, raw := range []string{"", "hello world", "localhost:8080", "example.com", "about:blank", "https://example.test"} {
		if err := view.navigate(ctx, raw); raw == "" && err == nil || raw != "" && err != nil {
			t.Fatalf("navigate %q: %v", raw, err)
		}
	}
	if err := view.pageCall(ctx, "Page.reload", nil); err != nil {
		t.Fatal(err)
	}
	if got := view.screencastParams(); got["maxWidth"] != 2560 {
		t.Fatalf("params=%v", got)
	}
	view.frameWidth = 100
	if got := view.screencastParams(); got["maxWidth"] != 320 {
		t.Fatalf("clamped params=%v", got)
	}
	if err := view.setFrameWidth(ctx, 900); err != nil {
		t.Fatal(err)
	}
	if err := view.setFrameWidth(ctx, 900); err != nil {
		t.Fatal(err)
	}
	if err := view.resize(ctx, 100, 100); err == nil {
		t.Fatal("invalid resize accepted")
	}
	if err := view.resize(ctx, 1000, 800); err != nil {
		t.Fatal(err)
	}
	view.onEvent(cdpMessage{Method: "Target.targetInfoChanged", Params: json.RawMessage(`{"targetInfo":{"targetId":"new","type":"page","title":"Changed","url":"https://changed"}}`)})
	view.onEvent(cdpMessage{Method: "Target.targetCreated", Params: json.RawMessage(`{"targetInfo":{"targetId":"ignore","type":"worker"}}`)})
	view.onEvent(cdpMessage{Method: "Page.screencastFrame", SessionID: "s", Params: json.RawMessage(`{"data":"next","sessionId":7,"metadata":{"deviceWidth":800,"deviceHeight":600}}`)})
	if view.state(2).Frame != "next" {
		t.Fatal("frame event not recorded")
	}
	ctxDone, cancel := context.WithCancel(ctx)
	cancel()
	view.waitFrame(ctxDone, 99, 10)
	view.mu.Lock()
	view.session = ""
	view.mu.Unlock()
	if err := view.pageCall(ctx, "x", nil); err == nil {
		t.Fatal("page call without session")
	}
	if err := view.attach(ctx, ""); err == nil {
		t.Fatal("empty target accepted")
	}
}

func TestCDPClientErrorsAndEvents(t *testing.T) {
	transport := newFakeCDPTransport()
	events := make(chan cdpMessage, 1)
	client := newCDPClient(transport, func(m cdpMessage) { events <- m })
	transport.replies <- []byte(`bad`)
	transport.replies <- []byte(`{"method":"Target.created"}`)
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
	if err := client.call(context.Background(), "", "ok", map[string]any{"x": 1}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	client.close()
	if !client.closed() {
		t.Fatal("client should be closed")
	}
	if err := client.call(context.Background(), "", "after-close", nil, nil); err == nil {
		t.Fatal("closed call succeeded")
	}
}

func TestBrowserAgentOperations(t *testing.T) {
	for _, ref := range []string{"", "x1", "e", "e-no"} {
		if _, err := browserRefSelector(ref); err == nil {
			t.Fatalf("invalid ref accepted: %q", ref)
		}
	}
	if selector, err := browserRefSelector(" e12 "); err != nil || !strings.Contains(selector, "e12") {
		t.Fatalf("selector=%q err=%v", selector, err)
	}
	transport := cvRScriptedCDPTransport{replies: make(chan []byte, 32), done: make(chan struct{})}
	view := &browserView{targets: map[string]*browserTarget{}, changed: make(chan struct{}), session: "s"}
	view.client = newCDPClient(&transport, nil)
	defer view.client.close()
	ctx := context.Background()
	var snap domain.BrowserSnapshot
	if err := view.evaluate(ctx, browserSnapshotScript, &snap); err != nil {
		t.Fatal(err)
	}
	if err := view.clickRef(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if err := view.typeRef(ctx, "e2", "hello", true); err != nil {
		t.Fatal(err)
	}
	if err := view.typeRef(ctx, "e2", "", false); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"space", "Tab", "Escape", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "PageUp", "PageDown", "Home", "End"} {
		if err := view.press(ctx, key); err != nil {
			t.Fatalf("press %s: %v", key, err)
		}
	}
	if err := view.press(ctx, "F13"); err == nil {
		t.Fatal("unknown key accepted")
	}
	if frame, err := view.screenshot(ctx); err != nil || frame != "image" {
		t.Fatalf("frame=%q err=%v", frame, err)
	}
	if snap, frame, err := view.agentOperation(ctx, domain.BrowserRequest{Operation: "snapshot"}); err != nil || snap == nil || frame != "" {
		t.Fatalf("snapshot=%+v frame=%q err=%v", snap, frame, err)
	}
	if snap, frame, err := view.agentOperation(ctx, domain.BrowserRequest{Operation: "screenshot"}); err != nil || snap != nil || frame != "image" {
		t.Fatalf("screenshot=%+v frame=%q err=%v", snap, frame, err)
	}
	view.session = ""
	if err := view.evaluate(ctx, "1", nil); err == nil {
		t.Fatal("evaluate without page")
	}
	if _, err := view.screenshot(ctx); err == nil {
		t.Fatal("screenshot without page")
	}
}

func TestHandleBrowserDispatchWithExistingView(t *testing.T) {
	transport := cvRScriptedCDPTransport{replies: make(chan []byte, 128), done: make(chan struct{})}
	view := &browserView{
		runID: "cvR-handle", endpoint: browserEndpoint{Kind: "local"},
		targets: map[string]*browserTarget{"tab": {ID: "tab", Title: "Tab", URL: "https://example.test"}},
		active:  "tab", session: "s", changed: make(chan struct{}), width: 1000, height: 800,
	}
	view.client = newCDPClient(&transport, view.onEvent)
	browserViews.Lock()
	browserViews.items[view.runID] = view
	browserViews.Unlock()
	t.Cleanup(func() {
		browserViews.Lock()
		delete(browserViews.items, view.runID)
		browserViews.Unlock()
		view.client.close()
	})
	ctx := context.Background()
	requests := []domain.BrowserRequest{
		{Operation: "status"},
		{Operation: "frame", After: 100, WaitMs: 1},
		{Operation: "navigate", URL: "example.com"},
		{Operation: "back"},
		{Operation: "forward"},
		{Operation: "reload"},
		{Operation: "resize", Width: 1000, Height: 700, FrameWidth: 900},
		{Operation: "activate", TargetID: "tab"},
		{Operation: "newTab"},
		{Operation: "input", Events: []domain.BrowserInputEvent{{Type: "text", Text: "hello"}, {Type: "key", Action: "up", Key: "a"}}},
		{Operation: "closeTab", TargetID: "tab"},
		{Operation: "snapshot"},
		{Operation: "screenshot"},
		{Operation: "click", Ref: "e1"},
		{Operation: "type", Ref: "e1", Text: "x", Submit: true},
		{Operation: "press", Key: "Enter"},
	}
	for _, request := range requests {
		state, err := HandleBrowser(ctx, view.runID, request)
		if err != nil || !state.Available {
			t.Fatalf("%s state=%+v err=%v", request.Operation, state, err)
		}
	}
	if _, err := HandleBrowser(ctx, view.runID, domain.BrowserRequest{Operation: "unknown"}); err == nil {
		t.Fatal("unsupported operation accepted")
	}
	view.mu.Lock()
	view.historyKey = view.active + "\x00https://example.test"
	view.canGoBack, view.canGoForward = true, false
	view.mu.Unlock()
	if back, forward := view.cachedHistoryFlags(ctx, "https://example.test"); !back || forward {
		t.Fatalf("cached history=%v %v", back, forward)
	}
	view.onEvent(cdpMessage{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"not-active"}`)})
}

func TestHandleBrowserOperationErrors(t *testing.T) {
	for i, request := range []domain.BrowserRequest{
		{Operation: "input", Events: []domain.BrowserInputEvent{{Type: "text", Text: "x"}}},
		{Operation: "navigate", URL: "example.com"},
		{Operation: "back"},
		{Operation: "reload"},
		{Operation: "resize", Width: 1000, Height: 700},
		{Operation: "activate", TargetID: "other"},
		{Operation: "newTab"},
		{Operation: "closeTab", TargetID: "tab"},
		{Operation: "snapshot"},
		{Operation: "screenshot"},
	} {
		runID := fmt.Sprintf("cvR-error-%d", i)
		transport := &cvRFailWriteTransport{done: make(chan struct{})}
		view := &browserView{runID: runID, endpoint: browserEndpoint{Kind: "local"}, targets: map[string]*browserTarget{"tab": {ID: "tab"}}, active: "tab", session: "s", changed: make(chan struct{})}
		view.client = newCDPClient(transport, nil)
		browserViews.Lock()
		browserViews.items[runID] = view
		browserViews.Unlock()
		_, err := HandleBrowser(context.Background(), runID, request)
		if err == nil {
			t.Fatalf("%s unexpectedly succeeded", request.Operation)
		}
		view.client.close()
		browserViews.Lock()
		delete(browserViews.items, runID)
		browserViews.Unlock()
	}
}

func TestProcessSandboxPathsProfilesAndCommands(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "nested", ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := discoverProjectAgentPaths(workspace)
	if err != nil || !cvRContains(paths, "AGENTS.md") || !cvRContains(paths, filepath.Join("nested", ".claude")) {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if _, err := validateAgentConfigTarget(workspace, "AGENTS.md"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	_ = os.WriteFile(outside, []byte("x"), 0o600)
	if err := os.Symlink(outside, filepath.Join(workspace, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := validateAgentConfigTarget(workspace, "CLAUDE.md"); err == nil {
		t.Fatal("outside symlink accepted")
	}
	if err := os.Remove(filepath.Join(workspace, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	env := appendEnvironment([]string{"A=1", "A=2", "bad"}, map[string]string{"A": "3", "B": "4"})
	if strings.Join(env, ",") != "A=3,B=4" {
		t.Fatalf("env=%v", env)
	}
	filtered := controlledBaseEnvironment([]string{"PATH=/bin", "OPENAI_API_KEY=x", "GIT_CONFIG_KEY_0=x"})
	if len(filtered) != 1 {
		t.Fatalf("filtered=%v", filtered)
	}
	req := SandboxRequirements{BunFS: true, CLIStates: []CLIStateRequirement{{Directory: ".cvR-state"}, {Directory: ".cvR-state"}, {Directory: ""}}}
	states, err := agentStateDirectories(req)
	if err != nil || len(states) != 1 {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	if first, err := agentStateDirectoryRequirements(req); err != nil || first == "" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	profile, err := seatbeltProfileCommand(workspace, req, true)
	if err != nil || !strings.Contains(profile, "deny file-read") {
		t.Fatalf("profile=%q err=%v", profile, err)
	}
	args, err := bwrapArgsCommand(workspace, req, true)
	if err != nil || !cvRContains(args, "--tmpfs") || !cvRContains(args, "--ro-bind") {
		t.Fatalf("args=%v err=%v", args, err)
	}
	emptyDir, emptyFile, err := isolationMaskSources(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(emptyDir); !info.IsDir() {
		t.Fatal("empty dir")
	}
	if info, _ := os.Stat(emptyFile); !info.Mode().IsRegular() {
		t.Fatal("empty file")
	}
	sb := &processSandbox{id: "id", workspace: workspace, driver: "bwrap", binary: "/bin/echo", profile: domain.SandboxProfile{Environment: map[string]string{"X": "1"}}}
	if _, err := sb.command(context.Background(), CommandSpec{}, nil); err == nil {
		t.Fatal("empty command accepted")
	}
	cmd, err := sb.command(context.Background(), CommandSpec{Argv: []string{"true"}}, []string{"PATH=/bin"})
	if err != nil || cmd.Dir != workspace || !strings.Contains(strings.Join(cmd.Env, " "), "TMPDIR=/tmp") {
		t.Fatalf("cmd=%+v err=%v", cmd, err)
	}
	sb.driver = "unknown"
	if _, err := sb.command(context.Background(), CommandSpec{Argv: []string{"true"}}, nil); err == nil {
		t.Fatal("unknown driver accepted")
	}
	if _, err := createSeatbelt("x", workspace, domain.SandboxProfile{}, SandboxRequirements{}); err == nil {
		t.Fatal("seatbelt unexpectedly available on linux")
	}
}

func TestManagerLocalLifecycleAndHelpers(t *testing.T) {
	root := t.TempDir()
	m := NewManagerWithLimit(root, 0)
	if m.Root() != root || m.RunWorkspace("run / one") == "" {
		t.Fatal("manager paths")
	}
	sb, err := m.AcquireRun(context.Background(), "run-one", domain.SandboxProfile{Driver: "local", Environment: map[string]string{"CVR": "1"}}, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
	if err != nil {
		t.Fatal(err)
	}
	driver, _ := sb.(interface{ Driver() string })
	if driver == nil || driver.Driver() != "local" || sandboxIsolationEnv(sb) == nil {
		t.Fatalf("sandbox=%T", sb)
	}
	got, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"sh", "-c", "printf cvR"}}, nil)
	if err != nil || strings.TrimSpace(got) != "cvR" {
		t.Fatalf("exec=%q err=%v", got, err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, "stale"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "persist-live"), 0o700)
	removed, err := m.ReclaimExcept([]string{"live"})
	if err != nil || removed != 2 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	stale := staleSandboxContainers(map[string]string{"a": "live", "b": "persist-live", "c": "dead"}, []string{"live"})
	if len(stale) != 1 || stale["c"] != "dead" {
		t.Fatalf("stale=%v", stale)
	}
	var nilManager *Manager
	if nilManager.Root() != "" || nilManager.RunWorkspace("x") != "" {
		t.Fatal("nil manager")
	}
}

func cvRContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type cvRErrorTransport struct{}

func (cvRErrorTransport) read() ([]byte, error) { return nil, errors.New("read") }
func (cvRErrorTransport) write([]byte) error    { return errors.New("write") }
func (cvRErrorTransport) close() error          { return nil }

type cvRFailWriteTransport struct {
	done chan struct{}
	once sync.Once
}

func (t *cvRFailWriteTransport) read() ([]byte, error) {
	<-t.done
	return nil, errors.New("closed")
}
func (t *cvRFailWriteTransport) write([]byte) error { return errors.New("write failed") }
func (t *cvRFailWriteTransport) close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

type cvRScriptedCDPTransport struct {
	replies chan []byte
	done    chan struct{}
	once    sync.Once
}

func (t *cvRScriptedCDPTransport) read() ([]byte, error) {
	select {
	case reply := <-t.replies:
		return reply, nil
	case <-t.done:
		return nil, errors.New("closed")
	}
}

func (t *cvRScriptedCDPTransport) write(body []byte) error {
	var msg cdpMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return err
	}
	result := `{}`
	switch msg.Method {
	case "Target.attachToTarget":
		result = `{"sessionId":"s2"}`
	case "Target.createTarget":
		result = `{"targetId":"tab2"}`
	case "Page.getLayoutMetrics":
		result = `{"cssVisualViewport":{"clientWidth":900,"clientHeight":700},"cssLayoutViewport":{"clientWidth":900,"clientHeight":700}}`
	case "Browser.getWindowForTarget":
		result = `{"windowId":1,"bounds":{"width":920,"height":750}}`
	case "Runtime.evaluate":
		var params struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		value := `{"text":"page","elements":[]}`
		if strings.Contains(params.Expression, "getBoundingClientRect") {
			value = `{"X":10,"Y":20}`
		} else if strings.Contains(params.Expression, "typeof e.select") {
			value = `true`
		} else if params.Expression == "document.readyState" {
			value = `"complete"`
		}
		result = `{"result":{"value":` + value + `}}`
	case "Page.captureScreenshot":
		result = `{"data":"image"}`
	}
	reply, _ := json.Marshal(cdpMessage{ID: msg.ID, Result: json.RawMessage(result)})
	t.replies <- reply
	return nil
}

func (t *cvRScriptedCDPTransport) close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}
