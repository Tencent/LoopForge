package mcpserver

import (
	"strings"
	"testing"

	"agentflow/internal/application"
)

// agents_save is how the SOP assistant writes roles; its tool description must
// name the gate modes and the diff_coverage settings, or the assistant can only
// guess mode=command.
func TestAgentsSaveDescribesDiffCoverageGates(t *testing.T) {
	var op application.Operation
	for _, candidate := range application.PublicOperations() {
		if candidate.Name == "agents_save" {
			op = candidate
		}
	}
	if op.Name == "" {
		t.Fatal("agents_save is missing")
	}
	for _, want := range []string{"mode=command", "mode=diff_coverage", "mode=manual", "executionScope=repositories", "reportPath", "go_coverprofile|lcov|cobertura", "thresholdPct", "minChangedLines", "baseBranch", "include/exclude"} {
		if !strings.Contains(op.Description, want) {
			t.Fatalf("agents_save description must mention %q: %s", want, op.Description)
		}
	}
	valid := map[string]any{"body": map[string]any{"id": "dev", "name": "Dev", "verification": []any{
		map[string]any{"id": "cov", "name": "覆盖率", "mode": "diff_coverage", "commands": []any{"go test -coverprofile=coverage.out ./..."}, "executionScope": "repositories",
			"coverage": map[string]any{"reportPath": "coverage.out", "reportFormat": "go_coverprofile", "thresholdPct": float64(80), "minChangedLines": float64(5)}},
		map[string]any{"id": "vet", "name": "vet", "mode": "command", "commands": []any{"go vet ./..."}, "executionScope": "run"},
	}}}
	if err := ValidateToolInput(op, valid); err != nil {
		t.Fatalf("a valid diff_coverage gate was rejected by the tool contract: %v", err)
	}
}
