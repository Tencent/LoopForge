package runner

import (
	"context"
	"net/http"
	"testing"

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
