package engine

import (
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestHasActiveNode(t *testing.T) {
	for _, active := range []string{"running", "waiting_approval", "pending_input"} {
		run := domain.Run{Nodes: map[string]*domain.NodeRun{
			"done": {NodeID: "done", Status: "succeeded"},
			"live": {NodeID: "live", Status: active},
		}}
		if !hasActiveNode(run) {
			t.Fatalf("%s node not treated as active", active)
		}
	}
	settled := domain.Run{Nodes: map[string]*domain.NodeRun{
		"nil":       nil,
		"failed":    {NodeID: "failed", Status: "failed"},
		"cancelled": {NodeID: "cancelled", Status: "cancelled"},
		"pending":   {NodeID: "pending", Status: "pending"},
	}}
	if hasActiveNode(settled) {
		t.Fatal("settled nodes treated as active")
	}
	if hasActiveNode(domain.Run{}) {
		t.Fatal("run without nodes treated as active")
	}
}

func TestCancelRunningNodesConvergesOnlyRunningNodes(t *testing.T) {
	finished := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	run := &domain.Run{ID: "run-cancel", Nodes: map[string]*domain.NodeRun{
		"nil":     nil,
		"blank":   {NodeID: "blank", Status: "running"},
		"kept":    {NodeID: "kept", Status: "running", FinishedAt: finished, Activity: "执行测试"},
		"failed":  {NodeID: "failed", Status: "failed"},
		"waiting": {NodeID: "waiting", Status: "waiting_approval"},
	}}
	(&Engine{}).cancelRunningNodes(run)

	blank := run.Nodes["blank"]
	if blank.Status != "cancelled" || blank.FinishedAt.IsZero() || blank.Activity != "运行终止时仍在执行，已收敛为已取消" {
		t.Fatalf("blank running node = %+v", blank)
	}
	kept := run.Nodes["kept"]
	if kept.Status != "cancelled" || !kept.FinishedAt.Equal(finished) || kept.Activity != "执行测试" {
		t.Fatalf("running node with existing details = %+v", kept)
	}
	if run.Nodes["failed"].Status != "failed" || run.Nodes["waiting"].Status != "waiting_approval" {
		t.Fatalf("non-running nodes changed: failed=%q waiting=%q", run.Nodes["failed"].Status, run.Nodes["waiting"].Status)
	}
	cancelledEvents := map[string]bool{}
	for _, event := range run.Events {
		if event.Type != "node.cancelled" || event.RunID != run.ID || event.Message != "运行终止，节点由执行中收敛为已取消" {
			t.Fatalf("unexpected event %+v", event)
		}
		cancelledEvents[event.NodeID] = true
	}
	if len(run.Events) != 2 || !cancelledEvents["blank"] || !cancelledEvents["kept"] {
		t.Fatalf("events = %+v, want node.cancelled for blank and kept", run.Events)
	}
}
