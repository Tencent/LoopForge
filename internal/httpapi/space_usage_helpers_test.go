package httpapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSpaceNodeSeconds(t *testing.T) {
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	now := start.Add(90 * time.Second)
	cases := []struct {
		name string
		node spaceLaneNode
		want int64
	}{
		{"finished", spaceLaneNode{StartedAt: start, FinishedAt: start.Add(42500 * time.Millisecond)}, 42},
		{"running counts up to now", spaceLaneNode{StartedAt: start}, 90},
		{"finished before start", spaceLaneNode{StartedAt: start, FinishedAt: start.Add(-time.Second)}, 0},
		{"zero length", spaceLaneNode{StartedAt: start, FinishedAt: start}, 0},
	}
	for _, tc := range cases {
		if got := spaceNodeSeconds(tc.node, now); got != tc.want {
			t.Errorf("%s: spaceNodeSeconds = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestSortedSpaceUsageDays(t *testing.T) {
	days := map[string]map[string]int64{
		"2026-11-14": {"deliver": 30},
		"2026-11-12": {"deliver": 10, "review": 5},
		"2026-11-13": {"review": 20},
	}
	got := sortedSpaceUsageDays(days)
	wantOrder := []string{"2026-11-12", "2026-11-13", "2026-11-14"}
	if len(got) != len(wantOrder) {
		t.Fatalf("days = %+v", got)
	}
	for i, day := range wantOrder {
		if got[i].Day != day {
			t.Fatalf("day %d = %q, want %q (%+v)", i, got[i].Day, day, got)
		}
	}
	if got[0].Lanes["review"] != 5 || got[0].Lanes["deliver"] != 10 {
		t.Fatalf("lanes of first day = %+v", got[0].Lanes)
	}

	empty := sortedSpaceUsageDays(nil)
	if empty == nil {
		t.Fatal("empty input must return a non-nil slice")
	}
	raw, err := json.Marshal(empty)
	if err != nil || string(raw) != "[]" {
		t.Fatalf("empty days encode as %s (%v), want []", raw, err)
	}
}
