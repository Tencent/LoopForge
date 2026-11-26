package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestMySQLConcurrentSOPsCannotClaimSameConversationWorkspace(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	runner := domain.Runner{ID: prefix + "-runner", OwnerUserID: 1, Status: domain.RunnerStatusOnline}
	if err := stores[0].PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stores[0].DB.Where("id = ?", runner.ID).Delete(&runnerRow{}) })
	owner := admissionMySQLRun(prefix+"-ws", prefix)
	owner.Kind, owner.Status = domain.RunKindWorkspace, "waiting"
	if err := stores[0].PutRun(owner); err != nil {
		t.Fatal(err)
	}
	ids := []string{prefix + "-first", prefix + "-second"}
	admissionMySQLCleanup(t, stores[0], append([]string{owner.ID}, ids...))
	// Both new IDs are in the same index gap. With an unlimited Runner, no
	// quota lock serializes the dispatches before they reach the owner lock.
	entered, release := make(chan struct{}, len(stores)), make(chan struct{})
	const callback = "test:sop_workspace_admission"
	for _, store := range stores {
		query := store.DB.Callback().Query()
		if err := query.Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			where, _ := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
			if tx.Statement.Table != "runs" || len(where.Exprs) != 1 {
				return
			}
			id, _ := where.Exprs[0].(clause.Expr)
			if _, locked := tx.Statement.Clauses["FOR"]; locked && len(id.Vars) == 1 && id.Vars[0] == owner.ID {
				select {
				case entered <- struct{}{}:
					<-release
				case <-release:
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer query.Remove(callback)
	}
	results := make(chan error, len(stores))
	for i, store := range stores {
		run := admissionMySQLRun(ids[i], prefix)
		run.ExecutionContext.ExecutionPinned, run.ExecutionContext.WorkspaceRunID = true, owner.ID
		run.ExecutionContext.ExecutionRunnerID = runner.ID
		go func() { results <- store.PutRun(run) }()
	}
	timeout := time.After(5 * time.Second)
waitForDispatches:
	for range stores {
		select {
		case <-entered:
		case <-timeout:
			t.Error("both dispatches did not reach the workspace lock")
			break waitForDispatches
		}
	}
	close(release)
	admitted := 0
	for range stores {
		select {
		case err := <-results:
			if err == nil {
				admitted++
			} else if !errors.Is(err, ErrWorkspaceInUse) {
				t.Errorf("dispatch returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("workspace admission did not finish")
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d runs into one conversation workspace", admitted)
	}
	if _, err := stores[0].BeginWorkspaceUse(owner.ID, time.Now()); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("conversation obtained a lease during SOP execution: %v", err)
	}
}

func TestMySQLConversationSandboxSelectionSerializesWithGeneration(t *testing.T) {
	stores, prefix := admissionMySQLStores(t)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("%s-chat-%d", prefix, i)
		t.Cleanup(func() {
			stores[0].DB.Where("session_id = ?", id).Delete(&assistantGenerationRow{})
			stores[0].DB.Where("id = ?", id).Delete(&assistantSessionRow{})
		})
		original := domain.AssistantSession{ID: id, ProjectID: prefix, UserID: 5001, SandboxProfileID: "first"}
		if err := stores[0].PutAssistantSession(original.UserID, original); err != nil {
			t.Fatal(err)
		}
		original, _ = stores[0].AssistantSession(original.UserID, id)
		start := make(chan struct{})
		generationDone, selectionDone := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			generationDone <- stores[0].BeginAssistantGeneration(domain.AssistantGeneration{ID: id, SessionID: id, UserID: original.UserID, StartedAt: time.Now()}, original)
		}()
		go func() { <-start; selectionDone <- stores[1].SetAssistantSessionSandbox(original.UserID, id, "second") }()
		close(start)
		if err := <-generationDone; err != nil {
			t.Fatal(err)
		}
		selectionErr := <-selectionDone
		saved, _ := stores[0].AssistantSession(original.UserID, id)
		want := "second"
		if selectionErr != nil {
			if !strings.Contains(selectionErr.Error(), "生成期间") {
				t.Fatal(selectionErr)
			}
			want = "first"
		}
		if saved.SandboxProfileID != want {
			t.Fatalf("stale generation save overwrote environment: got %q want %q", saved.SandboxProfileID, want)
		}
	}
}

func TestMySQLConversationWorkspaceMigrationIsScopedAndIdempotent(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("requires disposable MySQL")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	st, err := Open(dsn + sep + "multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	prefix := fmt.Sprintf("workspace-migration-%d", time.Now().UnixNano())
	for _, suffix := range []string{"bound", "roles", "unbound", "finished"} {
		id := prefix + "-" + suffix
		wf := domain.WorkflowDefinition{ID: domain.FreeCollaborationWorkflowID, CollaborationMode: "group"}
		nodes := map[string]*domain.NodeRun{}
		status := "waiting"
		if suffix == "roles" {
			nodes["group-member"] = &domain.NodeRun{NodeID: "group-member", Status: "running"}
		}
		if suffix == "finished" {
			status = "succeeded"
		}
		if err := st.PutRun(domain.Run{ID: id, WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: status, Nodes: nodes}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			st.DB.Where("run_id = ?", id).Delete(&runNodeRow{})
			st.DB.Where("id = ?", id).Delete(&runRow{})
			st.DB.Where("id = ?", id).Delete(&assistantSessionRow{})
			st.DB.Where("id = ?", id).Delete(&runnerTaskRow{})
		})
		if suffix == "bound" || suffix == "roles" {
			if err := st.PutAssistantSession(5002, domain.AssistantSession{ID: id, WorkspaceRunID: id}); err != nil {
				t.Fatal(err)
			}
		}
		if suffix == "roles" {
			if err := st.DB.Create(&runnerTaskRow{ID: id, RunnerID: "runner", Status: domain.RunnerTaskQueued, Payload: []byte(`{"runId":"` + id + `"}`)}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	script, err := os.ReadFile("../../migrations/020_conversation_workspaces.sql")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := st.DB.Exec(string(script)).Error; err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"bound": "waiting", "roles": "cancelled", "unbound": "cancelled", "finished": "succeeded"}
		for suffix, status := range want {
			run, _ := st.RunState(prefix + "-" + suffix)
			if run.IsWorkspace() != (suffix == "bound") {
				t.Fatalf("migration altered wrong record %s: kind=%s", suffix, run.Kind)
			}
			if run.Status != status {
				t.Fatalf("%s status = %s, want %s", suffix, run.Status, status)
			}
		}
		roles, _ := st.RunState(prefix + "-roles")
		if n := roles.Nodes["group-member"]; n == nil || n.Status != "cancelled" {
			t.Fatalf("running role task was not cancelled: %+v", n)
		}
		if task, ok := st.RunnerTask(prefix + "-roles"); !ok || task.Status != domain.RunnerTaskCanceled {
			t.Fatalf("queued role runner task = %+v", task)
		}
	}
}
