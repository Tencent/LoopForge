package engine

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// A store that rejects the artifact write must fail the node and must not
// announce an artifact that was never saved.
func TestScriptNodeArtifactWriteFailureFailsNode(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DB.Exec("CREATE TRIGGER reject_artifact BEFORE INSERT ON run_artifacts " +
		"WHEN NEW.node_id = 'write' BEGIN SELECT RAISE(ABORT, 'artifact write rejected'); END").Error; err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "artifact-write-fail", Name: "Artifact write", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{
			ID: "write", Name: "Write", Type: "script", ScriptLanguage: "shell",
			Script: "printf 'ready\\n' > out.md", MaxAttempts: 1,
			ArtifactOutputs: []domain.ArtifactContract{{Name: "out", Path: "out.md", Required: true}},
		}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 2))
	run, err := e.Start(wf.ID, "go")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitRun(t, s, run.ID, 5*time.Second, func(r domain.Run) bool { return r.Status == "failed" })
	node := finished.Nodes["write"]
	if node.Status != "failed" || !strings.Contains(node.Error, "artifact write rejected") {
		t.Fatalf("node did not fail with the write error: %#v", node)
	}
	page, err := s.QueryRunTelemetry(run.ID, "write", "", 0, 0, false, false, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Name == "artifact.published" {
			t.Fatalf("unsaved artifact was reported as published: %+v", event)
		}
	}
}
