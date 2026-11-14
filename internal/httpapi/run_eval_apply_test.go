package httpapi

import (
	"testing"

	"agentflow/internal/domain"
)

func TestRecommendationIDSetTrimsAndKeepsBlank(t *testing.T) {
	got := recommendationIDSet([]string{" rec-1 ", "rec-2", "  "})
	if len(got) != 3 || !got["rec-1"] || !got["rec-2"] || !got[""] {
		t.Fatalf("unexpected id set: %#v", got)
	}
	if empty := recommendationIDSet(nil); len(empty) != 0 {
		t.Fatalf("nil ids produced %#v", empty)
	}
}

func TestApplySelectedRecommendationsSkipsAndMarksUnapplyable(t *testing.T) {
	h := &runsHandlers{}
	recs := []domain.RunEvalRecommendation{
		{ID: "skip", Target: "agent", Action: "append_instructions", TargetID: "a"},
		{ID: "done", Status: domain.RunEvalRecApplied, ApplyError: "keep"},
		{ID: "view", Target: "run", Action: "note"},
	}
	wanted := map[string]bool{"done": true, "view": true}
	if !h.applySelectedRecommendations(domain.Run{}, "prj", recs, wanted) {
		t.Fatal("expected change for unapplyable recommendation")
	}
	if recs[0].ApplyError != "" || recs[0].Status != "" {
		t.Fatalf("unselected recommendation touched: %#v", recs[0])
	}
	if recs[1].ApplyError != "keep" || recs[1].Status != domain.RunEvalRecApplied {
		t.Fatalf("applied recommendation touched: %#v", recs[1])
	}
	if recs[2].ApplyError != "这条建议只能查看，不能一键应用" {
		t.Fatalf("unapplyable recommendation error = %q", recs[2].ApplyError)
	}
}

func TestApplySelectedRecommendationsReportsNoChange(t *testing.T) {
	h := &runsHandlers{}
	recs := []domain.RunEvalRecommendation{{ID: "done", Status: domain.RunEvalRecApplied}}
	if h.applySelectedRecommendations(domain.Run{}, "prj", recs, map[string]bool{"done": true, "missing": true}) {
		t.Fatal("expected no change")
	}
}
