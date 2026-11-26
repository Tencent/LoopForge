package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// TestWorkflowSaveRequiresExistingSandboxProfile 校验 SOP 保存路径上的沙箱档案检查：
// 标识缺失与档案不存在都由 requireWorkflowSandbox 统一回 400，且文案保持不变。
func TestWorkflowSaveRequiresExistingSandboxProfile(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "alpha", Key: "ALPHA", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	missingID := requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha",
	}, http.StatusBadRequest)
	if missingID["error"] != "workflow sandboxProfileId is required" {
		t.Fatalf("missing sandboxProfileId error = %q", missingID["error"])
	}

	unknownID := requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", domain.WorkflowDefinition{
		ID: "alpha-flow", Name: "Alpha flow", ProjectID: "alpha", SandboxProfileID: "ghost-sandbox",
	}, http.StatusBadRequest)
	if unknownID["error"] != "sandbox profile not found" {
		t.Fatalf("unknown sandboxProfileId error = %q", unknownID["error"])
	}

	if _, exists := s.Workflow("alpha-flow"); exists {
		t.Fatal("rejected workflow must not be stored")
	}
}
