package httpapi

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestValidateRunnerTaskOutputsRejectsEscapingPaths(t *testing.T) {
	for _, bad := range []string{"../x.md", "/etc/passwd", "a/../../x", "a\\..\\x", "."} {
		if msg := validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{Files: map[string]string{bad: "x"}}); msg == "" {
			t.Fatalf("file path %q accepted", bad)
		}
	}
	if msg := validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{Patches: map[string]string{"../svc": "x"}}); msg == "" {
		t.Fatal("patch dir escaping the workspace accepted")
	}
	big := strings.Repeat("x", 1<<20+1)
	if msg := validateRunnerTaskOutputs(&domain.RunnerTaskOutputs{Files: map[string]string{"a.md": big}}); msg == "" {
		t.Fatal("oversized file accepted")
	}
	ok := &domain.RunnerTaskOutputs{Files: map[string]string{"svc/notes.md": "x"}, Patches: map[string]string{"": "p", "svc": "p"}}
	if msg := validateRunnerTaskOutputs(ok); msg != "" {
		t.Fatalf("valid outputs rejected: %s", msg)
	}
}
