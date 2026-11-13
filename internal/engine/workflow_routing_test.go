package engine

import (
	"agentflow/internal/domain"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func routingWorkflow(route string) domain.WorkflowDefinition {
	return domain.WorkflowDefinition{ID: "routing-" + route, Name: "routing", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{
		{ID: "classify", Type: "script", Script: `printf '{"route":"` + route + `"}' > "$LOOPFORGE_ROUTE_FILE"`, MaxAttempts: 1},
		{ID: "small", Type: "script", Script: "echo small", MaxAttempts: 1},
		{ID: "large", Type: "script", Script: "echo large", MaxAttempts: 1},
		{ID: "implement", Type: "script", Script: "echo implement", MaxAttempts: 1},
		{ID: "delivery", Type: "script", Script: "echo delivery", MaxAttempts: 1, JoinRule: "none_failed_min_one_success"},
	}, Edges: []domain.WorkflowEdge{
		{ID: "small-edge", Source: "classify", Target: "small", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"small"}}},
		{ID: "large-edge", Source: "classify", Target: "large", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"large"}}},
		{ID: "implement-edge", Source: "large", Target: "implement"},
		{ID: "join-small", Source: "small", Target: "delivery"},
		{ID: "join-large", Source: "implement", Target: "delivery"},
	}}
}

func TestConditionalRoutingScriptEndToEnd(t *testing.T) {
	for _, choice := range []string{"small", "large"} {
		t.Run(choice, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			wf := routingWorkflow(choice)
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
			r, err := e.Start(wf.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, r.ID, "succeeded")
			got, _ := s.Run(r.ID)
			wantSmall, wantLarge := "succeeded", "skipped"
			if choice == "large" {
				wantSmall, wantLarge = "skipped", "succeeded"
			}
			if got.Nodes["small"].Status != wantSmall || got.Nodes["large"].Status != wantLarge || got.Nodes["implement"].Status != wantLarge || got.Nodes["delivery"].Status != "succeeded" {
				t.Fatalf("nodes: %+v %+v %+v %+v", got.Nodes["small"], got.Nodes["large"], got.Nodes["implement"], got.Nodes["delivery"])
			}
			for _, id := range []string{"small", "large", "implement"} {
				n := got.Nodes[id]
				if n.Status == "skipped" && (n.Attempt != 0 || n.SkipReason == "" || n.Usage.TotalTokens != 0) {
					t.Fatalf("skipped %s: %+v", id, n)
				}
			}
		})
	}
}

func TestConditionalRoutingArtifactEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, script, wantSmall, wantLarge string
	}{
		{"match", `printf '{"size":"small"}' > classification.json`, "succeeded", "skipped"},
		{"missing file", `echo no-artifact`, "skipped", "succeeded"},
		{"missing field", `printf '{}' > classification.json`, "skipped", "succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			wf := routingWorkflow("artifact")
			wf.Nodes[0].Script = tc.script
			wf.Nodes[0].ArtifactOutputs = []domain.ArtifactContract{{Name: "classification", Path: "classification.json"}}
			wf.Nodes[0].DefaultTarget = "large"
			wf.Edges[0].Condition = &domain.WorkflowCondition{Type: "artifact", Artifact: "classification", Pointer: "/size", Equals: json.RawMessage(`"small"`)}
			wf.Edges[1].Condition = nil
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
			r, err := e.Start(wf.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, r.ID, "succeeded")
			got, _ := s.Run(r.ID)
			if got.Nodes["small"].Status != tc.wantSmall || got.Nodes["large"].Status != tc.wantLarge {
				t.Fatalf("small=%+v large=%+v", got.Nodes["small"], got.Nodes["large"])
			}
		})
	}
}

func TestConditionalRoutingJoinAndArtifacts(t *testing.T) {
	wf := routingWorkflow("small")
	wf.Nodes[4].JoinRule = ""
	r := domain.Run{WorkflowSnapshot: wf, Nodes: map[string]*domain.NodeRun{"classify": {Status: "succeeded"}, "small": {Status: "succeeded"}, "large": {Status: "pending"}, "implement": {Status: "pending"}, "delivery": {Status: "pending"}}}
	d, err := decideRoute(wf, wf.Nodes[0], domain.RouteValues{"small"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Nodes["classify"].RouteDecision = d
	if !propagateSkipped(wf, &r) || r.Nodes["large"].Status != "skipped" || r.Nodes["implement"].Status != "skipped" || r.Nodes["delivery"].Status != "skipped" {
		t.Fatalf("skip propagation: %+v", r.Nodes)
	}
	if allSucceeded(wf, r) {
		t.Fatal("all-skipped sink must not succeed")
	}
	wf.Nodes[0].ArtifactOutputs = []domain.ArtifactContract{{Name: "classification", Path: "classification.json"}}
	wf.Edges[0].Condition = &domain.WorkflowCondition{Type: "artifact", Artifact: "classification", Pointer: "/size", Equals: json.RawMessage(`"small"`)}
	d, err = decideRoute(wf, wf.Nodes[0], nil, []domain.Artifact{{Name: "classification", Content: `{"size":"small"}`}})
	if err != nil || !d.SelectedEdges["0"] {
		t.Fatalf("artifact condition: %+v %v", d, err)
	}
	_, err = decideRoute(wf, wf.Nodes[0], nil, []domain.Artifact{{Name: "classification", Content: `{}`}})
	if err == nil || !strings.Contains(err.Error(), "没有匹配") {
		t.Fatalf("missing field: %v", err)
	}
	_, err = decideRoute(wf, wf.Nodes[0], nil, nil)
	if err == nil || !strings.Contains(err.Error(), "没有匹配") {
		t.Fatalf("missing artifact: %v", err)
	}
	_, err = decideRoute(wf, wf.Nodes[0], domain.RouteValues{"unknown"}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("invalid route: %v", err)
	}
}

func TestScriptRouteFileIsSafeAndUnique(t *testing.T) {
	run := domain.Run{}
	a, b := scriptRouteFile(run, "..", 1), scriptRouteFile(run, "../", 1)
	if a == b || strings.Contains(a, "..") || strings.Contains(b, "..") {
		t.Fatalf("unsafe or colliding paths: %q %q", a, b)
	}
}

func TestConditionalRoutingAgentAndReplay(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAgent(domain.AgentDefinition{ID: "router", Name: "router", ExecutorID: "mock", Prompt: "[[mock:route=small]]"}); err != nil {
		t.Fatal(err)
	}
	wf := routingWorkflow("agent")
	wf.Nodes[0] = domain.WorkflowNode{ID: "classify", Name: "classify", Type: "agent", AgentID: "router", ExecutorID: "mock", MaxAttempts: 1}
	wf.Nodes[1].ArtifactOutputs = []domain.ArtifactContract{{Name: "small-output", Path: "small.txt", Required: true}}
	wf.Nodes[1].Script = "echo small > small.txt"
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	first, _ := s.Run(r.ID)
	if first.Nodes["small"].Status != "succeeded" || first.Nodes["large"].Status != "skipped" {
		t.Fatalf("first route: %+v", first.Nodes)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "router", Name: "router", ExecutorID: "mock", Prompt: "[[mock:route=large]]"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Retry(r.ID, "classify", "route changed", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	second, _ := s.Run(r.ID)
	if second.Nodes["small"].Status != "skipped" || second.Nodes["large"].Status != "succeeded" || second.Nodes["delivery"].Status != "succeeded" {
		t.Fatalf("replay route: small=%+v large=%+v delivery=%+v", second.Nodes["small"], second.Nodes["large"], second.Nodes["delivery"])
	}
	for _, artifact := range activeRoutingArtifacts(second) {
		if artifact.NodeID == "small" {
			t.Fatalf("stale artifact exposed: %+v", artifact)
		}
	}
	view, _ := s.RunView(r.ID)
	found := false
	for _, artifact := range view.Artifacts {
		if artifact.NodeID == "small" && artifact.Name == "small-output" {
			found = true
			if !artifact.Stale {
				t.Fatalf("old artifact not marked stale: %+v", artifact)
			}
		}
	}
	if !found {
		t.Fatal("old artifact disappeared")
	}
}

func TestConditionalRoutingDefaultAndValidation(t *testing.T) {
	wf := routingWorkflow("none")
	wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "fallback", Type: "script", Script: "echo fallback"})
	wf.Nodes[0].DefaultTarget = "fallback"
	wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: "default", Source: "classify", Target: "fallback"})
	wf.Edges[0].Condition = &domain.WorkflowCondition{Type: "artifact", Artifact: "classification", Pointer: "/size", Equals: json.RawMessage(`"small"`)}
	wf.Edges[1].Condition = &domain.WorkflowCondition{Type: "artifact", Artifact: "classification", Pointer: "/size", Equals: json.RawMessage(`"large"`)}
	wf.Nodes[0].ArtifactOutputs = []domain.ArtifactContract{{Name: "classification", Path: "classification.json"}}
	if err := validate(wf); err != nil {
		t.Fatal(err)
	}
	d, err := decideRoute(wf, wf.Nodes[0], nil, nil)
	if err != nil || !d.SelectedEdges["5"] {
		t.Fatalf("default: %+v %v", d, err)
	}
	if len(WorkflowRoutingWarnings(wf)) != 0 {
		t.Fatal("join configured for skipped branches should not warn")
	}
	wf.Nodes[4].JoinRule = "all_success"
	if len(WorkflowRoutingWarnings(wf)) != 1 {
		t.Fatalf("warnings: %v", WorkflowRoutingWarnings(wf))
	}
	wf.Edges[0].Condition.Artifact = "missing"
	if err := validate(wf); err == nil || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("undeclared artifact accepted: %v", err)
	}
}

func TestConditionalRoutingFailures(t *testing.T) {
	for _, route := range []string{"invalid", ""} {
		t.Run(route, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			wf := routingWorkflow(route)
			if route == "" {
				wf.Nodes[0].Script = "echo no-route"
			}
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
			r, err := e.Start(wf.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, r.ID, "failed")
			got, _ := s.Run(r.ID)
			nr := got.Nodes["classify"]
			if nr.Status != "failed" || !strings.Contains(nr.Error, "small") || !strings.Contains(nr.Error, "large") {
				t.Fatalf("routing error: %+v", nr)
			}
			if got.Nodes["small"].Attempt != 0 || got.Nodes["large"].Attempt != 0 {
				t.Fatal("downstream started after invalid route")
			}
		})
	}
}

func TestConditionalRoutingRejectChangesRoute(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAgent(domain.AgentDefinition{ID: "router-reject", Name: "router", ExecutorID: "mock", Prompt: "[[mock:route=small]]"}); err != nil {
		t.Fatal(err)
	}
	wf := routingWorkflow("reject")
	wf.Nodes[0] = domain.WorkflowNode{ID: "classify", Type: "agent", AgentID: "router-reject", ExecutorID: "mock", MaxAttempts: 1}
	wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "approval", Type: "approval", MaxReworks: 2})
	wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: "approval", Source: "delivery", Target: "approval"})
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	first, _ := s.Run(r.ID)
	if first.Nodes["small"].Status != "succeeded" || first.Nodes["large"].Status != "skipped" {
		t.Fatalf("initial route: %+v", first.Nodes)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "router-reject", Name: "router", ExecutorID: "mock", Prompt: "[[mock:route=large]]"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject(r.ID, domain.RejectRequest{NodeID: "approval", TargetID: "classify", Reason: "change route"}); err != nil {
		t.Fatal(err)
	}
	// Reject persists "running" synchronously; the next waiting state belongs to the new attempt.
	waitFor(t, s, r.ID, "waiting")
	second, _ := s.Run(r.ID)
	if second.Nodes["classify"].Attempt != 2 || second.Nodes["small"].Status != "skipped" || second.Nodes["large"].Status != "succeeded" || second.Nodes["delivery"].Status != "succeeded" {
		t.Fatalf("changed route: classify=%+v small=%+v large=%+v delivery=%+v", second.Nodes["classify"], second.Nodes["small"], second.Nodes["large"], second.Nodes["delivery"])
	}
	if err := e.Approve(r.ID, "approval", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
}
