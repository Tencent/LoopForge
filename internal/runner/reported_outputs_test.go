package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
)

func startReportingRunner(t *testing.T, s *repo.Store, serverURL string) domain.Runner {
	t.Helper()
	daemon, err := New(Config{
		ServerURL:            serverURL,
		BootstrapToken:       "boot-secret",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		Name:                 "reporting-runner",
		MaxConcurrency:       1,
		HeartbeatInterval:    time.Second,
		LongPollTimeout:      2 * time.Second,
		DefaultWorkspaceRoot: filepath.Join(t.TempDir(), "workspace"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = daemon.Run(ctx) }()
	waitFor(t, 5*time.Second, "runner registration", func() bool { return len(s.Runners()) == 1 })
	runner := s.Runners()[0]
	if !runner.SupportsReportedOutputs() {
		t.Fatalf("runner version %q does not report outputs", runner.RunnerVersion)
	}
	runner.OwnerUserID = 42
	if err := s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultRunner(42, runner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return runner
}

func runToEnd(t *testing.T, s *repo.Store, e *engine.Engine, wf domain.WorkflowDefinition, rc domain.RepositoryContext) domain.Run {
	t.Helper()
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartWithExecutionContext(wf.ID, "", rc, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, wf.ID+" to finish", func() bool {
		current, ok := s.Run(run.ID)
		return ok && (current.Status == "succeeded" || current.Status == "failed")
	})
	final, _ := s.Run(run.ID)
	return final
}

func latestArtifact(run domain.Run, nodeID, name string) (domain.Artifact, bool) {
	var found domain.Artifact
	ok := false
	for _, artifact := range run.Artifacts {
		if artifact.NodeID == nodeID && artifact.Name == name {
			found, ok = artifact, true
		}
	}
	return found, ok
}

// 执行机器上报产物后，控制面直接用上报内容登记和交接，不再往自己的工作区副本写文件。
func TestRunnerReportedOutputsDriveArtifactsAndHandoff(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	remote := bareRemoteWithBaseline(t)
	startReportingRunner(t, s, server.URL)
	wf := domain.WorkflowDefinition{
		ID: "runner-reported-outputs", Name: "Runner reported outputs", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "plan", Name: "方案", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
				Script:          "printf 'plan body\\n' > plan.md && for f in README.md */README.md; do if [ -f \"$f\" ]; then printf 'changed\\n' >> \"$f\"; fi; done",
				ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "plan.md", MediaType: "text/markdown", Required: true}}},
			{ID: "review", Name: "评审", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
				Script:          "cat .agentflow/inputs/plan/plan.md > review.md && test -f human-context.md || true",
				ArtifactOutputs: []domain.ArtifactContract{{Name: "review", Path: "review.md", MediaType: "text/markdown", Required: true}}},
		},
		Edges: []domain.WorkflowEdge{{ID: "plan-review", Source: "plan", Target: "review", When: "success"}},
	}
	final := runToEnd(t, s, e, wf, domain.RepositoryContext{RepositoryID: "repo", Name: "svc", CloneURL: remote, BaseBranch: "master", WorkBranch: "fix/reported"})
	if final.Status != "succeeded" {
		t.Fatalf("status = %s, plan error = %q, review error = %q", final.Status, final.Nodes["plan"].Error, final.Nodes["review"].Error)
	}
	plan, ok := latestArtifact(final, "plan", "plan")
	if !ok || plan.Content != "plan body\n" {
		t.Fatalf("plan artifact = %+v, %v", plan, ok)
	}
	review, ok := latestArtifact(final, "review", "review")
	if !ok || review.Content != "plan body\n" {
		t.Fatalf("review did not read the upstream input on the runner: %+v, %v", review, ok)
	}
	patch, ok := latestArtifact(final, "plan", "repository-patch")
	if !ok || !strings.Contains(patch.Content, "README.md") {
		t.Fatalf("repository-patch must come from the runner checkout: %+v, %v", patch, ok)
	}
	if staging := e.RunWorkspace(final.ID); staging != "" {
		for _, rel := range []string{"plan.md", "review.md", filepath.Join(".agentflow", "inputs")} {
			if _, err := os.Stat(filepath.Join(staging, rel)); err == nil {
				t.Fatalf("control-plane workspace copy received %s", rel)
			}
		}
	}
}

// 节点失败时，执行机器已经写出的产物也要登记，方便排查。
func TestRunnerReportedOutputsKeepFailedNodeArtifacts(t *testing.T) {
	server, s, e := newDaemonTestServer(t)
	startReportingRunner(t, s, server.URL)
	wf := domain.WorkflowDefinition{
		ID: "runner-reported-failure", Name: "Runner reported failure", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "diagnose", Name: "诊断", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
				Script:          "printf 'what went wrong\\n' > diagnosis.md && exit 3",
				ArtifactOutputs: []domain.ArtifactContract{{Name: "diagnosis", Path: "diagnosis.md", MediaType: "text/markdown", Required: true}}},
		},
	}
	final := runToEnd(t, s, e, wf, domain.RepositoryContext{})
	if final.Status != "failed" {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	diagnosis, ok := latestArtifact(final, "diagnose", "diagnosis")
	if !ok || diagnosis.Content != "what went wrong\n" {
		t.Fatalf("failed node lost its artifact: %+v, %v", diagnosis, ok)
	}
}
