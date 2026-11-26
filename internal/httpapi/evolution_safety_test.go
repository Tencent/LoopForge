package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestEvolutionSharedAssetCannotStartOrPublish(t *testing.T) {
	env := newEvoTestEnv(t)
	p := env.seedPendingProposal(t, "shared-blocked")
	agent, _ := env.store.Agent(env.agent.ID)
	agent.Shared = true
	if err := env.store.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"projectId": "evo-p", "assetKind": "agent", "assetId": agent.ID}, http.StatusConflict)
	for _, body := range []map[string]any{{"mode": "canary"}, {"mode": "release"}, {"force": true}} {
		requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", body, http.StatusConflict)
	}
	requestJSON[map[string]any](t, env.router, http.MethodPut, "/api/evolution/policies/agent/"+agent.ID, map[string]any{"level": 2}, http.StatusConflict)
	stored, _ := env.store.EvoProposal(p.ID)
	live, _ := env.store.Agent(agent.ID)
	if stored.Status != domain.EvoProposalPending || live.Prompt != p.BaseContent {
		t.Fatal("blocked mutation changed live state")
	}
}

func TestEvolutionL3RequiresDeploymentOptIn(t *testing.T) {
	t.Setenv("AGENTFLOW_EVOLUTION_AUTO_ENABLED", "false")
	env := newEvoTestEnv(t)
	requestJSON[map[string]any](t, env.router, http.MethodPut, "/api/evolution/policies/agent/"+env.agent.ID, map[string]any{"level": 3}, http.StatusConflict)
	// A previously stored L3 policy cannot bypass the deployment switch.
	if err := env.store.PutEvoPolicy(domain.EvoPolicy{AssetKind: "agent", AssetID: env.agent.ID, ProjectID: "evo-p", Level: 3}); err != nil {
		t.Fatal(err)
	}
	p := env.seedPendingProposal(t, "auto-disabled")
	asset, _ := env.h.loadAsset(p.AssetKind, p.AssetID)
	if env.h.autoEvolution(asset) {
		t.Fatal("L3 default enabled")
	}
	if _, err := env.h.startCanary(p, asset, 0, 50, 5, "", true); err == nil {
		t.Fatal("automatic trial bypassed opt-in")
	}
	if _, err := env.h.startCanary(p, asset, env.user.ID, 50, 5, "", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		env.seedArmRun(t, "disabled-canary-"+string(rune('a'+i)), domain.ContentHash(p.CandidateContent), time.Now().UTC(), "succeeded")
		env.seedArmRun(t, "disabled-control-"+string(rune('a'+i)), p.BaseHash, time.Now().UTC(), "succeeded")
	}
	env.h.sweepTrials(time.Now().UTC())
	got, _ := env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalCanary || got.Canary.Status != domain.EvoCanaryPassed {
		t.Fatal("stored L3 policy automatically released while disabled")
	}
	overview := requestJSON[domain.EvoOverview](t, env.router, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if overview.AutoEnabled {
		t.Fatal("UI advertised automatic evolution")
	}
	t.Setenv("AGENTFLOW_EVOLUTION_AUTO_ENABLED", "true")
	env.h.sweepTrials(time.Now().UTC())
	got, _ = env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalReleased {
		t.Fatal("explicit opt-in did not enable L3")
	}
}

func TestEvolutionTrialStatsExcludeForeignProjects(t *testing.T) {
	env := newEvoTestEnv(t)
	hash := domain.ContentHash(env.agent.Prompt)
	env.seedArmRun(t, "own", hash, time.Now().UTC(), "succeeded")
	foreign, _ := env.store.RunState("own")
	foreign.ID, foreign.Status = "foreign", "failed"
	foreign.Revision = 0
	foreign.ExecutionContext.ProjectID = "other"
	foreign.Nodes = map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "failed"}}
	if err := env.store.PutRun(foreign); err != nil {
		t.Fatal(err)
	}
	if err := env.store.PutRunAssetPins([]domain.RunAssetPin{{RunID: foreign.ID, AssetKind: "agent", AssetID: env.agent.ID, Hash: hash, CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	asset, _ := env.h.loadAsset("agent", env.agent.ID)
	if _, ok := env.h.trialScore("other", "foreign", asset); !ok {
		t.Fatal("missing foreign fixture")
	}
	pins := env.store.AssetPinsBetween("agent", env.agent.ID, time.Time{}, time.Time{}, 100)
	stats := env.h.armStats("evo-p", pins, hash, asset, 0)
	if stats.Runs != 1 || stats.Clean != 1 || stats.Failed != 0 {
		t.Fatalf("foreign project polluted trial stats: %+v", stats)
	}
}

func TestEvolutionUnsupportedSamplesDoNotCallImprover(t *testing.T) {
	env := newEvoTestEnv(t)
	source := env.seedRun(t, "repository-source", "failed", "failed", "failed")
	source.Repository.RepositoryID = "bound-repo"
	if err := env.store.PutRun(source); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	setEvoImproveForTest(func(context.Context, domain.EvoProposal, string, string, domain.User) (string, error) {
		called <- struct{}{}
		return "", nil
	})
	t.Cleanup(func() { setEvoImproveForTest(nil) })
	p := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"projectId": "evo-p", "assetKind": "agent", "assetId": env.agent.ID}, http.StatusAccepted)
	failed := env.waitProposal(t, p.ID, domain.EvoProposalFailed)
	if !strings.Contains(failed.Error, "代码仓库") || len(env.store.EvoReplays(p.ID)) != 0 {
		t.Fatalf("unsupported sample was not rejected early: %+v", failed)
	}
	select {
	case <-called:
		t.Fatal("unsupported sample incurred a model call")
	default:
	}
}
