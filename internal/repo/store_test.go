package repo

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestPutMCPServerRejectsInvalidDefinition(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.PutMCPServer(domain.MCPServerDefinition{ID: "bad", Name: "Bad", Transport: "http", URL: "not-a-url"})
	if err == nil {
		t.Fatal("invalid MCP should be rejected")
	}
}

func TestPutAgentKeepsExistingProjectID(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutAgent(domain.AgentDefinition{ID: "gslb-fix", Name: "Fix", ProjectID: "gslb", Instructions: "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "gslb-fix", Name: "Fix v2", Instructions: "keep"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Agent("gslb-fix")
	if got.ProjectID != "gslb" || got.Name != "Fix v2" {
		t.Fatalf("project scope dropped: %+v", got)
	}
}

func TestPutAgentValidatesAndDefaultsHooks(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	agent := domain.AgentDefinition{ID: "hooked", Name: "Hooked", Hooks: []domain.AgentHook{{ID: "audit", Name: "Audit", Event: "PreToolUse", Language: "python", Script: "print('ok')"}}}
	if err := s.PutAgent(agent); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Agent(agent.ID)
	if len(stored.Hooks) != 1 || stored.Hooks[0].TimeoutSec != 10 {
		t.Fatalf("stored hooks = %+v", stored.Hooks)
	}
	agent.Hooks[0].Event = "Unsupported"
	if err := s.PutAgent(agent); err == nil {
		t.Fatal("unsupported common hook event should be rejected")
	}
}

func TestPutWorkflowKeepsExistingProjectID(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "gslb-fix", Name: "Fix", ProjectID: "gslb", SandboxProfileID: "local"}); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Workflow("gslb-fix")
	stored.Name = "Fix v2"
	stored.ProjectID = ""
	if err := s.PutWorkflow(stored); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Workflow("gslb-fix")
	if got.ProjectID != "gslb" || got.Name != "Fix v2" {
		t.Fatalf("workflow project scope dropped: %+v", got)
	}
}

func TestCommandNodeNormalizesToShellScript(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wf := domain.WorkflowDefinition{ID: "legacy-command", Name: "Legacy", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "run", Name: "Run", Type: "command", Command: "printf ok"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Workflow(wf.ID)
	if stored.Nodes[0].Type != "script" || stored.Nodes[0].ScriptLanguage != "shell" || stored.Nodes[0].Script != "printf ok" || stored.Nodes[0].Command != "" {
		t.Fatalf("legacy node was not normalized: %+v", stored.Nodes[0])
	}
}

func TestOpenRequiresMySQLDSN(t *testing.T) {
	t.Setenv("AGENTFLOW_MYSQL_DSN", "")
	s, err := Open("")
	if err == nil {
		s.Close()
		t.Fatal("Open without a DSN must fail: MySQL is where all state lives")
	}
}

func TestSeedPopulatesDefaultsForAnEmptyStore(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Executors()) == 0 || len(s.Sandboxes()) == 0 || len(s.Rules()) == 0 || len(s.Agents()) == 0 {
		t.Fatalf("defaults missing: executors=%d sandboxes=%d rules=%d agents=%d",
			len(s.Executors()), len(s.Sandboxes()), len(s.Rules()), len(s.Agents()))
	}
	if _, ok := s.Sandbox("local"); !ok {
		t.Fatal("expected seeded local sandbox")
	}
	if _, ok := s.Sandbox("skillhub-macos-seatbelt"); !ok {
		t.Fatal("expected seeded skillhub-macos-seatbelt sandbox")
	}
	if _, ok := s.Sandbox("skillhub-linux-bwrap"); !ok {
		t.Fatal("expected seeded skillhub-linux-bwrap sandbox")
	}
	if _, ok := s.Sandbox("container"); ok {
		t.Fatal("legacy container sandbox must not be seeded")
	}
	dockerfile, ok := s.Dockerfile(GenericBackendDockerfileID)
	if !ok || strings.TrimSpace(dockerfile.Content) == "" || !dockerfile.Shared {
		t.Fatalf("expected platform generic-backend dockerfile, got %+v ok=%v", dockerfile, ok)
	}
	box, ok := s.Sandbox(GenericDockerSandboxID)
	if !ok || box.Driver != "docker" || box.DockerfileID != GenericBackendDockerfileID || !box.Shared {
		t.Fatalf("expected platform generic-docker sandbox, got %+v ok=%v", box, ok)
	}
	if len(s.Projects()) != 1 {
		t.Fatalf("expected exactly the default project, got %d", len(s.Projects()))
	}
	if s.SyncConfig().GongfengMCPURL == "" {
		t.Fatal("default sync config was not seeded")
	}
	if n := len(s.Environments()); n != 0 {
		t.Fatalf("environments must come from the catalog, not code; seeded %d", n)
	}
	if n := len(s.Dockerfiles()); n != 1 {
		t.Fatalf("only the platform generic-backend dockerfile is ensured, got %d", n)
	}
}

// A rejected write must leave the reader's view untouched. If memory were
// updated before the row reached MySQL, the UI would show a value the database
// never accepted until the next restart.
func TestRejectedWriteDoesNotChangeReads(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original := domain.MCPServerDefinition{ID: "github", Name: "GitHub MCP", Transport: "stdio", Command: "github-mcp-server", PathEntries: []string{"tools/bin"}}
	if err := s.PutMCPServer(original); err != nil {
		t.Fatal(err)
	}
	broken := original
	broken.Transport = "http"
	broken.URL = "not-a-url"
	if err := s.PutMCPServer(broken); err == nil {
		t.Fatal("invalid MCP definition should be rejected")
	}
	stored, ok := s.MCPServer(original.ID)
	if !ok || stored.Transport != "stdio" || stored.URL != "" || len(stored.PathEntries) != 1 || stored.PathEntries[0] != "tools/bin" {
		t.Fatalf("rejected write leaked into reads: %+v", stored)
	}
}

func TestMemoryRunDoesNotShareCallerOrReaderData(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := domain.Run{
		ID: "run-1", WorkflowID: "wf", Status: "running",
		Nodes:     map[string]*domain.NodeRun{"build": {NodeID: "build", Status: "running"}},
		Telemetry: []domain.TelemetryEvent{{Sequence: 1, Message: "first"}, {Sequence: 2, Message: "second"}},
		Logs:      []domain.LogRecord{{Sequence: 1, Body: "log"}},
		Events:    []domain.RunEvent{{ID: "ev-1", Type: "started"}},
		Artifacts: []domain.Artifact{{ID: "art-1", Name: "result"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	run.Nodes["build"].Status = "caller-clobbered"
	run.Telemetry[0].Message = "caller-clobbered"
	stored, found := s.Run("run-1")
	if !found || stored.Nodes["build"].Status != "running" || stored.Telemetry[0].Message != "first" {
		t.Fatalf("PutRun retained caller-owned data: %+v", stored)
	}
	stored.Telemetry[0] = domain.TelemetryEvent{Sequence: 99, Message: "reader-clobbered"}
	stored.Logs[0].Body = "reader-clobbered"
	stored.Events[0].Type = "reader-clobbered"
	stored.Artifacts[0].Name = "reader-clobbered"
	stored.Nodes["build"].Status = "reader-clobbered"

	live, found := s.Run("run-1")
	if !found {
		t.Fatal("run went missing")
	}
	if live.Telemetry[0].Message != "first" || live.Logs[0].Body != "log" {
		t.Fatalf("snapshot shares telemetry/log arrays with the stored run: %+v", live)
	}
	if live.Events[0].Type != "started" || live.Artifacts[0].Name != "result" {
		t.Fatalf("snapshot shares event/artifact arrays with the stored run: %+v", live)
	}
	if live.Nodes["build"].Status != "running" {
		t.Fatalf("snapshot shares node pointers with the stored run: %+v", live.Nodes["build"])
	}
}

func TestRunStatusesReturnsStatusWithoutHydratingRunBodies(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(domain.Run{
		ID:     "status-only",
		Status: "running",
		Nodes:  map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running", Output: "heavy output"}},
		Logs:   []domain.LogRecord{{ID: "log", Body: "heavy log"}},
	}); err != nil {
		t.Fatal(err)
	}
	statuses := s.RunStatuses()
	if statuses["status-only"] != "running" {
		t.Fatalf("unexpected run statuses: %+v", statuses)
	}
}

func TestRunsWithActiveNodesWithoutHydratingRunBodies(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(domain.Run{
		ID:     "ghost-run",
		Status: "failed",
		Nodes: map[string]*domain.NodeRun{
			"work": {NodeID: "work", Status: "running", Output: "heavy output"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{
		ID:     "clean-run",
		Status: "failed",
		Nodes: map[string]*domain.NodeRun{
			"work": {NodeID: "work", Status: "failed"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	active := s.RunsWithActiveNodes()
	if !active["ghost-run"] {
		t.Fatalf("expected ghost-run to be active: %+v", active)
	}
	if active["clean-run"] {
		t.Fatalf("expected clean-run to be inactive: %+v", active)
	}
}

func TestDeleteRunRemovesSettledRunOnly(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutRun(domain.Run{
		ID:     "settled-run",
		Status: "succeeded",
		Nodes:  map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "succeeded"}},
		Logs:   []domain.LogRecord{{ID: "log", Body: "done"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{
		ID:     "live-run",
		Status: "running",
		Nodes:  map[string]*domain.NodeRun{"work": {NodeID: "work", Status: "running"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRun("missing-run"); err == nil {
		t.Fatal("expected error deleting a missing run")
	}
	if err := s.DeleteRun("live-run"); err == nil {
		t.Fatal("expected error deleting a running run")
	}
	if _, ok := s.Run("live-run"); !ok {
		t.Fatal("live run was deleted")
	}
	if err := s.DeleteRun("settled-run"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Run("settled-run"); ok {
		t.Fatal("settled run was not deleted")
	}
}

func TestRunSummariesOrderedNewestFirst(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Now().Add(-2 * time.Hour)
	t1 := time.Now().Add(-1 * time.Hour)
	t2 := time.Now()
	// Insert runs in non-descending order
	runs := []domain.Run{
		{ID: "run-old", WorkflowID: "wf", CreatedAt: t0, Status: "succeeded"},
		{ID: "run-mid", WorkflowID: "wf", CreatedAt: t1, Status: "succeeded"},
		{ID: "run-new", WorkflowID: "wf", CreatedAt: t2, Status: "succeeded"},
	}
	for _, r := range runs {
		if err := s.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := s.RunSummaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(summaries))
	}
	if summaries[0].ID != "run-new" || summaries[1].ID != "run-mid" || summaries[2].ID != "run-old" {
		t.Fatalf("runs not sorted newest first: got [0]=%s, [1]=%s, [2]=%s", summaries[0].ID, summaries[1].ID, summaries[2].ID)
	}
}
