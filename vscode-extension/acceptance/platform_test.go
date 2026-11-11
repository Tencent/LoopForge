package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/httpapi"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
)

func TestExtensionClientAgainstRealPlatform(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "false")
	gin.SetMode(gin.ReleaseMode)
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wf := domain.WorkflowDefinition{ID: "vscode-acceptance", Name: "VS Code acceptance", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	engine := engine.New(store, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	server := httptest.NewServer(httpapi.New(store, engine, tracker.NewService(), "", ""))
	defer server.Close()
	cmd := exec.Command("node", "platform-client.js", server.URL)
	cmd.Env = append(os.Environ(), "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("extension client: %v\n%s", err, out)
	}
	t.Log(string(out))
}
