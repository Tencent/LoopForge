package engine

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/verifysidecar"
)

func sidecarRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "-q")
	run("config", "user.email", "sidecar@example.com")
	run("config", "user.name", "sidecar")
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# acceptance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")
	return dir
}

// TestVerifySidecarArtifactBindsApproval is the P5 wiring: a script node calls
// the same-host sidecar, its JSON answer becomes a hash-carrying artifact, and
// the human gate approval binds to that artifact (P2).
func TestVerifySidecarArtifactBindsApproval(t *testing.T) {
	sidecarRepoDir := sidecarRepo(t)
	sidecar := httptest.NewServer(verifysidecar.New(sidecarRepoDir).Handler())
	defer sidecar.Close()

	remote := sidecarRepoDir
	script := fmt.Sprintf(`curl -sS -X POST -H 'Content-Type: application/json' -d '{"repo":"%s","ref":"HEAD","spec":"spec.md"}' %s/verify > verify.json`, remote, sidecar.URL)
	wf := domain.WorkflowDefinition{
		ID: "verify-sidecar-sop", Name: "Verify sidecar SOP", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: script, MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "verify", Path: "verify.json", MediaType: "application/json", Required: true}}},
			{ID: "gate", Name: "Gate", Type: "approval"},
		},
		Edges: []domain.WorkflowEdge{{ID: "verify-gate", Source: "verify", Target: "gate", When: "success"}},
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "waiting")

	stored, _ := s.Run(run.ID)
	var artifact *domain.Artifact
	for i := range stored.Artifacts {
		if stored.Artifacts[i].Name == "verify" {
			artifact = &stored.Artifacts[i]
		}
	}
	if artifact == nil {
		t.Fatalf("verify artifact missing: %+v", stored.Artifacts)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(artifact.Content), &answer); err != nil {
		t.Fatalf("verify artifact is not JSON: %v\n%s", err, artifact.Content)
	}
	for _, key := range []string{"treeHash", "specHash", "toolchainFp", "buildFp", "acceptance"} {
		if _, ok := answer[key]; !ok {
			t.Fatalf("sidecar answer missing %q: %s", key, artifact.Content)
		}
	}

	staleErr := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: "ffff",
	})
	if staleErr == nil {
		t.Fatal("approval bound to a wrong artifact hash was accepted")
	}
	if err := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: artifact.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Run(run.ID)
	if final.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("gate status = %q, want succeeded", final.Nodes["gate"].Status)
	}
}
