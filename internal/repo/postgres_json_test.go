package repo

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

// The repo layer carries its JSON in SQL: the transcript summary, the node
// projection and the analytics projections all read and rewrite the payload
// column in the database. MySQL's JSON functions have no Postgres equivalent,
// so every one of those statements grew a Postgres branch.
//
// This test runs the rewritten statements against a real Postgres database.
// The fixtures live in one transaction that is rolled back, so the test
// database keeps only the schema afterwards.
//
// It is skipped when AGENTFLOW_TEST_POSTGRES_URL is unset.
func TestPostgresJSONStatements(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_POSTGRES_URL is not set")
	}
	db, err := openDB(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	// This test writes fixtures, so the guard has to run before the first
	// write: a runtime database must never be touched. A skip would hide
	// exactly the mistake this guard exists for.
	var database string
	if err := db.Raw("SELECT current_database()").Row().Scan(&database); err != nil {
		t.Fatalf("read current_database: %v", err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("AGENTFLOW_TEST_POSTGRES_URL points at database %q, which is not a test database", database)
	}
	// The test database starts empty; the schema is created here with the
	// same incremental DDL the platform's own open path uses, so the schema
	// under test is the schema production would build.
	if err := autoMigrateIncremental(db); err != nil {
		t.Fatalf("auto migrate the postgres schema: %v", err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin transaction: %v", tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	store := newStore(tx)

	const runID = "pg-json-run"
	const sessionID = "pg-json-session"
	seedPostgresJSONFixtures(t, store, runID, sessionID)

	// runs.go: the run header projection keeps the frozen identity and the
	// publication metadata.
	runs, err := store.RunsRepo.RunSummariesFor(domain.AnalyticsFilter{}, "")
	if err != nil {
		t.Fatalf("run summaries: %v", err)
	}
	// The summary query filters nothing, so the fixture is looked up by id
	// instead of counted.
	var fixtureRun *domain.Run
	for i := range runs {
		if runs[i].ID == runID {
			fixtureRun = &runs[i]
			break
		}
	}
	if fixtureRun == nil {
		t.Fatalf("run summaries do not contain the fixture run %q", runID)
	}
	if got := fixtureRun.WorkflowSnapshot.Name; got != "流程" {
		t.Fatalf("snapshot name = %q, want 流程", got)
	}
	if got := fixtureRun.WorkflowSnapshot.Version; got != 3 {
		t.Fatalf("snapshot version = %d, want 3 (a JSON number, not a string)", got)
	}
	if got := fixtureRun.WorkflowSnapshot.PublishedRevision; got != 7 {
		t.Fatalf("snapshot publishedRevision = %d, want 7", got)
	}

	// runs.go: the project id is read as text.
	projectID, err := store.RunsRepo.RunWorkflowProjectID(runID)
	if err != nil {
		t.Fatalf("run workflow project id: %v", err)
	}
	if projectID != "proj-1" {
		t.Fatalf("project id = %q, want proj-1", projectID)
	}

	// runs.go: the frozen Dockerfile is read as a JSON value.
	dockerfiles := store.RunsRepo.LiveSandboxDockerfiles()
	if len(dockerfiles) != 1 || dockerfiles[0].Content != "FROM alpine" {
		t.Fatalf("dockerfiles = %+v, want the frozen Dockerfile", dockerfiles)
	}

	// runs.go: the node view truncates every long member and keeps the rest.
	view, ok := store.RunsRepo.RunView(runID)
	if !ok {
		t.Fatal("run view not found")
	}
	node := view.Nodes["node-1"]
	if node == nil {
		t.Fatalf("run view nodes = %+v, want node-1", view.Nodes)
	}
	for _, member := range []struct {
		name string
		got  string
		want string
	}{
		{"output", node.Output, postgresLongOutput[:2501]},
		{"error", node.Error, postgresLongError[:1201]},
		{"feedback", node.Feedback, postgresLongFeedback[:801]},
		{"activity", node.Activity, postgresLongActivity[:301]},
	} {
		if member.got != member.want {
			t.Fatalf("node %s = %d characters, want the %d character truncation of the fixture", member.name, len(member.got), len(member.want))
		}
	}

	// runs.go: a payload whose output is not a JSON string keeps its node.
	// MySQL's CASE unifies both arms to a string, so the value arrives as
	// text; a raw JSON value would fail the unmarshal and drop the node.
	numberNode := view.Nodes["node-number"]
	if numberNode == nil {
		t.Fatalf("run view dropped the node with a numeric output: %+v", view.Nodes)
	}
	if numberNode.Output != "42" {
		t.Fatalf("numeric output = %q, want 42 as text", numberNode.Output)
	}
	objectNode := view.Nodes["node-object"]
	if objectNode == nil {
		t.Fatalf("run view dropped the node with an object output: %+v", view.Nodes)
	}
	// jsonb orders the keys of an object by length and then bytewise, so the
	// stored text reads {"a": 2, "b": 1} where MySQL keeps the insertion
	// order {"b": 1, "a": 2}. Both are JSON strings, so the node survives.
	if objectNode.Output != `{"a": 2, "b": 1}` {
		t.Fatalf("object output = %q, want the jsonb text", objectNode.Output)
	}

	// runs.go: the node summary projection keeps the JSON values.
	summaries, err := store.RunsRepo.runNodeSummaries([]string{runID})
	if err != nil {
		t.Fatalf("run node summaries: %v", err)
	}
	nodeSummary := summaries[runID]["node-1"]
	if nodeSummary == nil {
		t.Fatalf("node summaries = %+v, want node-1", summaries)
	}
	if nodeSummary.Provider != "openai" || nodeSummary.ReworkCount != 2 || nodeSummary.ToolCalls != 3 {
		t.Fatalf("node summary = %+v, want the projected fields", nodeSummary)
	}
	if nodeSummary.Usage.TotalTokens != 10 {
		t.Fatalf("node summary usage = %+v, want 10 total tokens", nodeSummary.Usage)
	}

	// runs.go: the telemetry event id is read as text.
	if !store.RunsRepo.TelemetryEventExists(runID, "evt-1") {
		t.Fatal("telemetry event not found")
	}

	// runs.go / runs_sop_detection.go: the collaboration mode filter reads
	// the snapshot as text.
	detections, err := store.RunsRepo.PendingSOPDetections("proj-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("pending sop detections: %v", err)
	}
	if len(detections) != 1 || detections[0] != runID {
		t.Fatalf("pending sop detections = %v, want the fixture run", detections)
	}

	// assistant_run_meta.go: the workflow name and the message count.
	metas := store.RunsRepo.SidebarRunMetas([]string{runID})
	if metas[runID].WorkflowName != "流程" {
		t.Fatalf("sidebar workflow name = %q, want 流程", metas[runID].WorkflowName)
	}
	counts := store.AssistantRepo.AssistantSessionMessageCounts([]string{sessionID})
	if counts[sessionID] != 2 {
		t.Fatalf("assistant message count = %d, want 2", counts[sessionID])
	}

	// assistant_shared.go: the metadata projection drops the transcript.
	session, ok := store.AssistantRepo.AssistantSessionMetadata(sessionID)
	if !ok {
		t.Fatal("assistant session metadata not found")
	}
	if len(session.Messages) != 0 {
		t.Fatalf("assistant session metadata kept %d messages", len(session.Messages))
	}

	// assistant.go: the summary projection counts the messages and caches
	// the participants.
	var rows []assistantSessionRow
	if err := store.AssistantRepo.assistantSessionSummaries().Where("id = ?", sessionID).Find(&rows).Error; err != nil {
		t.Fatalf("assistant session summaries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("assistant session summaries = %d rows, want 1", len(rows))
	}
	var summary struct {
		Messages []domain.AssistantMessage `json:"messages"`
		Count    int                       `json:"executionMessageCount"`
		Summary  []struct {
			Role      string `json:"role"`
			AuthorID  int64  `json:"authorId"`
			AuthorNam string `json:"authorName"`
		} `json:"participantSummaryMessages"`
	}
	if err := json.Unmarshal(rows[0].Payload, &summary); err != nil {
		t.Fatalf("decode summary payload: %v", err)
	}
	if len(summary.Messages) != 0 {
		t.Fatalf("summary projection kept %d messages", len(summary.Messages))
	}
	if summary.Count != 2 {
		t.Fatalf("executionMessageCount = %d, want 2", summary.Count)
	}
	if len(summary.Summary) != 1 || summary.Summary[0].Role != "user" {
		t.Fatalf("participant summary = %+v, want the one user message", summary.Summary)
	}
	if summary.Summary[0].AuthorID != 7 {
		t.Fatalf("participant author id = %d, want 7 (a JSON number)", summary.Summary[0].AuthorID)
	}
	if summary.Summary[0].AuthorNam != "张三" {
		t.Fatalf("participant author name = %q, want 张三", summary.Summary[0].AuthorNam)
	}

	// assistant.go: the fallback projection drops the transcript too.
	legacy, err := store.AssistantRepo.findAssistantSessionSummaries("id = ?", sessionID)
	if err != nil {
		t.Fatalf("find assistant session summaries: %v", err)
	}
	if len(legacy) != 1 {
		t.Fatalf("find assistant session summaries = %d rows, want 1", len(legacy))
	}

	// assistant.go: the participant backfill writes the cached participants.
	before, err := store.AssistantRepo.findAssistantSessionSummaries("id = ?", sessionID)
	if err != nil {
		t.Fatalf("read sessions before backfill: %v", err)
	}
	if store.AssistantRepo.backfillAssistantParticipants(before) == 0 {
		t.Fatal("participant backfill wrote no row")
	}
	after, err := store.AssistantRepo.findAssistantSessionSummaries("id = ?", sessionID)
	if err != nil {
		t.Fatalf("read sessions after backfill: %v", err)
	}
	var cached struct {
		Version      int                           `json:"participantsVersion"`
		Participants []domain.AssistantParticipant `json:"participants"`
	}
	if err := json.Unmarshal(after[0].Payload, &cached); err != nil {
		t.Fatalf("decode cached participants: %v", err)
	}
	if cached.Version != 1 {
		t.Fatalf("participantsVersion = %d, want 1 (a JSON number)", cached.Version)
	}
	if len(cached.Participants) != 1 || cached.Participants[0].ID != 7 {
		t.Fatalf("cached participants = %+v, want the one user", cached.Participants)
	}

	// assistant.go: the title setter rewrites the payload members.
	if err := store.AssistantRepo.SetAssistantSessionTitle(1, sessionID, "proj-1", "新标题"); err != nil {
		t.Fatalf("set assistant session title: %v", err)
	}
	var titled struct {
		Title       string `json:"title"`
		TitleEdited bool   `json:"titleEdited"`
		Source      string `json:"source"`
	}
	if _, ok := store.AssistantRepo.AssistantSessionMetadata(sessionID); !ok {
		t.Fatal("titled session not found")
	}
	var titledPayload []byte
	if err := store.DB.Raw("SELECT payload FROM assistant_sessions WHERE id = ?", sessionID).Row().Scan(&titledPayload); err != nil {
		t.Fatalf("read titled payload: %v", err)
	}
	if err := json.Unmarshal(titledPayload, &titled); err != nil {
		t.Fatalf("decode titled payload: %v", err)
	}
	if titled.Title != "新标题" || !titled.TitleEdited {
		t.Fatalf("titled payload = %+v, want the new title and the edited flag", titled)
	}
	if titled.Source == "" {
		t.Fatalf("titled payload = %+v, want the inferred source", titled)
	}

	// analytics.go: the tool signal counts the successes.
	signals, err := store.AnalyticsRepo.analyticsRunSignals([]string{runID})
	if err != nil {
		t.Fatalf("analytics run signals: %v", err)
	}
	if got := signals[runID].toolSucceeded; got != 1 {
		t.Fatalf("tool succeeded = %d, want 1", got)
	}
	if got := signals[runID].toolFailed; got != 0 {
		t.Fatalf("tool failed = %d, want 0", got)
	}

	// analytics_activity_usage.go: the usage projection reads the assistant
	// replies out of the transcript.
	usageIDs := []string{sessionID}
	var usageSeen []analyticsUsageMessage
	if err := store.AnalyticsRepo.analyticsSessionUsage(usageIDs, func(id string, messages []analyticsUsageMessage) {
		usageSeen = messages
	}); err != nil {
		t.Fatalf("analytics session usage: %v", err)
	}
	if len(usageSeen) != 1 {
		t.Fatalf("analytics usage messages = %+v, want the one assistant reply", usageSeen)
	}
	if usageSeen[0].Index != 1 {
		t.Fatalf("analytics usage index = %d, want 1", usageSeen[0].Index)
	}
	if usageSeen[0].Usage == nil || usageSeen[0].Usage.TotalTokens != 5 {
		t.Fatalf("analytics usage = %+v, want 5 total tokens", usageSeen[0].Usage)
	}

	// analytics_activity_usage.go: the fallback projection reads the whole
	// transcript when the JSON_TABLE projection is unavailable. Postgres has
	// no JSON_TABLE, but the fallback path still has to work there.
	previous := noAnalyticsUsageProjection.Swap(true)
	t.Cleanup(func() { noAnalyticsUsageProjection.Store(previous) })
	var fallbackUsage []analyticsUsageMessage
	if err := store.AnalyticsRepo.analyticsSessionUsage(usageIDs, func(id string, messages []analyticsUsageMessage) {
		fallbackUsage = messages
	}); err != nil {
		noAnalyticsUsageProjection.Store(previous)
		t.Fatalf("analytics session usage fallback: %v", err)
	}
	noAnalyticsUsageProjection.Store(previous)
	if len(fallbackUsage) != 1 {
		t.Fatalf("analytics fallback usage messages = %+v, want the one assistant reply", fallbackUsage)
	}
	if fallbackUsage[0].Index != 1 {
		t.Fatalf("analytics fallback usage index = %d, want 1", fallbackUsage[0].Index)
	}
	if fallbackUsage[0].Usage == nil || fallbackUsage[0].Usage.TotalTokens != 5 {
		t.Fatalf("analytics fallback usage = %+v, want 5 total tokens", fallbackUsage[0].Usage)
	}

	// analytics_activity.go / analytics_dashboard.go /
	// analytics_dashboard_signals.go: the snapshot and metadata projections.
	snapshot := domain.AnalyticsSnapshot{Dashboard: &domain.AnalyticsDashboard{}}
	if err := store.AnalyticsRepo.analyticsActivity(domain.AnalyticsFilter{}, nil, nil, &snapshot); err != nil {
		t.Fatalf("analytics activity: %v", err)
	}
	issues := map[string]domain.Issue{
		"issue-1": {ID: "issue-1", ProjectID: "proj-1", DeliveryState: "merged", AssigneeUserID: 1},
	}
	signalRuns, err := store.AnalyticsRepo.analyticsDashboardSignalRuns(runs, domain.AnalyticsFilter{UserID: "1"}, issues)
	if err != nil {
		t.Fatalf("analytics dashboard signal runs: %v", err)
	}
	found := false
	for _, run := range signalRuns {
		if run.ID == runID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("analytics dashboard signal runs dropped the fixture run %q", runID)
	}
	if len(signalRuns) != len(runs) {
		t.Fatalf("analytics dashboard signal runs = %d, want %d", len(signalRuns), len(runs))
	}
	if err := store.AnalyticsRepo.analyticsDashboard(domain.AnalyticsFilter{}, runs, issues, map[string]domain.WorkflowDefinition{}, map[string]analyticsRunSignals{}, &snapshot); err != nil {
		t.Fatalf("analytics dashboard: %v", err)
	}
	deliveries := 0
	if err := store.AnalyticsRepo.analyticsDashboardDeliveries(domain.AnalyticsFilter{}, issues, func(time.Time, bool) {
		deliveries++
	}); err != nil {
		t.Fatalf("analytics dashboard deliveries: %v", err)
	}
	if deliveries == 0 {
		t.Fatal("analytics dashboard deliveries visited nothing")
	}
}

// The node view truncates every member it projects, so the fixture values are
// longer than the four limits (2501 / 1201 / 801 / 301).
var (
	postgresLongOutput   = strings.Repeat("x", 3000)
	postgresLongError    = strings.Repeat("e", 1500)
	postgresLongFeedback = strings.Repeat("f", 1000)
	postgresLongActivity = strings.Repeat("a", 400)
)

// seedPostgresJSONFixtures writes one row per JSON shape the rewritten
// statements read. The caller rolls the transaction back.
func seedPostgresJSONFixtures(t *testing.T, store *Store, runID, sessionID string) {
	t.Helper()
	if err := store.DB.Exec(`INSERT INTO runs (id, kind, workflow_id, status, snapshot, execution_context, input, created_at, updated_at)
		VALUES (?, 'workflow', 'wf-1', 'succeeded', ?::jsonb, ?::jsonb, 'do it', now(), now())`, runID,
		`{"id":"snap-1","name":"流程","collaborationMode":"solo","status":"published","version":3,"publishedRevision":7,`+
			`"projectId":"proj-1","sandboxSnapshot":{"dockerfile":{"name":"Dockerfile","content":"FROM alpine"}}}`,
		`{"issueId":"issue-1","projectId":"proj-1","responsibleUserId":1}`).Error; err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if err := store.DB.Exec(`INSERT INTO mr_delivery_states (mr_project, mr_iid, mr_url, state, target_branch, issue_id, merged_at, created_at, updated_at)
		VALUES ('repo-1', 1, 'https://example.test/repo-1/merge_requests/1', 'merged', 'main', 'issue-1', now(), now(), now())`).Error; err != nil {
		t.Fatalf("insert mr delivery state: %v", err)
	}
	if err := store.DB.Exec(`INSERT INTO run_nodes (run_id, node_id, status, attempt, payload)
		VALUES (?, 'node-1', 'succeeded', 1, ?::jsonb)`, runID,
		`{"output":"`+postgresLongOutput+`","error":"`+postgresLongError+`","feedback":"`+postgresLongFeedback+`",`+
			`"activity":"`+postgresLongActivity+`","provider":"openai","model":"gpt","reworkCount":2,`+
			`"usage":{"totalTokens":10},"toolCalls":3,"startedAt":"2026-01-01T00:00:00Z","finishedAt":"2026-01-01T00:01:00Z"}`).Error; err != nil {
		t.Fatalf("insert run node: %v", err)
	}
	// A node whose output is a JSON number and one whose output is a JSON
	// object: MySQL stringifies both, so both must survive the run view.
	if err := store.DB.Exec(`INSERT INTO run_nodes (run_id, node_id, status, attempt, payload)
		VALUES (?, 'node-number', 'succeeded', 1, ?::jsonb)`, runID,
		`{"output":42,"provider":"openai","model":"gpt"}`).Error; err != nil {
		t.Fatalf("insert run node with a numeric output: %v", err)
	}
	if err := store.DB.Exec(`INSERT INTO run_nodes (run_id, node_id, status, attempt, payload)
		VALUES (?, 'node-object', 'succeeded', 1, ?::jsonb)`, runID,
		`{"output":{"b":1,"a":2},"provider":"openai","model":"gpt"}`).Error; err != nil {
		t.Fatalf("insert run node with an object output: %v", err)
	}
	if err := store.DB.Exec(`INSERT INTO run_telemetry (run_id, seq, payload) VALUES (?, 1, ?::jsonb)`, runID,
		`{"id":"evt-1","category":"tool","name":"tool.call","phase":"finish","level":"info","tool":{"success":true}}`).Error; err != nil {
		t.Fatalf("insert run telemetry: %v", err)
	}
	if err := store.DB.Exec(`INSERT INTO assistant_sessions (id, user_id, project_id, title, model, source, task_id, payload, created_at, updated_at)
		VALUES (?, 1, 'proj-1', '会话', 'gpt', '', '', ?::jsonb, now(), now())`, sessionID,
		`{"title":"会话","messages":[{"role":"user","authorId":7,"authorName":"张三","content":"你好"},`+
			`{"role":"assistant","content":"ok","createdAt":"2026-01-01T00:00:00Z","usage":{"totalTokens":5}}]}`).Error; err != nil {
		t.Fatalf("insert assistant session: %v", err)
	}
}
