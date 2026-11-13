package domain

import (
	"testing"
	"time"
)

func TestScanTimingEventsTracksIntervalsAndTerminal(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	m := func(n int) time.Duration { return time.Duration(n) * time.Minute }
	events := timingEvents(t0,
		m(5), "b", "node.started", // listed first, sorted into place
		m(1), "a", "node.started",
		m(2), "a", "node.started", // retry closes the first attempt
		m(4), "a", "gate.waiting",
		m(6), "c", "node.started",
		m(8), "", "run.parked", // closes every open node
		m(9), "", "run.failed",
		m(10), "d", "node.started", // clears the earlier terminal event
	)
	events = append(events, RunEvent{Type: "node.started", NodeID: "z"}) // zero timestamp is ignored

	scan := scanTimingEvents(events)
	if !scan.started {
		t.Fatal("started = false, want true")
	}
	if !scan.lastEvent.Equal(t0.Add(m(10))) {
		t.Fatalf("lastEvent = %v, want +10m", scan.lastEvent)
	}
	if !scan.terminalAt.IsZero() {
		t.Fatalf("terminalAt = %v, want zero after the later node.started", scan.terminalAt)
	}
	var total time.Duration
	for _, iv := range scan.intervals {
		total += iv.end.Sub(iv.start)
	}
	// a: 1-2, 2-4; b: 5-8; c: 6-8 → 1+2+3+2 = 8m
	if len(scan.intervals) != 4 || total != m(8) {
		t.Fatalf("intervals = %+v (total %v), want 4 intervals totalling 8m", scan.intervals, total)
	}
	if len(scan.open) != 1 || !scan.open["d"].Equal(t0.Add(m(10))) {
		t.Fatalf("open = %+v, want only d at +10m", scan.open)
	}
	if _, ok := scan.open["z"]; ok {
		t.Fatal("zero-timestamp event opened a node")
	}
}

func TestScanTimingEventsKeepsFinalTerminal(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	scan := scanTimingEvents(timingEvents(t0,
		time.Minute, "a", "node.started",
		3*time.Minute, "", "run.cancelled",
	))
	if !scan.terminalAt.Equal(t0.Add(3*time.Minute)) || len(scan.open) != 0 {
		t.Fatalf("scan = %+v, want terminal at +3m and no open nodes", scan)
	}
	if len(scan.intervals) != 1 || scan.intervals[0].end.Sub(scan.intervals[0].start) != 2*time.Minute {
		t.Fatalf("intervals = %+v, want one 2m interval closed by the run event", scan.intervals)
	}
}

func TestScanTimingEventsWithoutNodeStarts(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	scan := scanTimingEvents(timingEvents(t0,
		time.Minute, "a", "node.succeeded",
		2*time.Minute, "", "run.succeeded",
	))
	if scan.started || len(scan.intervals) != 0 || len(scan.open) != 0 {
		t.Fatalf("scan = %+v, want no started nodes and no intervals", scan)
	}
	if !scan.terminalAt.Equal(t0.Add(2*time.Minute)) || !scan.lastEvent.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("terminalAt/lastEvent = %v/%v, want +2m", scan.terminalAt, scan.lastEvent)
	}
}

func TestNodeRowIntervals(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	intervals, open := nodeRowIntervals(map[string]*NodeRun{
		"done":    {Status: "succeeded", StartedAt: t0, FinishedAt: t0.Add(3 * time.Minute)},
		"running": {Status: "running", StartedAt: t0.Add(time.Minute)},
		"paused":  {Status: "paused", StartedAt: t0.Add(time.Minute)},
		"pending": {Status: "pending"},
		"nil":     nil,
	})
	if len(intervals) != 1 || !intervals[0].start.Equal(t0) || !intervals[0].end.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("intervals = %+v, want only the finished node", intervals)
	}
	if len(open) != 1 || !open["running"].Equal(t0.Add(time.Minute)) {
		t.Fatalf("open = %+v, want only the running node", open)
	}
}
