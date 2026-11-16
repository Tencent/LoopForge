package repo

import "testing"

func TestAnalyticsUserEntryReusesAndDefaultsUnassigned(t *testing.T) {
	values := map[string]*analyticsUserAccumulator{}
	first := analyticsUserEntry(values, "", "2026-11-13")
	if first.item.UserID != "unassigned" || first.item.Date != "2026-11-13" || !first.item.CostKnown {
		t.Fatalf("unexpected new user entry: %+v", first.item)
	}
	if again := analyticsUserEntry(values, "unassigned", "2026-11-13"); again != first {
		t.Fatalf("expected the same accumulator for the same user and day")
	}
	if other := analyticsUserEntry(values, "unassigned", "2026-11-14"); other == first {
		t.Fatalf("expected a new accumulator for another day")
	}
	if len(values) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(values))
	}
}

func TestAnalyticsProjectEntryReusesAndDefaultsUnassigned(t *testing.T) {
	values := map[string]*analyticsProjectAccumulator{}
	first := analyticsProjectEntry(values, "")
	if first.item.ProjectID != "unassigned" || !first.item.CostKnown || first.issues == nil {
		t.Fatalf("unexpected new project entry: %+v", first)
	}
	if again := analyticsProjectEntry(values, "unassigned"); again != first {
		t.Fatalf("expected the same accumulator for the same project")
	}
	if analyticsProjectEntry(values, "p1") == first || len(values) != 2 {
		t.Fatalf("expected a separate accumulator per project, got %d entries", len(values))
	}
}

func TestAnalyticsSourceEntryReusesAndDefaultsLocal(t *testing.T) {
	values := map[string]*analyticsAccumulator{}
	first := analyticsSourceEntry(values, "")
	if first.item.Key != "local" || first.item.Name != "local" {
		t.Fatalf("unexpected new source entry: %+v", first.item)
	}
	if again := analyticsSourceEntry(values, "local"); again != first {
		t.Fatalf("expected the same accumulator for the same source")
	}
	if analyticsSourceEntry(values, "tapd") == first || len(values) != 2 {
		t.Fatalf("expected a separate accumulator per source, got %d entries", len(values))
	}
}
