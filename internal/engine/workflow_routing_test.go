package engine

import (
	"agentflow/internal/domain"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func routingWorkflow(route string) domain.WorkflowDefinition {
	return domain.WorkflowDefinition{ID: "routing-" + route, Name: "routing", SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{
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

func TestConditionalRoutingWithAutomaticPreview(t *testing.T) {
	for _, route := range []string{"small", "large"} {
		t.Run(route, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			wf := routingWorkflow(route)
			// Leave two separate sinks so that preview must join a selected and
			// an unselected branch, as in the reported reproduction.
			wf.Nodes = wf.Nodes[:4]
			wf.Edges = wf.Edges[:3]
			wf.Preview = &domain.PreviewContract{Enabled: true}
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
			preview := got.Nodes[domain.PreviewBuildNodeID]
			if preview == nil || preview.Status != "succeeded" || preview.Attempt != 1 {
				t.Fatalf("preview must execute after selected branch: %+v", preview)
			}
			skipped := "large"
			if route == "large" {
				skipped = "small"
			}
			if got.Nodes[skipped].Status != "skipped" {
				t.Fatalf("unselected branch = %+v", got.Nodes[skipped])
			}
		})
	}
}

func TestConditionalRoutingWithExistingLegacyPreviewNode(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("small")
	wf.Nodes = wf.Nodes[:4]
	wf.Edges = wf.Edges[:3]
	wf.Preview = &domain.PreviewContract{Enabled: true}
	preview := domain.PreviewBuildNode(wf)
	preview.JoinRule = "" // A preview node saved before the new join rule existed.
	wf.Nodes = append(wf.Nodes, preview)
	wf.Edges = append(wf.Edges,
		domain.WorkflowEdge{ID: "preview-small", Source: "small", Target: preview.ID},
		domain.WorkflowEdge{ID: "preview-large", Source: "implement", Target: preview.ID})
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
	if nr := got.Nodes[preview.ID]; nr == nil || nr.Status != "succeeded" || nr.Attempt != 1 {
		t.Fatalf("legacy preview did not run: %+v", nr)
	}
}

func TestConditionalRoutingBranchJoinThenDelivery(t *testing.T) {
	for _, choice := range []string{"small", "large"} {
		t.Run(choice, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			wf := routingWorkflow(choice)
			// The shared fixture's delivery node is the join. Add a distinct
			// downstream delivery node to exercise the complete acceptance DAG.
			wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "final-delivery", Type: "script", Script: "echo delivered", MaxAttempts: 1})
			wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: "deliver", Source: "delivery", Target: "final-delivery"})
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
			for id, want := range map[string]string{"small": wantSmall, "large": wantLarge, "implement": wantLarge, "delivery": "succeeded", "final-delivery": "succeeded"} {
				if got.Nodes[id].Status != want {
					t.Fatalf("%s: got %+v, want %s", id, got.Nodes[id], want)
				}
			}
		})
	}
}

func TestConditionalRoutingSkippedAgentDoesNotExecute(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"selected-agent", "skipped-agent"} {
		if err := s.PutAgent(domain.AgentDefinition{ID: id, Name: id, ExecutorID: "mock"}); err != nil {
			t.Fatal(err)
		}
	}
	wf := routingWorkflow("small")
	wf.Nodes[1] = domain.WorkflowNode{ID: "small", Name: "selected", Type: "agent", AgentID: "selected-agent", ExecutorID: "mock", MaxAttempts: 1}
	wf.Nodes[2] = domain.WorkflowNode{ID: "large", Name: "skipped", Type: "agent", AgentID: "skipped-agent", ExecutorID: "mock", MaxAttempts: 1}
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
	selected, skipped := got.Nodes["small"], got.Nodes["large"]
	if selected.Status != "succeeded" || selected.Attempt != 1 {
		t.Fatalf("selected Agent did not execute: %+v", selected)
	}
	if skipped.Status != "skipped" || skipped.Attempt != 0 || skipped.SandboxID != "" || skipped.SessionID != "" || skipped.Provider != "" || skipped.Usage.TotalTokens != 0 || skipped.ToolCalls != 0 || !skipped.StartedAt.IsZero() {
		t.Fatalf("skipped Agent has execution state: %+v", skipped)
	}
	page, err := s.QueryRunTelemetry(r.ID, "large", "", 0, 0, false, false, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Category == "sandbox" || event.Category == "model" || event.Name == "agent.progress" || event.Name == "process.started" {
			t.Fatalf("skipped Agent produced execution telemetry: %+v", event)
		}
	}
}

func TestConditionalRoutingArtifactEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, script, wantSmall, wantLarge string
	}{
		{"match", `printf '{"size":"small"}' > classification.json`, "succeeded", "skipped"},
		{"blob-backed JSON", `printf '{"size":"small","pad":"' > classification.json; head -c 1048576 /dev/zero | tr '\000' 'x' >> classification.json; printf '"}' >> classification.json`, "succeeded", "skipped"},
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
			if tc.name == "blob-backed JSON" {
				stored, ok := s.RunProgress(r.ID)
				if !ok {
					t.Fatal("run not found")
				}
				for _, artifact := range stored.Artifacts {
					if artifact.NodeID == "classify" && artifact.Name == "classification" && artifact.BlobID != "" && artifact.Size > domain.MaxInlineRunnerArtifactBytes {
						return
					}
				}
				t.Fatal("routing JSON was not persisted as a blob")
			}
		})
	}
}

func TestRouteDecisionSurvivesEdgeReordering(t *testing.T) {
	wf := routingWorkflow("small")
	decision, err := decideRoute(wf, wf.Nodes[0], domain.RouteValues{"small"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{"classify": {Status: "succeeded", RouteDecision: decision}}}
	wf.Edges[0], wf.Edges[1] = wf.Edges[1], wf.Edges[0]
	if ready, skip, _ := nodeReadiness(wf, run, wf.Nodes[1]); !ready || skip {
		t.Fatal("selected branch lost after reorder")
	}
	if ready, skip, _ := nodeReadiness(wf, run, wf.Nodes[2]); ready || !skip {
		t.Fatal("unselected branch changed after reorder")
	}
}

func TestRouteDecisionLegacyAndIndexFallback(t *testing.T) {
	wf := routingWorkflow("small")
	wf.Edges[0].ID = ""
	wf.Edges[1].ID = "index:0"
	decision, err := decideRoute(wf, wf.Nodes[0], domain.RouteValues{"small"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.SelectedEdges["index:0"] || decision.SelectedEdges["id:index:0"] {
		t.Fatalf("ambiguous keys: %+v", decision)
	}
	run := domain.Run{Nodes: map[string]*domain.NodeRun{"classify": {Status: "succeeded", RouteDecision: decision}}}
	if ready, _, _ := nodeReadiness(wf, run, wf.Nodes[1]); !ready {
		t.Fatal("index fallback not selected")
	}
	run.Nodes["classify"].RouteDecision = &domain.RouteDecision{SelectedEdges: map[string]bool{"0": true, "1": false}}
	if ready, _, _ := nodeReadiness(wf, run, wf.Nodes[1]); !ready {
		t.Fatal("legacy decision not selected")
	}
	if _, skip, _ := nodeReadiness(wf, run, wf.Nodes[2]); !skip {
		t.Fatal("legacy unselected branch not skipped")
	}
	wf.Edges[0].ID = wf.Edges[1].ID
	if err := validate(wf); err == nil || !strings.Contains(err.Error(), "duplicate routing edge id") {
		t.Fatalf("duplicate IDs: %v", err)
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
	if err != nil || !d.SelectedEdges["id:small-edge"] {
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
	if err != nil || !d.SelectedEdges["id:default"] {
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

func TestWorkflowRoutingWarningsIncludeDefaultBranch(t *testing.T) {
	wf := routingWorkflow("small")
	wf.Nodes[0].DefaultTarget = "large"
	wf.Nodes[4].JoinRule = "all_success"
	wf.Edges = append(wf.Edges, domain.WorkflowEdge{ID: "default-large", Source: "classify", Target: "large"})
	warnings := WorkflowRoutingWarnings(wf)
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "delivery") {
			found = true
		}
	}
	if !found {
		t.Fatalf("default branch warning = %v", warnings)
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

func TestConditionalRoutingAgentRejectsInvalidRoute(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAgent(domain.AgentDefinition{ID: "invalid-router", Name: "invalid router", ExecutorID: "mock", Prompt: "[[mock:route=unknown]]"}); err != nil {
		t.Fatal(err)
	}
	wf := routingWorkflow("invalid-agent")
	wf.Nodes[0] = domain.WorkflowNode{ID: "classify", Type: "agent", AgentID: "invalid-router", ExecutorID: "mock", MaxAttempts: 1}
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
	if nr.Status != "failed" || !strings.Contains(nr.Error, "small") || !strings.Contains(nr.Error, "large") || !strings.Contains(nr.Error, "unknown") {
		t.Fatalf("Agent invalid route error should include allowed and submitted labels: %+v", nr)
	}
	if got.Nodes["small"].Attempt != 0 || got.Nodes["large"].Attempt != 0 {
		t.Fatal("downstream started after invalid Agent route")
	}
}

func TestConditionalRoutingRefreshRejectsNewConditionsOnCompletedNode(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "refresh-routing", Name: "refresh", Version: 1, SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{
		{ID: "classify", Type: "script", Script: "echo initial", MaxAttempts: 1},
		{ID: "approval", Type: "approval", MaxAttempts: 1},
		{ID: "small", Type: "script", Script: "echo small", MaxAttempts: 1},
	}, Edges: []domain.WorkflowEdge{{ID: "a", Source: "classify", Target: "approval"}, {ID: "b", Source: "approval", Target: "small"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	old, _ := s.Run(r.ID)
	if old.Nodes["classify"].Status != "succeeded" || old.Nodes["classify"].RouteDecision != nil {
		t.Fatalf("initial completed source = %+v", old.Nodes["classify"])
	}
	latest, _ := s.Workflow(wf.ID)
	latest.Version++
	latest.Nodes = append(latest.Nodes, domain.WorkflowNode{ID: "large", Type: "script", Script: "echo large", MaxAttempts: 1})
	latest.Edges = append(latest.Edges, domain.WorkflowEdge{ID: "conditional", Source: "classify", Target: "large", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"large"}}})
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err == nil || !strings.Contains(err.Error(), "classify") || !strings.Contains(err.Error(), "路由决策") {
		t.Fatalf("expected actionable refresh error, got %v", err)
	}
	unchanged, _ := s.Run(r.ID)
	if unchanged.WorkflowSnapshot.Version != old.WorkflowSnapshot.Version || unchanged.Nodes["large"] != nil || unchanged.Nodes["approval"].Status != "waiting_approval" {
		t.Fatalf("rejected refresh changed run: %+v", unchanged)
	}
	if err := e.Approve(r.ID, "approval", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
}

func TestConditionalRoutingRefreshRejectsPendingHumanRoute(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("refresh-pending-human")
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r := domain.Run{ID: "refresh-pending-human-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	r.Nodes["classify"] = &domain.NodeRun{
		NodeID: "classify", Status: "waiting_approval", Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", Route: domain.RouteValues{"small"}},
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	latest.Nodes = append(latest.Nodes, domain.WorkflowNode{ID: "extra", Type: "script"})
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err == nil || !strings.Contains(err.Error(), "classify") || !strings.Contains(err.Error(), "路由决策") {
		t.Fatalf("expected pending route refresh error, got %v", err)
	}
}

// A human gate submitted without a route value carries nothing to protect.
// Refreshing is the only way out when the frozen conditional edge itself is
// wrong, because approving that gate would fail in decideRoute anyway.
func TestConditionalRoutingRefreshAllowsPendingHumanGateWithoutRoute(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("refresh-human-no-route")
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r := domain.Run{ID: "refresh-human-no-route-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	r.Nodes["classify"] = &domain.NodeRun{
		NodeID: "classify", Status: "waiting_approval", Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", ActionRequired: "确认是否可以小改"},
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	latest.Nodes = append(latest.Nodes, domain.WorkflowNode{ID: "extra", Type: "script"})
	// The new SOP drops the conditional edges, so approving the waiting gate no
	// longer needs a route the old attempt never produced.
	latest.Edges = []domain.WorkflowEdge{{ID: "route-small", Source: "classify", Target: "small"}}
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err != nil {
		t.Fatalf("refresh should be allowed once the new SOP asks for no decision, got %v", err)
	}
	updated, ok := s.RunState(r.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	found := false
	for _, node := range updated.WorkflowSnapshot.Nodes {
		if node.ID == "extra" {
			found = true
		}
	}
	if !found {
		t.Fatalf("refresh kept the old graph: %+v", updated.WorkflowSnapshot.Nodes)
	}
	// The refresh is only useful if the parked gate can actually be approved
	// afterwards: no conditions left means no decision to produce.
	if err := e.Approve(r.ID, "classify", ""); err != nil {
		t.Fatalf("approve after refresh: %v", err)
	}
	approved, ok := s.RunState(r.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if got := approved.Nodes["classify"]; got.Status != "succeeded" || got.RouteDecision != nil {
		t.Fatalf("approval after refresh: status=%q routeDecision=%+v", got.Status, got.RouteDecision)
	}
}

// A pending route only matters for a node that actually owns conditional
// edges; a gate elsewhere in the same conditional SOP stays refreshable.
func TestConditionalRoutingRefreshAllowsPendingGateOnNonRoutingNode(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("refresh-gate-no-conditions")
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r := domain.Run{ID: "refresh-gate-no-conditions-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	r.Nodes["delivery"] = &domain.NodeRun{
		NodeID: "delivery", Status: "waiting_approval", Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", Route: domain.RouteValues{"small"}},
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	latest.Nodes = append(latest.Nodes, domain.WorkflowNode{ID: "extra", Type: "script"})
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err != nil {
		t.Fatalf("refresh should be allowed for a node without conditional edges, got %v", err)
	}
	if err := e.Approve(r.ID, "delivery", ""); err != nil {
		t.Fatalf("approve after refresh: %v", err)
	}
	approved, ok := s.RunState(r.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if got := approved.Nodes["delivery"]; got.Status != "succeeded" || got.RouteDecision != nil {
		t.Fatalf("approval after refresh: status=%q routeDecision=%+v", got.Status, got.RouteDecision)
	}
}

// An operator pause reuses waiting_approval without a gate result. Resuming
// re-executes the script from scratch, so the route is recomputed against the
// graph frozen at that moment and there is no stale intent to protect.
// Publishing a new SOP must therefore keep refreshing a paused routing script
// instead of telling the operator to start a new run.
func TestConditionalRoutingRefreshAllowsUserPausedRoutingScript(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("refresh-user-paused")
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r := domain.Run{ID: "refresh-user-paused-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", PauseRequested: true, Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	// nodeCancelled leaves exactly this shape: waiting_approval, a pause
	// activity, and no GateResult.
	r.Nodes["classify"] = &domain.NodeRun{
		NodeID: "classify", Status: "waiting_approval", Activity: "已由用户暂停，继续后脚本将从头执行",
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	// Only the script body changes; the conditional edges stay put on both
	// sides, which is the case the guard used to refuse.
	latest.Nodes[0].Script = `printf '{"route":"large"}' > "$LOOPFORGE_ROUTE_FILE"`
	latest.Nodes = append(latest.Nodes, domain.WorkflowNode{ID: "extra", Type: "script", Script: "echo extra", MaxAttempts: 1})
	latest.Edges = append(latest.Edges, domain.WorkflowEdge{ID: "extra-edge", Source: "delivery", Target: "extra"})
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err != nil {
		t.Fatalf("refresh should be allowed for a user-paused routing node, got %v", err)
	}
	updated, ok := s.RunState(r.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	if updated.WorkflowSnapshot.Version != latest.Version {
		t.Fatalf("refresh kept version %d, want %d", updated.WorkflowSnapshot.Version, latest.Version)
	}
	nodes := map[string]domain.WorkflowNode{}
	for _, node := range updated.WorkflowSnapshot.Nodes {
		nodes[node.ID] = node
	}
	if _, found := nodes["extra"]; !found {
		t.Fatalf("refresh kept the old node set: %+v", updated.WorkflowSnapshot.Nodes)
	}
	if !strings.Contains(nodes["classify"].Script, "large") {
		t.Fatalf("refresh kept the old script: %q", nodes["classify"].Script)
	}
	if updated.Nodes["extra"] == nil || updated.Nodes["extra"].Status != "pending" {
		t.Fatalf("new node should start pending: %+v", updated.Nodes["extra"])
	}
	edges := map[string]domain.WorkflowEdge{}
	for _, edge := range updated.WorkflowSnapshot.Edges {
		edges[edge.ID] = edge
	}
	if _, found := edges["extra-edge"]; !found {
		t.Fatalf("refresh kept the old edge set: %+v", updated.WorkflowSnapshot.Edges)
	}
	for _, id := range []string{"small-edge", "large-edge"} {
		if edges[id].Condition == nil {
			t.Fatalf("conditional edge %s missing after refresh: %+v", id, updated.WorkflowSnapshot.Edges)
		}
	}
	if got := updated.Nodes["classify"]; got == nil || got.Status != "waiting_approval" || got.GateResult != nil || got.RouteDecision != nil {
		t.Fatalf("paused node changed across refresh: %+v", got)
	}
}

// A pending route value was produced against a graph with no routing at all.
// Handing it a new graph that does ask for a decision would let that value pick
// a branch, so the refresh has to be refused even though the frozen graph has
// no conditional edge.
func TestConditionalRoutingRefreshRejectsPendingRouteWithoutFrozenConditions(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("refresh-route-no-conditions")
	linear := []domain.WorkflowEdge{{ID: "route-small", Source: "classify", Target: "small"}}
	wf.Edges = linear
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r := domain.Run{ID: "refresh-route-no-conditions-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	r.Nodes["classify"] = &domain.NodeRun{
		NodeID: "classify", Status: "waiting_approval", Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", ActionRequired: "确认是否小改", Route: domain.RouteValues{"small"}},
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	latest.Edges = []domain.WorkflowEdge{
		{ID: "small-edge", Source: "classify", Target: "small", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"small"}}},
		{ID: "large-edge", Source: "classify", Target: "large", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"large"}}},
	}
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err == nil || !strings.Contains(err.Error(), "classify") || !strings.Contains(err.Error(), "路由决策") {
		t.Fatalf("expected refresh to be refused for a pending route value, got %v", err)
	}
}

// A required manual role gate parks the node after an Agent pass, so the saved
// decision is pass rather than human. Approving it still rebuilds the route
// from the saved value, which means the same guard has to cover this path.
func TestConditionalRoutingRefreshRejectsConditionsAddedToManualRoleGate(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putGateTestAgent(t, s, "gated-router", []domain.VerificationGate{{ID: "human", Name: "人工确认", Mode: "manual", Required: true}})
	wf := routingWorkflow("refresh-manual-gate")
	wf.Edges = []domain.WorkflowEdge{{ID: "route-small", Source: "classify", Target: "small"}}
	wf.Nodes[0] = domain.WorkflowNode{ID: "classify", Name: "classify", Type: "agent", AgentID: "gated-router", ExecutorID: "mock", MaxAttempts: 1}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	parked, ok := s.RunState(r.ID)
	if !ok {
		t.Fatal("run disappeared")
	}
	classify := parked.Nodes["classify"]
	if classify.Status != "waiting_approval" || classify.GateResult == nil || classify.GateResult.Decision != "pass" {
		t.Fatalf("manual role gate should park a passed node: status=%q gate=%+v", classify.Status, classify.GateResult)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	latest.Edges = []domain.WorkflowEdge{
		{ID: "small-edge", Source: "classify", Target: "small", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"small"}}},
		{ID: "large-edge", Source: "classify", Target: "large", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"large"}}},
	}
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err == nil || !strings.Contains(err.Error(), "classify") || !strings.Contains(err.Error(), "条件分支") {
		t.Fatalf("expected refresh to be refused while a manual role gate waits, got %v", err)
	}
}

// A gate that never produced a route becomes un-approvable if the refreshed
// graph starts asking it for one, so the refresh has to be refused up front.
func TestConditionalRoutingRefreshRejectsConditionsAddedToWaitingGate(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("refresh-gate-gains-route")
	linear := []domain.WorkflowEdge{{ID: "route-small", Source: "classify", Target: "small"}}
	wf.Edges = linear
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r := domain.Run{ID: "refresh-gate-gains-route-run", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "waiting", Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	r.Nodes["classify"] = &domain.NodeRun{
		NodeID: "classify", Status: "waiting_approval", Activity: "等待人工确认",
		GateResult: &domain.GateResult{Decision: "human", ActionRequired: "确认是否小改"},
	}
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	latest, ok := s.Workflow(wf.ID)
	if !ok {
		t.Fatal("workflow disappeared")
	}
	latest.Version++
	latest.Edges = []domain.WorkflowEdge{
		{ID: "small-edge", Source: "classify", Target: "small", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"small"}}},
		{ID: "large-edge", Source: "classify", Target: "large", Condition: &domain.WorkflowCondition{Type: "route", Values: []string{"large"}}},
	}
	if err := s.PutWorkflow(latest); err != nil {
		t.Fatal(err)
	}
	if err := e.RefreshRunSOP(r.ID); err == nil || !strings.Contains(err.Error(), "classify") || !strings.Contains(err.Error(), "条件分支") {
		t.Fatalf("expected refresh to be refused, got %v", err)
	}
}

func TestConditionalRoutingMissingDecisionFailsInsteadOfHanging(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := routingWorkflow("small")
	r := domain.Run{ID: "missing-route-decision", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "running", Nodes: map[string]*domain.NodeRun{}}
	for _, node := range wf.Nodes {
		r.Nodes[node.ID] = &domain.NodeRun{NodeID: node.ID, Status: "pending"}
	}
	r.Nodes["classify"].Status = "succeeded"
	if err := s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	if _, err := e.acquireLease(r); err != nil {
		t.Fatal(err)
	}
	e.execute(context.Background(), r.ID)
	got, _ := s.Run(r.ID)
	if got.Status != "failed" || got.Nodes["classify"].Status != "failed" || !strings.Contains(got.Nodes["classify"].Error, "缺少路由决策") {
		t.Fatalf("inconsistent state did not fail clearly: run=%s node=%+v", got.Status, got.Nodes["classify"])
	}
	for _, id := range []string{"small", "large"} {
		if got.Nodes[id].Attempt != 0 {
			t.Fatalf("downstream %s started without a routing decision", id)
		}
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

func TestConditionalRoutingHumanRouteAppliesOnlyAfterApproval(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAgent(domain.AgentDefinition{ID: "human-router", Name: "human router", ExecutorID: "mock", Prompt: "[[mock:gate=human]][[mock:route=small]]"}); err != nil {
		t.Fatal(err)
	}
	wf := routingWorkflow("human")
	wf.Nodes[0] = domain.WorkflowNode{ID: "classify", Name: "classify", Type: "agent", AgentID: "human-router", ExecutorID: "mock", MaxAttempts: 1}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	r, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	waiting, _ := s.Run(r.ID)
	if waiting.Nodes["classify"].Status != "waiting_approval" || waiting.Nodes["classify"].RouteDecision != nil {
		t.Fatalf("human route must wait without committing a decision: %+v", waiting.Nodes["classify"])
	}
	if err := e.Approve(r.ID, "classify", "route confirmed"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	got, _ := s.Run(r.ID)
	if got.Nodes["classify"].RouteDecision == nil || !got.Nodes["classify"].RouteDecision.SelectedEdges["id:small-edge"] {
		t.Fatalf("approval did not commit saved route: %+v", got.Nodes["classify"].RouteDecision)
	}
	if got.Nodes["small"].Status != "succeeded" || got.Nodes["large"].Status != "skipped" {
		t.Fatalf("approved route selected wrong branches: small=%+v large=%+v", got.Nodes["small"], got.Nodes["large"])
	}
}

func TestConditionalRoutingHumanRouteIsClearedOnUpstreamReject(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutAgent(domain.AgentDefinition{ID: "human-reject-router", Name: "human reject router", ExecutorID: "mock", Prompt: "[[mock:gate=human]][[mock:route=small]]"}); err != nil {
		t.Fatal(err)
	}
	wf := routingWorkflow("human-reject")
	wf.Nodes = append([]domain.WorkflowNode{{ID: "prepare", Name: "prepare", Type: "script", Script: "echo prepare", MaxAttempts: 1}}, wf.Nodes...)
	wf.Edges = append([]domain.WorkflowEdge{{ID: "prepare-classify", Source: "prepare", Target: "classify"}}, wf.Edges...)
	wf.Nodes[1] = domain.WorkflowNode{ID: "classify", Name: "classify", Type: "agent", AgentID: "human-reject-router", ExecutorID: "mock", RejectTo: "prepare", MaxAttempts: 1, MaxReworks: 2}
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
	if first.Nodes["classify"].GateResult == nil || first.Nodes["classify"].GateResult.Route[0] != "small" {
		t.Fatalf("human route was not saved: %+v", first.Nodes["classify"].GateResult)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "human-reject-router", Name: "human reject router", ExecutorID: "mock", Prompt: "[[mock:gate=human]][[mock:route=large]]"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Reject(r.ID, domain.RejectRequest{NodeID: "classify", TargetID: "prepare", Reason: "route needs reconsideration"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "waiting")
	second, _ := s.Run(r.ID)
	if second.Nodes["classify"].RouteDecision != nil || second.Nodes["small"].Status != "pending" || second.Nodes["large"].Status != "pending" {
		t.Fatalf("reject must clear old route before rerun: classify=%+v small=%+v large=%+v", second.Nodes["classify"], second.Nodes["small"], second.Nodes["large"])
	}
	if err := e.Approve(r.ID, "classify", "new route confirmed"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, r.ID, "succeeded")
	got, _ := s.Run(r.ID)
	if got.Nodes["small"].Status != "skipped" || got.Nodes["large"].Status != "succeeded" {
		t.Fatalf("rerun did not recalculate route: small=%+v large=%+v", got.Nodes["small"], got.Nodes["large"])
	}
}

func TestRoutingRoleFingerprintAcceptsUnchangedLegacyRole(t *testing.T) {
	role := domain.AgentDefinition{ID: "classifier", Description: "Help text", Prompt: "Classify"}
	expected := legacyRoutingRoleFingerprint(role)
	if expected == routingRoleFingerprint(role) || !matchesRoutingRoleFingerprint(role, expected) {
		t.Fatal("an unchanged role should match its legacy fingerprint")
	}
	role.Prompt = "Use a different classifier"
	if matchesRoutingRoleFingerprint(role, expected) {
		t.Fatal("a changed prompt must not match the legacy fingerprint")
	}
}

// Node-owned routing outputs can be generated by a role's verification commands
// even when the role does not declare any artifacts of its own.
func TestConditionalRoutingReplayChecksActualRoleDrift(t *testing.T) {
	for _, tc := range []struct {
		name            string
		roleOutputs     []domain.ArtifactContract
		editCommand     bool
		editDescription bool
	}{
		{name: "changed verification command", editCommand: true},
		{name: "unchanged role with an unrelated output", roleOutputs: []domain.ArtifactContract{{Name: "role-summary", Path: "summary.md"}}},
		{name: "changed description only", editDescription: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			role := domain.AgentDefinition{ID: "classifier", Name: "分类器", ExecutorID: "mock", ArtifactOutputs: tc.roleOutputs,
				Verification: []domain.VerificationGate{{ID: "write", Name: "Write classification", Mode: "command", Required: true, ExecutionScope: domain.ExecutionScopeRun,
					Commands: []string{`printf '{"size":"small"}' > classification.json`}}}}
			if err := s.PutAgent(role); err != nil {
				t.Fatal(err)
			}
			wf := routingWorkflow("retry-role-drift")
			wf.Nodes[0] = domain.WorkflowNode{ID: "classify", Name: "classify", Type: "agent", AgentID: role.ID, ExecutorID: "mock", MaxAttempts: 1,
				DefaultTarget: "large", ArtifactOutputs: []domain.ArtifactContract{{Name: "classification", Path: "classification.json"}}}
			wf.Edges[0].Condition = &domain.WorkflowCondition{Type: "artifact", Artifact: "classification", Pointer: "/size", Equals: json.RawMessage(`"small"`)}
			wf.Edges[1].Condition = nil
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
			old, err := e.Start(wf.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			assertSmall := func(id string) {
				t.Helper()
				waitFor(t, s, id, "succeeded")
				got, ok := s.RunState(id)
				if !ok || got.Nodes["small"].Status != "succeeded" || got.Nodes["large"].Status != "skipped" {
					t.Fatalf("expected Small in run %s, got %+v", id, got.Nodes)
				}
			}
			assertSmall(old.ID)
			if tc.editCommand {
				role.Verification[0].Commands[0] = `printf '{"size":"small"}' > classification-v2.json`
				if err := s.PutAgent(role); err != nil {
					t.Fatal(err)
				}
				wf, _ = s.Workflow(wf.ID)
				wf.Nodes[0].ArtifactOutputs[0] = domain.ArtifactContract{Name: "classification-v2", Path: "classification-v2.json"}
				wf.Edges[0].Condition.Artifact = "classification-v2"
				if err := s.PutWorkflow(wf); err != nil {
					t.Fatal(err)
				}
				fresh, err := e.Start(wf.ID, "")
				if err != nil {
					t.Fatal(err)
				}
				assertSmall(fresh.ID)
				if err := e.Retry(old.ID, "classify", "", ""); err == nil || !strings.Contains(err.Error(), "路由角色配置已变更") {
					t.Fatalf("expected drift refusal for old run, got %v", err)
				}
				assertSmall(old.ID)
				return
			}
			if tc.editDescription {
				role.Description = "Updated help text"
				if err := s.PutAgent(role); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Retry(old.ID, "classify", "", ""); err != nil {
				t.Fatalf("no execution-relevant role change; retry should succeed: %v", err)
			}
			assertSmall(old.ID)
			got, _ := s.RunState(old.ID)
			if got.Nodes["classify"].Attempt != 2 {
				t.Fatalf("router attempt = %d, want 2", got.Nodes["classify"].Attempt)
			}
		})
	}
}
