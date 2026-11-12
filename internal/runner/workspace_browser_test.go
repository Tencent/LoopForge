package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"agentflow/internal/domain"
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
