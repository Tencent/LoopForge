package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssetValidationBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"docker missing", ValidateDockerfile(DockerfileDefinition{})},
		{"docker content", ValidateDockerfile(DockerfileDefinition{ID: "d", Name: "d"})},
		{"docker path", ValidateDockerfile(DockerfileDefinition{ID: "d", Name: "d", Content: "FROM x", Files: map[string]string{"../x": "x"}})},
		{"env missing", ValidateEnvironment(EnvironmentDefinition{})},
		{"env variable", ValidateEnvironment(EnvironmentDefinition{ID: "e", Name: "e", Variables: map[string]string{"x=y": "z"}})},
		{"env path", ValidateEnvironment(EnvironmentDefinition{ID: "e", Name: "e", PathEntries: []string{"../x"}})},
		{"env nul", ValidateEnvironment(EnvironmentDefinition{ID: "e", Name: "e", InstallScript: "\x00"})},
		{"skill size", ValidateSkillFileRefs([]SkillFileRef{{Path: "x", Size: SkillFileMaxBytes + 1}})},
	} {
		if tc.err == nil {
			t.Fatalf("%s unexpectedly valid", tc.name)
		}
	}
	for _, path := range []string{"", ".", "/abs", "C:x", "a//b", "a/../b"} {
		if ValidateRelativeFilePath(path) == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
	if ValidateSkillFileRefs([]SkillFileRef{{Path: "dir/a", Size: 2}}) != nil {
		t.Fatal("valid skill ref rejected")
	}
	if NormalizeSkillFileMode(0) != 0o644 || NormalizeSkillFileMode(0o1755) != 0o755 {
		t.Fatal("mode normalization")
	}
}

func TestConflictBlockExtractionBranches(t *testing.T) {
	dir := t.TempDir()
	content := "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> main\n"
	if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binary"), []byte{0, 1}, 0o600); err != nil {
		t.Fatal(err)
	}
	blocks := ExtractConflictBlocks(dir, []string{"missing", "binary", "conflict.txt"})
	if len(blocks) != 1 || blocks[0].Ours != "ours" || blocks[0].Theirs != "theirs" {
		t.Fatalf("blocks = %#v", blocks)
	}
	lines := make([]string, 45)
	for i := range lines {
		lines[i] = strings.Repeat("x", 50)
	}
	malformed := "<<<<<<< one\n" + strings.Join(lines, "\n") + "\n=======\ntheirs\n<<<<<<< nested\nours2\n=======\ntheirs2\n>>>>>>> end"
	if got := ParseConflictMarkers("f", []byte(malformed), 1); len(got) != 1 || !strings.Contains(got[0].Ours, "截断") {
		t.Fatalf("malformed blocks = %#v", got)
	}
}

func TestMetricAndEffortRemainingBranches(t *testing.T) {
	if ValidateAgentEffort("", "unknown") != nil || ValidateAgentEffort("high", "codex") != nil {
		t.Fatal("valid effort rejected")
	}
	if ValidateAgentEffort("high", "unknown") == nil || ValidateAgentEffort("impossible", "codex") == nil {
		t.Fatal("invalid effort accepted")
	}
	if NormalizeMetricCategory("需求") != MetricCategoryRequirement || NormalizeMetricCategory("bug修复") != MetricCategoryBugfix || NormalizeMetricCategory("") != MetricCategoryOther {
		t.Fatal("metric category")
	}
	if analyticsPercentile(nil, 50) != 0 || analyticsPercentile([]int64{3, 1, 2}, 100) != 3 || analyticsPercentile([]int64{3, 1, 2}, 50) != 2 {
		t.Fatal("analytics percentile")
	}
}

func TestMCPValidationBranches(t *testing.T) {
	base := MCPServerDefinition{ID: "m", Name: "m", Transport: "stdio", Command: "mcp"}
	bad := []MCPServerDefinition{
		{},
		{ID: "m", Name: "m", Transport: "http", URL: "file:///x"},
		{ID: "m", Name: "m", Transport: "stdio"},
		cvSMCP(base, func(v *MCPServerDefinition) { v.TimeoutSec = 601 }),
		cvSMCP(base, func(v *MCPServerDefinition) { v.Scope = "project" }),
		cvSMCP(base, func(v *MCPServerDefinition) { v.Scope = "personal" }),
		cvSMCP(base, func(v *MCPServerDefinition) { v.Scope = "weird" }),
		cvSMCP(base, func(v *MCPServerDefinition) { v.AuthType = "bad" }),
		cvSMCP(base, func(v *MCPServerDefinition) { v.AuthType = "github-token" }),
		cvSMCP(base, func(v *MCPServerDefinition) { v.PathEntries = []string{"../x"} }),
	}
	for i, item := range bad {
		if ValidateMCPServer(item) == nil {
			t.Fatalf("bad MCP %d accepted: %#v", i, item)
		}
	}
	httpMCP := MCPServerDefinition{ID: "m", Name: "m", Transport: "http", URL: "https://x", Scope: "project", ProjectID: "p", AuthType: "github-token", Headers: map[string]string{"Authorization": "secret"}}
	if ValidateMCPServer(httpMCP) == nil {
		t.Fatal("persisted auth accepted")
	}
	httpMCP.Headers = nil
	if err := ValidateMCPServer(httpMCP); err != nil {
		t.Fatal(err)
	}
}

func cvSMCP(v MCPServerDefinition, mutate func(*MCPServerDefinition)) MCPServerDefinition {
	mutate(&v)
	return v
}

func TestPreviewContractAndSessionBranches(t *testing.T) {
	var nilContract *PreviewContract
	if nilContract.Static() || nilContract.Index() != "index.html" || nilContract.SourceOrDist() != PreviewDefaultSource || nilContract.Retention() == 0 {
		t.Fatal("nil preview defaults")
	}
	c := &PreviewContract{Enabled: true, SourcePath: ` /dist\site/ `, RetentionSec: PreviewMaxRetentionSec + 1}
	c.Normalize()
	if c.SourcePath != "dist/site" || c.Kind != PreviewKindStatic || c.RetentionSec != PreviewMaxRetentionSec {
		t.Fatalf("normalized = %#v", c)
	}
	for _, bad := range []*PreviewContract{
		{Enabled: true, Kind: "service"},
		{Enabled: true, ExecutorID: "other"},
		{Enabled: true, SourcePath: "../x"},
	} {
		if bad.Validate() == nil {
			t.Fatalf("bad contract accepted: %#v", bad)
		}
	}
	for _, raw := range []string{"file:///x", "https://u:p@x", "https:///none"} {
		if _, err := NormalizePreviewBackendURL(raw); err == nil {
			t.Fatalf("backend %q accepted", raw)
		}
	}
	if got, err := NormalizePreviewBackendURL("https://x/path#frag"); err != nil || got != "https://x/path" {
		t.Fatalf("backend = %q, %v", got, err)
	}
	for _, status := range []string{PreviewPending, PreviewArchiving} {
		if !((RunPreview{Status: status}).ActiveHold()) {
			t.Fatal(status)
		}
	}
	now := time.Now()
	if (PreviewSession{RevokedAt: now}).Live(now) || (PreviewSession{ExpiresAt: now.Add(-time.Second)}).Live(now) || !(PreviewSession{}).Live(now) {
		t.Fatal("session liveness")
	}
}

func TestRunEvalRunnerAndSpaceBranches(t *testing.T) {
	recs := []RunEvalRecommendation{
		{Target: "workflow", Action: "update_node", NodeID: "n"},
		{Target: "agent", Action: "upsert_gate", TargetID: "a"},
		{Target: "agent", Action: "remove_gate", TargetID: "a"},
		{Target: "agent", Action: "append_instructions", TargetID: "a"},
		{Target: "agent", Action: "append_task_prompt", TargetID: "a"},
	}
	for _, rec := range recs {
		if !rec.Applyable() {
			t.Fatalf("not applyable: %#v", rec)
		}
		rec.Status = RunEvalRecApplied
		if rec.Applyable() {
			t.Fatal("applied recommendation remained applyable")
		}
	}
	if NormalizeRunEvalAction("", "") != "" || NormalizeRunEvalAction("", "x.y") != "x.y" || NormalizeRunEvalAction("Agent", "UP") != "agent.up" {
		t.Fatal("action normalization")
	}
	policy := RunEvalPolicy{Dimensions: []RunEvalDimension{{ID: "a", Weight: 0}, {ID: "b", Weight: 2}}}
	if _, _, err := ScoreRunEvalDimensions(policy, map[string]int{"a": 1}); err == nil {
		t.Fatal("missing score accepted")
	}
	if _, _, err := ScoreRunEvalDimensions(policy, map[string]int{"a": 101, "b": 1}); err == nil {
		t.Fatal("range accepted")
	}
	r := Runner{Runtimes: []RunnerRuntime{{Provider: "x", Status: RuntimeStatusOnline, AuthMode: RuntimeAuthPersonal}, {Provider: "off", Status: RuntimeStatusDisabled}}}
	if !r.SupportsProvider("X") || r.SupportsProvider("off") || r.SupportsProvider("") || r.RuntimeAuthMode("x") != RuntimeAuthPersonal || r.RuntimeAuthMode("codebuddy") != RuntimeAuthPersonal {
		t.Fatal("runner runtime selection")
	}
	for _, artifact := range []SpaceArtifact{
		{Kind: "bad", Title: "x"},
		{Kind: "doc"},
		{Kind: "doc", Title: strings.Repeat("x", 121)},
		{Kind: "doc", Title: "x", Body: strings.Repeat("x", SpaceArtifactBodyLimit+1)},
		{Kind: "doc", Title: "x", URL: "ftp://x"},
	} {
		if artifact.Validate() == nil {
			t.Fatalf("artifact accepted: %#v", artifact)
		}
	}
}

func TestScheduleLaneAndVerificationBranches(t *testing.T) {
	job := ScheduleJob{WorkflowID: "default", UseIssueWorkflow: true}
	if job.ResolveWorkflow(Issue{WorkflowID: " issue "}) != "issue" || !job.DispatchesSOP() || job.DispatchLimit() != MaxScheduleDispatch {
		t.Fatal("job defaults")
	}
	job.IssueStatuses = []string{" IN_REVIEW ", "bad", "todo"}
	if got := job.TriggerIssueStatuses(); len(got) != 2 || got[0] != "todo" {
		t.Fatalf("statuses = %#v", got)
	}
	for _, source := range []string{"", "all", "both", "gongfeng", "tapd"} {
		if _, err := NormalizeScheduleSource(source); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NormalizeScheduleSource("bad"); err == nil {
		t.Fatal("bad source accepted")
	}
	for _, lane := range []string{"product", "front", "back", "uiux", "bugfix", "other"} {
		if SOPLaneLabel(lane) == "" {
			t.Fatal(lane)
		}
	}
	for _, wf := range []WorkflowDefinition{{ID: "generic-product"}, {Name: "前端交付"}, {Tags: []string{"设计交付"}}, {ID: "requirement-development"}, {ID: "issue-fix"}, {ID: "x"}} {
		if InferSOPLane(wf) == "" {
			t.Fatal(wf)
		}
	}
	gates := []VerificationGate{{ID: "g", Commands: []string{"echo ok", "git status", ""}}}
	split := SplitVerificationGates(gates)
	if len(split) != 2 || split[0].ExecutionScope != ExecutionScopeRun || split[1].ExecutionScope != ExecutionScopeRepositories {
		t.Fatalf("split = %#v", split)
	}
	for _, command := range []string{"", "set -e", "echo x"} {
		if CommandNeedsRepository(command) {
			t.Fatalf("command %q needs repo", command)
		}
	}
	for _, gate := range []VerificationGate{
		{ID: "x", Mode: "bad"},
		{ID: "x", Mode: "command", Commands: []string{"x"}},
		{ID: "x", Mode: "manual", ExecutionScope: "bad"},
	} {
		if ValidateVerificationGate(gate) == nil {
			t.Fatalf("gate accepted: %#v", gate)
		}
	}
}

func TestSOPBindingInheritanceBranches(t *testing.T) {
	issue := Issue{WorkflowID: " legacy ", AssigneeUserID: 7}
	if got := issue.EffectiveSOPBindings(); len(got) != 1 || got[0].WorkflowID != "legacy" || got[0].AssigneeUserID != 7 {
		t.Fatalf("legacy bindings = %#v", got)
	}
	explicit := []IssueSOPBinding{{WorkflowID: "b", Order: 2}, {WorkflowID: "a", Order: 1, DependsOn: []string{"missing", "sop-b"}}}
	issue.SOPBindings = explicit
	if got := issue.EffectiveSOPBindings(); len(got) != 2 || got[0].WorkflowID != "a" {
		t.Fatalf("explicit bindings = %#v", got)
	}
	project := Project{WorkflowID: "project-wf"}
	if got := project.EffectiveSOPBindings(); len(got) != 1 || got[0].WorkflowID != "project-wf" {
		t.Fatalf("project bindings = %#v", got)
	}
	if binding, ok := ResolveSOPBinding(issue, project, "b"); !ok || binding.WorkflowID != "b" {
		t.Fatalf("resolved binding = %#v, %v", binding, ok)
	}
	if _, ok := ResolveSOPBinding(issue, project, ""); ok {
		t.Fatal("empty workflow resolved")
	}
	books := NormalizeSOPPlaybooks([]SOPPlaybook{{Name: ""}, {ID: "x", Default: true}, {ID: "x", Default: true}})
	if len(books) != 2 || !books[1].Default || books[0].Name == "" {
		t.Fatalf("playbooks = %#v", books)
	}
}
