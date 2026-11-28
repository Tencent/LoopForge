package repo

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// MySQL's utf8mb4_unicode_ci collation makes LIKE case-insensitive, so a
// search for "mr-123" finds "MR-123" there. Postgres compares case-sensitively,
// so every SQL-level pattern match in the repo layer has to use ILIKE there —
// otherwise a search that works on MySQL silently finds nothing on Postgres.
//
// The tests below run against a real Postgres database and are skipped when
// AGENTFLOW_TEST_POSTGRES_URL is unset.
//
// The fixtures spell the searched values in a different case than the pattern
// on purpose: that is the only way to see the difference between MySQL's
// collation and Postgres' LIKE.

// postgresCaseRun inserts a run row for the case-sensitivity fixtures.
func postgresCaseRun(t *testing.T, store *Store, id, projectID string) {
	t.Helper()
	if err := store.DB.Exec(
		"INSERT INTO runs (id, kind, workflow_id, status, execution_context, created_at) VALUES (?, 'loop', 'pg-case-workflow', 'succeeded', ?::jsonb, now())",
		id, `{"projectId":"`+projectID+`"}`).Error; err != nil {
		t.Fatalf("insert the run %s: %v", id, err)
	}
}

// postgresCaseArtifact inserts a run artifact with the given name, path and
// content.
func postgresCaseArtifact(t *testing.T, store *Store, id, runID, name, path, content string) {
	t.Helper()
	if err := store.DB.Exec(
		"INSERT INTO run_artifacts (id, run_id, node_id, attempt, name, path, content, created_at) VALUES (?, ?, '', 1, ?, ?, ?, now())",
		id, runID, name, path, content).Error; err != nil {
		t.Fatalf("insert the artifact %s: %v", id, err)
	}
}

// The helper itself has to pick the case-insensitive operator on Postgres.
func TestPostgresCaseInsensitivePatternMatch(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := upsert(store.DB, "generic_templates", map[string]any{
		"id":         "pg-case-template",
		"name":       "MixedCase Template",
		"payload":    []byte(`{"id":"pg-case-template"}`),
		"updated_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed the template: %v", err)
	}
	var names []string
	if err := store.DB.Table("generic_templates").Select("name").
		Where("name "+store.likeOperator()+" ?", "mixedcase%").Pluck("name", &names).Error; err != nil {
		t.Fatalf("search the templates: %v", err)
	}
	if len(names) != 1 || names[0] != "MixedCase Template" {
		t.Fatalf("the case-insensitive search matched %v, want the mixed-case template", names)
	}
	if !rec.contains("name ILIKE") {
		t.Fatal("the search did not use the case-insensitive operator")
	}
}

// The run evaluations of a LOOP carry the "sop-check-" prefix of the SOP
// detection. MySQL's collation ignores the case of that prefix, so a
// mixed-case prefix is a detection row there too.
func TestPostgresRunEvalQueriesSkipTheDetectionRows(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-eval-run"
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
		ID: "eval-normal", RunID: runID, Status: domain.RunEvalSucceeded, CreatedAt: base,
	}); err != nil {
		t.Fatalf("seed the evaluation: %v", err)
	}
	if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
		ID: "SOP-CHECK-mixed", RunID: runID, Status: domain.RunEvalSucceeded, CreatedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("seed the detection evaluation: %v", err)
	}
	latest, ok := store.RunsRepo.LatestRunEval(runID)
	if !ok {
		t.Fatal("no evaluation was found")
	}
	if latest.ID != "eval-normal" {
		t.Fatalf("the latest evaluation = %q, want the one that is not a detection row", latest.ID)
	}
	if !rec.contains("id NOT ILIKE") {
		t.Fatal("the evaluation lookup did not use the case-insensitive operator")
	}

	// The same rule applies to the running evaluation.
	if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
		ID: "eval-running", RunID: runID, Status: domain.RunEvalRunning, CreatedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("seed the running evaluation: %v", err)
	}
	if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
		ID: "SOP-CHECK-running", RunID: runID, Status: domain.RunEvalRunning, CreatedAt: base.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("seed the running detection evaluation: %v", err)
	}
	active, ok := store.RunsRepo.ActiveRunEval(runID)
	if !ok {
		t.Fatal("no running evaluation was found")
	}
	if active.ID != "eval-running" {
		t.Fatalf("the running evaluation = %q, want the one that is not a detection row", active.ID)
	}
}

// The trim keeps the newest evaluations of a run and never drops a detection
// row. MySQL's collation treats a mixed-case prefix as the detection prefix.
func TestPostgresRunEvalTrimKeepsTheDetectionRows(t *testing.T) {
	store, _ := postgresFixtureStore(t)
	runID := "pg-case-eval-trim-run"
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// The detection row is the oldest one, so it is the first the trim would
	// drop.
	if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
		ID: "SOP-CHECK-oldest", RunID: runID, Status: domain.RunEvalSucceeded, CreatedAt: base,
	}); err != nil {
		t.Fatalf("seed the detection evaluation: %v", err)
	}
	for i := 0; i < 12; i++ {
		if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
			ID:        fmt.Sprintf("eval-%02d", i),
			RunID:     runID,
			Status:    domain.RunEvalSucceeded,
			CreatedAt: base.Add(time.Duration(i+1) * time.Minute),
		}); err != nil {
			t.Fatalf("seed the evaluation %d: %v", i, err)
		}
	}
	var ids []string
	if err := store.DB.Model(&runEvalRow{}).Where("run_id = ?", runID).Order("id").Pluck("id", &ids).Error; err != nil {
		t.Fatalf("read the evaluations: %v", err)
	}
	if !slices.Contains(ids, "SOP-CHECK-oldest") {
		t.Fatalf("the trim dropped the detection row: %v", ids)
	}
	if len(ids) != 11 {
		t.Fatalf("the kept evaluations = %d, want the detection row and the newest ten", len(ids))
	}
}

// The SOP detection finds its own evaluations by the "sop-check-" prefix.
func TestPostgresSOPDetectionQueries(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-detection-run"
	projectID := "pg-case-detection-project"
	postgresCaseRun(t, store, runID, projectID)
	if err := store.RunsRepo.PutRunEval(domain.RunEvalRecord{
		ID: "SOP-CHECK-mixed", RunID: runID, Status: domain.RunEvalSucceeded,
	}); err != nil {
		t.Fatalf("seed the detection evaluation: %v", err)
	}
	evaluations := store.RunsRepo.SOPDetectionEvaluations([]string{runID})
	if _, ok := evaluations[runID]; !ok {
		t.Fatalf("the detection evaluations = %+v, want the mixed-case detection row", evaluations)
	}
	// The run carries a finished detection, so it does not wait for one.
	pending, err := store.RunsRepo.PendingSOPDetections(projectID, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("list the pending detections: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("the pending detections = %v, want none: the run already has a detection row", pending)
	}
	if !rec.contains("id ILIKE") {
		t.Fatal("the detection lookup did not use the case-insensitive operator")
	}
	if !rec.contains("checks.id ILIKE") {
		t.Fatal("the pending detection join did not use the case-insensitive operator")
	}
}

// The shared LOOP conversations are found by the "sop-" prefix, and the
// cross-user list keeps the project conversations of the other users.
func TestPostgresAssistantSessionLists(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-session-run"
	if _, err := store.AssistantRepo.EnsureRunAssistantSession(runID, "platform", "case session", 1); err != nil {
		t.Fatalf("create the shared session: %v", err)
	}
	shared := store.AssistantRepo.ListSharedAssistantSessions()
	if len(shared) != 1 || shared[0].ID != RunAssistantSessionID(runID) {
		t.Fatalf("the shared sessions = %+v, want the shared LOOP session", shared)
	}
	// A project conversation of another user keeps its own row.
	if err := store.AssistantRepo.PutAssistantSession(7, domain.AssistantSession{
		ID: "pg-case-project-session", UserID: 7, ProjectID: "platform", Title: "project talk",
	}); err != nil {
		t.Fatalf("create the project session: %v", err)
	}
	sharedSessions, projectSessions := store.AssistantRepo.ListCrossUserAssistantSessions(1)
	if len(sharedSessions) != 1 {
		t.Fatalf("the shared sessions = %+v, want the shared LOOP session", sharedSessions)
	}
	if len(projectSessions) != 1 || projectSessions[0].ID != "pg-case-project-session" {
		t.Fatalf("the project sessions = %+v, want the project conversation of the other user", projectSessions)
	}
	if !rec.contains("id ILIKE") {
		t.Fatal("the session list did not use the case-insensitive operator")
	}
}

// The knowledge usage events are searched by their free-text fields.
func TestPostgresKnowledgeUsageSearch(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Create(&knowledgeUsageEventRow{
		OwnerUserID: 4242,
		ReadID:      "pg-case-read",
		ConnectorID: "pg-case-connector",
		DocumentKey: "pg-case-document",
		SourceName:  "Gongfeng",
		DocumentID:  "doc-1",
		Title:       "MR-123 Report",
		FileName:    "MR-123.md",
		Query:       "MR-123",
		CallerKind:  domain.KnowledgeCallerSOP,
		CalledAt:    time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed the usage event: %v", err)
	}
	// Another user's event carries the needle in another field: the OR of the
	// searched fields must not escape the owner filter.
	if err := store.DB.Create(&knowledgeUsageEventRow{
		OwnerUserID: 9999,
		ReadID:      "pg-case-other-read",
		ConnectorID: "pg-case-connector",
		DocumentKey: "pg-case-other-document",
		FileName:    "MR-123 Other.md",
		CallerKind:  domain.KnowledgeCallerSOP,
		CalledAt:    time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed the other usage event: %v", err)
	}
	page := store.KnowledgeRepo.ListKnowledgeUsageEvents(4242, domain.KnowledgeUsageEventFilter{
		Query: "mr-123", Limit: 10,
	})
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("the search returned %d rows (total %d), want the mixed-case event", len(page.Items), page.Total)
	}
	if page.Items[0].Title != "MR-123 Report" {
		t.Fatalf("the search matched %q, want the mixed-case event", page.Items[0].Title)
	}
	if !rec.contains("title ILIKE") {
		t.Fatal("the usage search did not use the case-insensitive operator")
	}
}

// The knowledge candidates are the run artifacts named knowledge.md.
func TestPostgresKnowledgeArtifacts(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-knowledge-run"
	projectID := "pg-case-knowledge-project"
	postgresCaseRun(t, store, runID, projectID)
	postgresCaseArtifact(t, store, "pg-case-knowledge-artifact", runID, "KNOWLEDGE.MD", "workspace/KNOWLEDGE.MD", "")
	candidates := store.RunsRepo.KnowledgeArtifacts(projectID, 10)
	if len(candidates) != 1 {
		t.Fatalf("the knowledge candidates = %d, want the mixed-case artifact", len(candidates))
	}
	if candidates[0].Artifact.Name != "KNOWLEDGE.MD" {
		t.Fatalf("the candidate = %q, want the mixed-case artifact", candidates[0].Artifact.Name)
	}
	if !rec.contains("path ILIKE") {
		t.Fatal("the knowledge lookup did not use the case-insensitive operator")
	}
}

// The MR delivery states are discovered from the run artifacts that carry a
// merge request. MySQL's collation finds an artifact whose name spells the
// marker in another case.
func TestPostgresMRDeliveryDiscovery(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-delivery-run"
	projectID := "pg-case-delivery-project"
	postgresCaseRun(t, store, runID, projectID)
	postgresCaseArtifact(t, store, "pg-case-delivery-artifact", runID, "MR-body", "mr-body.md", domain.GongfengMRURL("team/proj", 42))
	if err := store.DeliveryRepo.DiscoverMRDeliveryStatesFromArtifacts(); err != nil {
		t.Fatalf("discover the MR delivery states: %v", err)
	}
	var states []mrDeliveryStateRow
	if err := store.DB.Where("mr_project = ?", "team/proj").Find(&states).Error; err != nil {
		t.Fatalf("read the delivery states: %v", err)
	}
	if len(states) != 1 || states[0].MRIID != 42 {
		t.Fatalf("the delivery states = %+v, want the MR of the mixed-case artifact", states)
	}
	if !rec.contains("run_artifacts.name ILIKE '%mr%'") {
		t.Fatal("the MR discovery did not use the case-insensitive operator")
	}
}

// The MR texts of a run are read from its artifacts. The merge request path of
// the artifact is spelled in another case, so only a case-insensitive match
// finds it — the pull request it carries is the delivery evidence.
func TestPostgresMRTextsByRun(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-mr-text-run"
	postgresCaseRun(t, store, runID, "pg-case-mr-text-project")
	content := "https://git.example.com/team/proj/-/MERGE_REQUESTS/42 https://github.com/team/proj/pull/7"
	postgresCaseArtifact(t, store, "pg-case-mr-text-artifact", runID, "mr-body", "mr-body.md", content)
	texts := store.DeliveryRepo.mrTextsByRun([]domain.Run{{ID: runID}})
	if !strings.Contains(texts[runID], "team/proj/pull/7") {
		t.Fatalf("the MR text = %q, want the delivery link of the mixed-case artifact", texts[runID])
	}
	if !rec.contains("content ILIKE") {
		t.Fatal("the MR text lookup did not use the case-insensitive operator")
	}
}

// The business report counts the MRs a run delivered, from the same artifacts.
func TestPostgresBusinessDeliveryMRs(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-business-run"
	postgresCaseRun(t, store, runID, "pg-case-business-project")
	content := "https://git.example.com/team/proj/-/MERGE_REQUESTS/42 https://github.com/team/proj/pull/7"
	postgresCaseArtifact(t, store, "pg-case-business-artifact", runID, "mr-body", "mr-body.md", content)
	mrs, err := store.AnalyticsRepo.businessDeliveryMRs([]domain.Run{{ID: runID}})
	if err != nil {
		t.Fatalf("read the delivery MRs: %v", err)
	}
	if len(mrs[runID]) != 1 {
		t.Fatalf("the delivery MRs = %+v, want the MR of the mixed-case artifact", mrs[runID])
	}
	if !rec.contains("content ILIKE") {
		t.Fatal("the business MR lookup did not use the case-insensitive operator")
	}
}

// The admission buckets match the legacy project buckets next to the platform
// bucket.
func TestPostgresAdmissionBucketQueries(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	if err := store.DB.Exec(
		"INSERT INTO runs (id, kind, workflow_id, status, quota_bucket, admission_state, created_at) VALUES ('pg-case-admission-run', 'loop', 'pg-case-workflow', 'queued', 'platform', 'unstarted', now())",
	).Error; err != nil {
		t.Fatalf("seed the queued run: %v", err)
	}
	loads, err := store.RunsRepo.LoadSnapshots(nil)
	if err != nil {
		t.Fatalf("load the snapshots: %v", err)
	}
	if loads["platform"].QueuedRuns != 1 {
		t.Fatalf("the platform load = %+v, want the queued run", loads["platform"])
	}
	queued, err := newAdmissionScope(store.DB).hasQueued("platform", "")
	if err != nil {
		t.Fatalf("check the queue: %v", err)
	}
	if !queued {
		t.Fatal("the queued run was not found in the platform bucket")
	}
	if !rec.contains("quota_bucket ILIKE") {
		t.Fatal("the admission queries did not use the case-insensitive operator")
	}
}

// The queue promotion reads the queued runs of the bucket and of the project
// auto buckets.
func TestPostgresPromoteBucketQuery(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-auto-run"
	if err := store.DB.Exec(
		"INSERT INTO runs (id, kind, workflow_id, status, quota_bucket, admission_state, revision, created_at) VALUES (?, 'loop', 'pg-case-workflow', 'queued', 'PROJECT-AUTO:pg-case', 'unstarted', 1, now())",
		runID,
	).Error; err != nil {
		t.Fatalf("seed the queued run: %v", err)
	}
	plans := queuedAutoPlans{byID: map[string]queuedAutoPlan{
		runID: {run: domain.Run{ID: runID}},
	}}
	promoted, err := store.RunsRepo.promoteBucket("platform", plans)
	if err != nil {
		t.Fatalf("promote the bucket: %v", err)
	}
	if len(promoted) != 1 || promoted[0] != runID {
		t.Fatalf("the promoted runs = %v, want the queued run", promoted)
	}
	if !rec.contains("quota_bucket ILIKE 'project-auto:%'") {
		t.Fatal("the promotion did not use the case-insensitive operator")
	}
}

// A workspace lease can be taken over from a background save, whose lease
// token carries the "save:" prefix. The token spells the prefix in another
// case, so only the case-insensitive operator finds the takeover.
func TestPostgresWorkspaceLeaseQuery(t *testing.T) {
	store, rec := postgresFixtureStore(t)
	runID := "pg-case-lease-run"
	postgresCaseRun(t, store, runID, "pg-case-lease-project")
	now := time.Now().UTC()
	if err := store.DB.Exec(
		"INSERT INTO run_workspace_checkpoints (run_id, last_used_at, lease_token, lease_until, lease_renewed_at) VALUES (?, ?, 'SAVE:background', ?, ?)",
		runID, now, now.Add(10*time.Minute), now,
	).Error; err != nil {
		t.Fatalf("seed the background save lease: %v", err)
	}
	token, err := store.RunsRepo.BeginWorkspaceUse(runID, now)
	if err != nil {
		t.Fatalf("take the workspace lease: %v", err)
	}
	if token == "" {
		t.Fatal("no lease token was returned")
	}
	if !rec.contains("lease_token ILIKE") {
		t.Fatal("the lease query did not use the case-insensitive operator")
	}
}
