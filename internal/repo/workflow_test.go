package repo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestWorkflowOptimisticRevisionPreventsLostUpdate(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	original := domain.WorkflowDefinition{ID: "team-sop", Name: "Team SOP", Status: "draft"}
	if err := s.PutWorkflow(original); err != nil {
		t.Fatal(err)
	}
	left, _ := s.Workflow(original.ID)
	right := left
	left.Description = "left editor"
	if err := s.PutWorkflow(left); err != nil {
		t.Fatal(err)
	}
	right.Description = "right editor"
	if err := s.PutWorkflow(right); err == nil {
		t.Fatal("stale workflow update unexpectedly overwrote the newer revision")
	}
}

func TestLegacyWorkflowReadsDefaultEmptySandboxProfileToLocal(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	legacy := domain.WorkflowDefinition{ID: "legacy-empty-sandbox", Name: "Legacy"}
	rawLegacy, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Table("workflows").Create(map[string]any{
		"id": legacy.ID, "name": legacy.Name, "version": 0, "revision": 0, "status": "", "payload": rawLegacy,
	}).Error; err != nil {
		t.Fatal(err)
	}

	workflow, ok := s.Workflow(legacy.ID)
	if !ok {
		t.Fatal("legacy workflow not found")
	}
	if workflow.SandboxProfileID != "local" {
		t.Fatalf("sandboxProfileId = %q, want local", workflow.SandboxProfileID)
	}
	workflows := s.Workflows()
	found := false
	for _, candidate := range workflows {
		if candidate.ID == legacy.ID {
			found = true
			if candidate.SandboxProfileID != "local" {
				t.Fatalf("list sandboxProfileId = %q, want local", candidate.SandboxProfileID)
			}
		}
	}
	if !found {
		t.Fatal("legacy workflow missing from list")
	}
}

// The legacy per-node sandbox field is migrated while decoding a stored
// workflow payload, which is now always a MySQL row.
func TestDecodingRejectsConflictingLegacyNodeSandboxProfiles(t *testing.T) {
	raw := []byte(`{"id":"bad","name":"Bad","nodes":[{"id":"a","sandboxProfileId":"local"},{"id":"b","sandboxProfileId":"container"}]}`)
	var wf domain.WorkflowDefinition
	err := json.Unmarshal(raw, &wf)
	if err == nil || !strings.Contains(err.Error(), "conflicting legacy node sandbox profiles") {
		t.Fatalf("expected explicit migration conflict, got %v", err)
	}
}

func TestFirstIssueInProjectStartsAtOne(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutIssue(domain.Issue{ID: "first", ProjectID: "new-project", Title: "First"}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.Issue("first")
	if first.Number != 1 {
		t.Fatalf("first issue number = %d, want 1", first.Number)
	}
	if err := s.PutIssue(domain.Issue{ID: "second", ProjectID: "new-project", Title: "Second"}); err != nil {
		t.Fatal(err)
	}
	second, _ := s.Issue("second")
	if second.Number != 2 {
		t.Fatalf("second issue number = %d, want 2", second.Number)
	}
}

func TestNormalizeSandboxProfileKeepsOnlyDriverRelevantFields(t *testing.T) {
	base := domain.SandboxProfile{ReadOnlyRootFS: true, Environment: map[string]string{"TOOL_MODE": "test"}}
	local := normalizeSandboxProfile(func() domain.SandboxProfile { p := base; p.Driver = "local"; return p }())
	if local.ReadOnlyRootFS {
		t.Fatalf("local retained irrelevant sandbox fields: %+v", local)
	}
	if local.Environment["TOOL_MODE"] != "test" {
		t.Fatalf("local lost sandbox environment: %+v", local)
	}
	light := normalizeSandboxProfile(func() domain.SandboxProfile { p := base; p.Driver = "seatbelt"; return p }())
	if light.ReadOnlyRootFS {
		t.Fatalf("seatbelt normalization mismatch: %+v", light)
	}
	docker := normalizeSandboxProfile(func() domain.SandboxProfile { p := base; p.Driver = "docker"; p.DockerfileID = "runtime"; return p }())
	if docker.DockerfileID != "runtime" || !docker.ReadOnlyRootFS || docker.EnvironmentID != "" {
		t.Fatalf("docker lost relevant sandbox fields: %+v", docker)
	}
	fromFile := normalizeSandboxProfile(domain.SandboxProfile{Driver: "docker", DockerfileID: "runtime", EnvironmentID: "php8", ReadOnlyRootFS: true})
	if fromFile.DockerfileID != "runtime" || fromFile.EnvironmentID != "" || !fromFile.ReadOnlyRootFS {
		t.Fatalf("docker dockerfile profile mismatch: %+v", fromFile)
	}
	lightEnv := normalizeSandboxProfile(domain.SandboxProfile{Driver: "seatbelt", DockerfileID: "runtime", EnvironmentID: "php8"})
	if lightEnv.DockerfileID != "" || lightEnv.EnvironmentID != "php8" {
		t.Fatalf("seatbelt environment profile mismatch: %+v", lightEnv)
	}
	if light.Environment["TOOL_MODE"] != "test" || docker.Environment["TOOL_MODE"] != "test" {
		t.Fatalf("managed sandbox lost environment: light=%+v docker=%+v", light, docker)
	}
}

func TestPutDockerfileAndEnvironmentAndDockerSandbox(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutDockerfile(domain.DockerfileDefinition{ID: "slim", Name: "Slim"}); err == nil {
		t.Fatal("empty dockerfile content should be rejected")
	}
	if err := s.PutDockerfile(domain.DockerfileDefinition{ID: "slim", Name: "Slim", Content: "FROM debian:bookworm-slim"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnvironment(domain.EnvironmentDefinition{ID: "php8-custom", Name: "PHP 8 custom", Variables: map[string]string{"PHP_VERSION": "8.4.0"}, PathEntries: []string{"bin"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "docker-missing", Name: "Missing", Driver: "docker"}); err == nil {
		t.Fatal("docker sandbox without dockerfile should be rejected")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "docker-unknown", Name: "Unknown", Driver: "docker", DockerfileID: "missing"}); err == nil {
		t.Fatal("unknown dockerfile should be rejected")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "docker-ok", Name: "OK", Driver: "docker", DockerfileID: "slim"}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Sandbox("docker-ok")
	if !ok || got.DockerfileID != "slim" {
		t.Fatalf("docker sandbox not stored as dockerfile reference: %+v", got)
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "seatbelt-php", Name: "Seatbelt PHP", Driver: "seatbelt", EnvironmentID: "php8-custom"}); err != nil {
		t.Fatal(err)
	}
}

func TestPutSandboxRejectsInvalidEnvironment(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, environment := range []map[string]string{{"": "value"}, {"BAD=NAME": "value"}, {"GOOD": "bad\x00value"}} {
		err := s.PutSandbox(domain.SandboxProfile{ID: "invalid", Name: "Invalid", Driver: "local", Environment: environment})
		if err == nil {
			t.Fatalf("invalid environment was accepted: %#v", environment)
		}
	}
}

func TestWorkflowMetricsSummarizeClosureReworkAndNodes(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "metrics", Name: "Metrics", Nodes: []domain.WorkflowNode{{ID: "build", Name: "Build"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := domain.Run{ID: "run-metrics", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded", Nodes: map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "succeeded", Attempt: 2, ReworkCount: 1, StartedAt: now, FinishedAt: now.Add(2 * time.Second)}}, Usage: domain.TokenUsage{TotalTokens: 123}, ToolCalls: 4, Artifacts: []domain.Artifact{{ID: "artifact"}}, CreatedAt: now, UpdatedAt: now.Add(3 * time.Second)}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	metrics := s.WorkflowMetrics()
	var got *domain.WorkflowMetrics
	for i := range metrics {
		if metrics[i].WorkflowID == wf.ID {
			got = &metrics[i]
		}
	}
	if got == nil || got.TotalRuns != 1 || got.SucceededRuns != 1 || got.ClosureRate != 100 || got.ReworkedRuns != 1 || got.TotalTokens != 123 || got.TotalArtifacts != 1 {
		t.Fatalf("unexpected workflow metrics: %#v", got)
	}
	if len(got.NodeMetrics) != 1 || got.NodeMetrics[0].Executions != 2 || got.NodeMetrics[0].Reworks != 1 {
		t.Fatalf("unexpected node metrics: %#v", got.NodeMetrics)
	}
}

func TestEmptyStoreDoesNotSeedWorkflowTemplates(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if workflows := s.Workflows(); len(workflows) != 0 {
		t.Fatalf("empty store seeded workflow templates: %#v", workflows)
	}
}
