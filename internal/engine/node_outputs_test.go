package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestReportedOutputsValidateRepositoryScopedContracts(t *testing.T) {
	run := domain.Run{ID: "run-1", Repositories: []domain.RepositoryContext{
		{RepositoryID: "a", Name: "a", WorkspaceDir: "svc-a"},
		{RepositoryID: "b", Name: "b", WorkspaceDir: "svc-b"},
	}}
	node := domain.WorkflowNode{ID: "impl", ExecutionScope: domain.ExecutionScopeRepositories, ArtifactOutputs: []domain.ArtifactContract{{Name: "notes", Path: "notes.md", Required: true}}}
	if got := artifactOutputFiles(run, node); strings.Join(got, ",") != "svc-a/notes.md,svc-b/notes.md" {
		t.Fatalf("output files = %v", got)
	}
	reported := newReportedOutputs(nil)
	reported.absorb(&domain.RunnerTaskOutputs{Files: map[string]string{"svc-a/notes.md": "A"}})
	if err := validateArtifactOutputs(reported, run, node); err == nil || !strings.Contains(err.Error(), "svc-b") {
		t.Fatalf("missing svc-b output not reported: %v", err)
	}
	reported.absorb(&domain.RunnerTaskOutputs{Files: map[string]string{"svc-b/notes.md": "B"}, Patches: map[string]string{"svc-b": "repository-patch summary\nM\tmain.go"}})
	if err := validateArtifactOutputs(reported, run, node); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectArtifactsMode(reported, run, node, "", false)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]string{}
	for _, artifact := range artifacts {
		byKey[artifact.RepositoryDir+"/"+artifact.Name] = artifact.Content
	}
	if byKey["svc-a/notes"] != "A" || byKey["svc-b/notes"] != "B" || !strings.Contains(byKey["svc-b/repository-patch"], "main.go") {
		t.Fatalf("artifacts = %v", byKey)
	}
	if _, ok := byKey["svc-a/repository-patch"]; ok {
		t.Fatal("unchanged checkout produced a patch")
	}

	reported.reset()
	reported.absorb(&domain.RunnerTaskOutputs{TooLarge: []string{"svc-a/notes.md"}, Files: map[string]string{"svc-b/notes.md": "B"}})
	if err := validateArtifactOutputs(reported, run, node); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized output not reported as such: %v", err)
	}
}

func TestReadGateResultFromReportedOutputs(t *testing.T) {
	path := gateResultPath("review", 1)
	reported := newReportedOutputs(nil)
	if result, err := readGateResultFrom(reported, path); err != nil || result != nil {
		t.Fatalf("absent gate result = %+v, %v", result, err)
	}
	reported.absorb(&domain.RunnerTaskOutputs{Files: map[string]string{path: `{"decision":"pass","reason":"ok"}`}})
	result, err := readGateResultFrom(reported, path)
	if err != nil || result == nil || result.Decision != "pass" {
		t.Fatalf("gate result = %+v, %v", result, err)
	}
}

func TestRunnerArtifactInputsMatchWorkspaceMount(t *testing.T) {
	wf := domain.WorkflowDefinition{Edges: []domain.WorkflowEdge{{Source: "impl", Target: "review"}}}
	run := domain.Run{Artifacts: []domain.Artifact{
		{NodeID: "impl", Attempt: 1, Name: "notes", MediaType: "text/markdown", RepositoryDir: "svc-a", Content: "A"},
		{NodeID: "impl", Attempt: 1, Name: "repository-patch", RepositoryDir: "svc-a", Content: "repository-patch summary\nM\tmain.go"},
	}}
	manifest, files := runnerArtifactInputs(wf, domain.WorkflowNode{ID: "review"}, run)
	if len(files) != 1 || files[0].Path != ".agentflow/inputs/impl/svc-a/notes.md" || files[0].Content != "A" {
		t.Fatalf("files = %+v", files)
	}
	if !strings.Contains(manifest, files[0].Path) || !strings.Contains(manifest, "source of truth") {
		t.Fatalf("manifest = %s", manifest)
	}
	dir := t.TempDir()
	local, err := materializeInputs(dir, wf, domain.WorkflowNode{ID: "review"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(local, files[0].Path) {
		t.Fatalf("local manifest points elsewhere than the mounted file: %s", local)
	}
}
