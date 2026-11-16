package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestOpenRemoteSandboxErrorBranches(t *testing.T) {
	if _, err := OpenRemoteSandbox(context.Background(), "id", "", domain.SandboxProfile{}, SandboxRequirements{}, SandboxResources{}); err == nil {
		t.Fatal("empty workspace accepted")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRemoteSandbox(context.Background(), "id", blocked, domain.SandboxProfile{}, SandboxRequirements{}, SandboxResources{}); err == nil {
		t.Fatal("file workspace accepted")
	}
	if _, err := OpenRemoteSandbox(context.Background(), "id", t.TempDir(), domain.SandboxProfile{Driver: "unknown"}, SandboxRequirements{}, SandboxResources{}); err == nil {
		t.Fatal("unknown driver accepted")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := OpenRemoteSandbox(context.Background(), "id", t.TempDir(), domain.SandboxProfile{Driver: "bwrap"}, SandboxRequirements{}, SandboxResources{}); err == nil {
		t.Fatal("missing bwrap accepted")
	}
	if _, err := OpenRemoteSandbox(context.Background(), "id", t.TempDir(), domain.SandboxProfile{Driver: "seatbelt"}, SandboxRequirements{}, SandboxResources{}); err == nil {
		t.Fatal("missing seatbelt accepted")
	}
	resources := SandboxResources{Environment: domain.EnvironmentDefinition{ID: "broken", InstallScript: "exit 9"}}
	if _, err := OpenRemoteSandbox(context.Background(), "id", t.TempDir(), domain.SandboxProfile{EnvironmentID: "broken"}, SandboxRequirements{}, resources); err == nil {
		t.Fatal("failing environment install accepted")
	}
}

func TestWorkspacePreparationReusableReceipts(t *testing.T) {
	node := domain.WorkflowNode{ID: "setup", RunOnWorkspaceRebuild: true}
	workflow := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{node}}
	state := WorkspacePreparationStatus{
		Status:      "ready",
		Environment: "epoch",
		Steps: []WorkspacePreparationStep{{
			NodeID:      node.ID,
			Status:      "ready",
			Fingerprint: preparationFingerprint(node, nil, nil),
		}},
	}
	if !WorkspacePreparationReusable(state, workflow, nil, "epoch") {
		t.Fatal("matching receipt not reusable")
	}
	state.Steps[0].Status = "failed"
	if WorkspacePreparationReusable(state, workflow, nil, "epoch") {
		t.Fatal("failed receipt reusable")
	}
	state.Steps[0].Status = "ready"
	state.Steps[0].Fingerprint = "stale"
	if WorkspacePreparationReusable(state, workflow, nil, "epoch") {
		t.Fatal("stale receipt reusable")
	}
	cyclic := domain.WorkflowDefinition{
		Nodes: []domain.WorkflowNode{{ID: "a", RunOnWorkspaceRebuild: true}, {ID: "b"}},
		Edges: []domain.WorkflowEdge{{Source: "a", Target: "b"}, {Source: "b", Target: "a"}},
	}
	if WorkspacePreparationReusable(WorkspacePreparationStatus{Status: "ready", Environment: "epoch"}, cyclic, nil, "epoch") {
		t.Fatal("cyclic preparation reusable")
	}
}

func TestSandboxIsolationEnvironmentConcreteTypes(t *testing.T) {
	process := &processSandbox{profile: domain.SandboxProfile{Environment: map[string]string{"P": "1"}}}
	local := &localSandbox{environment: map[string]string{"L": "1"}}
	docker := &dockerSandbox{profile: domain.SandboxProfile{Environment: map[string]string{"D": "1"}}}
	if process.IsolationEnv()["P"] != "1" || local.IsolationEnv()["L"] != "1" || docker.IsolationEnv()["D"] != "1" {
		t.Fatal("concrete isolation environment missing")
	}
	if WithSandboxCredentials(nil, RunCredentials{}) != nil {
		t.Fatal("nil sandbox wrapped")
	}
	if restored, err := RestoreMissingWorkspaceArtifacts(t.TempDir(), nil); err != nil || restored != nil {
		t.Fatalf("empty artifacts=%v err=%v", restored, err)
	}
	if _, err := RestoreMissingWorkspaceArtifacts(filepath.Join(t.TempDir(), "missing"), []domain.WorkspaceArtifactFile{{
		Path: "file.txt", Content: "x", SHA256: WorkspaceContentSHA("x"),
	}}); err == nil {
		t.Fatal("missing artifact root accepted")
	}
	artifactRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactRoot, "parent"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreMissingWorkspaceArtifacts(artifactRoot, []domain.WorkspaceArtifactFile{{
		Path: "parent/file.txt", Content: "x", SHA256: WorkspaceContentSHA("x"),
	}})
	if err == nil || len(restored) != 0 {
		t.Fatalf("unsafe artifact parent restored=%v err=%v", restored, err)
	}
	command := CommandSpec{Provider: "other"}
	if got := command.WithPrivateHomeDir(" "); got.PrivateHome || len(got.environment) != 0 {
		t.Fatalf("empty private home changed command=%+v", got)
	}
	if got := command.WithMCPStartupWait(time.Second); len(got.environment) != 0 {
		t.Fatalf("non-codebuddy wait changed command=%+v", got)
	}
	if _, err := OpenRemoteSandbox(context.Background(), "id", t.TempDir(), domain.SandboxProfile{EnvironmentID: "missing"}, SandboxRequirements{}, SandboxResources{}); err == nil {
		t.Fatal("missing environment accepted")
	}
	if got := safeID(strings.Repeat("a", 60)); len(got) != 48 {
		t.Fatalf("long safe id length=%d", len(got))
	}
}

func TestWorkspacePreparationTrivial(t *testing.T) {
	if !WorkspacePreparationTrivial(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "agent", Type: "agent"}}}) {
		t.Fatal("workflow without preparation steps must be trivial")
	}
	if WorkspacePreparationTrivial(domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "setup", Type: "script", RunOnWorkspaceRebuild: true}}}) {
		t.Fatal("workflow with a preparation step is not trivial")
	}
}
