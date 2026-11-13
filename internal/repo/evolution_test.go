package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestEvolutionRepoVersionsAndProposals(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	if got := st.EvoLevel(domain.EvoAssetAgent, "coder"); got != domain.EvoDefaultLevel {
		t.Fatalf("default level = %d", got)
	}
	if err := st.PutEvoPolicy(domain.EvoPolicy{AssetKind: domain.EvoAssetAgent, AssetID: "coder", ProjectID: "p1", Level: 9}); err != nil {
		t.Fatal(err)
	}
	if got := st.EvoLevel(domain.EvoAssetAgent, "coder"); got != domain.NormalizeEvoLevel(9) {
		t.Fatalf("level not normalized: %d", got)
	}
	if err := st.PutEvoPolicy(domain.EvoPolicy{AssetKind: "skill", AssetID: "x"}); err == nil {
		t.Fatal("unsupported asset kind must be rejected")
	}

	for i, body := range []string{"v1 正文", "v2 正文", "v1 正文"} {
		v, err := st.AppendAssetVersion(domain.AssetVersion{AssetKind: domain.EvoAssetAgent, AssetID: "coder", Content: body, Source: domain.EvoVersionManual})
		if err != nil {
			t.Fatal(err)
		}
		if v.Version != i+1 || v.Hash != domain.ContentHash(body) {
			t.Fatalf("version %d: %+v", i, v)
		}
	}
	versions := st.AssetVersions(domain.EvoAssetAgent, "coder")
	if len(versions) != 3 || versions[0].Version != 3 || versions[0].Content != "v1 正文" {
		t.Fatalf("versions newest first: %+v", versions)
	}
	if latest, ok := st.LatestAssetVersion(domain.EvoAssetAgent, "coder"); !ok || latest.Version != 3 {
		t.Fatalf("latest: %+v", latest)
	}
	if byHash, ok := st.AssetVersionByHash(domain.EvoAssetAgent, "coder", domain.ContentHash("v1 正文")); !ok || byHash.Version != 3 {
		t.Fatalf("by hash should return newest match: %+v", byHash)
	}
	if v2, ok := st.AssetVersion(domain.EvoAssetAgent, "coder", 2); !ok || v2.Content != "v2 正文" {
		t.Fatalf("v2: %+v", v2)
	}
	if other, err := st.AppendAssetVersion(domain.AssetVersion{AssetKind: domain.EvoAssetRule, AssetID: "coder", Content: "rule"}); err != nil || other.Version != 1 {
		t.Fatalf("versions are numbered per asset: %+v %v", other, err)
	}

	p := domain.EvoProposal{ID: "evp-1", ProjectID: "p1", AssetKind: domain.EvoAssetAgent, AssetID: "coder", Status: domain.EvoProposalValidating, BaseContent: "v1 正文"}
	p.Replays = []domain.EvoReplay{{ID: "ignored"}}
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	replay := domain.EvoReplay{ID: "evr-1", ProposalID: "evp-1", Group: domain.EvoGroupDiagnosis, SourceRunID: "run-a", Status: "running",
		Overrides: domain.EvoAssetOverride{AgentPrompts: map[string]string{"coder": "候选正文"}}}
	if err := st.PutEvoReplay(replay); err != nil {
		t.Fatal(err)
	}
	got, ok := st.EvoProposal("evp-1")
	if !ok || len(got.Replays) != 1 || got.Replays[0].ID != "evr-1" {
		t.Fatalf("proposal should carry stored replays only: %+v", got)
	}
	if ov, ok := st.EvoReplayOverrides("evr-1"); !ok || ov.AgentPrompts["coder"] != "候选正文" {
		t.Fatalf("overrides: %+v", ov)
	}
	if open, ok := st.OpenEvoProposalForAsset(domain.EvoAssetAgent, "coder"); !ok || open.ID != "evp-1" {
		t.Fatalf("open proposal lookup: %+v", open)
	}
	p.Status = domain.EvoProposalReleased
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.OpenEvoProposalForAsset(domain.EvoAssetAgent, "coder"); ok {
		t.Fatal("released proposal is not open")
	}
	if list := st.EvoProposals("p1", 10, domain.EvoProposalReleased); len(list) != 1 {
		t.Fatalf("status filter: %+v", list)
	}
	if list := st.EvoProposals("p2", 10); len(list) != 0 {
		t.Fatalf("project filter: %+v", list)
	}
}

func TestEvolutionRepoFindingsPinsSignalsSamples(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	f := domain.EvoFinding{ID: "evf-1", ProjectID: "p1", AssetKind: domain.EvoAssetRule, AssetID: "r1", Status: domain.EvoFindingOpen, Title: "t"}
	if err := st.PutEvoFinding(f); err != nil {
		t.Fatal(err)
	}
	if open, ok := st.OpenEvoFindingForAsset("p1", domain.EvoAssetRule, "r1"); !ok || open.ID != "evf-1" {
		t.Fatalf("open finding: %+v", open)
	}
	f.Status = domain.EvoFindingDismissed
	if err := st.PutEvoFinding(f); err != nil {
		t.Fatal(err)
	}
	if list := st.EvoFindings("p1", domain.EvoFindingOpen); len(list) != 0 {
		t.Fatalf("dismissed finding listed as open: %+v", list)
	}
	if list := st.EvoFindings("p1"); len(list) != 1 || list[0].Status != domain.EvoFindingDismissed {
		t.Fatalf("all findings: %+v", list)
	}

	pins := []domain.RunAssetPin{
		{RunID: "run-a", AssetKind: domain.EvoAssetAgent, AssetID: "coder", Version: 1, Hash: "h1"},
		{RunID: "run-a", AssetKind: domain.EvoAssetRule, AssetID: "r1", Version: 0, Hash: "h2"},
	}
	if err := st.PutRunAssetPins(pins); err != nil {
		t.Fatal(err)
	}
	pins[0].Hash = "changed"
	if err := st.PutRunAssetPins(pins); err != nil {
		t.Fatal(err)
	}
	got := st.RunAssetPins("run-a")
	if len(got) != 2 || got[0].AssetKind != domain.EvoAssetAgent || got[0].Hash != "h1" {
		t.Fatalf("pins must be immutable and sorted: %+v", got)
	}
	if ids := st.RunIDsPinnedTo(domain.EvoAssetRule, "r1", time.Time{}, 10); len(ids) != 1 || ids[0] != "run-a" {
		t.Fatalf("pinned runs: %v", ids)
	}

	old := time.Now().UTC().AddDate(0, 0, -30)
	if err := st.AddEvoSignal(domain.EvoSignal{ID: "evs-old", ProjectID: "p1", RunID: "run-a", Kind: "user_feedback", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvoSignal(domain.EvoSignal{ID: "evs-new", ProjectID: "p1", RunID: "run-a", Kind: "user_feedback", Detail: "差"}); err != nil {
		t.Fatal(err)
	}
	if sigs := st.EvoSignals("p1", time.Now().UTC().AddDate(0, 0, -14)); len(sigs) != 1 || sigs[0].ID != "evs-new" {
		t.Fatalf("signals window: %+v", sigs)
	}

	if err := st.PutEvoSample(domain.EvoSample{ProjectID: "p1", RunID: "run-a", Note: "n"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEvoSample(domain.EvoSample{ProjectID: "p1", RunID: "run-a", Note: "n2"}); err != nil {
		t.Fatal(err)
	}
	if samples := st.EvoSamples("p1"); len(samples) != 1 || samples[0].Note != "n2" {
		t.Fatalf("samples upsert: %+v", samples)
	}
	if err := st.DeleteEvoSample("p1", "run-a"); err != nil {
		t.Fatal(err)
	}
	if samples := st.EvoSamples("p1"); len(samples) != 0 {
		t.Fatalf("sample not deleted: %+v", samples)
	}
}
