package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowUnmarshalLiftsConsistentLegacyNodeSandbox(t *testing.T) {
	var workflow WorkflowDefinition
	raw := []byte(`{"id":"legacy","nodes":[{"id":"a","sandboxProfileId":"local"},{"id":"b","sandboxProfileId":"local"}]}`)
	if err := json.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.SandboxProfileID != "local" {
		t.Fatalf("sandboxProfileId = %q, want local", workflow.SandboxProfileID)
	}
	marshaled, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(marshaled), `"sandboxProfileId"`) != 1 {
		t.Fatalf("legacy node sandbox profile was retained: %s", marshaled)
	}
}

func TestWorkflowUnmarshalRejectsConflictingLegacyNodeSandboxes(t *testing.T) {
	var workflow WorkflowDefinition
	err := json.Unmarshal([]byte(`{"id":"legacy","nodes":[{"id":"a","sandboxProfileId":"local"},{"id":"b","sandboxProfileId":"container"}]}`), &workflow)
	if err == nil || !strings.Contains(err.Error(), "conflicting legacy node sandbox profiles") {
		t.Fatalf("expected explicit conflict, got %v", err)
	}
}

func TestRunUnmarshalLiftsConsistentLegacyNodeSandboxID(t *testing.T) {
	var run Run
	raw := []byte(`{"id":"run-1","nodes":{"a":{"nodeId":"a","sandboxId":"sandbox-1"},"b":{"nodeId":"b","sandboxId":"sandbox-1"}}}`)
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.SandboxID != "sandbox-1" {
		t.Fatalf("sandboxId = %q, want sandbox-1", run.SandboxID)
	}
}

func TestSandboxEnvironmentIsSerializedButRemovedExecutorEnvIsNot(t *testing.T) {
	raw, err := json.Marshal(struct {
		Executor ExecutorDefinition `json:"executor"`
		Sandbox  SandboxProfile     `json:"sandbox"`
	}{Executor: ExecutorDefinition{ID: "mock"}, Sandbox: SandboxProfile{ID: "local", Environment: map[string]string{"PATH": "/custom/bin"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"env"`) {
		t.Fatalf("removed executor env field was serialized: %s", raw)
	}
	if !strings.Contains(string(raw), `"environment":{"PATH":"/custom/bin"}`) {
		t.Fatalf("sandbox environment was not serialized: %s", raw)
	}
}
