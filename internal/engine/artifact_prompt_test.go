package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestDownstreamPromptRendersArtifactBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{name: "inline", content: "PLAN-BODY-123"},
		{name: "chunked", content: "PLAN-BODY-123" + strings.Repeat("x", domain.MaxInlineRunnerArtifactBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := repo.OpenTest()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			run := runArtifactPromptSOP(t, store, tc.content)
			output := run.Nodes["reader"].Output
			if !strings.Contains(output, "READ<<"+tc.content+">>END") {
				start := strings.Index(output, "READ<<")
				t.Fatalf("reader prompt did not receive the upstream artifact body: %.80q", output[max(start, 0):])
			}
		})
	}
}

func runArtifactPromptSOP(t *testing.T, store *repo.Store, content string) domain.Run {
	t.Helper()
	const sandboxID = "artifact-prompt-sandbox"
	if err := store.PutSandbox(domain.SandboxProfile{ID: sandboxID, Name: "Artifact prompt sandbox", Driver: domain.SandboxDriverNative}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExecutor(domain.ExecutorDefinition{ID: "mock", Name: "Mock executor", Kind: "mock", Protocol: "events-v1"}); err != nil {
		t.Fatal(err)
	}
	putGateTestAgent(t, store, "writer-agent", nil)
	if err := store.PutAgent(domain.AgentDefinition{ID: "reader-agent", Name: "reader-agent", ExecutorID: "mock", Prompt: "READ<<{{artifact.writer.plan}}>>END"}); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{
		ID: fmt.Sprintf("artifact-prompt-%d", time.Now().UnixNano()), Name: "Artifact prompt", SandboxProfileID: sandboxID,
		Nodes: []domain.WorkflowNode{
			{ID: "writer", Name: "Writer", Type: "agent", AgentID: "writer-agent", MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "plan", Path: "plan.md", Required: true}}},
			{ID: "reader", Name: "Reader", Type: "agent", AgentID: "reader-agent", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{{ID: "writer-reader", Source: "writer", Target: "reader", When: "success"}},
	}
	if err := store.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	manager := runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))
	eng := New(store, manager)
	eng.ConfigureExecution("artifact-prompt-worker", "", 30*time.Second, false)
	run, err := eng.Start(workflow.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	workspace := manager.RunWorkspace(run.ID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "plan.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	eng.ConfigureExecution("artifact-prompt-worker", "", 30*time.Second, true)
	if _, err := eng.acquireLease(run); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.execute(ctx, run.ID)
	}()
	defer func() { cancel(); <-done }()
	for ctx.Err() == nil {
		current, ok := store.RunState(run.ID)
		if !ok {
			t.Fatal("could not read run")
		}
		if domain.TerminalRunStatus(current.Status) {
			if current.Status != "succeeded" {
				t.Fatalf("run %s: writer=%q reader=%q", current.Status, current.Nodes["writer"].Error, current.Nodes["reader"].Error)
			}
			return current
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return domain.Run{}
}
