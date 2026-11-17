package httpapi

import (
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// While one user holds the lease a second save is refused with the holder name,
// which is what stops two tabs from silently overwriting each other.
func TestWorkflowSaveRefusedWhileLockedByOthers(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	alice := domain.User{ID: 1, LoginName: "alice"}
	bob := domain.User{ID: 2, LoginName: "bob"}
	for _, user := range []domain.User{alice, bob} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutProject(domain.Project{ID: "p1", Key: "P1", Name: "P1", OwnerID: 1,
		Members: []domain.ProjectMember{{UserID: 2, Role: "maintainer"}}}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "ws"))), tracker.NewService(), "", "")
	wf := domain.WorkflowDefinition{ID: "wf-lock", Name: "Lock", ProjectID: "p1", SandboxProfileID: "generic-docker",
		Nodes: []domain.WorkflowNode{{ID: "n1", Name: "step", Type: "script", Script: "true"}}}
	saved := requestJSONAuthed[domain.WorkflowDefinition](t, h, alice, "PUT", "/api/workflows", wf, 200)

	// Alice takes the lease; Bob cannot save, and cannot take the lease
	// without asking for a takeover.
	requestJSONAuthed[map[string]any](t, h, alice, "POST", "/api/workflows/wf-lock/edit-lock", map[string]any{}, 200)
	saved.Name = "bob edit"
	requestJSONAuthed[map[string]any](t, h, bob, "PUT", "/api/workflows", saved, 409)
	requestJSONAuthed[map[string]any](t, h, bob, "POST", "/api/workflows/wf-lock/edit-lock", map[string]any{}, 409)

	// After an explicit takeover Bob can save.
	requestJSONAuthed[map[string]any](t, h, bob, "POST", "/api/workflows/wf-lock/edit-lock", map[string]any{"takeover": true}, 200)
	fresh := requestJSONAuthed[domain.WorkflowDefinition](t, h, bob, "GET", "/api/workflows/wf-lock", nil, 200)
	fresh.Name = "bob edit"
	requestJSONAuthed[domain.WorkflowDefinition](t, h, bob, "PUT", "/api/workflows", fresh, 200)
}
