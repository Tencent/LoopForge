package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestPrepareInvocationWorkspaceWritesInputsAndClearsGateResult(t *testing.T) {
	workspace := t.TempDir()
	gate := ".agentflow/gate-results/review/attempt-1.json"
	if err := os.MkdirAll(filepath.Join(workspace, filepath.Dir(gate)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, gate), []byte(`{"decision":"pass"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	inv := &domain.RunnerInvocation{
		GateResultPath: gate,
		Inputs:         []domain.RunnerInputFile{{Path: ".agentflow/inputs/plan/plan.md", Content: "v1"}},
	}
	if err := prepareInvocationWorkspace(workspace, inv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, gate)); !os.IsNotExist(err) {
		t.Fatalf("stale gate result survived: %v", err)
	}
	inv.Inputs[0].Content = "v2"
	if err := prepareInvocationWorkspace(workspace, inv); err != nil {
		t.Fatalf("rewriting a read-only input: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(workspace, ".agentflow/inputs/plan/plan.md"))
	if string(got) != "v2" {
		t.Fatalf("input = %q", got)
	}
	for _, bad := range []string{"../escape.md", ".agentflow/other/x.md", "/etc/passwd"} {
		err := prepareInvocationWorkspace(workspace, &domain.RunnerInvocation{Inputs: []domain.RunnerInputFile{{Path: bad}}})
		if err == nil {
			t.Fatalf("input path %q accepted", bad)
		}
	}
}

func TestCollectTaskOutputsReportsOnlyRegularFilesInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxArtifactUploadBytes+1)
	if err := os.WriteFile(filepath.Join(workspace, "big.md"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "nested", "deep", "misplaced.md"), []byte("found"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := collectTaskOutputs(workspace, &domain.RunnerInvocation{
		ReportOutputs:  true,
		RecoverOutputs: true,
		OutputFiles:    []string{"report.md", "link.md", "big.md", "missing.md", "misplaced.md", "../outside.md"},
	})
	if out == nil || out.Files["report.md"] != "ok" {
		t.Fatalf("outputs = %+v", out)
	}
	if _, ok := out.Files["link.md"]; ok {
		t.Fatal("symlink target was uploaded")
	}
	if len(out.TooLarge) != 1 || out.TooLarge[0] != "big.md" {
		t.Fatalf("tooLarge = %v", out.TooLarge)
	}
	if out.Files["misplaced.md"] != "found" || len(out.Recovered) != 1 || !strings.Contains(out.Recovered[0], "nested/deep/misplaced.md") {
		t.Fatalf("recovery = %v, files = %v", out.Recovered, out.Files)
	}
	if out.Facts == nil {
		t.Fatal("workspace facts missing")
	}
	if collectTaskOutputs(workspace, &domain.RunnerInvocation{}) != nil {
		t.Fatal("outputs reported without ReportOutputs")
	}
}
