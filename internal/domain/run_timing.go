package domain

import (
	"sort"
	"time"
)

// RunTiming separates time a run actually spent executing nodes from the
// wall clock since it was created. The wall clock also contains time the run
// sat failed, paused, parked, or waiting for a human, which can be days.
type RunTiming struct {
	// ExecutionMs is the union of node execution intervals across every
	// attempt, so retries count and parallel nodes do not double count.
	ExecutionMs int64 `json:"executionMs"`
	// WallMs runs from creation to the terminal run event, or to AsOf while
	// the run is still open.
	WallMs    int64 `json:"wallMs"`
	WaitingMs int64 `json:"waitingMs"`
	// Executing reports that a node is running at AsOf, so clients can keep
	// advancing ExecutionMs locally between refreshes.
	Executing bool      `json:"executing,omitempty"`
	AsOf      time.Time `json:"asOf"`
}

// RunTimingEventTypes lists the events ComputeRunTiming reads. Callers that
// load events only for timing can filter on these.
var RunTimingEventTypes = []string{
	"node.started", "node.succeeded", "node.failed", "node.paused", "node.cancelled",
	"node.interrupted", "node.passed", "gate.waiting", "gate.auto_approved",
	"run.parked", "run.paused", "run.failed", "run.cancelled", "run.succeeded",
}

var nodeIntervalClosers = map[string]bool{
	"node.succeeded": true, "node.failed": true, "node.paused": true, "node.cancelled": true,
	"node.interrupted": true, "node.passed": true, "gate.waiting": true, "gate.auto_approved": true,
}

var runIntervalClosers = map[string]bool{
	"run.parked": true, "run.paused": true, "run.failed": true, "run.cancelled": true, "run.succeeded": true,
}

var runTerminalEvents = map[string]bool{"run.failed": true, "run.cancelled": true, "run.succeeded": true}

// ComputeRunTiming derives timing from the run event log. Node rows only keep
// the latest attempt, and approval waits leave FinishedAt at the decision
// time, so the event log is the only source that covers every attempt. Runs
// with no node.started events fall back to node rows.
func ComputeRunTiming(run Run, events []RunEvent, now time.Time) RunTiming {
	timing, _, _ := computeRunTiming(run, events, now)
	return timing
}

// RunExecutionInterval is one observed span of node execution, excluding waits.
type RunExecutionInterval struct {
	StartedAt  time.Time
	FinishedAt time.Time
}

// RunExecutionIntervals shares the timing calculation with ComputeRunTiming.
// The boolean distinguishes an observed zero from missing timing evidence.
func RunExecutionIntervals(run Run, events []RunEvent, now time.Time) ([]RunExecutionInterval, bool) {
	_, intervals, known := computeRunTiming(run, events, now)
	return intervals, known
}

type timingInterval struct{ start, end time.Time }

func computeRunTiming(run Run, events []RunEvent, now time.Time) (RunTiming, []RunExecutionInterval, bool) {
	scan := scanTimingEvents(events)
	intervals, open, lastEvent := scan.intervals, scan.open, scan.lastEvent

	out := RunTiming{AsOf: now}
	end := runTimingEnd(run, scan.terminalAt, lastEvent, now)
	if !scan.started {
		intervals, open = nodeRowIntervals(run.Nodes)
	}
	openEnd := lastEvent
	if run.Status == "running" {
		openEnd = end
		out.Executing = len(open) > 0
	}
	for _, start := range open {
		if openEnd.After(start) {
			intervals = append(intervals, timingInterval{start, openEnd})
		}
	}

	merged, total, known := mergeExecutionIntervals(intervals, run.CreatedAt, end)
	out.ExecutionMs = total.Milliseconds()
	if !run.CreatedAt.IsZero() && end.After(run.CreatedAt) {
		out.WallMs = end.Sub(run.CreatedAt).Milliseconds()
	}
	if out.WallMs < out.ExecutionMs {
		out.WallMs = out.ExecutionMs
	}
	out.WaitingMs = out.WallMs - out.ExecutionMs
	return out, merged, known
}

// timingEventScan is what one pass over the run event log observed: closed
// node execution intervals, nodes still open at the last event, the last and
// terminal event times, and whether any node.started event was seen.
type timingEventScan struct {
	intervals             []timingInterval
	open                  map[string]time.Time
	lastEvent, terminalAt time.Time
	started               bool
}

// scanTimingEvents walks events in timestamp order, opening an interval on
// node.started and closing it on node closers or, for every open node, on run
// closers. A later node.started clears an earlier terminal run event, so a
// resumed run is not treated as finished. Events without a timestamp are
// ignored.
func scanTimingEvents(events []RunEvent) timingEventScan {
	scan := timingEventScan{open: map[string]time.Time{}}
	closeNode := func(nodeID string, at time.Time) {
		if start, ok := scan.open[nodeID]; ok {
			scan.intervals = append(scan.intervals, timingInterval{start, at})
			delete(scan.open, nodeID)
		}
	}
	sorted := append([]RunEvent(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	for _, ev := range sorted {
		at := ev.Timestamp
		if at.IsZero() {
			continue
		}
		scan.lastEvent = at
		switch {
		case ev.Type == "node.started" && ev.NodeID != "":
			scan.started = true
			closeNode(ev.NodeID, at)
			scan.open[ev.NodeID] = at
		case nodeIntervalClosers[ev.Type] && ev.NodeID != "":
			closeNode(ev.NodeID, at)
		case runIntervalClosers[ev.Type]:
			for nodeID := range scan.open {
				closeNode(nodeID, at)
			}
		}
		if runTerminalEvents[ev.Type] {
			scan.terminalAt = at
		} else if ev.Type == "node.started" {
			scan.terminalAt = time.Time{}
		}
	}
	return scan
}

// nodeRowIntervals rebuilds execution intervals from node rows for runs whose
// event log has no node.started events: finished nodes give a closed interval
// and running nodes stay open from their StartedAt.
func nodeRowIntervals(nodes map[string]*NodeRun) ([]timingInterval, map[string]time.Time) {
	var intervals []timingInterval
	open := map[string]time.Time{}
	for nodeID, node := range nodes {
		if node == nil || node.StartedAt.IsZero() {
			continue
		}
		switch {
		case !node.FinishedAt.IsZero():
			intervals = append(intervals, timingInterval{node.StartedAt, node.FinishedAt})
		case node.Status == "running":
			open[nodeID] = node.StartedAt
		}
	}
	return intervals, open
}

// runTimingEnd picks the instant a run's timing stops: now while the run is
// open, otherwise the terminal run event, falling back to the last event, the
// run's UpdatedAt, and finally now.
func runTimingEnd(run Run, terminalAt, lastEvent, now time.Time) time.Time {
	if !TerminalRunStatus(run.Status) {
		return now
	}
	for _, at := range []time.Time{terminalAt, lastEvent, run.UpdatedAt} {
		if !at.IsZero() {
			return at
		}
	}
	return now
}

// mergeExecutionIntervals clips intervals to [createdAt, end], unions the
// overlapping ones, and returns the merged spans with their total duration.
// known reports whether any interval was a valid observation, so an observed
// zero can be told apart from missing timing evidence.
func mergeExecutionIntervals(intervals []timingInterval, createdAt, end time.Time) ([]RunExecutionInterval, time.Duration, bool) {
	var clipped []timingInterval
	known := false
	for _, iv := range intervals {
		if !createdAt.IsZero() && iv.start.Before(createdAt) {
			iv.start = createdAt
		}
		if iv.end.After(end) {
			iv.end = end
		}
		if !iv.start.IsZero() && !iv.end.IsZero() && !iv.end.Before(iv.start) {
			known = true
		}
		if iv.end.After(iv.start) {
			clipped = append(clipped, iv)
		}
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].start.Before(clipped[j].start) })
	var total time.Duration
	var merged []RunExecutionInterval
	for i := 0; i < len(clipped); {
		cur := clipped[i]
		i++
		for i < len(clipped) && !clipped[i].start.After(cur.end) {
			if clipped[i].end.After(cur.end) {
				cur.end = clipped[i].end
			}
			i++
		}
		total += cur.end.Sub(cur.start)
		merged = append(merged, RunExecutionInterval{StartedAt: cur.start, FinishedAt: cur.end})
	}
	return merged, total, known
}
