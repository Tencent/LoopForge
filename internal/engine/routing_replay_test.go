package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestConditionalRoutingStartRechecksSelection(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := New(s, runtime.NewManager(t.TempDir()))
	wf := routingWorkflow("recheck-selection")
	node := domain.WorkflowNode{ID: "large", Type: "agent", AgentID: "missing-role"}
	wf.Nodes[2] = node
	wf.RoutingRoleFingerprints = map[string]string{node.ID: "old-fingerprint"}
	r := domain.Run{ID: "recheck-selection", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Nodes: map[string]*domain.NodeRun{
		"classify": {NodeID: "classify", Status: "succeeded", RouteDecision: &domain.RouteDecision{EdgeKeyVersion: 1, SelectedEdges: map[string]bool{"id:large-edge": true}}},
		"large":    {NodeID: "large", Status: "pending"},
	}}
	selected := readyNodes(wf, r)
	if len(selected) != 1 || selected[0].ID != node.ID {
		t.Fatalf("expected the old scheduler selection to contain Large: %+v", selected)
	}
	// Rework has reset the upstream before the queued worker can start.
	r.Nodes["classify"].Status, r.Nodes["classify"].RouteDecision = "pending", nil
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	if _, err := e.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	e.executeNode(context.Background(), r.ID, wf, selected[0])
	got, ok := s.RunState(r.ID)
	if !ok || got.Nodes[node.ID].Status != "pending" || got.Nodes[node.ID].Error != "" || got.Nodes[node.ID].Attempt != 0 {
		t.Fatalf("stale selection must not start or fail Large: %+v", got.Nodes[node.ID])
	}
}

func TestConditionalRoutingReplayChecksOnlySelectedRoles(t *testing.T) {
	for _, tc := range []struct {
		name, routes, status, wantError string
		reject, legacy                  bool
		wantLargeAttempt                int
	}{
		{name: "retry keeps Small", routes: "small small", status: "succeeded"},
		{name: "retry now selects Large", routes: "small large", status: "failed", wantError: "路由角色配置已变更"},
		{name: "reject keeps Small", routes: "small small", status: "waiting", reject: true},
		{name: "reject now selects Large", routes: "small large", status: "failed", reject: true, wantError: "路由角色配置已变更"},
		{name: "legacy first visit to Large", routes: "small large", status: "succeeded", legacy: true, wantLargeAttempt: 1},
		{name: "legacy previously executed Large is skipped", routes: "large small", status: "succeeded", legacy: true, wantLargeAttempt: 1},
		{name: "legacy previously executed Large is selected", routes: "large large", status: "failed", legacy: true, wantLargeAttempt: 1, wantError: "没有保存原路由角色的配置"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			role := domain.AgentDefinition{ID: "large-router", Name: "Large router", ExecutorID: "mock",
				Verification: []domain.VerificationGate{{ID: "write", Name: "Write classification", Mode: "command", Required: true, ExecutionScope: domain.ExecutionScopeRun,
					Commands: []string{`printf '{"size":"large"}' > classification.json`}}}}
			if err := s.PutAgent(role); err != nil {
				t.Fatal(err)
			}
			first, second, _ := strings.Cut(tc.routes, " ")
			wf := routingWorkflow("selected-role-drift")
			wf.Nodes[0].Script = `case "$LOOPFORGE_ROUTE_FILE" in
				*/attempt-1.json) printf '{"route":"` + first + `"}' > "$LOOPFORGE_ROUTE_FILE" ;;
				*) printf '{"route":"` + second + `"}' > "$LOOPFORGE_ROUTE_FILE" ;;
				esac`
			wf.Nodes[2] = domain.WorkflowNode{ID: "large", Name: "Large", Type: "agent", AgentID: role.ID, ExecutorID: "mock", MaxAttempts: 3,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "classification", Path: "classification.json"}}}
			wf.Edges[2].Condition = &domain.WorkflowCondition{Type: "artifact", Artifact: "classification", Pointer: "/size", Equals: []byte(`"large"`)}
			if tc.reject {
				wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "approval", Type: "approval"})
				wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: "approval", Source: "delivery", Target: "approval"})
			}
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
			r, err := e.Start(wf.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			initialStatus := "succeeded"
			if tc.reject {
				initialStatus = "waiting"
			}
			waitFor(t, s, r.ID, initialStatus)
			before, ok := s.RunState(r.ID)
			if !ok || before.Nodes[first].Status != "succeeded" || before.WorkflowSnapshot.RoutingRoleFingerprints["large"] == "" {
				t.Fatalf("initial branch %s did not succeed with a frozen role: %+v", first, before.Nodes)
			}
			if tc.legacy {
				before.WorkflowSnapshot.RoutingRoleFingerprints = nil
				if err := s.PutRun(before); err != nil {
					t.Fatal(err)
				}
			} else {
				role.Verification[0].Commands[0] = `printf '{"size":"changed"}' > classification.json`
				if err := s.PutAgent(role); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reject {
				err = e.Reject(r.ID, domain.RejectRequest{NodeID: "approval", TargetID: "classify", Reason: "reroute"})
			} else {
				err = e.Retry(r.ID, "classify", "reroute", "")
			}
			if err != nil {
				t.Fatalf("upstream replay was rejected before selecting a branch: %v", err)
			}
			waitFor(t, s, r.ID, tc.status)
			after, ok := s.RunState(r.ID)
			if !ok {
				t.Fatal("run disappeared")
			}
			router, large := after.Nodes["classify"], after.Nodes["large"]
			if router.Attempt != 2 || router.RouteDecision == nil || !router.RouteDecision.SelectedEdges["id:"+second+"-edge"] {
				t.Fatalf("upstream script did not select %s again: %+v", second, router)
			}
			if large.Attempt != tc.wantLargeAttempt {
				t.Fatalf("Large attempt=%d, want %d", large.Attempt, tc.wantLargeAttempt)
			}
			switch {
			case tc.wantError != "":
				if large.Status != "failed" || !strings.Contains(large.Error, tc.wantError) || after.Nodes["implement"].Status != "pending" {
					t.Fatalf("Large should fail before execution, got %+v", large)
				}
				for _, event := range after.Events[len(before.Events):] {
					if event.NodeID == "large" && (event.Type == "node.started" || event.Type == "node.retry") {
						t.Fatalf("configuration failure unexpectedly started or retried Large: %+v", event)
					}
				}
			case second == "small":
				if large.Status != "skipped" || after.Nodes["small"].Status != "succeeded" {
					t.Fatalf("Small should complete and Large stay skipped: small=%+v large=%+v", after.Nodes["small"], large)
				}
			case large.Status != "succeeded":
				t.Fatalf("first execution of legacy Large should succeed, got %+v", large)
			}
		})
	}
}
