package repo

import (
	"fmt"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// knowledgeLimitFixture stores knowledge.md artifacts, one node each, newest
// first in the order given: count artifacts per (runID, projectID) pair.
func knowledgeLimitFixture(t *testing.T, s *Store, runs ...knowledgeLimitRun) {
	t.Helper()
	base := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	minute := 10000
	for _, run := range runs {
		ctx := domain.TelemetryContext{ProjectID: run.projectID}
		if err := s.DB.Create(&runRow{ID: run.runID, WorkflowID: "wf", Status: "succeeded", ExecutionContext: jsonBytes(ctx)}).Error; err != nil {
			t.Fatal(err)
		}
		for i := 0; i < run.count; i++ {
			minute--
			at := base.Add(time.Duration(minute) * time.Minute)
			row := runArtifactRow{
				ID: fmt.Sprintf("%s-%03d", run.runID, i), RunID: run.runID, NodeID: fmt.Sprintf("node-%03d", i),
				Attempt: 1, Name: "knowledge.md", Path: "knowledge.md", Created: &at,
			}
			if err := s.DB.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
}

type knowledgeLimitRun struct {
	runID, projectID string
	count            int
}

// A project filter over-fetches (4x the page) so newer artifacts of other
// projects do not starve the page, and the page itself is still cut at the
// caller's limit, not at the over-fetched row count.
func TestKnowledgeArtifactsProjectFilterOverFetchesButKeepsLimit(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	knowledgeLimitFixture(t, s,
		knowledgeLimitRun{"newer-other", "other", 5},
		knowledgeLimitRun{"older-mine", "mine", 3},
	)

	got := s.KnowledgeArtifacts("mine", 2)
	if len(got) != 2 {
		t.Fatalf("project page = %d candidates, want 2: %+v", len(got), got)
	}
	for _, c := range got {
		if c.ProjectID != "mine" {
			t.Fatalf("candidate of another project: %+v", c)
		}
	}
	if got[0].Artifact.ID != "older-mine-000" || got[1].Artifact.ID != "older-mine-001" {
		t.Fatalf("want the two newest artifacts of the project, got %s, %s", got[0].Artifact.ID, got[1].Artifact.ID)
	}
	// Without a project filter the page is exactly limit rows, newest first.
	all := s.KnowledgeArtifacts("", 2)
	if len(all) != 2 || all[0].Artifact.ID != "newer-other-000" || all[1].Artifact.ID != "newer-other-001" {
		t.Fatalf("unfiltered page = %+v", all)
	}
}

// Out-of-range limits fall back to the default page of 100.
func TestKnowledgeArtifactsClampsOversizedLimit(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	knowledgeLimitFixture(t, s, knowledgeLimitRun{"many", "p", 101})

	for _, limit := range []int{-1, 0, 501} {
		if got := s.KnowledgeArtifacts("", limit); len(got) != 100 {
			t.Fatalf("limit %d: got %d candidates, want the default 100", limit, len(got))
		}
	}
	if got := s.KnowledgeArtifacts("", 500); len(got) != 101 {
		t.Fatalf("limit 500: got %d candidates, want all 101", len(got))
	}
}
