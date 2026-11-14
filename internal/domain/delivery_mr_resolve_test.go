package domain

import "testing"

func TestFallbackRunDeliveryMRsDeduplicatesProjectsCaseInsensitively(t *testing.T) {
	candidates := []artifactMRCandidate{
		{mr: DeliveryMR{Project: "Team/App", IID: 3, URL: GongfengMRURL("Team/App", 3)}},
		{mr: DeliveryMR{Project: "team/app", IID: 2, URL: GongfengMRURL("team/app", 2)}},
		{mr: DeliveryMR{Project: "team/lib", IID: 1, URL: GongfengMRURL("team/lib", 1)}},
	}
	run := Run{Telemetry: []TelemetryEvent{{Attributes: map[string]any{"url": GongfengMRURL("team/other", 9)}}}}
	got := fallbackRunDeliveryMRs(run, candidates)
	if len(got) != 2 || got[0].IID != 3 || got[1].Project != "team/lib" {
		t.Fatalf("want first MR per project from artifacts only, got %+v", got)
	}
}

func TestFallbackRunDeliveryMRsUsesTelemetryWithoutCandidates(t *testing.T) {
	run := Run{Telemetry: []TelemetryEvent{
		{Attributes: map[string]any{"url": "https://git.example.com/x/y/-/merge_requests/4"}},
		{Attributes: map[string]any{"url": 42}},
		{Attributes: map[string]any{"url": "https://example.com/not-an-mr"}},
		{Attributes: map[string]any{"url": "https://git.example.com/X/Y/-/merge_requests/5"}},
		{Attributes: map[string]any{"url": "https://git.example.com/x/z/-/merge_requests/6"}},
	}}
	got := fallbackRunDeliveryMRs(run, nil)
	if len(got) != 2 || got[0].Project != "x/y" || got[0].IID != 4 || got[1].Project != "x/z" || got[1].IID != 6 {
		t.Fatalf("want deduplicated telemetry MRs, got %+v", got)
	}
	if got := fallbackRunDeliveryMRs(Run{}, nil); got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", got)
	}
}

func TestPickRepositoryDeliveryMRPrefersBranchLookup(t *testing.T) {
	rc := RepositoryContext{RepositoryID: "repo-a", Name: "a", GongfengProjectID: " g/a ", WorkBranch: "feature/x"}
	candidates := []artifactMRCandidate{{mr: DeliveryMR{Project: "g/a", IID: 1, URL: GongfengMRURL("g/a", 1)}}}
	byBranch := func(got RepositoryContext) (DeliveryMR, bool) {
		if got.RepositoryID != "repo-a" {
			t.Fatalf("byBranch received %+v", got)
		}
		return DeliveryMR{IID: 12, GongfengState: "opened"}, true
	}
	picked, ok := pickRepositoryDeliveryMR(rc, candidates, byBranch)
	if !ok || picked.IID != 12 || picked.Project != "g/a" || picked.URL != GongfengMRURL("g/a", 12) {
		t.Fatalf("branch lookup must win and be completed: %+v ok=%v", picked, ok)
	}
	if picked.RepositoryID != "repo-a" || picked.Repository != "a" || picked.GongfengState != "opened" {
		t.Fatalf("repository identity not attached: %+v", picked)
	}
}

func TestPickRepositoryDeliveryMRFallsBackToSameProjectCandidate(t *testing.T) {
	candidates := []artifactMRCandidate{
		{mr: DeliveryMR{Project: "g/other", IID: 9, URL: GongfengMRURL("g/other", 9)}},
		{mr: DeliveryMR{Project: "G/A", IID: 5, URL: "https://git.example.com/G/A/-/merge_requests/5"}},
		{mr: DeliveryMR{Project: "g/a", IID: 4, URL: GongfengMRURL("g/a", 4)}},
	}
	calls := 0
	byBranch := func(RepositoryContext) (DeliveryMR, bool) { calls++; return DeliveryMR{}, false }

	rc := RepositoryContext{RepositoryID: "repo-a", Name: "a", GongfengProjectID: "g/a", WorkBranch: "feature/x"}
	picked, ok := pickRepositoryDeliveryMR(rc, candidates, byBranch)
	if !ok || picked.IID != 5 || picked.Project != "G/A" || picked.URL != "https://git.example.com/G/A/-/merge_requests/5" || picked.RepositoryID != "repo-a" {
		t.Fatalf("want first same-project candidate, got %+v ok=%v", picked, ok)
	}
	if calls != 1 {
		t.Fatalf("byBranch calls = %d, want 1", calls)
	}

	rc.WorkBranch = "  "
	if _, ok := pickRepositoryDeliveryMR(rc, candidates, byBranch); !ok || calls != 1 {
		t.Fatalf("blank work branch must skip byBranch: ok=%v calls=%d", ok, calls)
	}
	if picked, ok := pickRepositoryDeliveryMR(rc, candidates, nil); !ok || picked.IID != 5 {
		t.Fatalf("nil byBranch must still use candidates: %+v ok=%v", picked, ok)
	}
}

func TestPickRepositoryDeliveryMRReportsMissing(t *testing.T) {
	rc := RepositoryContext{RepositoryID: "repo-c", GongfengProjectID: "g/c", WorkBranch: "feature/x"}
	candidates := []artifactMRCandidate{{mr: DeliveryMR{Project: "g/a", IID: 1}}}
	picked, ok := pickRepositoryDeliveryMR(rc, candidates, func(RepositoryContext) (DeliveryMR, bool) { return DeliveryMR{IID: 7}, false })
	if ok || picked != (DeliveryMR{}) {
		t.Fatalf("want no MR for repository without match, got %+v ok=%v", picked, ok)
	}
}
