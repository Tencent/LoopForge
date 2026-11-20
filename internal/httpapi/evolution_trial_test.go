package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func (env *evoTestEnv) seedPendingProposal(t *testing.T, id string) domain.EvoProposal {
	t.Helper()
	p := domain.EvoProposal{
		ID: id, ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, AssetName: env.agent.Name,
		Title: "补充自测要求", Status: domain.EvoProposalPending, BaseContent: env.agent.Prompt, BaseHash: domain.ContentHash(env.agent.Prompt),
		CandidateContent: env.agent.Prompt + "\n提交前必须运行单元测试。" + evoTestCandidateMark,
		Summary:          domain.EvoReplaySummary{Total: 1, Finished: 1, Fixed: 1, Passed: true, Verdict: "通过"},
	}
	if err := env.store.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = env.store.EvoProposal(p.ID)
	return p
}

// seedArmRun stores a finished business run and the asset pin it started
// with, as the engine would for a trial or post-release run.
func (env *evoTestEnv) seedArmRun(t *testing.T, id, hash string, at time.Time, nodeStatus string) {
	t.Helper()
	status := "succeeded"
	if nodeStatus == "failed" {
		status = "failed"
	}
	env.seedRun(t, id, status, nodeStatus, "")
	if err := env.store.PutRunAssetPins([]domain.RunAssetPin{{RunID: id, AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Hash: hash, CreatedAt: at}}); err != nil {
		t.Fatal(err)
	}
}

func TestEvolutionCanaryPromoteThenWatchDegradesAndAutoRollsBack(t *testing.T) {
	t.Setenv("AGENTFLOW_EVOLUTION_AUTO_ENABLED", "true")
	env := newEvoTestEnv(t)
	p := env.seedPendingProposal(t, "evp-trial")
	candidateHash := domain.ContentHash(p.CandidateContent)

	trial := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", map[string]any{"mode": "canary", "percent": 30}, http.StatusOK)
	if trial.Status != domain.EvoProposalCanary || trial.Canary == nil || trial.Canary.Percent != 30 || trial.Canary.Status != domain.EvoCanaryRunning {
		t.Fatalf("trial = %+v", trial)
	}
	if live, _ := env.store.Agent(env.agent.ID); live.Prompt != env.agent.Prompt {
		t.Fatal("a trial must not touch the catalog")
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals", map[string]any{"projectId": "evo-p", "assetKind": "agent", "assetId": env.agent.ID}, http.StatusConflict)

	at := time.Now().UTC()
	for i := 0; i < 5; i++ {
		env.seedArmRun(t, "evo-canary-"+string(rune('a'+i)), candidateHash, at, "succeeded")
		env.seedArmRun(t, "evo-control-"+string(rune('a'+i)), p.BaseHash, at, "succeeded")
	}
	replay := domain.Run{ID: "evo-trial-replay", WorkflowID: env.wf.ID, Status: "failed", WorkflowSnapshot: env.wf,
		ExecutionContext: domain.TelemetryContext{ProjectID: "evo-p", Purpose: domain.RunPurposeEvolutionReplay},
		Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "failed"}}, CreatedAt: at, UpdatedAt: at}
	if err := env.store.PutRun(replay); err != nil {
		t.Fatal(err)
	}
	_ = env.store.PutRunAssetPins([]domain.RunAssetPin{{RunID: replay.ID, AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Hash: candidateHash, CreatedAt: at}})

	env.h.sweepTrials(time.Now().UTC())
	got, _ := env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalCanary || got.Canary.Status != domain.EvoCanaryPassed || got.Canary.Canary.Runs != 5 || got.Canary.Control.Runs != 5 {
		t.Fatalf("an equal trial should pass at L2 and wait for a human (replays excluded): %+v", got.Canary)
	}
	if !strings.Contains(got.Canary.Verdict, "不差于当前版") {
		t.Fatalf("verdict = %q", got.Canary.Verdict)
	}
	overview := requestJSON[domain.EvoOverview](t, env.router, http.MethodGet, "/api/evolution/overview?projectId=evo-p", nil, http.StatusOK)
	if len(overview.Trial) != 1 || overview.Trial[0].ID != p.ID {
		t.Fatalf("overview trial = %+v", overview.Trial)
	}

	time.Sleep(20 * time.Millisecond)
	released := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", map[string]any{"note": "全量"}, http.StatusOK)
	if released.Status != domain.EvoProposalReleased || released.Canary.Status != domain.EvoCanaryPromoted || released.Watch == nil || released.Watch.Status != domain.EvoWatchWatching {
		t.Fatalf("promotion = %+v", released)
	}
	if released.Watch.PrevVersion != 1 || released.Watch.Version != 2 || released.Watch.Hash != candidateHash {
		t.Fatalf("watch = %+v", released.Watch)
	}
	if live, _ := env.store.Agent(env.agent.ID); !strings.Contains(live.Prompt, evoTestCandidateMark) {
		t.Fatal("promotion must release the candidate")
	}

	after := time.Now().UTC().Add(time.Second)
	for i, status := range []string{"failed", "failed", "failed", "succeeded", "succeeded"} {
		env.seedArmRun(t, "evo-after-"+string(rune('a'+i)), candidateHash, after, status)
	}
	env.h.sweepTrials(time.Now().UTC())
	got, _ = env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalReleased || got.Watch.Status != domain.EvoWatchDegraded || got.Watch.Baseline.Runs != 5 || got.Watch.Current.Failed != 3 {
		t.Fatalf("a worse release should be flagged at L2 (baseline from the control arm): %+v", got.Watch)
	}
	if live, _ := env.store.Agent(env.agent.ID); !strings.Contains(live.Prompt, evoTestCandidateMark) {
		t.Fatal("L2 only flags a degradation")
	}

	if err := env.store.PutEvoPolicy(domain.EvoPolicy{AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, ProjectID: "evo-p", Level: domain.EvoLevelAuto}); err != nil {
		t.Fatal(err)
	}
	env.h.sweepTrials(time.Now().UTC())
	got, _ = env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalRolledBack || got.Watch.Status != domain.EvoWatchRolledBack {
		t.Fatalf("L3 should roll a degraded release back: status=%s watch=%+v", got.Status, got.Watch)
	}
	if live, _ := env.store.Agent(env.agent.ID); live.Prompt != env.agent.Prompt {
		t.Fatalf("rollback must restore the previous body: %q", live.Prompt)
	}
	if v, ok := env.store.LatestAssetVersion(domain.EvoAssetAgent, env.agent.ID); !ok || v.Source != domain.EvoVersionRollback || !strings.Contains(v.Note, "L3 自动回滚") {
		t.Fatalf("latest version = %+v", v)
	}
}

func TestEvolutionCanaryStopsWhenWorseOrEdited(t *testing.T) {
	env := newEvoTestEnv(t)
	p := env.seedPendingProposal(t, "evp-worse")
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+p.ID+"/approve", map[string]any{"mode": "canary"}, http.StatusOK)
	at := time.Now().UTC()
	for i := 0; i < 3; i++ {
		env.seedArmRun(t, "evo-worse-c-"+string(rune('a'+i)), domain.ContentHash(p.CandidateContent), at, "failed")
		env.seedArmRun(t, "evo-worse-k-"+string(rune('a'+i)), p.BaseHash, at, "succeeded")
	}
	env.h.sweepTrials(time.Now().UTC())
	got, _ := env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalFailed || got.Canary.Status != domain.EvoCanaryFailed || !strings.Contains(got.Error, "提前停止") {
		t.Fatalf("a clearly worse trial should stop early: status=%s canary=%+v", got.Status, got.Canary)
	}
	if live, _ := env.store.Agent(env.agent.ID); live.Prompt != env.agent.Prompt {
		t.Fatal("a failed trial never touches the catalog")
	}

	edited := env.seedPendingProposal(t, "evp-edited")
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+edited.ID+"/approve", map[string]any{"mode": "canary"}, http.StatusOK)
	agent, _ := env.store.Agent(env.agent.ID)
	agent.Prompt += "\n手动补充"
	if err := env.store.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	env.h.sweepTrials(time.Now().UTC())
	got, _ = env.store.EvoProposal(edited.ID)
	if got.Status != domain.EvoProposalFailed || got.Canary.Status != domain.EvoCanaryAborted {
		t.Fatalf("an external edit should end the trial: %+v", got.Canary)
	}

	agent.Prompt = env.agent.Prompt
	_ = env.store.PutAgent(agent)
	aborted := env.seedPendingProposal(t, "evp-abort")
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+aborted.ID+"/approve", map[string]any{"mode": "canary"}, http.StatusOK)
	rejected := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, "/api/evolution/proposals/"+aborted.ID+"/reject", map[string]any{"note": "先不试了"}, http.StatusOK)
	if rejected.Status != domain.EvoProposalRejected || rejected.Canary.Status != domain.EvoCanaryAborted || rejected.TrialActive() {
		t.Fatalf("reject should stop the trial: %+v", rejected.Canary)
	}
}

func TestEvolutionWaitsForAllPlannedReplays(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedValidatingProposal(t, "evp-starting")
	p, _ := env.store.EvoProposal("evp-starting")
	p.ExpectedReplays = 2
	if err := env.store.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	if got := env.h.sweepProposal(p, time.Now().UTC()); got.Status != domain.EvoProposalValidating {
		t.Fatalf("zero created samples must keep validating: %+v", got)
	}
	env.seedRun(t, "evo-starting-src", "succeeded", "succeeded", "")
	env.seedReplay(t, p.ID, "evo-starting-src", domain.Run{ID: "evo-starting-first", WorkflowID: env.wf.ID, Status: "succeeded", WorkflowSnapshot: env.wf})
	if got := env.h.sweepProposal(p, time.Now().UTC()); got.Status != domain.EvoProposalValidating {
		t.Fatalf("a partial sample set must keep validating: %+v", got)
	}
	if got := env.h.sweepProposal(p, time.Now().UTC().Add(3*time.Hour)); got.Status != domain.EvoProposalFailed || !strings.Contains(got.Error, "未全部创建") {
		t.Fatalf("abandoned initialization must time out: %+v", got)
	}
}

func TestEvolutionPlansOnlySameProjectSamples(t *testing.T) {
	env := newEvoTestEnv(t)
	asset, ok := env.h.loadAsset(domain.EvoAssetAgent, env.agent.ID)
	if !ok {
		t.Fatal("missing asset")
	}
	for _, status := range []string{"succeeded", "failed"} {
		run := domain.Run{ID: "evo-other-" + status, WorkflowID: env.wf.ID, Status: status, WorkflowSnapshot: env.wf,
			ExecutionContext: domain.TelemetryContext{ProjectID: "other-project"},
			Nodes:            map[string]*domain.NodeRun{"code": {NodeID: "code", Status: status}}, CreatedAt: time.Now().UTC()}
		if err := env.store.PutRun(run); err != nil {
			t.Fatal(err)
		}
		if err := env.store.PutRunAssetPins([]domain.RunAssetPin{{RunID: run.ID, AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Hash: domain.ContentHash(env.agent.Prompt), CreatedAt: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
	}
	env.seedRun(t, "evo-same-project", "failed", "failed", "test failed")
	plans := env.h.planReplays(domain.EvoProposal{ProjectID: "evo-p"}, domain.EvoFinding{SampleRunIDs: []string{"evo-other-failed", "evo-same-project"}}, asset)
	if len(plans) != 1 || plans[0].Run.ID != "evo-same-project" {
		t.Fatalf("cross-project samples selected: %+v", plans)
	}
}

func TestEvolutionL3StartsTrialAndReleasesItself(t *testing.T) {
	t.Setenv("AGENTFLOW_EVOLUTION_AUTO_ENABLED", "true")
	env := newEvoTestEnv(t)
	if err := env.store.PutEvoPolicy(domain.EvoPolicy{AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, ProjectID: "evo-p", Level: domain.EvoLevelAuto}); err != nil {
		t.Fatal(err)
	}
	env.seedRun(t, "evo-l3-src", "succeeded", "succeeded", "")
	env.seedValidatingProposal(t, "evp-l3")
	env.seedReplay(t, "evp-l3", "evo-l3-src", domain.Run{ID: "evo-l3-replay", WorkflowID: env.wf.ID, Status: "succeeded", WorkflowSnapshot: env.wf,
		Nodes: map[string]*domain.NodeRun{"code": {NodeID: "code", Status: "succeeded"}}})
	p := env.h.sweepProposal(domain.EvoProposal{ID: "evp-l3"}, time.Now().UTC())
	if p.Status != domain.EvoProposalCanary || p.Canary == nil || !p.Canary.Auto {
		t.Fatalf("L3 should start a trial once replay passes: status=%s canary=%+v summary=%+v", p.Status, p.Canary, p.Summary)
	}
	at := time.Now().UTC()
	for i := 0; i < 5; i++ {
		env.seedArmRun(t, "evo-l3-c-"+string(rune('a'+i)), domain.ContentHash(p.CandidateContent), at, "succeeded")
		env.seedArmRun(t, "evo-l3-k-"+string(rune('a'+i)), p.BaseHash, at, "succeeded")
	}
	env.h.sweepTrials(time.Now().UTC())
	got, _ := env.store.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalReleased || got.Canary.Status != domain.EvoCanaryPromoted || got.Watch == nil || !strings.Contains(got.DecisionNote, "L3 自动全量") {
		t.Fatalf("L3 should release a trial that is not worse: status=%s canary=%+v note=%q", got.Status, got.Canary, got.DecisionNote)
	}
}
