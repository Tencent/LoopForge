package repo

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestAnalyticsCountsModelErrorEvents(t *testing.T) {
	store, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	run := domain.Run{ID: "model-errors", Status: "failed", CreatedAt: now, UpdatedAt: now, Events: []domain.RunEvent{
		{ID: "a", RunID: "model-errors", Type: "model.error", Timestamp: now, Attributes: map[string]any{"class": "rate_limited"}},
		{ID: "b", RunID: "model-errors", Type: "model.retry_waiting", Timestamp: now, Attributes: map[string]any{"class": "rate_limited"}},
		{ID: "c", RunID: "model-errors", Type: "model.error", Timestamp: now, Attributes: map[string]any{"class": "auth"}},
	}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	got := store.AnalyticsRepo.analyticsModelErrors([]string{run.ID})
	if len(got) != 2 || got[0].Class != "auth" || got[0].Failures != 1 || got[1].Class != "rate_limited" || got[1].Failures != 1 || got[1].Retries != 1 {
		t.Fatalf("counts = %+v", got)
	}
}
