package domain

import (
	"testing"
	"time"
)

func TestClassifyMRDeliveryState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, state, target, def, integrate, want string
	}{
		{name: "mainline master", state: "merged", target: "master", def: "master", want: "merged"},
		{name: "mainline origin prefix", state: "merged", target: "origin/master", def: "master", want: "merged"},
		{name: "release default", state: "merged", target: "release/2.0", def: "release/2.0", want: "merged"},
		{name: "integrate baseline", state: "merged", target: "agentflow/integration", def: "master", integrate: "agentflow/integration", want: "integrated"},
		{name: "other side branch", state: "merged", target: "feature/x", def: "master", want: "integrated"},
		{name: "merged no target", state: "merged", target: "", def: "master", want: "unknown"},
		{name: "opened to main", state: "opened", target: "master", def: "master", want: "opened"},
		{name: "closed to integrate", state: "closed", target: "agentflow/integration", def: "master", want: "closed"},
		{name: "empty state", state: "", target: "master", def: "master", want: "unknown"},
		{name: "unknown default accepts main", state: "merged", target: "main", def: "", want: "merged"},
		{name: "unknown default accepts master", state: "merged", target: "master", def: "", want: "merged"},
		{name: "unknown default rejects integrate", state: "merged", target: "agentflow/integration", def: "", want: "integrated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyMRDeliveryState(tc.state, tc.target, tc.def, tc.integrate)
			if got != tc.want {
				t.Fatalf("ClassifyMRDeliveryState(%q,%q,%q) = %q, want %q", tc.state, tc.target, tc.def, got, tc.want)
			}
		})
	}
}

func TestFinalizeAnalyticsDeliveryRequiresMainline(t *testing.T) {
	t.Parallel()
	merged := AnalyticsDelivery{SOPSucceeded: true, MRURL: "http://x", MRState: "merged"}
	FinalizeAnalyticsDelivery(&merged)
	if !merged.Delivered {
		t.Fatal("mainline merged + SOP success must be delivered")
	}
	integrated := AnalyticsDelivery{SOPSucceeded: true, MRURL: "http://x", MRState: "integrated"}
	FinalizeAnalyticsDelivery(&integrated)
	if integrated.Delivered {
		t.Fatal("baseline merge must not count as delivered")
	}
}

func TestEffectiveAnalyticsMRStatePromotesIntegrated(t *testing.T) {
	t.Parallel()
	if got := EffectiveAnalyticsMRState("integrated", "merged"); got != "merged" {
		t.Fatalf("promoted integrated = %q", got)
	}
	if got := EffectiveAnalyticsMRState("integrated", "integrated"); got != "integrated" {
		t.Fatalf("unpromoted integrated = %q", got)
	}
	if got := EffectiveAnalyticsMRState("opened", "merged"); got != "opened" {
		t.Fatalf("opened must not inherit old mainline label = %q", got)
	}
}

func TestKeepMainlineDeliveryLabel(t *testing.T) {
	t.Parallel()
	if !KeepMainlineDeliveryLabel("merged", "integrated") {
		t.Fatal("must keep mainline after work MR still targets baseline")
	}
	if !KeepMainlineDeliveryLabel("merged", "unknown") {
		t.Fatal("must keep mainline while target_branch is still syncing")
	}
	if KeepMainlineDeliveryLabel("merged", "closed") {
		t.Fatal("closed work MR should be allowed to replace mainline")
	}
	if KeepMainlineDeliveryLabel("integrated", "merged") {
		t.Fatal("must not block an upgrade to mainline")
	}
}

func TestBranchListed(t *testing.T) {
	t.Parallel()
	refs := []string{"agentflow/integration", "feature/fix-issue-77", "origin/master"}
	if !BranchListed(refs, "master") {
		t.Fatal("origin/master should count as master")
	}
	if BranchListed(refs, "release") {
		t.Fatal("unrelated branch should not match")
	}
}

func TestWorkLandedWithPromote(t *testing.T) {
	t.Parallel()
	promote := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	if !WorkLandedWithPromote(promote.Add(-time.Hour), promote) {
		t.Fatal("work merged before promote must land")
	}
	if WorkLandedWithPromote(promote.Add(time.Hour), promote) {
		t.Fatal("work merged after promote must stay on baseline")
	}
	if !WorkLandedWithPromote(time.Time{}, promote) {
		t.Fatal("missing work time must not block promote")
	}
}

func TestSummarizeDeliveriesSplitsMainlineAndBaseline(t *testing.T) {
	t.Parallel()
	sum := SummarizeDeliveries([]AnalyticsDelivery{
		{SOPSucceeded: true, MRURL: "a", MRState: "merged", Delivered: true},
		{SOPSucceeded: true, MRURL: "b", MRState: "integrated", Delivered: false},
	})
	if sum.MRMerged != 1 || sum.MRIntegrated != 1 || sum.Delivered != 1 {
		t.Fatalf("summary=%+v", sum)
	}
}
