package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/evolution"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestEvolutionReplayRejectsExternalWorkflows(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := New(st, runtime.NewManager(t.TempDir()))
	agent := domain.AgentDefinition{ID: "replay-safe", Name: "Replay", ProjectID: "welcome", ExecutorID: "mock"}
	wf := domain.WorkflowDefinition{ID: "replay-safety", Name: "Replay", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "work", Type: "agent", AgentID: agent.ID}}}
	for _, tc := range []struct {
		name   string
		change func(*domain.WorkflowDefinition, *domain.AgentDefinition, *domain.Run)
		want   string
	}{
		{"repository", func(_ *domain.WorkflowDefinition, _ *domain.AgentDefinition, r *domain.Run) {
			r.Repository.RepositoryID = "bound-repo"
		}, "代码仓库"},
		{"current script", func(w *domain.WorkflowDefinition, _ *domain.AgentDefinition, _ *domain.Run) {
			w.Nodes[0].Type = "script"
			w.Nodes[0].Script = "exit 0"
		}, "script"},
		{"nested SOP", func(w *domain.WorkflowDefinition, _ *domain.AgentDefinition, _ *domain.Run) { w.Nodes[0].Type = "sop" }, "sop"},
		{"external credential", func(w *domain.WorkflowDefinition, _ *domain.AgentDefinition, _ *domain.Run) {
			w.Nodes[0].CredentialEnv = map[string]string{"TOKEN": "deployment"}
		}, "外部凭据"},
		{"hook", func(_ *domain.WorkflowDefinition, a *domain.AgentDefinition, _ *domain.Run) {
			a.Hooks = []domain.AgentHook{{ID: "hook", Name: "Hook", Event: "Stop", Language: "shell", Script: "exit 0"}}
		}, "生命周期脚本"},
		{"MR verification", func(_ *domain.WorkflowDefinition, a *domain.AgentDefinition, _ *domain.Run) {
			a.Verification = []domain.VerificationGate{{ID: "mr", Mode: "mr"}}
		}, "MR 验证"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, a := wf, agent
			w.ID = wf.ID + "-" + strings.ReplaceAll(tc.name, " ", "-")
			w.Nodes = append([]domain.WorkflowNode(nil), wf.Nodes...)
			source := domain.Run{WorkflowID: w.ID, WorkflowSnapshot: wf}
			tc.change(&w, &a, &source)
			if err := st.PutAgent(a); err != nil {
				t.Fatal(err)
			}
			if err := st.PutWorkflow(w); err != nil {
				t.Fatal(err)
			}
			if err := e.ValidateEvolutionReplay(source); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if _, _, err := e.StartEvolutionReplay(source, nil, domain.TelemetryContext{}, nil); err == nil {
				t.Fatal("unsupported source created a replay run")
			}
		})
	}
	if err := st.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	ctx := domain.TelemetryContext{ProjectID: "welcome", Purpose: domain.RunPurposeEvolutionReplay}
	if _, err := e.StartWithExecutionContext(wf.ID, "direct", domain.RepositoryContext{RepositoryID: "repo"}, ctx); err == nil {
		t.Fatal("direct replay start bypassed the repository restriction")
	}
	if err := e.ValidateEvolutionReplay(domain.Run{WorkflowID: wf.ID}); err != nil {
		t.Fatal(err)
	}
	// Local command verification gates without bound repos or external credentials are allowed in replay.
	cmdAgent := agent
	cmdAgent.ID = "replay-safe-cmd"
	cmdAgent.Verification = []domain.VerificationGate{{ID: "command", Mode: "command", Commands: []string{"exit 0"}, ExecutionScope: "run"}}
	cmdWF := wf
	cmdWF.ID = "replay-safety-cmd"
	cmdWF.Nodes = []domain.WorkflowNode{{ID: "work", Type: "agent", AgentID: cmdAgent.ID}}
	if err := st.PutAgent(cmdAgent); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkflow(cmdWF); err != nil {
		t.Fatal(err)
	}
	if err := e.ValidateEvolutionReplay(domain.Run{WorkflowID: cmdWF.ID}); err != nil {
		t.Fatalf("expected local command verification gate to be allowed in replay, got: %v", err)
	}
}

func TestEvolutionReplayKeepsOnlyModelCredentials(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{ID: 22, LoginName: "replay-owner", CodeBuddyAPIKey: "model-test", CodexAPIKey: "codex-test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveGitHTTPCredential(22, "", domain.GitHTTPCredentialSecret{GitHTTPCredential: domain.GitHTTPCredential{Host: "github.com", Platform: domain.GitPlatformGitHub}, Token: "git-test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "writer", Name: "Writer", Scope: "project", ProjectID: "welcome", Transport: "http", URL: "https://mcp.example.test", Headers: map[string]string{"Authorization": "test"}}); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	business := domain.Run{ExecutionContext: domain.TelemetryContext{ProjectID: "welcome", ResponsibleUserID: 22}}
	live := e.resolveRunCredentials(business)
	if len(live.GitHTTPCredentials) != 1 || len(live.MCPServers) != 1 {
		t.Fatal("business credentials changed")
	}
	replay := business
	replay.ExecutionContext.Purpose = domain.RunPurposeEvolutionReplay
	got := e.resolveRunCredentials(replay)
	if got.CodeBuddy.APIKey != "model-test" || got.Codex.APIKey != "codex-test" {
		t.Fatal("model credentials were removed")
	}
	if len(got.GitHTTPCredentials)+len(got.GitSSHKeys)+len(got.Extra)+len(got.MCPServers) != 0 || got.GitUserName != "" {
		t.Fatal("replay received external credentials")
	}
	if len(e.resolveRunScriptMCPAssets(replay).MCPServers) != 0 {
		t.Fatal("script MCP lookup bypassed replay filtering")
	}
	agent := applyReplayAgent(nil, domain.AgentDefinition{PlatformMCP: true, KnowledgeBase: true, MCPServers: []string{"writer"}}, replay)
	if agent.PlatformMCP || agent.KnowledgeBase || len(agent.MCPServers) != 0 {
		t.Fatal("replay role can still resolve MCPs")
	}
	assets := domain.AgentAssetBundle{MCPServers: live.MCPServers, MCPFiles: map[string]map[string][]byte{"writer": {"config": []byte("secret")}}}
	applyReplayAssets(nil, &assets, replay)
	if len(assets.MCPServers)+len(assets.MCPFiles) != 0 {
		t.Fatal("inline MCP configuration survived")
	}
	inv := &domain.RunnerInvocation{}
	bindRunnerRepositories(inv, replay, "owner", "owner@example.test")
	if inv.GitHTTPUserID != 0 || inv.GitSSHUserID != 0 || inv.Repository != nil || len(inv.Repositories) != 0 {
		t.Fatal("runner claim would rehydrate external credentials")
	}
}

func TestEvolutionCanaryRestrictsProjectAndPrivateAsset(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	a := domain.AgentDefinition{ID: "private-trial", Name: "Private", ProjectID: "welcome", Prompt: "live"}
	if err := st.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	p := domain.EvoProposal{ID: "private-trial", ProjectID: "welcome", AssetKind: "agent", AssetID: a.ID, Status: domain.EvoProposalCanary, BaseContent: a.Prompt, CandidateContent: "candidate", Canary: &domain.EvoCanary{Percent: 90, Status: domain.EvoCanaryRunning, StartedAt: time.Now().UTC()}}
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	wf := domain.WorkflowDefinition{ProjectID: "welcome", Nodes: []domain.WorkflowNode{{AgentID: a.ID}}}
	runID := "trial"
	for evolution.CanaryBucket(runID, p.ID) >= 90 {
		runID += "x"
	}
	if ids, _ := e.assignEvolutionCanaries(runID, wf, domain.TelemetryContext{ProjectID: "welcome"}); len(ids) != 1 {
		t.Fatal("own project did not receive the trial")
	}
	if ids, _ := e.assignEvolutionCanaries(runID, wf, domain.TelemetryContext{ProjectID: "other"}); len(ids) != 0 {
		t.Fatal("trial leaked into another project")
	}
	a.Shared = true
	if err := st.PutAgent(a); err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.assignEvolutionCanaries(runID, wf, domain.TelemetryContext{ProjectID: "welcome"}); len(ids) != 0 {
		t.Fatal("a newly shared asset kept assigning trial traffic")
	}
}

func TestEvolutionReplayRunnerDispatchHasNoExternalBindings(t *testing.T) {
	e, _ := cvEEngine(t)
	runner := cvERunner(t, e, "restricted-runner", domain.RunnerStatusOnline, "codex")
	run := cvERunnerRun("restricted-dispatch", runner.ID)
	run.ExecutionContext.ProjectID, run.ExecutionContext.Purpose = "welcome", domain.RunPurposeEvolutionReplay
	node := domain.WorkflowNode{ID: "work", Type: "agent"}
	sb := &runEnvironment{credentials: runtime.RunCredentials{Codex: runtime.ProviderCredential{APIKey: "model-test"}}}
	if _, err := e.dispatchRunnerTask(context.Background(), runner.ID, run, node, sb, "workspace", domain.ExecutorDefinition{Kind: "codex"}, domain.AgentDefinition{ID: "role"}, domain.AgentAssetBundle{}, "test", "inv", runtime.InvocationOptions{}, nil); err == nil || !strings.Contains(err.Error(), "本机登录") {
		t.Fatalf("replay used machine login: %v", err)
	}
	runner.Runtimes[0].AuthMode = domain.RuntimeAuthPersonal
	runner.RunnerVersion = "0.13.0"
	if err := e.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if _, err := e.dispatchRunnerTask(context.Background(), runner.ID, run, node, sb, "workspace", domain.ExecutorDefinition{Kind: "codex"}, domain.AgentDefinition{ID: "role"}, domain.AgentAssetBundle{}, "test", "inv", runtime.InvocationOptions{}, nil); err == nil || !strings.Contains(err.Error(), domain.RunnerVersionEvolutionReplay) {
		t.Fatalf("old runner must be rejected for evolution replay: %v", err)
	}
	runner.RunnerVersion = domain.RunnerVersionEvolutionReplay
	if err := e.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.dispatchRunnerTask(ctx, runner.ID, run, node, sb, "workspace", domain.ExecutorDefinition{Kind: "codex"}, domain.AgentDefinition{ID: "role"}, domain.AgentAssetBundle{}, "test", "inv", runtime.InvocationOptions{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatch: %v", err)
	}
	tasks := e.repo.RunnerTasks()
	if len(tasks) != 1 || tasks[0].Invocation == nil {
		t.Fatalf("unexpected queued tasks: %d", len(tasks))
	}
	inv := tasks[0].Invocation
	if !inv.DisableBrowser || inv.GitHTTPUserID != 0 || inv.GitSSHUserID != 0 || len(inv.MCPCallServers) != 0 || len(inv.GitHTTPCredentials) != 0 {
		t.Fatal("runner payload can acquire external bindings")
	}
}
