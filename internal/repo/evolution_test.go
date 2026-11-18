package repo

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
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
	p = got
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

func evolutionQueueFixture(t *testing.T, st *Store, id string) []domain.EvoProposal {
	t.Helper()
	if err := st.PutRule(domain.RuleDefinition{ID: id, Name: "共享规则", Shared: true, Content: "initial"}); err != nil {
		t.Fatal(err)
	}
	out := []domain.EvoProposal{}
	for i := 0; i < 2; i++ {
		f := domain.EvoFinding{ID: fmt.Sprintf("%s-f%d", id, i), ProjectID: fmt.Sprintf("p%d", i), AssetKind: domain.EvoAssetRule,
			AssetID: id, Status: domain.EvoFindingOpen, Title: fmt.Sprintf("问题%d", i)}
		if err := st.PutEvoFinding(f); err != nil {
			t.Fatal(err)
		}
		out = append(out, domain.EvoProposal{ID: fmt.Sprintf("%s-p%d", id, i), ProjectID: f.ProjectID, FindingID: f.ID,
			AssetKind: f.AssetKind, AssetID: id, Status: domain.EvoProposalDrafting})
	}
	return out
}

func TestEvolutionQueueConcurrentAdmission(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "serial")
	testEvolutionConcurrentAdmission(t, [2]*Store{st, st}, ps)
}

func testEvolutionConcurrentAdmission(t *testing.T, stores [2]*Store, ps []domain.EvoProposal) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, _, err := stores[i%2].SubmitEvoProposal(ps[i%2], fmt.Sprintf("%s-q%d", ps[0].AssetID, i), "")
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	st := stores[0]
	current, ok, err := st.CurrentEvoProposalForAsset(ps[0].AssetKind, ps[0].AssetID)
	if err != nil || !ok {
		t.Fatalf("current: %+v %v", current, err)
	}
	queues := []domain.EvoQueueItem{}
	for _, item := range append(st.EvoQueue("p0"), st.EvoQueue("p1")...) {
		if item.AssetID == ps[0].AssetID && item.AssetKind == ps[0].AssetKind {
			queues = append(queues, item)
		}
	}
	if len(queues) != 1 || queues[0].FindingID == current.FindingID {
		t.Fatalf("exactly one distinct intent queued: %+v", queues)
	}
	countProposals := func() int64 {
		var count int64
		if err := st.DB.Model(&evoProposalRow{}).Where("asset_kind = ? AND asset_id = ?", ps[0].AssetKind, ps[0].AssetID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	if countProposals() != 1 {
		t.Fatal("queue must not create candidates")
	}
	p := ps[0]
	if current.ProjectID == p.ProjectID {
		p = ps[1]
	}
	if _, _, _, err := st.SubmitEvoProposal(p, "unused", queues[0].ID); !errors.Is(err, ErrEvoBusy) {
		t.Fatalf("cannot start queued intent while occupied: %v", err)
	}
	current.Status = domain.EvoProposalRejected
	if err := st.PutEvoProposal(current); err != nil {
		t.Fatal(err)
	}
	if countProposals() != 1 {
		t.Fatal("ending current must not automatically start next")
	}
	rule, _ := st.Rule(p.AssetID)
	rule.Content = "latest live body"
	if err := st.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	started, queue, created, err := st.SubmitEvoProposal(p, "unused", queues[0].ID)
	if err != nil || queue != nil || !created || started.BaseContent != rule.Content || started.BaseVersion != 2 {
		t.Fatalf("manual start must use latest baseline: %+v queue=%+v created=%v err=%v", started, queue, created, err)
	}
	if item, _ := st.EvoQueueItem(queues[0].ID); item.Status != "started" || item.ProposalID != started.ID {
		t.Fatalf("queue claim not persisted: %+v", item)
	}
}

func TestEvolutionQueueMySQLConcurrentAdmission(t *testing.T) {
	stores, id := admissionMySQLStores(t)
	ps := evolutionQueueFixture(t, stores[0], id)
	t.Cleanup(func() {
		for _, table := range []string{"evo_queue", "evo_proposals", "evo_findings", "asset_versions", "evo_asset_gates"} {
			if err := stores[0].DB.Table(table).Where("asset_kind = ? AND asset_id = ?", domain.EvoAssetRule, id).Delete(map[string]any{}).Error; err != nil {
				t.Error(err)
			}
		}
		if err := stores[0].DB.Table("rules").Where("id = ?", id).Delete(map[string]any{}).Error; err != nil {
			t.Error(err)
		}
	})
	testEvolutionConcurrentAdmission(t, stores, ps)
}

func TestEvolutionProposalCASAndWatchOccupancy(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "watch")
	p, _, _, err := st.SubmitEvoProposal(ps[0], "q0", "")
	if err != nil {
		t.Fatal(err)
	}
	stale := p
	p.CandidateContent, p.Summary.Passed, p.Status = "candidate", true, domain.EvoProposalPending
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	p.Status = domain.EvoProposalCanary
	p.Canary = &domain.EvoCanary{Status: domain.EvoCanaryRunning}
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	if _, err := st.ReleaseEvoProposal(p, 0, "", false, 5); err == nil {
		t.Fatal("running canary must not publish")
	}
	p.Canary.Status = domain.EvoCanaryPassed
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	released, err := st.ReleaseEvoProposal(p, 0, "", false, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, queued, created, err := st.SubmitEvoProposal(ps[1], "watch-q", ""); err != nil || queued == nil || created {
		t.Fatalf("released watching must hold shared asset: queued=%+v created=%v err=%v", queued, created, err)
	}
	released.Watch.Status = domain.EvoWatchDegraded
	if err := st.PutEvoProposal(released); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.SubmitEvoProposal(ps[1], "unused", "watch-q"); !errors.Is(err, ErrEvoBusy) {
		t.Fatalf("degraded watch must hold asset: %v", err)
	}
	stale.Status = domain.EvoProposalValidating
	if err := st.PutEvoProposal(stale); !errors.Is(err, ErrEvoStale) {
		t.Fatalf("late generator must not overwrite release: %v", err)
	}
	if _, err := st.RollbackEvoAsset("rule", "watch", domain.ContentHash("candidate"), 1, 0, "rollback"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.EvoProposal(p.ID)
	if got.Status != domain.EvoProposalRolledBack || got.Watch.Active() {
		t.Fatalf("rollback must close own active watch: %+v", got)
	}
	if _, err := st.ReleaseEvoProposal(p, 0, "", false, 5); !errors.Is(err, ErrEvoStale) {
		t.Fatalf("late release must not revive rollback: %v", err)
	}
}

func TestEvolutionPublishAndRollbackAtomicFailure(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "atomic")
	p, _, _, err := st.SubmitEvoProposal(ps[0], "q", "")
	if err != nil {
		t.Fatal(err)
	}
	p.CandidateContent, p.Status, p.Summary.Passed = "candidate", domain.EvoProposalPending, true
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	fail := errors.New("injected evolution write failure")
	failureTable := "asset_versions"
	inject := func() {
		t.Helper()
		callback := func(tx *gorm.DB) {
			if tx.Statement.Table == failureTable {
				tx.AddError(fail)
			}
		}
		if err := st.DB.Callback().Create().Before("gorm:create").Register("evo_write_failure", callback); err != nil {
			t.Fatal(err)
		}
		if err := st.DB.Callback().Update().Before("gorm:update").Register("evo_write_failure", callback); err != nil {
			t.Fatal(err)
		}
	}
	remove := func() {
		_ = st.DB.Callback().Create().Remove("evo_write_failure")
		_ = st.DB.Callback().Update().Remove("evo_write_failure")
	}
	defer remove()
	for _, table := range []string{"rules", "asset_versions", "evo_proposals", "evo_findings"} {
		failureTable = table
		inject()
		if _, err := st.ReleaseEvoProposal(p, 0, "", false, 5); !errors.Is(err, fail) {
			t.Fatalf("expected injected %s failure: %v", table, err)
		}
		if rule, _ := st.Rule("atomic"); rule.Content != "initial" {
			t.Fatal("failed publish must roll back catalog")
		}
		if got, _ := st.EvoProposal(p.ID); got.Status != domain.EvoProposalPending || got.Revision != p.Revision {
			t.Fatalf("failed publish changed proposal: %+v", got)
		}
		if f, _ := st.EvoFinding(p.FindingID); f.Status != domain.EvoFindingProposed {
			t.Fatal("failed publish must not resolve finding")
		}
		if len(st.AssetVersions("rule", "atomic")) != 1 {
			t.Fatal("failed publish must not append a version")
		}
		remove()
	}
	if _, err := st.ReleaseEvoProposal(p, 0, "", false, 5); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rules", "asset_versions", "evo_proposals", "evo_findings"} {
		failureTable = table
		inject()
		if _, err := st.RollbackEvoAsset("rule", "atomic", domain.ContentHash("candidate"), 1, 0, "rollback"); !errors.Is(err, fail) {
			t.Fatalf("expected rollback %s write failure: %v", table, err)
		}
		if rule, _ := st.Rule("atomic"); rule.Content != "candidate" {
			t.Fatal("failed rollback must preserve published body")
		}
		if got, _ := st.EvoProposal(p.ID); got.Status != domain.EvoProposalReleased || !got.Watch.Active() {
			t.Fatal("failed rollback must preserve release and watch")
		}
		if len(st.AssetVersions("rule", "atomic")) != 2 {
			t.Fatal("failed rollback must not append a version")
		}
		remove()
	}
}

func TestEvolutionPublicationRejectsCatalogABA(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "aba")
	p, _, _, err := st.SubmitEvoProposal(ps[0], "q", "")
	if err != nil {
		t.Fatal(err)
	}
	p.CandidateContent, p.Status, p.Summary.Passed = "candidate", domain.EvoProposalPending, true
	if err := st.PutEvoProposal(p); err != nil {
		t.Fatal(err)
	}
	p, _ = st.EvoProposal(p.ID)
	rule, _ := st.Rule("aba")
	rule.Content = "temporary edit"
	if err := st.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	rule.Content = "initial"
	if err := st.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReleaseEvoProposal(p, 0, "", false, 5); !errors.Is(err, ErrEvoStale) {
		t.Fatalf("same body after external edit must not validate an old baseline: %v", err)
	}
	if len(st.AssetVersions("rule", "aba")) != 1 {
		t.Fatal("stale publication must not change versions")
	}
}

func TestEvolutionLegacyParallelConflictIsBlocked(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ps := evolutionQueueFixture(t, st, "legacy")
	for _, p := range ps {
		if err := st.DB.Create(&evoProposalRow{ID: p.ID, ProjectID: p.ProjectID, AssetKind: p.AssetKind, AssetID: p.AssetID, Status: p.Status, Payload: mustJSON(p)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := st.SubmitEvoProposal(ps[0], "q", ""); !errors.Is(err, ErrEvoParallel) {
		t.Fatalf("must explicitly block illegal historical parallel state: %v", err)
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
