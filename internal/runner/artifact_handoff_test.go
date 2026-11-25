package runner

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestSOPRunnerArtifactHandoffReachesApprovalAndCompletes(t *testing.T) {
	server, store, eng := newDaemonTestServer(t)
	root := t.TempDir()
	daemon, err := New(Config{
		ServerURL: server.URL, BootstrapToken: "boot-secret",
		StatePath: filepath.Join(root, "state.json"), DefaultWorkspaceRoot: filepath.Join(root, "workspaces"),
		HeartbeatInterval: time.Second, LongPollTimeout: time.Second, DisableAutoUpdate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &artifactRetryTransport{base: http.DefaultTransport}
	daemon.client.http.Transport = transport
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, 5*time.Second, "Runner online with file transfer support", func() bool {
		runners := store.Runners()
		return len(runners) == 1 && runners[0].Status == domain.RunnerStatusOnline && runners[0].SupportsArtifactTransfer()
	})
	savedRunner := store.Runners()[0]
	savedRunner.OwnerUserID = 42
	if err := store.PutRunner(savedRunner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultRunner(42, savedRunner.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{
		ID: "runner-artifact-handoff", Name: "Artifact handoff", SandboxProfileID: "test-native",
		Nodes: []domain.WorkflowNode{
			{
				ID: "writing-plans", Name: "Writing plans", Type: "script", ScriptLanguage: "shell", MaxAttempts: 3,
				Script: "set -e\nhead -c 20971520 /dev/zero | tr '\\000' 'p' > plan.md\nhead -c 1048576 plan.md > first.md\ncp first.md second.md\ncp plan.md optional.md\nprintf x >> optional.md",
				ArtifactOutputs: []domain.ArtifactContract{
					{Name: "plan", Path: "plan.md", Required: true},
					{Name: "first", Path: "first.md", Required: true},
					{Name: "second", Path: "second.md", Required: true},
					{Name: "optional", Path: "optional.md"},
				},
			},
			{
				ID: "review", Name: "Review", Type: "script", ScriptLanguage: "shell", MaxAttempts: 1,
				Script: "set -e\ninput=.agentflow/inputs/writing-plans\ntest \"$(wc -c < \"$input/plan.md\" | tr -d ' ')\" = 20971520\ncmp plan.md \"$input/plan.md\"\ncmp first.md \"$input/first.md\"\ncmp second.md \"$input/second.md\"\nprintf 'verified handoff'",
			},
			{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1},
			{ID: "finish", Name: "Finish", Type: "script", ScriptLanguage: "shell", Script: "printf 'completed'", MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "plan-review", Source: "writing-plans", Target: "review", When: "success"},
			{ID: "review-approval", Source: "review", Target: "approval", When: "success"},
			{ID: "approval-finish", Source: "approval", Target: "finish", When: "success"},
		},
	}
	if err := store.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	run, err := eng.StartWithExecutionContext(workflow.ID, "", domain.RepositoryContext{}, domain.TelemetryContext{ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	var current domain.Run
	waitFor(t, 25*time.Second, "SOP reaching its human approval", func() bool {
		current, _ = store.RunProgress(run.ID)
		return current.Status == "waiting" || domain.TerminalRunStatus(current.Status)
	})
	if current.Status != "waiting" || current.Nodes["approval"].Status != "waiting_approval" || current.Nodes["review"].Status != "succeeded" {
		t.Fatalf("run=%s plan=%+v review=%+v approval=%+v", current.Status, current.Nodes["writing-plans"], current.Nodes["review"], current.Nodes["approval"])
	}
	if current.Nodes["writing-plans"].Attempt != 1 || transport.planUploads.Load() != 2 {
		t.Fatalf("node executed %d times, plan upload attempted %d times", current.Nodes["writing-plans"].Attempt, transport.planUploads.Load())
	}
	for _, artifact := range current.Artifacts {
		if artifact.Name == "optional" {
			t.Fatal("oversized optional artifact was archived")
		}
	}
	if current.Nodes["finish"].Status != "pending" {
		t.Fatal("downstream ran before approval")
	}
	if err := eng.Approve(run.ID, "approval", "verified"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "SOP completion after human approval", func() bool {
		current, _ = store.RunProgress(run.ID)
		return domain.TerminalRunStatus(current.Status)
	})
	if current.Status != "succeeded" || !strings.Contains(current.Nodes["finish"].Output, "completed") {
		t.Fatalf("run=%s finish=%+v", current.Status, current.Nodes["finish"])
	}
}

type artifactRetryTransport struct {
	base        http.RoundTripper
	planUploads atomic.Int32
}

func (t *artifactRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || req.Method != http.MethodPut || !strings.HasSuffix(req.URL.Path, "/output-file") || req.URL.Query().Get("path") != "plan.md" {
		return res, err
	}
	// MySQL/SQLite already committed the bytes on the server when the first
	// response is dropped. A replay must return the same immutable reference.
	if t.planUploads.Add(1) == 1 && res.StatusCode == http.StatusOK {
		_ = res.Body.Close()
		res.Body = io.NopCloser(strings.NewReader("simulated lost acknowledgement"))
		res.StatusCode = http.StatusServiceUnavailable
	}
	return res, nil
}
