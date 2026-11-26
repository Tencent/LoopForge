package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gorilla/websocket"
)

func TestHandleRunnerWorkspaceBrowserRejectsMissingBrowser(t *testing.T) {
	d := &Daemon{}
	request := domain.RunnerWorkspaceRequest{RunID: "run-1", Operation: domain.RunnerWorkspaceOperationBrowser}
	got := d.handleRunnerWorkspaceBrowser(context.Background(), request)
	if got.Status != http.StatusBadRequest || got.Error != "invalid browser request" {
		t.Fatalf("missing browser=%+v", got)
	}
}

// 浏览器请求不依赖工作区根目录：即便 run id 非法，也应先按浏览器分支处理。
func TestReadRunnerWorkspaceDispatchesBrowserBeforeWorkspaceRoot(t *testing.T) {
	d := &Daemon{}
	request := domain.RunnerWorkspaceRequest{RunID: "..", Operation: domain.RunnerWorkspaceOperationBrowser}
	got := d.readRunnerWorkspace(context.Background(), request)
	if got.Status != http.StatusBadRequest || got.Error != "invalid browser request" {
		t.Fatalf("browser dispatch=%+v", got)
	}
	request.Operation = domain.RunnerWorkspaceOperationTree
	if got := d.readRunnerWorkspace(context.Background(), request); got.Error == "invalid browser request" {
		t.Fatalf("non-browser request took the browser branch: %+v", got)
	}
}

// Ensure 时浏览器拉不起来：直接返回带原因的 200 状态，不再调用 HandleBrowser。
func TestHandleRunnerWorkspaceBrowserReportsEnsureFailure(t *testing.T) {
	t.Setenv("AGENTFLOW_BROWSER", "off")
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}}
	request := domain.RunnerWorkspaceRequest{
		RunID:     "run-browser-ensure-off",
		Operation: domain.RunnerWorkspaceOperationBrowser,
		Browser:   &domain.BrowserRequest{Operation: "start", Ensure: true},
	}
	got := d.handleRunnerWorkspaceBrowser(context.Background(), request)
	var state domain.BrowserState
	if got.Status != http.StatusOK || got.Error != "" || json.Unmarshal(got.Payload, &state) != nil {
		t.Fatalf("ensure failure=%+v", got)
	}
	if state.Available || state.Reason != "平台已关闭 Agent 浏览器" {
		t.Fatalf("ensure failure state=%+v", state)
	}
}

func TestDockerBrowserRequestsDoNotWaitForRunnerHelper(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_BROWSER", "always")
	t.Setenv("AGENTFLOW_RUNNER_BINARY", filepath.Join(root, "{os}", "{arch}", "agentflow-runner"))
	// Exercise the Docker browser path without launching a real browser.
	if err := os.WriteFile(filepath.Join(root, "docker"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":/usr/bin:/bin")

	const runID = "browser-helper-sync"
	if err := os.MkdirAll(filepath.Join(root, "runs", runID, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := domain.RunnerWorkspaceRequest{
		RunID: runID, WorkspaceLayout: domain.RunnerWorkspaceLayoutShared,
		Operation: domain.RunnerWorkspaceOperationBrowser, Browser: &domain.BrowserRequest{Ensure: true},
		Sandbox: &domain.RunnerSandboxSpec{
			Profile:    domain.SandboxProfile{Driver: domain.SandboxDriverDocker},
			Dockerfile: &domain.DockerfileDefinition{Content: "FROM fixture"},
		},
		Workflow: &domain.WorkflowDefinition{},
	}
	upgrader := websocket.Upgrader{}
	result := make(chan error, 1)
	probe := func(conn *websocket.Conn) error {
		if err := conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
			return err
		}
		var hello controlMessage
		if err := conn.ReadJSON(&hello); err != nil {
			return err
		}
		const slots = 8
		for index := range slots {
			if err := conn.WriteJSON(controlMessage{
				Type: "workspace.request", RequestID: fmt.Sprint(index), WorkspaceRequest: &request,
			}); err != nil {
				return err
			}
		}
		for range slots {
			var response controlMessage
			if err := conn.ReadJSON(&response); err != nil {
				return fmt.Errorf("browser requests waited for the helper: %w", err)
			}
			if response.WorkspaceResponse == nil || response.WorkspaceResponse.Status != http.StatusOK {
				return fmt.Errorf("unexpected browser response: %+v", response.WorkspaceResponse)
			}
		}
		info := domain.RunnerWorkspaceRequest{RunID: runID, WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, Operation: domain.RunnerWorkspaceOperationInfo}
		if err := conn.WriteJSON(controlMessage{Type: "workspace.request", RequestID: "info", WorkspaceRequest: &info}); err != nil {
			return err
		}
		var response controlMessage
		if err := conn.ReadJSON(&response); err != nil {
			return err
		}
		if response.WorkspaceResponse == nil || response.WorkspaceResponse.Status != http.StatusOK {
			return fmt.Errorf("workspace info did not have a free slot: %+v", response.WorkspaceResponse)
		}
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		result <- probe(conn)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "fixture", DefaultWorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	// Model a cold start whose first helper download is still in flight.
	d.dockerHelperSyncDone = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.controlLoop(ctx)
	}()
	defer func() { cancel(); <-done }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control loop did not complete the browser and workspace requests")
	}
}

// 只读操作（status/frame）不续期沙箱，写操作续期；HandleBrowser 报错映射为 409。
func TestHandleRunnerWorkspaceBrowserTouchesSandboxOnlyForWrites(t *testing.T) {
	const runID = "run-browser-touch"
	stale := time.Now().Add(-time.Hour)
	entry := &runSandbox{runID: runID, lastUsed: stale}
	other := &runSandbox{runID: "run-browser-other", lastUsed: stale}
	d := &Daemon{runSandboxes: map[string]*runSandbox{"a": entry, "b": other}}
	request := domain.RunnerWorkspaceRequest{RunID: runID, Operation: domain.RunnerWorkspaceOperationBrowser}

	request.Browser = &domain.BrowserRequest{Operation: "status"}
	got := d.handleRunnerWorkspaceBrowser(context.Background(), request)
	var state domain.BrowserState
	if got.Status != http.StatusOK || json.Unmarshal(got.Payload, &state) != nil || state.Available {
		t.Fatalf("status without browser=%+v", got)
	}
	if !entry.lastUsed.Equal(stale) {
		t.Fatal("read-only browser operation renewed the run sandbox")
	}

	// 非法的开发服务器请求：HandleBrowser 返回错误，应映射为 409 并保留报错文字。
	request.Browser = &domain.BrowserRequest{Operation: "http", HTTP: &domain.BrowserHTTPRequest{Port: 0, Path: "/"}}
	got = d.handleRunnerWorkspaceBrowser(context.Background(), request)
	if got.Status != http.StatusConflict || got.Error != "无效的开发服务器请求" {
		t.Fatalf("invalid http=%+v", got)
	}
	if !entry.lastUsed.After(stale) {
		t.Fatal("write browser operation did not renew the run sandbox")
	}
	if !other.lastUsed.Equal(stale) {
		t.Fatal("browser operation renewed another run's sandbox")
	}
}
