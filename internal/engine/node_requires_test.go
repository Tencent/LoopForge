package engine

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func requiresWorkflow(id, requirement string) domain.WorkflowDefinition {
	return domain.WorkflowDefinition{
		ID: id, Name: id, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "produce", Name: "Produce", Type: "script", ScriptLanguage: "shell",
				Script: `printf '%s' '{"verdict":"PASS","diffHash":"d1"}' > verify.json`, MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "verify", Path: "verify.json", MediaType: "application/json", Required: true}}},
			{ID: "consume", Name: "Consume", Type: "script", ScriptLanguage: "shell",
				Script: "printf ran > ran.txt", MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "ran", Path: "ran.txt", Required: true}},
				Requires:        []string{requirement}},
		},
		Edges: []domain.WorkflowEdge{{ID: "produce-consume", Source: "produce", Target: "consume", When: "success"}},
	}
}

func TestNodeRequiresArtifactHashMismatchStopsNode(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := requiresWorkflow("requires-mismatch", "artifact:verify@deadbeef")
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	finished, _ := s.Run(run.ID)
	consume := finished.Nodes["consume"]
	if consume == nil || consume.Status != "failed" {
		t.Fatalf("consume = %+v, want failed", consume)
	}
	var actual string
	for _, artifact := range finished.Artifacts {
		if artifact.Name == "verify" {
			actual = artifact.SHA256
		}
		if artifact.Name == "ran" {
			t.Fatalf("required node executed and produced an artifact: %+v", artifact)
		}
	}
	if actual == "" {
		t.Fatalf("produce did not publish the verify artifact: %+v", finished.Artifacts)
	}
	if !strings.Contains(consume.Error, "deadbeef") || !strings.Contains(consume.Error, actual) {
		t.Fatalf("error %q must name expected and actual hash %s", consume.Error, actual)
	}
}

func TestNodeRequiresMatchingArtifactHashRuns(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum := sha256.Sum256([]byte(`{"verdict":"PASS","diffHash":"d1"}`))
	wf := requiresWorkflow("requires-match", fmt.Sprintf("artifact:verify@%x", sum))
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "succeeded")
	finished, _ := s.Run(run.ID)
	if finished.Nodes["consume"] == nil || finished.Nodes["consume"].Status != "succeeded" {
		t.Fatalf("consume = %+v, want succeeded", finished.Nodes["consume"])
	}
}

func TestNodeRequiresVerdictMustMatch(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := requiresWorkflow("requires-verdict-reject", "verdict:FAIL@d1")
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	finished, _ := s.Run(run.ID)
	consume := finished.Nodes["consume"]
	if consume == nil || consume.Status != "failed" || !strings.Contains(consume.Error, "FAIL") || !strings.Contains(consume.Error, "PASS") {
		t.Fatalf("consume = %+v, want failed naming expected FAIL and actual PASS", consume)
	}
}

func TestNodeRequiresRejectsMalformedVerdictDocument(t *testing.T) {
	t.Parallel()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{
		ID: "requires-verdict-broken", Name: "requires-verdict-broken", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "produce", Name: "Produce", Type: "script", ScriptLanguage: "shell",
				Script: "printf 'not json' > verify.json", MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "verify", Path: "verify.json", MediaType: "application/json", Required: true}}},
			{ID: "consume", Name: "Consume", Type: "script", ScriptLanguage: "shell",
				Script: "printf ran > ran.txt", MaxAttempts: 1,
				Requires: []string{"verdict:PASS@d1"}},
		},
		Edges: []domain.WorkflowEdge{{ID: "produce-consume", Source: "produce", Target: "consume", When: "success"}},
	}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "failed")
	finished, _ := s.Run(run.ID)
	consume := finished.Nodes["consume"]
	if consume == nil || consume.Status != "failed" || !strings.Contains(consume.Error, "不是合法 JSON") {
		t.Fatalf("consume = %+v, want failed naming malformed JSON", consume)
	}
}

func TestWorkflowValidationRejectsMalformedRequires(t *testing.T) {
	for _, requirement := range []string{"verify@abc", "artifact:verify", "verdict:PASS", "artifact:@abc"} {
		wf := requiresWorkflow("requires-invalid-"+strings.NewReplacer(":", "-", "@", "-").Replace(requirement), requirement)
		if err := validate(wf); err == nil {
			t.Fatalf("validate accepted malformed requirement %q", requirement)
		}
	}
}
