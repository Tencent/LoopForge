package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestNodeArtifactCommitFailure(t *testing.T) {
	store := storeRejectingPlanArtifact(t)
	run := runArtifactSOP(t, store, "# Plan\n", "failed")
	assertArtifactCommitFailed(t, run, "artifact write rejected")
}

func TestNodeArtifactCommitFailureRetriesTerminalDeadlock(t *testing.T) {
	checkArtifactCommitLockConflict(t, 1213)
}

func TestNodeArtifactCommitFailureRetriesLockWaitTimeout(t *testing.T) {
	checkArtifactCommitLockConflict(t, 1205)
}

func checkArtifactCommitLockConflict(t *testing.T, code uint16) {
	t.Helper()
	store := storeRejectingPlanArtifact(t)
	var lockErrorInjected atomic.Bool
	err := store.DB.Callback().Create().Before("gorm:create").Register("test:lock-failed-node", func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == "run_nodes" && ok && values["node_id"] == "writing-plans" &&
			values["status"] == "failed" && !lockErrorInjected.Swap(true) {
			tx.AddError(&mysql.MySQLError{Number: code, Message: "lock conflict writing failed node"})
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	run := runArtifactSOP(t, store, "# Plan\n", "failed")
	if !lockErrorInjected.Load() {
		t.Fatalf("failed-node write did not encounter injected MySQL error %d", code)
	}
	assertArtifactCommitFailed(t, run, "artifact write rejected")
	commitFailures := 0
	for _, event := range run.Telemetry {
		if event.NodeID == "writing-plans" && event.Name == "node.commit.failed" {
			commitFailures++
		}
	}
	if commitFailures != 1 {
		t.Fatalf("got %d artifact commit failure observations, want 1", commitFailures)
	}
}

func storeRejectingPlanArtifact(t *testing.T) *repo.Store {
	t.Helper()
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.DB.Exec(`CREATE TRIGGER reject_plan BEFORE INSERT ON run_artifacts
		WHEN NEW.name = 'plan' BEGIN SELECT RAISE(ABORT, 'artifact write rejected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	return store
}

func TestOptionalArtifactCommitFailureAllowsApproval(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.DB.Exec(`CREATE TRIGGER reject_optional BEFORE INSERT ON run_artifacts
		WHEN NEW.name = 'optional' BEGIN SELECT RAISE(ABORT, 'optional storage unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	run := runArtifactSOP(t, store, "# Plan\n", "waiting", "optional diagnostics")
	assertArchivedPlan(t, store, run, "# Plan\n")
}

func TestMySQLNodeArtifactCommit(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_RUN_ARTIFACT_TEST_DSN")
	if dsn == "" {
		t.Skip("set AGENTFLOW_RUN_ARTIFACT_TEST_DSN to a disposable MySQL server")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.DBName = ""
	admin, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	database := fmt.Sprintf("loopforge_artifact_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE `"+database+"` CHARACTER SET utf8mb4"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE `" + database + "`"); err != nil {
			t.Error(err)
		}
	})
	config.DBName = database
	config.ParseTime = true
	config.MultiStatements = true
	store, err := repo.Open(config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	const columnQuery = `SELECT DATA_TYPE, CHARACTER_SET_NAME, COLLATION_NAME, IS_NULLABLE
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE()
		AND TABLE_NAME = 'run_artifacts' AND COLUMN_NAME = 'content'`
	var freshType, charset, collation, nullable string
	if err := store.DB.Raw(columnQuery).Row().Scan(&freshType, &charset, &collation, &nullable); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Exec(`ALTER TABLE run_artifacts MODIFY COLUMN content
		TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`).Error; err != nil {
		t.Fatal(err)
	}
	boundary := strings.Repeat("界", 21845) // 65,535 UTF-8 bytes.
	legacy := runArtifactSOP(t, store, boundary, "waiting")
	assertArchivedPlan(t, store, legacy, boundary)
	rejected := runArtifactSOP(t, store, boundary+"x", "failed")
	assertArtifactCommitFailed(t, rejected, "Error 1406")
	if freshType != "mediumtext" {
		t.Fatalf("new installation uses %q, want mediumtext", freshType)
	}

	migration, err := os.ReadFile("../../migrations/042_run_artifacts_content_mediumtext.sql")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if err := store.DB.Exec(string(migration)).Error; err != nil {
			t.Fatalf("migration %d: %v", attempt+1, err)
		}
		var dataType string
		if err := store.DB.Raw(columnQuery).Row().Scan(&dataType, &charset, &collation, &nullable); err != nil {
			t.Fatal(err)
		}
		if dataType != "mediumtext" || charset != "utf8mb4" || collation != "utf8mb4_bin" || nullable != "NO" {
			t.Fatalf("migration %d: unexpected column %s %s %s nullable=%s", attempt+1, dataType, charset, collation, nullable)
		}
	}
	assertArchivedPlan(t, store, legacy, boundary)
	for _, content := range []string{boundary + "x", strings.Repeat("x", domain.MaxSOPArtifactBytes), strings.Repeat("\xff\x00", 1024)} {
		run := runArtifactSOP(t, store, content, "waiting")
		assertArchivedPlan(t, store, run, content)
	}
	oversized := runArtifactSOP(t, store, strings.Repeat("x", domain.MaxSOPArtifactBytes+1), "failed")
	assertOversizedPlanRejected(t, oversized)
}

func runArtifactSOP(t *testing.T, store *repo.Store, content, wantStatus string, optional ...string) domain.Run {
	t.Helper()
	const sandboxID = "artifact-sandbox"
	if err := store.PutSandbox(domain.SandboxProfile{ID: sandboxID, Name: "Artifact sandbox", Driver: domain.SandboxDriverNative}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExecutor(domain.ExecutorDefinition{ID: "mock", Name: "Mock executor", Kind: "mock", Protocol: "events-v1"}); err != nil {
		t.Fatal(err)
	}
	putGateTestAgent(t, store, "plan-agent", nil)
	workflow := domain.WorkflowDefinition{
		ID: fmt.Sprintf("artifact-commit-%d", time.Now().UnixNano()), Name: "Artifact commit", SandboxProfileID: sandboxID,
		Nodes: []domain.WorkflowNode{
			{ID: "writing-plans", Name: "Writing plans", Type: "agent", AgentID: "plan-agent", MaxAttempts: 3,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "plan.md", Required: true}}},
			{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "plan-approval", Source: "writing-plans", Target: "approval", When: "success"}},
	}
	if len(optional) != 0 {
		workflow.Nodes[0].ArtifactOutputs = append(workflow.Nodes[0].ArtifactOutputs, domain.ArtifactContract{Name: "optional", Path: "optional.md"})
	}
	if err := store.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	manager := runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))
	eng := New(store, manager)
	// Queue on an API-only instance so the fixture can be placed before execution.
	eng.ConfigureExecution("artifact-worker", "", 30*time.Second, false)
	run, err := eng.Start(workflow.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	workspace := manager.RunWorkspace(run.ID)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "plan.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if len(optional) != 0 {
		if err := os.WriteFile(filepath.Join(workspace, "optional.md"), []byte(optional[0]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	eng.ConfigureExecution("artifact-worker", "", 30*time.Second, true)
	if _, err := eng.acquireLease(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	runID := run.ID
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.execute(ctx, runID)
	}()
	defer func() { cancel(); <-done }()
	for ctx.Err() == nil {
		eng.flushAllObservations()
		current, ok := store.Run(runID)
		if !ok {
			t.Fatal("could not read run")
		}
		run = current
		if run.Status == wantStatus {
			t.Logf("%d bytes: run=%s, node=%s, approval=%s, artifacts=%d", len(content), run.Status, run.Nodes["writing-plans"].Status, run.Nodes["approval"].Status, len(run.Artifacts))
			return run
		}
		if (wantStatus == "failed" && planPublished(run)) || domain.TerminalRunStatus(run.Status) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	node := run.Nodes["writing-plans"]
	t.Fatalf("%d bytes: run=%s, node=%s, approval=%s, artifacts=%d, plan published=%t, error=%q, want run=%s", len(content), run.Status, node.Status, run.Nodes["approval"].Status, len(run.Artifacts), planPublished(run), node.Error, wantStatus)
	return domain.Run{}
}

func planPublished(run domain.Run) bool {
	return slices.ContainsFunc(run.Telemetry, func(event domain.TelemetryEvent) bool {
		return event.NodeID == "writing-plans" && event.Name == "artifact.published" && event.Message == "plan"
	})
}

func assertArtifactCommitFailed(t *testing.T, run domain.Run, cause string) {
	t.Helper()
	node := run.Nodes["writing-plans"]
	if node.Status != "failed" || node.Attempt != 1 || node.FinishedAt.IsZero() || !strings.Contains(node.Error, cause) {
		t.Fatalf("node status=%s attempt=%d finished=%s error=%q, want one failed attempt with %q", node.Status, node.Attempt, node.FinishedAt, node.Error, cause)
	}
	if node.GateResult == nil || node.GateResult.Decision != "pass" {
		t.Fatal("expected the agent to pass its gate before the artifact write failed")
	}
	if run.Nodes["approval"].Status != "pending" || len(run.Artifacts) != 0 || planPublished(run) {
		t.Fatalf("rejected write advanced the run or published an artifact: approval=%s artifacts=%d plan published=%t", run.Nodes["approval"].Status, len(run.Artifacts), planPublished(run))
	}
	if !slices.ContainsFunc(run.Events, func(event domain.RunEvent) bool {
		return event.NodeID == "writing-plans" && event.Type == "node.failed" && strings.Contains(event.Message, cause)
	}) {
		t.Fatal("missing persisted node failure event")
	}
}

func assertArchivedPlan(t *testing.T, store *repo.Store, run domain.Run, content string) {
	t.Helper()
	if run.Nodes["writing-plans"].Status != "succeeded" || run.Nodes["approval"].Status != "waiting_approval" || len(run.Artifacts) != 1 {
		t.Fatal("the completed node did not persist its artifact and start approval")
	}
	artifact, ok := store.Artifact(run.ID, run.Artifacts[0].ID)
	if !ok || artifact.Size != int64(len(content)) || artifact.Content != content {
		t.Fatalf("stored artifact differs from the %d-byte plan", len(content))
	}
}

func assertOversizedPlanRejected(t *testing.T, run domain.Run) {
	t.Helper()
	if got := run.Nodes["writing-plans"].Error; !strings.Contains(got, "plan (plan.md): "+errOutputTooLarge.Error()) {
		t.Fatalf("oversized plan returned %q", got)
	}
	archived := slices.ContainsFunc(run.Artifacts, func(artifact domain.Artifact) bool {
		return artifact.NodeID == "writing-plans" && artifact.Name == "plan"
	})
	if archived || planPublished(run) || run.Nodes["approval"].Status != "pending" || run.Nodes["writing-plans"].Attempt != 1 {
		t.Fatal("oversized plan was archived, published, retried, or passed to approval")
	}
}

// A failed terminal write that is not a deadlock must not strand the run: the
// node's auto-reject still resets it with its upstream so the run finishes.
func TestNodeFailureWriteErrorKeepsAutoReject(t *testing.T) {
	for name, injected := range map[string]error{
		"lock wait timeout": &mysql.MySQLError{Number: 1205, Message: "lock wait timeout writing failed node"},
		"plain error":       fmt.Errorf("transient store failure"),
		"deadlock budget":   &mysql.MySQLError{Number: 1213, Message: "deadlock writing failed node"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			var failures atomic.Int32
			limit := int32(1)
			if name == "deadlock budget" {
				limit = 6 // every RetryDeadlock attempt is a deadlock victim
			}
			err = s.DB.Callback().Create().Before("gorm:create").Register("test:fail-review-node", func(tx *gorm.DB) {
				values, ok := tx.Statement.Dest.(map[string]any)
				if tx.Statement.Table == "run_nodes" && ok && values["node_id"] == "review" && values["status"] == "failed" {
					if failures.Add(1) <= limit {
						tx.AddError(injected)
					}
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutAgent(domain.AgentDefinition{ID: "write-repair-agent", Name: "write-repair-agent", ExecutorID: "mock", Prompt: "repair"}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutAgent(domain.AgentDefinition{ID: "write-review-agent", Name: "write-review-agent", ExecutorID: "mock", Prompt: "[[mock:gate=rework,pass]]"}); err != nil {
				t.Fatal(err)
			}
			wf := domain.WorkflowDefinition{ID: "write-error-reject", Name: "write error reject", SandboxProfileID: "test-native", Nodes: []domain.WorkflowNode{
				{ID: "repair", Name: "repair", Type: "agent", AgentID: "write-repair-agent", ExecutorID: "mock", MaxAttempts: 1},
				{ID: "review", Name: "review", Type: "agent", AgentID: "write-review-agent", ExecutorID: "mock", MaxAttempts: 3, MaxReworks: 3, RejectTo: "repair"},
			}, Edges: []domain.WorkflowEdge{{ID: "e1", Source: "repair", Target: "review", When: "success"}}}
			if err := s.PutWorkflow(wf); err != nil {
				t.Fatal(err)
			}
			e := New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
			run, err := e.Start(wf.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, s, run.ID, "succeeded")
			if failures.Load() < limit {
				t.Fatalf("failed-node write hit %d injected errors, want %d", failures.Load(), limit)
			}
			current, _ := s.Run(run.ID)
			if repair := current.Nodes["repair"]; repair.ReworkCount != 1 {
				t.Fatalf("repair reworkCount = %d, want the auto-reject to rework it once", repair.ReworkCount)
			}
			if review := current.Nodes["review"]; review.Status != "succeeded" {
				t.Fatalf("review status = %q, want succeeded after the rework", review.Status)
			}
		})
	}
}
