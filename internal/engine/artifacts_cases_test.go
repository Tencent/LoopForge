package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestArtifactMaterializationAndRecoveryBranches(t *testing.T) {
	workspace := t.TempDir()
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "source", Target: "target"}}}
	node := domain.WorkflowNode{ID: "target"}
	run := domain.Run{Artifacts: []domain.Artifact{{
		NodeID: "source", Name: "notes", MediaType: "text/markdown", Content: "hello", SHA256: "sha",
	}}}
	manifest, err := materializeInputs(workspace, wf, node, run)
	if err != nil || !strings.Contains(manifest, ".agentflow/inputs/source/notes.md") {
		t.Fatalf("manifest=%q err=%v", manifest, err)
	}
	input := filepath.Join(workspace, ".agentflow", "inputs", "source", "notes.md")
	if data, err := os.ReadFile(input); err != nil || string(data) != "hello" {
		t.Fatalf("input=%q err=%v", data, err)
	}
	if err := os.Chmod(input, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeInputs(workspace, wf, node, run); err != nil {
		t.Fatal(err)
	}

	badWorkspace := t.TempDir()
	badDir := filepath.Join(badWorkspace, ".agentflow", "inputs", "source")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(badWorkspace, "elsewhere"), filepath.Join(badDir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeInputs(badWorkspace, wf, node, run); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink err=%v", err)
	}
	patchRun := domain.Run{Artifacts: []domain.Artifact{{
		NodeID: "source", Name: "repository-patch", MediaType: "text/plain", Content: "summary only", SHA256: "patch-sha",
	}}}
	if patchManifest, err := materializeInputs(t.TempDir(), wf, node, patchRun); err != nil || !strings.Contains(patchManifest, "No full diff is stored") {
		t.Fatalf("patch manifest=%q err=%v", patchManifest, err)
	}
	gitWorkspace := t.TempDir()
	if out, err := exec.Command("git", "-C", gitWorkspace, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	patchRun.Artifacts[0].Content = "diff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new\n"
	if patchManifest, err := materializeInputs(gitWorkspace, wf, node, patchRun); err != nil || !strings.Contains(patchManifest, "Applied a legacy unified diff") {
		t.Fatalf("applied patch manifest=%q err=%v", patchManifest, err)
	}
	if data, err := os.ReadFile(filepath.Join(gitWorkspace, "new.txt")); err != nil || string(data) != "new\n" {
		t.Fatalf("patched file=%q err=%v", data, err)
	}

	recoveryWorkspace := t.TempDir()
	nested := filepath.Join(recoveryWorkspace, "nested", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deliverable.md"), []byte("recovered"), 0o644); err != nil {
		t.Fatal(err)
	}
	recoveryNode := domain.WorkflowNode{ArtifactOutputs: []domain.ArtifactContract{{Name: "deliverable", Path: "out/deliverable.md", Required: true}}}
	recovered := recoverMisplacedArtifacts(recoveryWorkspace, "", recoveryNode)
	if len(recovered) != 1 {
		t.Fatalf("recovered=%v", recovered)
	}
	if data, err := os.ReadFile(filepath.Join(recoveryWorkspace, "out", "deliverable.md")); err != nil || string(data) != "recovered" {
		t.Fatalf("recovered file=%q err=%v", data, err)
	}
	recoveryNode.ExecutionScope = domain.ExecutionScopeRepositories
	if got := recoverMisplacedArtifacts(recoveryWorkspace, "", recoveryNode); got != nil {
		t.Fatalf("repository recovery=%v", got)
	}
	if findNamedFile("", "x", 2) != "" || findNamedFile(recoveryWorkspace, "", 2) != "" {
		t.Fatal("empty file search")
	}
	removeUntrackedHostLeak("", filepath.Join(recoveryWorkspace, "out", "deliverable.md"))
	removeUntrackedHostLeak(recoveryWorkspace, "")
	removeUntrackedHostLeak(recoveryWorkspace, filepath.Join(t.TempDir(), "outside"))
}

func TestArtifactCollectionErrorsAndFreeze(t *testing.T) {
	workspace := t.TempDir()
	node := domain.WorkflowNode{
		ID:             "node",
		ExecutionScope: domain.ExecutionScopeRepositories,
		ArtifactOutputs: []domain.ArtifactContract{{
			Name: "required", Path: "required.md", Required: true,
		}},
	}
	run := domain.Run{ID: "artifact-run", Nodes: map[string]*domain.NodeRun{"node": {Attempt: 2}}}
	if err := validateArtifactContracts(workspace, run, node); err == nil {
		t.Fatal("repository-scoped contract without repositories accepted")
	}
	if _, err := collectArtifacts(workspace, run, node, "output"); err == nil {
		t.Fatal("repository-scoped collection without repositories accepted")
	}
	if artifacts, err := collectArtifactsBestEffort(workspace, run, node, "output"); err != nil || len(artifacts) != 0 {
		t.Fatalf("best effort=%v err=%v", artifacts, err)
	}

	node.ExecutionScope = ""
	node.ArtifactOutputs[0].Path = "../escape"
	if _, err := collectArtifacts(workspace, run, node, "output"); err == nil {
		t.Fatal("invalid artifact path accepted")
	}
	if artifacts, err := collectArtifactsBestEffort(workspace, run, node, "output"); err != nil || len(artifacts) != 0 {
		t.Fatalf("invalid best effort=%v err=%v", artifacts, err)
	}

	node.ArtifactOutputs[0].Path = "huge.md"
	if err := os.WriteFile(filepath.Join(workspace, "huge.md"), make([]byte, maxArtifactBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectArtifacts(workspace, run, node, "output"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("large artifact err=%v", err)
	}
	if artifacts, err := collectArtifactsBestEffort(workspace, run, node, "output"); err != nil || len(artifacts) != 0 {
		t.Fatalf("large best effort=%v err=%v", artifacts, err)
	}

	e, st := cvEEngine(t)
	stored := domain.Run{ID: "freeze-run", Nodes: map[string]*domain.NodeRun{"node": {Attempt: 1}}}
	if err := st.PutRun(stored); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "freeze.md"), []byte("frozen"), 0o644); err != nil {
		t.Fatal(err)
	}
	freezeNode := domain.WorkflowNode{ID: "node", ArtifactOutputs: []domain.ArtifactContract{{Name: "freeze", Path: "freeze.md", Required: true}}}
	e.freezeAvailableArtifacts(stored.ID, freezeNode, workspace, nil, "result body")
	updated, ok := st.RunState(stored.ID)
	if !ok || len(updated.Artifacts) == 0 || updated.Artifacts[0].Name != "freeze" {
		t.Fatalf("frozen artifacts=%v", updated.Artifacts)
	}
	reported := newReportedOutputs(nil)
	reported.absorb(&domain.RunnerTaskOutputs{
		Files:     map[string]string{"reported.md": "runner output"},
		Recovered: []string{"reported.md"},
	})
	reportedNode := domain.WorkflowNode{ID: "node", ArtifactOutputs: []domain.ArtifactContract{{Name: "reported", Path: "reported.md", Required: true}}}
	e.freezeAvailableArtifacts(stored.ID, reportedNode, workspace, reported, "reported body")
	updated, _ = st.RunState(stored.ID)
	if len(updated.Artifacts) < 2 {
		t.Fatalf("reported artifacts=%v", updated.Artifacts)
	}
	e.freezeAvailableArtifacts("missing-run", freezeNode, workspace, nil, "ignored")
}
