package domain

import (
	"testing"
	"time"
)

func TestRunTimingEndFallbacks(t *testing.T) {
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	now := base.Add(time.Hour)
	terminal, last, updated := base.Add(10*time.Minute), base.Add(20*time.Minute), base.Add(30*time.Minute)
	cases := []struct {
		name                  string
		status                string
		terminalAt, lastEvent time.Time
		updatedAt             time.Time
		want                  time.Time
	}{
		{"open run uses now", "running", terminal, last, updated, now},
		{"terminal event wins", "succeeded", terminal, last, updated, terminal},
		{"falls back to last event", "failed", time.Time{}, last, updated, last},
		{"falls back to updatedAt", "cancelled", time.Time{}, time.Time{}, updated, updated},
		{"falls back to now", "succeeded", time.Time{}, time.Time{}, time.Time{}, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := Run{Status: tc.status, UpdatedAt: tc.updatedAt}
			if got := runTimingEnd(run, tc.terminalAt, tc.lastEvent, now); !got.Equal(tc.want) {
				t.Fatalf("runTimingEnd = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMergeExecutionIntervalsClipsAndUnions(t *testing.T) {
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	intervals := []timingInterval{
		{at(20), at(30)},
		{at(-5), at(5)},  // clipped to createdAt
		{at(25), at(40)}, // overlaps the first one
		{at(50), at(70)}, // clipped to end
		{at(80), at(90)}, // entirely after end: dropped
	}
	merged, total, known := mergeExecutionIntervals(intervals, at(0), at(60))
	if !known {
		t.Fatal("known = false, want true")
	}
	want := []RunExecutionInterval{{at(0), at(5)}, {at(20), at(40)}, {at(50), at(60)}}
	if len(merged) != len(want) {
		t.Fatalf("merged = %v, want %v", merged, want)
	}
	for i := range want {
		if !merged[i].StartedAt.Equal(want[i].StartedAt) || !merged[i].FinishedAt.Equal(want[i].FinishedAt) {
			t.Fatalf("merged[%d] = %v, want %v", i, merged[i], want[i])
		}
	}
	if total != 35*time.Minute {
		t.Fatalf("total = %v, want 35m", total)
	}
}

func TestMergeExecutionIntervalsKnownZero(t *testing.T) {
	base := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	merged, total, known := mergeExecutionIntervals([]timingInterval{{base, base}}, time.Time{}, base.Add(time.Minute))
	if !known || total != 0 || len(merged) != 0 {
		t.Fatalf("got merged=%v total=%v known=%v, want empty, 0, true", merged, total, known)
	}
	if _, _, known := mergeExecutionIntervals(nil, time.Time{}, base); known {
		t.Fatal("known = true for no intervals")
	}
}
