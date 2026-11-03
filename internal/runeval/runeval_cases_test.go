package runeval

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestApplyRecommendationBranches(t *testing.T) {
	wf := domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "n", MaxAttempts: 1}}}
	for _, tc := range []struct {
		name string
		rec  domain.RunEvalRecommendation
		wf   *domain.WorkflowDefinition
		ag   *domain.AgentDefinition
		err  bool
	}{
		{"missing workflow", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node"}, nil, nil, true},
		{"missing node", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "x", Set: map[string]any{}}, &wf, nil, true},
		{"missing set", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "n"}, &wf, nil, true},
		{"bad attempts", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "n", Set: map[string]any{"maxAttempts": 0}}, &wf, nil, true},
		{"bad reworks", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "n", Set: map[string]any{"maxReworks": -1}}, &wf, nil, true},
		{"bad failure", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "n", Set: map[string]any{"onFailure": "oops"}}, &wf, nil, true},
		{"bad retry", domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "n", Set: map[string]any{"retryStrategy": "oops"}}, &wf, nil, true},
		{"missing agent", domain.RunEvalRecommendation{Target: "agent", Action: "append_instructions", Text: "x"}, nil, nil, true},
		{"unsupported", domain.RunEvalRecommendation{Action: "wat"}, &wf, &domain.AgentDefinition{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ApplyRecommendation(tc.rec, tc.wf, tc.ag); (err != nil) != tc.err {
				t.Fatalf("error = %v", err)
			}
		})
	}
	rec := domain.RunEvalRecommendation{Target: "workflow", Action: "update_node", NodeID: "n", Set: map[string]any{"maxAttempts": "3", "maxReworks": float64(2), "onFailure": "reject", "retryStrategy": "targeted_fix"}}
	if err := ApplyRecommendation(rec, &wf, nil); err != nil || wf.Nodes[0].MaxAttempts != 3 {
		t.Fatalf("update = %#v, %v", wf.Nodes[0], err)
	}
}

func TestAgentTextAndGateBranches(t *testing.T) {
	agent := domain.AgentDefinition{}
	appendRec := domain.RunEvalRecommendation{Target: "agent", Action: "append_task_prompt", Set: map[string]any{"instructionsDelta": "constraint"}}
	if err := ApplyRecommendation(appendRec, nil, &agent); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRecommendation(appendRec, nil, &agent); err != nil || len(agent.TaskPrompt.Constraints) != 1 {
		t.Fatalf("duplicate constraint: %#v, %v", agent.TaskPrompt, err)
	}
	if err := appendTaskPrompt(&agent, domain.RunEvalRecommendation{}); err == nil {
		t.Fatal("empty task prompt accepted")
	}
	if err := appendAgentText(&agent.Instructions, domain.RunEvalRecommendation{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := appendAgentText(&agent.Instructions, domain.RunEvalRecommendation{Text: "second"}); err != nil {
		t.Fatal(err)
	}
	if err := appendAgentText(&agent.Instructions, domain.RunEvalRecommendation{Text: "second"}); err != nil || strings.Count(agent.Instructions, "second") != 1 {
		t.Fatal("instruction duplicate")
	}
	for _, rec := range []domain.RunEvalRecommendation{
		{Target: "agent", Action: "upsert_gate"},
		{Target: "agent", Action: "upsert_gate", Gate: &domain.VerificationGate{ID: "g", OnFailure: "bad"}},
	} {
		if err := ApplyRecommendation(rec, nil, &agent); err == nil {
			t.Fatalf("accepted bad gate: %#v", rec)
		}
	}
	gate := domain.RunEvalRecommendation{Target: "agent", Action: "upsert_gate", Set: map[string]any{"gateId": "g"}}
	if err := ApplyRecommendation(gate, nil, &agent); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRecommendation(gate, nil, &agent); err != nil || len(agent.Verification) != 1 {
		t.Fatal("gate update failed")
	}
	if err := ApplyRecommendation(domain.RunEvalRecommendation{Target: "agent", Action: "remove_gate", Set: map[string]any{"gateId": "missing"}}, nil, &agent); err == nil {
		t.Fatal("missing gate removed")
	}
	if err := ApplyRecommendation(domain.RunEvalRecommendation{Target: "agent", Action: "remove_gate", Set: map[string]any{"gateId": "g"}}, nil, &agent); err != nil || len(agent.Verification) != 0 {
		t.Fatalf("remove gate: %#v, %v", agent.Verification, err)
	}
}

func TestPromptParsingCatalogAndHelpers(t *testing.T) {
	system, user := BuildPrompt(domain.RunEvalPolicy{}, domain.RunEvalFacts{RunID: "r"})
	if !strings.Contains(system, "recommendations") || !strings.Contains(user, `"runId":"r"`) {
		t.Fatalf("prompt = %q / %q", system, user)
	}
	for _, raw := range []string{"", "none", "```json\n{}\n```", `{"summary":""}`, `{"summary":"x","dimensions":{}}`} {
		if _, err := ParseModelResult(raw, domain.RunEvalPolicy{}); err == nil {
			t.Fatalf("accepted invalid result %q", raw)
		}
	}
	rec := normalizeRecommendation(domain.RunEvalRecommendation{Action: "append_instructions"}, 2)
	if rec.ID != "rec-3" || rec.Status != domain.RunEvalRecPending {
		t.Fatalf("normalized = %#v", rec)
	}
	rec = normalizeRecommendation(domain.RunEvalRecommendation{Action: "upsert_gate", Gate: &domain.VerificationGate{ID: "g"}}, 0)
	if rec.Target != "workflow" {
		t.Fatalf("gate target = %q", rec.Target)
	}
	if got := extractJSONObject("prefix {\"a\":1} suffix"); got != `{"a":1}` {
		t.Fatalf("JSON = %q", got)
	}
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Generic: true, Nodes: []domain.WorkflowNode{
		{ID: "a", AgentID: "agent"}, {ID: "b", AgentID: "agent"}, {ID: "c", AgentID: "missing"},
	}}}
	catalog := BuildCatalog(run, func(id string) (domain.AgentDefinition, bool) {
		if id == "missing" {
			return domain.AgentDefinition{}, false
		}
		return domain.AgentDefinition{ID: id, Name: "A", Instructions: strings.Repeat("x", 500), TaskPrompt: &domain.TaskPromptSpec{Objective: strings.Repeat("y", 300)}}, true
	})
	if len(catalog.Nodes) != 3 || len(catalog.Agents) != 1 {
		t.Fatalf("catalog = %#v", catalog)
	}
	if CloneID(" wf ", "") != "wf-copy" || CloneID("wf", "p") != "wf-p" {
		t.Fatal("clone ID")
	}
}

func TestRankAndClipHelpers(t *testing.T) {
	tools := map[string]*domain.RunEvalToolRepeat{}
	errors := map[string]*domain.RunEvalErrorFact{}
	for i := 0; i < 15; i++ {
		tools[string(rune('a'+i))] = &domain.RunEvalToolRepeat{Tool: "x", Count: i, Failed: i % 3}
		bumpError(errors, "n", strings.Repeat(" 错 ", i+1))
	}
	tools["skip"] = &domain.RunEvalToolRepeat{Count: 1}
	if len(rankedTools(tools)) > maxToolRepeats || len(rankedErrors(errors)) > maxErrorFacts {
		t.Fatal("rank limits not applied")
	}
	if got := appendUnique([]string{"a"}, "a"); len(got) != 1 {
		t.Fatal("duplicate appended")
	}
	if got := clip("你好世界", 2); got != "你好…" {
		t.Fatalf("clip = %q", got)
	}
}
