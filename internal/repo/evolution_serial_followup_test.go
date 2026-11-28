package repo

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestEvolutionQueueReentryAfterCancelAndCompletion(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "reentry")
	current, _, _, err := st.SubmitEvoProposal(ps[0], "unused", "")
	if err != nil {
		t.Fatal(err)
	}
	_, first, _, err := st.SubmitEvoProposal(ps[1], "queue-first", "")
	if err != nil || first == nil {
		t.Fatalf("first queue: %+v %v", first, err)
	}
	if _, err := st.RejectEvoQueue(first.ID); err != nil {
		t.Fatal(err)
	}
	_, second, _, err := st.SubmitEvoProposal(ps[1], "queue-second", "")
	if err != nil || second == nil || second.ID != "queue-second" || second.Status != "queued" {
		t.Fatalf("cancelled queue must be eligible again: %+v %v", second, err)
	}
	current.Status = domain.EvoProposalRejected
	if err := st.PutEvoProposal(current); err != nil {
		t.Fatal(err)
	}
	started, queue, created, err := st.SubmitEvoProposal(ps[1], "unused", second.ID)
	if err != nil || !created || queue != nil {
		t.Fatalf("manual start: %+v %v", queue, err)
	}
	started.Status = domain.EvoProposalRejected
	if err := st.PutEvoProposal(started); err != nil {
		t.Fatal(err)
	}
	rule, _ := st.Rule(ps[1].AssetID)
	rule.Content = "updated baseline after last attempt"
	if err := st.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	retry := ps[1]
	retry.ID = "reentry-new-attempt"
	next, queue, created, err := st.SubmitEvoProposal(retry, "unused", "")
	if err != nil || !created || queue != nil || next.BaseContent != rule.Content || next.CandidateContent != "" {
		t.Fatalf("completed queue must not return a terminal queue envelope: %+v %+v %v", next, queue, err)
	}
	if historical, ok := st.EvoProposal(started.ID); !ok || historical.Status != domain.EvoProposalRejected {
		t.Fatal("retry must preserve the earlier proposal history")
	}
}

func TestEvolutionLegacyParallelCanCancelWithoutRevival(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "legacy-cancel")
	for _, p := range ps {
		if err := st.DB.Create(&evoProposalRow{ID: p.ID, ProjectID: p.ProjectID, AssetKind: p.AssetKind, AssetID: p.AssetID, Status: p.Status, Payload: mustJSON(p)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p, _ := st.EvoProposal(ps[0].ID)
	stale := p
	p.Status = domain.EvoProposalRejected
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatalf("legacy conflict must allow cancelling one candidate: %v", err)
	}
	current, busy, err := st.CurrentEvoProposalForAsset(p.AssetKind, p.AssetID)
	if err != nil || !busy || current.ID != ps[1].ID {
		t.Fatalf("remaining candidate: %+v %v", current, err)
	}
	current.Status = domain.EvoProposalRejected
	if err := st.PutEvoProposal(current); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEvoProposal(stale); !errors.Is(err, ErrEvoStale) {
		t.Fatalf("late writer revived a cancelled candidate: %v", err)
	}
}

func TestEvolutionTrafficCASPreservesEvidenceAndBaseline(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "traffic-cas")
	p, _, _, err := st.SubmitEvoProposal(ps[0], "unused", "")
	if err != nil {
		t.Fatal(err)
	}
	p.Status, p.CandidateContent, p.Summary.Passed = domain.EvoProposalPending, "candidate", true
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	p.Status = domain.EvoProposalCanary
	p.Canary = &domain.EvoCanary{Status: domain.EvoCanaryRunning, Percent: 50, MinRuns: 10, StartedAt: time.Now().UTC(),
		Canary: domain.EvoArmStats{Runs: 5, Clean: 5}, Control: domain.EvoArmStats{Runs: 5, Clean: 3, Reworked: 1, Failed: 1}, Verdict: "waiting"}
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	changed, err := st.AdjustEvoCanaryTraffic(p, 25)
	if err != nil {
		t.Fatal(err)
	}
	expected := *p.Canary
	expected.Percent = 25
	if !reflect.DeepEqual(changed.Canary, &expected) || changed.Revision != p.Revision+1 {
		t.Fatalf("traffic update reset evidence: %+v", changed)
	}
	if _, err := st.AdjustEvoCanaryTraffic(p, 10); !errors.Is(err, ErrEvoStale) {
		t.Fatalf("stale traffic update: %v", err)
	}
	noOp, err := st.AdjustEvoCanaryTraffic(changed, 25)
	if err != nil || noOp.Revision != changed.Revision {
		t.Fatalf("same traffic must be a no-op: %v", err)
	}
	rule, _ := st.Rule(p.AssetID)
	rule.Content = "external change"
	if err := st.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdjustEvoCanaryTraffic(changed, 50); !errors.Is(err, ErrEvoStale) {
		t.Fatalf("drifted baseline accepted traffic: %v", err)
	}
	unchanged, _ := st.EvoProposal(p.ID)
	if unchanged.Canary.Percent != 25 {
		t.Fatal("rejected adjustment mutated traffic")
	}
}
