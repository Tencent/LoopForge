package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestKnowledgeCandidatesFilterProjectBeforeLimit(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := range 6 {
		id := fmt.Sprintf("knowledge-run-%d", i)
		project := "other"
		if i == 0 {
			project = "target"
		}
		if err := st.PutRun(domain.Run{
			ID: id, WorkflowID: "distill", Status: "succeeded",
			ExecutionContext: domain.TelemetryContext{ProjectID: project, IssueID: "issue-" + id},
			Artifacts:        []domain.Artifact{{ID: "artifact-" + id, RunID: id, NodeID: "learn", Name: "knowledge.md", Path: "knowledge.md", Content: "body", CreatedAt: base.Add(time.Duration(i) * time.Minute)}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	items := st.KnowledgeArtifacts("target", 1)
	if len(items) != 1 || items[0].Artifact.RunID != "knowledge-run-0" || items[0].ProjectID != "target" {
		t.Fatalf("project candidate was displaced by other projects: %+v", items)
	}
	if items[0].Artifact.Content != "" || items[0].WorkflowID != "distill" || items[0].RunStatus != "succeeded" || items[0].IssueID != "issue-knowledge-run-0" {
		t.Fatalf("candidate metadata = %+v", items[0])
	}
}

func TestKnowledgeCandidatesFillPageAfterRetryDeduplication(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	run := domain.Run{ID: "retry-run", WorkflowID: "distill", Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: "project"}}
	run.Artifacts = append(run.Artifacts, domain.Artifact{ID: "older-distinct", RunID: run.ID, NodeID: "learn", Path: "older/knowledge.md", CreatedAt: base})
	// More than one batch of retries must not crowd out the older candidate.
	for i := range 105 {
		run.Artifacts = append(run.Artifacts, domain.Artifact{ID: fmt.Sprintf("retry-%03d", i), RunID: run.ID, NodeID: "learn", Attempt: i + 1, Path: "knowledge.md", CreatedAt: base.Add(time.Duration(i+1) * time.Minute)})
	}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"", "project"} {
		items := st.KnowledgeArtifacts(project, 2)
		if len(items) != 2 || items[0].Artifact.ID != "retry-104" || items[1].Artifact.ID != "older-distinct" {
			t.Fatalf("project %q: retry rows consumed the candidate limit: %+v", project, items)
		}
	}
}
