package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestEvolutionReplayCompareListsNodeChanges(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "evo-cmp-src", "succeeded", "succeeded", "")
	wf := env.wf
	wf.Nodes = append([]domain.WorkflowNode{{ID: "design", Name: "方案设计", Type: "agent"}}, wf.Nodes...)
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "code" {
			wf.Nodes[i].Name, wf.Nodes[i].RejectTo, wf.Nodes[i].MaxReworks = "独立评审", "design", 1
		}
	}
	env.seedValidatingProposal(t, "evp-cmp")
	env.seedReplay(t, "evp-cmp", "evo-cmp-src", domain.Run{ID: "evo-cmp-replay", WorkflowID: wf.ID, Status: "succeeded", WorkflowSnapshot: wf,
		Nodes: map[string]*domain.NodeRun{
			"design": {NodeID: "design", Status: "succeeded", ReworkCount: 1},
			"code":   {NodeID: "code", Status: "succeeded", GateResult: &domain.GateResult{Decision: "pass", Reason: "补齐了 e2e 用例"}},
		}})
	env.h.sweepProposal(domain.EvoProposal{ID: "evp-cmp"}, time.Now().UTC())
	replays := env.store.EvoReplays("evp-cmp")
	if len(replays) != 1 {
		t.Fatalf("replays = %+v", replays)
	}

	got := requestJSONAuthed[evoReplayCompare](t, env.router, env.user, http.MethodGet, "/api/evolution/proposals/evp-cmp/replays/"+replays[0].ID+"/compare", nil, http.StatusOK)
	if got.SourceRunID != "evo-cmp-src" || got.RunID != "evo-cmp-replay" || !strings.Contains(got.Input, "实现登录页") || len(got.Nodes) != 2 {
		t.Fatalf("compare = %+v", got)
	}
	design, code := got.Nodes[0], got.Nodes[1]
	if design.ID != "design" || design.Source != nil || design.Replay == nil || design.Replay.Rework != 1 || !design.Changed || design.Shaped {
		t.Fatalf("design = %+v", design)
	}
	if code.ID != "code" || !code.Shaped || code.Source == nil || code.Replay == nil || code.Replay.Decision != "pass" || code.Replay.Reason != "补齐了 e2e 用例" || !code.Changed {
		t.Fatalf("code = %+v", code)
	}

	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodGet, "/api/evolution/proposals/evp-cmp/replays/nope/compare", nil, http.StatusNotFound)
}

func TestEvolutionReplayCompareRejectsOtherProjectRuns(t *testing.T) {
	for _, side := range []string{"source", "replay"} {
		t.Run(side, func(t *testing.T) {
			env := newEvoTestEnv(t)
			env.seedValidatingProposal(t, "evp-private")
			for _, runSide := range []string{"source", "replay"} {
				projectID := "evo-p"
				if runSide == side {
					projectID = "other-project"
				}
				run := domain.Run{ID: "evo-private-" + runSide, WorkflowID: env.wf.ID, Status: "succeeded", WorkflowSnapshot: env.wf,
					ExecutionContext: domain.TelemetryContext{ProjectID: projectID}}
				if err := env.store.PutRun(run); err != nil {
					t.Fatal(err)
				}
			}
			if err := env.store.PutEvoReplay(domain.EvoReplay{ID: "evr-evo-private-replay", ProposalID: "evp-private", ProjectID: "evo-p", SourceRunID: "evo-private-source", RunID: "evo-private-replay"}); err != nil {
				t.Fatal(err)
			}
			requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodGet, "/api/evolution/proposals/evp-private/replays/evr-evo-private-replay/compare", nil, http.StatusForbidden)
		})
	}
}
