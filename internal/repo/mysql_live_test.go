package repo

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestMySQLReadsObserveExternalChangesWithoutReload(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	suffix := time.Now().UTC().UnixNano()
	rule := domain.RuleDefinition{ID: fmt.Sprintf("direct-rule-%d", suffix), Name: "before", Content: "before"}
	runID := fmt.Sprintf("direct-run-%d", suffix)
	defer func() {
		_ = s.DB.Exec(`DELETE FROM run_artifacts WHERE run_id=?`, runID)
		_ = s.DB.Exec(`DELETE FROM run_logs WHERE run_id=?`, runID)
		_ = s.DB.Exec(`DELETE FROM run_telemetry WHERE run_id=?`, runID)
		_ = s.DB.Exec(`DELETE FROM run_events WHERE run_id=?`, runID)
		_ = s.DB.Exec(`DELETE FROM run_nodes WHERE run_id=?`, runID)
		_ = s.DB.Exec(`DELETE FROM runs WHERE id=?`, runID)
		_ = s.DB.Exec(`DELETE FROM rules WHERE id=?`, rule.ID)
	}()
	if err := s.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	rule.Name = "changed outside Store"
	rule.Content = "database wins"
	raw, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`UPDATE rules SET name=?, payload=? WHERE id=?`, rule.Name, raw, rule.ID).Error; err != nil {
		t.Fatal(err)
	}

	got, ok := s.Rule(rule.ID)
	if !ok || got.Name != rule.Name || got.Content != rule.Content {
		t.Fatalf("read did not observe direct MySQL update: ok=%v got=%+v", ok, got)
	}
	run := domain.Run{
		ID:                runID,
		WorkflowID:        runID,
		Status:            "running",
		Nodes:             map[string]*domain.NodeRun{"node": {NodeID: "node", Status: "running"}},
		Telemetry:         []domain.TelemetryEvent{{ID: "telemetry", Sequence: 1, Source: domain.TelemetrySource{Adapter: "live-test"}}},
		TelemetrySequence: 1,
		Logs:              []domain.LogRecord{{ID: "log", Sequence: 1, Level: "warn"}},
		LogSequence:       1,
		Artifacts:         []domain.Artifact{{ID: "artifact", RunID: runID, NodeID: "node", Name: "report", Path: "report.md"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	statuses := s.RunStatuses()
	if statuses[run.ID] != "running" {
		t.Fatalf("RunStatuses did not read the persisted status: %+v", statuses)
	}
	runs := s.Runs()
	var listed *domain.Run
	for i := range runs {
		if runs[i].ID == run.ID {
			listed = &runs[i]
			break
		}
	}
	if listed == nil || listed.Nodes["node"] == nil || listed.Nodes["node"].Status != "running" {
		t.Fatalf("Runs did not batch-hydrate the persisted node: %+v", listed)
	}
	summaries, err := s.RunSummaries()
	if err != nil {
		t.Fatal(err)
	}
	var summary *domain.Run
	for i := range summaries {
		if summaries[i].ID == run.ID {
			summary = &summaries[i]
			break
		}
	}
	if summary == nil || summary.Nodes["node"] == nil || summary.Nodes["node"].Status != "running" {
		t.Fatalf("RunSummaries did not retain node status: %+v", summary)
	}
	if summary.Nodes["node"].Output != "" || summary.WorkflowSnapshot.ID != "" {
		t.Fatalf("RunSummaries transferred heavy fields: %+v", summary)
	}
	if summary.ArtifactCount != 1 || len(summary.Artifacts) != 0 {
		t.Fatalf("RunSummaries did not reduce artifacts to a count: %+v", summary)
	}
	analytics := s.Analytics(domain.AnalyticsFilter{WorkflowID: runID})
	if analytics.Coverage.AdapterEvents["live-test"] != 1 || analytics.Coverage.LogLevels["warn"] != 1 {
		t.Fatalf("Analytics did not aggregate MySQL coverage: %+v", analytics.Coverage)
	}
	var persistedStatus string
	if err := s.DB.Raw(`SELECT status FROM runs WHERE id=?`, run.ID).Scan(&persistedStatus).Error; err != nil || persistedStatus != "running" {
		t.Fatalf("PutRun did not synchronously reach MySQL: status=%q err=%v", persistedStatus, err)
	}
	if err := s.DB.Exec(`UPDATE runs SET status='failed' WHERE id=?`, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotStatus := s.RunStatuses()[run.ID]; gotStatus != "failed" {
		t.Fatalf("RunStatuses did not observe direct MySQL update: %q", gotStatus)
	}
	if gotRun, ok := s.Run(run.ID); !ok || gotRun.Status != "failed" {
		t.Fatalf("Run did not observe direct MySQL update: ok=%v run=%+v", ok, gotRun)
	}
}

func TestMySQLMCPFilesRoundTrip(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id := fmt.Sprintf("mysql-mcp-files-%d", time.Now().UTC().UnixNano())
	server := domain.MCPServerDefinition{ID: id, Name: "MySQL MCP files", Transport: "stdio", Command: "python3"}
	if err := s.PutMCPServer(server); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.DeleteMCPServer(id) }()
	if err := s.PutMCPFiles(id, []SkillFileInput{{Path: "server.py", Content: []byte("print('ok')\n"), Mode: 0o755}}); err != nil {
		t.Fatal(err)
	}
	stored, ok := s.MCPServer(id)
	if !ok || len(stored.FileRefs) != 1 || stored.FileRefs[0].Path != "server.py" || stored.FileRefs[0].Mode != 0o755 {
		t.Fatalf("stored=%+v ok=%v", stored, ok)
	}
	content, ok := s.MCPFileContent(id, "server.py")
	if !ok || string(content) != "print('ok')\n" {
		t.Fatalf("content=%q ok=%v", content, ok)
	}
}

func TestMySQLRunnerTaskCompletionScrubsCredentials(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	suffix := time.Now().UTC().UnixNano()
	taskID := fmt.Sprintf("credential-task-%d", suffix)
	runnerID := fmt.Sprintf("credential-runner-%d", suffix)
	defer func() { _ = s.DB.Exec(`DELETE FROM runner_tasks WHERE id=?`, taskID) }()
	task := domain.RunnerTask{
		ID: taskID, RunnerID: runnerID, Provider: "codex", Status: domain.RunnerTaskQueued, MaxAttempts: 1,
		Sandbox: &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "runner-docker", Driver: "docker"}},
		Invocation: &domain.RunnerInvocation{
			CredentialSource: domain.RuntimeAuthPersonal,
			CodexAPIKey:      "codex-secret",
			CodexBaseURL:     "https://gateway.example/v1",
			Repository:       &domain.RunnerRepository{CloneURL: "https://example.com/repo.git", GitToken: "git-secret"},
		},
	}
	if err := s.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.ClaimRunnerTask(runnerID, []string{"codex"}, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if claimed.Sandbox == nil || claimed.Sandbox.Profile.Driver != "docker" {
		t.Fatalf("claimed task lost runner sandbox: %+v", claimed.Sandbox)
	}
	if err := s.CompleteRunnerTask(claimed.ID, runnerID, domain.RunnerTaskSuccess, nil, "done", "", nil, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := s.DB.Raw(`SELECT payload FROM runner_tasks WHERE id=?`, taskID).Scan(&payload).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "codex-secret") || strings.Contains(payload, "git-secret") || strings.Contains(payload, "gateway.example") {
		t.Fatalf("completed task retained credential material: %s", payload)
	}
}
