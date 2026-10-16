package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm/logger"
)

type queryRecorder struct {
	logger.Interface
	mu      sync.Mutex
	queries []string
}

func (r *queryRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	r.mu.Lock()
	r.queries = append(r.queries, sql)
	r.mu.Unlock()
}
func queryTestStore(t testing.TB) *Store {
	t.Helper()
	var s *Store
	var err error
	if dsn := os.Getenv("AGENTFLOW_QUERY_TEST_MYSQL_DSN"); dsn != "" {
		s, err = Open(dsn)
	} else {
		s, err = OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestQuerySummaryProjectionAndScope(t *testing.T) {
	s := queryTestStore(t)
	prefix := fmt.Sprintf("query-%d-", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Millisecond)
	issue := domain.Issue{ID: prefix + "issue", ProjectID: "platform", Title: "legacy"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteIssue(issue.ID) })
	node := domain.NodeRun{NodeID: "node", Status: "succeeded", Attempt: 3, ReworkCount: 2, Provider: "mock", Model: "model", Output: strings.Repeat("large-output", 100000), Feedback: strings.Repeat("feedback", 10000), Error: strings.Repeat("e", 200), StartedAt: now.Add(-time.Minute), FinishedAt: now, ToolCalls: 7, Usage: domain.TokenUsage{TotalTokens: 123, CostUSD: 1.2}}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("%s%d", prefix, i)
		ctx := domain.TelemetryContext{IssueID: issue.ID}
		if i == 1 {
			ctx.ProjectID = "other"
		}
		if i == 2 {
			ctx.IssueID = "other"
		}
		run := domain.Run{ID: id, WorkflowID: prefix, CreatedAt: now, Status: "succeeded", ExecutionContext: ctx, Nodes: map[string]*domain.NodeRun{"node": &node}, Artifacts: []domain.Artifact{{ID: id + "artifact", RunID: id, Content: strings.Repeat("artifact", 1000)}}}
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.DeleteRun(id) })
	}
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	got, err := s.RunSummariesFor(domain.AnalyticsFilter{ProjectID: "platform", WorkflowID: prefix, From: now, To: now}, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != prefix+"0" {
		t.Fatalf("scope mismatch: %+v", got)
	}
	expected := summarizeNode(node, "node", "succeeded", 3)
	if !reflect.DeepEqual(*got[0].Nodes["node"], expected) {
		t.Fatalf("projection lost summary fields: got=%+v want=%+v", got[0].Nodes["node"], expected)
	}
	if got[0].ArtifactCount != 1 || len(got[0].Artifacts) != 0 {
		t.Fatal("artifact summary changed")
	}
	found := false
	for _, q := range recorder.queries {
		if strings.Contains(q, "run_nodes") {
			found = true
			if !strings.Contains(q, "JSON_OBJECT") || !strings.Contains(q, " IN ") {
				t.Fatalf("unbounded payload query: %s", q)
			}
		}
	}
	if !found {
		t.Fatal("missing node projection")
	}
	if got, err := s.RunSummariesFor(domain.AnalyticsFilter{WorkflowID: prefix, From: now.Add(time.Second)}, ""); err != nil || len(got) != 0 {
		t.Fatalf("time filter: %v %v", got, err)
	}
}

func TestProjectListBatchesMembers(t *testing.T) {
	s := queryTestStore(t)
	prefix := fmt.Sprintf("project-query-%d-", time.Now().UnixNano())
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("%s%d", prefix, i)
		if err := s.PutProject(domain.Project{ID: id, Name: id, Members: []domain.ProjectMember{{UserID: 1, Role: "maintainer"}}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			s.DB.Exec("DELETE FROM project_members WHERE project_id=?", id)
			s.DB.Exec("DELETE FROM projects WHERE id=?", id)
		})
	}
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	projects := s.Projects()
	if len(recorder.queries) != 2 {
		t.Fatalf("expected 2 queries regardless of project count, got %d", len(recorder.queries))
	}
	for _, p := range projects {
		if strings.HasPrefix(p.ID, prefix) && (len(p.Members) != 1 || p.Members[0].UserID != 1) {
			t.Fatalf("members lost: %+v", p)
		}
	}
}

func TestRunStreamBoundsHistory(t *testing.T) {
	s := queryTestStore(t)
	id := fmt.Sprintf("stream-query-%d", time.Now().UnixNano())
	run := domain.Run{ID: id, Revision: 3, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}, LogSequence: 150, TelemetrySequence: 150, Artifacts: []domain.Artifact{{ID: id + "a", RunID: id, Content: "must not load"}}}
	for i := 1; i <= 150; i++ {
		run.Logs = append(run.Logs, domain.LogRecord{Sequence: int64(i), Body: "log"})
		run.Telemetry = append(run.Telemetry, domain.TelemetryEvent{Sequence: int64(i), Message: "event"})
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteRun(id) })
	got, ok := s.RunStreamView(id)
	if !ok {
		t.Fatal("missing stream view")
	}
	if len(got.Logs) != 80 || got.Logs[0].Sequence != 71 || got.Logs[79].Sequence != 150 || len(got.Telemetry) != 80 || got.Telemetry[0].Sequence != 71 {
		t.Fatal("stream tail is not bounded/ordered")
	}
	if got.LogSequence != 150 || got.TelemetrySequence != 150 || len(got.Artifacts) != 1 || got.Artifacts[0].Content != "" {
		t.Fatal("stream metadata changed")
	}
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	if rev, ok := s.RunRevision(id); !ok || rev != 4 {
		t.Fatal("revision mismatch")
	}
	if !s.RunExists(id) || s.RunExists(id+"missing") {
		t.Fatal("existence mismatch")
	}
	for _, q := range recorder.queries {
		if strings.Contains(q, "SELECT *") || strings.Contains(q, "run_nodes") {
			t.Fatalf("heavy existence/revision lookup: %s", q)
		}
	}
}

func TestLiveSandboxRunIDsSkipsChildTables(t *testing.T) {
	s := queryTestStore(t)
	prefix := fmt.Sprintf("sandbox-ids-%d-", time.Now().UnixNano())
	keep := domain.Run{ID: prefix + "failed", Status: "failed", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "failed", Output: strings.Repeat("x", 1024)}}}
	drop := domain.Run{ID: prefix + "succeeded", Status: "succeeded", WorkspaceCleanedAt: time.Now().UTC(), Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "succeeded", Output: strings.Repeat("x", 1024)}}}
	if err := s.PutRun(keep); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(drop); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteRun(keep.ID); _ = s.DeleteRun(drop.ID) })
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	ids := s.LiveSandboxRunIDs()
	foundKeep, foundDrop := false, false
	for _, id := range ids {
		if id == keep.ID {
			foundKeep = true
		}
		if id == drop.ID {
			foundDrop = true
		}
	}
	if !foundKeep || foundDrop {
		t.Fatalf("ids=%v", ids)
	}
	for _, q := range recorder.queries {
		if strings.Contains(q, "run_nodes") || strings.Contains(q, "run_artifacts") || strings.Contains(q, "run_logs") || strings.Contains(q, "run_telemetry") {
			t.Fatalf("sandbox id lookup loaded children: %s", q)
		}
	}
}

func BenchmarkNodeSummaryProjection(b *testing.B) {
	s := queryTestStore(b)
	id := fmt.Sprintf("bench-query-%d", time.Now().UnixNano())
	node := domain.NodeRun{NodeID: "node", Status: "succeeded", Output: strings.Repeat("x", 1024*1024), Usage: domain.TokenUsage{TotalTokens: 100}}
	run := domain.Run{ID: id, Status: "succeeded", Nodes: map[string]*domain.NodeRun{}}
	for i := 0; i < 24; i++ {
		cp := node
		cp.NodeID = fmt.Sprintf("node-%d", i)
		run.Nodes[cp.NodeID] = &cp
	}
	if err := s.PutRun(run); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.DeleteRun(id) })
	b.Run("full_payload_before", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var rows []runNodeRow
			if err := s.DB.Where("run_id=?", id).Find(&rows).Error; err != nil {
				b.Fatal(err)
			}
			for _, row := range rows {
				var n domain.NodeRun
				_ = json.Unmarshal(row.Payload, &n)
				_ = summarizeNode(n, row.NodeID, row.Status, row.Attempt)
			}
		}
	})
	b.Run("sql_projection_after", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := s.runNodeSummaries([]string{id}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestRunViewLimitsLargeTextBeforeTransfer(t *testing.T) {
	s := queryTestStore(t)
	id := fmt.Sprintf("detail-query-%d", time.Now().UnixNano())
	text := strings.Repeat("中文测试", 10000)
	run := domain.Run{ID: id, Status: "succeeded", Nodes: map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "succeeded", Output: text, Error: text, Feedback: text, Activity: text, SessionID: "session"}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteRun(id) })
	view, ok := s.RunView(id)
	if !ok {
		t.Fatal("missing view")
	}
	n := view.Nodes["n"]
	if n == nil {
		t.Fatal("missing node")
	}
	if n.Output != string([]rune(text)[:2501]) || n.Error != string([]rune(text)[:1201]) || n.Feedback != string([]rune(text)[:801]) || n.Activity != string([]rune(text)[:301]) || n.SessionID != "session" {
		t.Fatal("detail projection is not bounded or lost state")
	}
	full, ok := s.Run(id)
	if !ok || full.Nodes["n"].Output != text {
		t.Fatal("full engine read was truncated")
	}
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("missing-%d", i)
	}
	ids = append(ids, id)
	nodes, err := s.runNodeSummaries(ids)
	if err != nil || nodes[id]["n"] == nil {
		t.Fatalf("summary batch boundary: %v", err)
	}
	counts, err := s.artifactCounts(ids)
	if err != nil || counts[id] != 0 {
		t.Fatalf("artifact batch boundary: %v", err)
	}
}

func TestUnchangedMRLinkDoesNotWrite(t *testing.T) {
	s := queryTestStore(t)
	project := fmt.Sprintf("mr-query-%d", time.Now().UnixNano())
	if err := s.EnsureMRDeliveryState(project, 1, "url", "issue"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Exec("DELETE FROM mr_delivery_states WHERE mr_project=?", project) })
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	if err := s.EnsureMRDeliveryState(project, 1, "url", "issue"); err != nil {
		t.Fatal(err)
	}
	for _, q := range recorder.queries {
		if strings.HasPrefix(q, "INSERT") || strings.HasPrefix(q, "UPDATE") {
			t.Fatalf("unchanged link was rewritten: %s", q)
		}
	}
	if err := s.EnsureMRDeliveryState(project, 1, "new-url", "issue"); err != nil {
		t.Fatal(err)
	}
	state, ok := s.mrDeliveryState(project, 1)
	if !ok || state.MRURL != "new-url" {
		t.Fatal("changed link was not persisted")
	}
}

func TestRunnerControlDoesNotLoadPayload(t *testing.T) {
	s := queryTestStore(t)
	id := fmt.Sprintf("runner-control-query-%d", time.Now().UnixNano())
	if err := s.PutRunnerTask(domain.RunnerTask{ID: id, Provider: "mock", Status: "running", DesiredState: "cancelled", LeaseOwner: "owner", Prompt: strings.Repeat("large prompt", 100000)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Exec("DELETE FROM runner_tasks WHERE id=?", id) })
	recorder := &queryRecorder{Interface: logger.Default}
	s.DB.Logger = recorder
	got, ok := s.RunnerTaskControl(id)
	if !ok || got.LeaseOwner != "owner" || got.DesiredState != "cancelled" || got.Status != "running" || got.Prompt != "" {
		t.Fatal("control state changed")
	}
	if len(recorder.queries) != 1 || strings.Contains(recorder.queries[0], "payload") || strings.Contains(recorder.queries[0], "SELECT *") {
		t.Fatal("control loaded payload")
	}
}

func TestRunSummaryEmptyProjectKeepsArray(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.RunSummariesFor(domain.AnalyticsFilter{ProjectID: "missing"}, "")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatal("project-scoped empty result must remain []")
	}
	unfiltered, err := s.RunSummaries()
	if err != nil || unfiltered != nil {
		t.Fatal("unfiltered empty result must remain null")
	}
}
