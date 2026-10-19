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

func runTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

// ComputeRunTiming derives timing from the run event log. Node rows only keep
// the latest attempt, and approval waits leave FinishedAt at the decision
// time, so the event log is the only source that covers every attempt. Runs
// with no node.started events fall back to node rows.
func ComputeRunTiming(run Run, events []RunEvent, now time.Time) RunTiming {
	type interval struct{ start, end time.Time }
	var intervals []interval
	open := map[string]time.Time{}
	var lastEvent, terminalAt time.Time
	started := false
	closeNode := func(nodeID string, at time.Time) {
		if start, ok := open[nodeID]; ok {
			intervals = append(intervals, interval{start, at})
			delete(open, nodeID)
		}
	}
	sorted := append([]RunEvent(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Timestamp.Before(sorted[j].Timestamp) })
	for _, ev := range sorted {
		at := ev.Timestamp
		if at.IsZero() {
			continue
		}
		lastEvent = at
		switch {
		case ev.Type == "node.started" && ev.NodeID != "":
			started = true
			closeNode(ev.NodeID, at)
			open[ev.NodeID] = at
		case nodeIntervalClosers[ev.Type] && ev.NodeID != "":
			closeNode(ev.NodeID, at)
		case runIntervalClosers[ev.Type]:
			for nodeID := range open {
				closeNode(nodeID, at)
			}
		}
		if runTerminalEvents[ev.Type] {
			terminalAt = at
		} else if ev.Type == "node.started" {
			terminalAt = time.Time{}
		}
	}

	out := RunTiming{AsOf: now}
	end := now
	if runTerminal(run.Status) {
		end = terminalAt
		if end.IsZero() {
			end = lastEvent
		}
		if end.IsZero() {
			end = run.UpdatedAt
		}
		if end.IsZero() {
			end = now
		}
	}
	if !started {
		intervals = intervals[:0]
		open = map[string]time.Time{}
		for nodeID, node := range run.Nodes {
			if node == nil || node.StartedAt.IsZero() {
				continue
			}
			switch {
			case !node.FinishedAt.IsZero():
				intervals = append(intervals, interval{node.StartedAt, node.FinishedAt})
			case node.Status == "running":
				open[nodeID] = node.StartedAt
			}
		}
	}
	openEnd := lastEvent
	if run.Status == "running" {
		openEnd = end
		out.Executing = len(open) > 0
	}
	for _, start := range open {
		if openEnd.After(start) {
			intervals = append(intervals, interval{start, openEnd})
		}
	}

	var clipped []interval
	for _, iv := range intervals {
		if !run.CreatedAt.IsZero() && iv.start.Before(run.CreatedAt) {
			iv.start = run.CreatedAt
		}
		if iv.end.After(end) {
			iv.end = end
		}
		if iv.end.After(iv.start) {
			clipped = append(clipped, iv)
		}
	}
	sort.Slice(clipped, func(i, j int) bool { return clipped[i].start.Before(clipped[j].start) })
	var total time.Duration
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
	}
	out.ExecutionMs = total.Milliseconds()
	if !run.CreatedAt.IsZero() && end.After(run.CreatedAt) {
		out.WallMs = end.Sub(run.CreatedAt).Milliseconds()
	}
	if out.WallMs < out.ExecutionMs {
		out.WallMs = out.ExecutionMs
	}
	out.WaitingMs = out.WallMs - out.ExecutionMs
	return out
}
