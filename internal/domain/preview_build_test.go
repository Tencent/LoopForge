package domain

import (
	"strings"
	"testing"
	"time"
)

func TestAttachPreviewBuildAppendsCodeBuddyNode(t *testing.T) {
	wf := WorkflowDefinition{
		ID:      "front",
		Preview: &PreviewContract{Enabled: true, Model: "gpt-5.6-terra"},
		Nodes: []WorkflowNode{
			{ID: "impl", Name: "实现", Type: "script"},
			{ID: "test", Name: "测试", Type: "script"},
		},
		Edges: []WorkflowEdge{{ID: "e1", Source: "impl", Target: "test", When: "success"}},
	}
	got, err := AttachPreviewBuild(wf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(got.Nodes))
	}
	last := got.Nodes[len(got.Nodes)-1]
	if last.ID != PreviewBuildNodeID || last.AgentID != PreviewBuildAgentID || last.ExecutorID != PreviewBuildExecutor {
		t.Fatalf("injected node = %+v", last)
	}
	if last.JoinRule != "none_failed_min_one_success" {
		t.Fatalf("preview join rule = %q", last.JoinRule)
	}
	prompt := PreviewBuildPrompt(got.Preview)
	if !strings.Contains(prompt, "base: './'") || !strings.Contains(prompt, "./assets/") || !strings.Contains(prompt, "拦截 fetch/XHR") {
		t.Fatalf("preview build prompt must force a relative asset base and gateway-intercepted backend: %s", prompt)
	}
	if !strings.Contains(prompt, "没有前端工程") || !strings.Contains(prompt, "禁止在非前端仓库里编造静态站点") {
		t.Fatalf("preview build prompt must refuse to fabricate a site: %s", prompt)
	}
	if len(last.ArtifactOutputs) != 1 || last.ArtifactOutputs[0].Path != "dist/index.html" {
		t.Fatalf("artifact = %+v", last.ArtifactOutputs)
	}
	found := false
	for _, edge := range got.Edges {
		if edge.Source == "test" && edge.Target == PreviewBuildNodeID {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing edge from success sink: %+v", got.Edges)
	}
	agent := PreviewBuildAgent(got.Preview)
	if agent.Model != "gpt-5.6-terra" || agent.ExecutorID != PreviewBuildExecutor {
		t.Fatalf("synthetic agent = %+v", agent)
	}
}

func TestAttachPreviewBuildPreservesExistingNodeAndDefaultsJoin(t *testing.T) {
	for _, rule := range []string{"", "all_success", "none_failed_min_one_success"} {
		wf := WorkflowDefinition{Preview: &PreviewContract{Enabled: true}, Nodes: []WorkflowNode{{ID: PreviewBuildNodeID, JoinRule: rule}}}
		got, err := AttachPreviewBuild(wf)
		if err != nil {
			t.Fatal(err)
		}
		want := rule
		if want == "" {
			want = "none_failed_min_one_success"
		}
		if got.Nodes[0].JoinRule != want {
			t.Fatalf("rule %q: got %q, want %q", rule, got.Nodes[0].JoinRule, want)
		}
		if wf.Nodes[0].JoinRule != rule {
			t.Fatalf("attach mutated input join rule %q", wf.Nodes[0].JoinRule)
		}
	}
}

func TestAttachPreviewBuildIsIdempotentAndStrippedFromCatalog(t *testing.T) {
	wf := WorkflowDefinition{
		Preview: &PreviewContract{Enabled: true},
		Nodes:   []WorkflowNode{{ID: "impl", Type: "script"}},
	}
	once, err := AttachPreviewBuild(wf)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := AttachPreviewBuild(once)
	if err != nil {
		t.Fatal(err)
	}
	if len(twice.Nodes) != 2 {
		t.Fatalf("second attach should replace, not duplicate: %d nodes", len(twice.Nodes))
	}
	stripped := StripPreviewBuild(twice)
	if len(stripped.Nodes) != 1 || stripped.Nodes[0].ID != "impl" {
		t.Fatalf("catalog must not keep the injected node: %+v", stripped.Nodes)
	}
}

func TestPreviewContractClampsRetentionTo30Days(t *testing.T) {
	c := &PreviewContract{Enabled: true, RetentionSec: 90 * 24 * 3600}
	c.Normalize()
	if c.RetentionSec != PreviewMaxRetentionSec {
		t.Fatalf("retention = %d, want max %d", c.RetentionSec, PreviewMaxRetentionSec)
	}
	unset := &PreviewContract{Enabled: true}
	if got := unset.Retention(); got != time.Duration(PreviewMaxRetentionSec)*time.Second {
		t.Fatalf("default retention = %s, want 30d", got)
	}
}

func TestPreviewContractRejectsNonCodeBuddy(t *testing.T) {
	c := &PreviewContract{Enabled: true, ExecutorID: "claude", Model: "x"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected CodeBuddy-only error")
	}
	disabled := &PreviewContract{Enabled: false, ExecutorID: "claude"}
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled preview must skip executor checks: %v", err)
	}
}

func TestNormalizePreviewBackendURL(t *testing.T) {
	got, err := NormalizePreviewBackendURL(" https://api.example.com/v1 ")
	if err != nil || got != "https://api.example.com/v1" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NormalizePreviewBackendURL("javascript:alert(1)"); err == nil {
		t.Fatal("expected rejection")
	}
	if _, err := NormalizePreviewBackendURL("https://user:pass@api.example.com"); err == nil {
		t.Fatal("expected credential rejection")
	}
	empty, err := NormalizePreviewBackendURL("  ")
	if err != nil || empty != "" {
		t.Fatalf("empty = %q %v", empty, err)
	}
}
