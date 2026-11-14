package repo

import (
	"reflect"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDistinctArtifactRunIDsKeepsFirstSeenOrder(t *testing.T) {
	rows := []runArtifactRow{{RunID: "b"}, {RunID: "a"}, {RunID: "b"}, {RunID: "c"}, {RunID: "a"}}
	if got, want := distinctArtifactRunIDs(rows), []string{"b", "a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("distinctArtifactRunIDs = %v, want %v", got, want)
	}
	if got := distinctArtifactRunIDs(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty rows must yield an empty non-nil slice, got %#v", got)
	}
}

func TestLatestKnowledgeCandidatesFiltersAndDedupes(t *testing.T) {
	meta := map[string]knowledgeRunMeta{
		"r1": {workflowID: "wf1", status: "succeeded", ctx: domain.TelemetryContext{ProjectID: "p1", IssueID: "i1"}},
		"r2": {workflowID: "wf2", status: "failed", ctx: domain.TelemetryContext{ProjectID: "p2", IssueID: "i2"}},
	}
	rows := []runArtifactRow{
		{ID: "a3", RunID: "r1", NodeID: "n", Path: "knowledge.md", Attempt: 3},
		{ID: "a2", RunID: "r1", NodeID: "n", Path: "knowledge.md", Attempt: 2}, // older retry
		{ID: "b1", RunID: "r2", NodeID: "n", Path: "knowledge.md"},
		{ID: "x1", RunID: "missing", NodeID: "n", Path: "knowledge.md"}, // unknown run
		{ID: "a4", RunID: "r1", NodeID: "m", Path: "docs/knowledge.md"},
	}
	ids := func(list []domain.KnowledgeCandidate) []string {
		out := []string{}
		for _, c := range list {
			out = append(out, c.Artifact.ID)
		}
		return out
	}

	all := latestKnowledgeCandidates(rows, meta, "", 10)
	if got, want := ids(all), []string{"a3", "b1", "a4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all projects = %v, want %v", got, want)
	}
	first := all[0]
	if first.ProjectID != "p1" || first.IssueID != "i1" || first.WorkflowID != "wf1" || first.RunStatus != "succeeded" || first.Artifact.Attempt != 3 {
		t.Fatalf("candidate must carry run metadata: %+v", first)
	}
	if got, want := ids(latestKnowledgeCandidates(rows, meta, "p1", 10)), []string{"a3", "a4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("project p1 = %v, want %v", got, want)
	}
	if got, want := ids(latestKnowledgeCandidates(rows, meta, "", 2)), []string{"a3", "b1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("limit 2 = %v, want %v", got, want)
	}
	if got := latestKnowledgeCandidates(nil, meta, "", 10); got == nil || len(got) != 0 {
		t.Fatalf("no rows must yield an empty non-nil slice, got %#v", got)
	}
}

func TestKnowledgeArtifactsListsLatestPerRunNodePath(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putRun := func(id, workflowID, projectID string) {
		t.Helper()
		ctx := domain.TelemetryContext{ProjectID: projectID, IssueID: id + "-issue"}
		if err := s.DB.Create(&runRow{ID: id, WorkflowID: workflowID, Status: "succeeded", ExecutionContext: jsonBytes(ctx)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	putArtifact := func(id, runID, name, path string, attempt, minute int) {
		t.Helper()
		at := base.Add(time.Duration(minute) * time.Minute)
		row := runArtifactRow{ID: id, RunID: runID, NodeID: "node", Attempt: attempt, Name: name, Path: path, Content: "body", Created: &at}
		if err := s.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	putRun("kr1", "wf1", "kp1")
	putRun("kr2", "wf2", "kp2")
	putArtifact("k-old", "kr1", "knowledge.md", "knowledge.md", 1, 1)
	putArtifact("k-new", "kr1", "knowledge.md", "knowledge.md", 2, 2)
	putArtifact("k-nested", "kr2", "notes", "out/knowledge.md", 1, 3)
	putArtifact("k-other", "kr1", "summary.md", "summary.md", 1, 4)

	got := s.KnowledgeArtifacts("", 0)
	if len(got) != 2 || got[0].Artifact.ID != "k-nested" || got[1].Artifact.ID != "k-new" {
		t.Fatalf("all projects = %+v", got)
	}
	if got[1].ProjectID != "kp1" || got[1].IssueID != "kr1-issue" || got[1].WorkflowID != "wf1" || got[1].RunStatus != "succeeded" {
		t.Fatalf("candidate metadata = %+v", got[1])
	}
	if got[1].Artifact.Content != "" {
		t.Fatalf("knowledge candidates must be metadata only: %+v", got[1].Artifact)
	}
	scoped := s.KnowledgeArtifacts("kp1", 10)
	if len(scoped) != 1 || scoped[0].Artifact.ID != "k-new" {
		t.Fatalf("project kp1 = %+v", scoped)
	}
	if none := s.KnowledgeArtifacts("missing-project", 10); none == nil || len(none) != 0 {
		t.Fatalf("unknown project must yield an empty list, got %#v", none)
	}
}
