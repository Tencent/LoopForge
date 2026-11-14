package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The usage endpoint must sum spaceNodeSeconds over every started node of a
// lane (running nodes up to the request time), roll the lane totals into the
// space total, and return the per-day token buckets from sortedSpaceUsageDays.
func TestSpaceUsageAggregatesNodeSecondsAndDays(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	const projectID = "cvb-usage-wiring"
	if err := f.s.PutProject(domain.Project{ID: projectID, Key: "CUW", Name: "Usage wiring", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	room := domain.ChatRoom{ID: "cvb-usage-space", ProjectID: projectID, Name: "Usage", CreatedBy: f.user.ID}
	if err := f.s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	dayA := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	dayB := time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local)
	putLane := func(runID, wfID, wfName string, created time.Time, nodes map[string]*domain.NodeRun) {
		t.Helper()
		wf := domain.WorkflowDefinition{ID: wfID, Name: wfName}
		for id := range nodes {
			wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: id, Name: id, Type: "approval"})
		}
		wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "not-started", Name: "not-started", Type: "approval"})
		run := domain.Run{ID: runID, WorkflowID: wfID, Status: "running", CreatedAt: created, UpdatedAt: now, Revision: 1, WorkflowSnapshot: wf, Nodes: nodes}
		if err := f.s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		msg := domain.ChatMessage{ID: "dispatch-" + runID, RoomID: room.ID, UserID: f.user.ID, Author: "owner", Kind: domain.ChatKindDispatch,
			RunID: runID, WorkflowID: wfID, WorkflowName: wfName, Body: "dispatch", CreatedAt: created}
		if err := f.s.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	// Lane A: two finished nodes (10s + 5s) on dayA.
	putLane("cvb-usage-run-a", "wf-a", "Lane A", dayA, map[string]*domain.NodeRun{
		"a1": {NodeID: "a1", Status: "succeeded", StartedAt: dayA, FinishedAt: dayA.Add(10 * time.Second), Usage: domain.TokenUsage{TotalTokens: 7}},
		"a2": {NodeID: "a2", Status: "succeeded", StartedAt: dayA.Add(time.Minute), FinishedAt: dayA.Add(time.Minute + 5*time.Second), Usage: domain.TokenUsage{TotalTokens: 4}},
	})
	// Lane B: a finished 20s node on dayB and a node still running for ~30s.
	putLane("cvb-usage-run-b", "wf-b", "Lane B", dayB, map[string]*domain.NodeRun{
		"b1": {NodeID: "b1", Status: "succeeded", StartedAt: dayB, FinishedAt: dayB.Add(20 * time.Second), Usage: domain.TokenUsage{TotalTokens: 9}},
		"b2": {NodeID: "b2", Status: "running", StartedAt: now.Add(-30 * time.Second), Usage: domain.TokenUsage{TotalTokens: 2}},
	})

	rec := cvBRequest(t, f, http.MethodGet, "/api/spaces/"+room.ID+"/usage", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Seconds int64 `json:"seconds"`
		Lanes   []struct {
			RunID   string `json:"runId"`
			Seconds int64  `json:"seconds"`
			Nodes   []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"lanes"`
		Days []spaceUsageDay `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode usage: %v (%s)", err, rec.Body.String())
	}
	seconds := map[string]int64{}
	for _, l := range got.Lanes {
		seconds[l.RunID] = l.Seconds
		if len(l.Nodes) != 2 {
			t.Errorf("lane %s nodes = %+v, want only the two started nodes", l.RunID, l.Nodes)
		}
	}
	if seconds["cvb-usage-run-a"] != 15 {
		t.Errorf("lane A seconds = %d, want 10+5=15", seconds["cvb-usage-run-a"])
	}
	// The running node counts up to the request time (~30s, allow slack for slow CI).
	if b := seconds["cvb-usage-run-b"]; b < 20+29 || b > 20+120 {
		t.Errorf("lane B seconds = %d, want 20 + ~30 for the running node", b)
	}
	if got.Seconds != seconds["cvb-usage-run-a"]+seconds["cvb-usage-run-b"] {
		t.Errorf("space seconds = %d, want sum of lanes %v", got.Seconds, seconds)
	}

	today := now.Local().Format("2006-01-02")
	want := []spaceUsageDay{
		{Day: "2026-11-10", Lanes: map[string]int64{"Lane A": 11}},
		{Day: "2026-11-12", Lanes: map[string]int64{"Lane B": 9}},
		{Day: today, Lanes: map[string]int64{"Lane B": 2}},
	}
	if len(got.Days) != len(want) {
		t.Fatalf("days = %+v, want %+v", got.Days, want)
	}
	for i, w := range want {
		g := got.Days[i]
		if g.Day != w.Day || len(g.Lanes) != len(w.Lanes) {
			t.Fatalf("day %d = %+v, want %+v (all: %+v)", i, g, w, got.Days)
		}
		for lane, tokens := range w.Lanes {
			if g.Lanes[lane] != tokens {
				t.Errorf("day %s lane %q tokens = %d, want %d", w.Day, lane, g.Lanes[lane], tokens)
			}
		}
	}
}
