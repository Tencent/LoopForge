package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// 用户在面板上主动启动浏览器：浏览器开在该运行的共享沙箱里，面板操作续期沙箱，沙箱回收时浏览器随之关闭。
func TestRunnerStartsPanelBrowserInRunSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := runtime.FindChromium(); err != nil {
		t.Skip("chromium not installed")
	}
	d, request, _ := runnerPreparationFixture(t)
	defer d.closeAllRunSandboxes(context.Background())
	request.Operation = domain.RunnerWorkspaceOperationBrowser
	request.Browser = &domain.BrowserRequest{Operation: "start", Ensure: true}
	result := d.readRunnerWorkspace(context.Background(), request)
	var state domain.BrowserState
	if result.Status != 200 || json.Unmarshal(result.Payload, &state) != nil || !state.Available {
		t.Fatalf("start: %d %s %s", result.Status, result.Error, result.Payload)
	}
	if !runtime.BrowserRunning(context.Background(), request.RunID) || len(d.runSandboxes) != 1 {
		t.Fatalf("browser running=%v sandboxes=%d", runtime.BrowserRunning(context.Background(), request.RunID), len(d.runSandboxes))
	}

	stale := time.Now().Add(-time.Hour)
	for _, entry := range d.runSandboxes {
		entry.lastUsed = stale
	}
	request.Browser = &domain.BrowserRequest{Operation: "reload"}
	if result := d.readRunnerWorkspace(context.Background(), request); result.Status != 200 {
		t.Fatalf("reload: %+v", result)
	}
	d.reapIdleRunSandboxes(context.Background(), 30*time.Minute)
	if len(d.runSandboxes) != 1 {
		t.Fatal("operating the panel browser did not keep the run sandbox alive")
	}

	d.closeAllRunSandboxes(context.Background())
	if runtime.BrowserRunning(context.Background(), request.RunID) {
		t.Fatal("browser outlived the run sandbox")
	}
}
