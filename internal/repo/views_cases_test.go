package repo

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestRoomViewsFiltersSignalsAndFallbacks(t *testing.T) {
	s := cvEStore(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	room := domain.ChatRoom{ID: "cve-room", ProjectID: "platform", Name: "Coverage", CreatedBy: 7, CreatedAt: base}
	if err := s.PutRoom(room); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRoom(domain.ChatRoom{ID: room.ID, Name: "Updated"}); err != nil {
		t.Fatal(err)
	}
	gotRoom, ok := s.Room(room.ID)
	if !ok || gotRoom.ProjectID != "platform" || gotRoom.CreatedBy != 7 || gotRoom.Name != "Updated" {
		t.Fatalf("room=%+v ok=%v", gotRoom, ok)
	}
	if rooms := s.Rooms("platform"); len(rooms) != 1 {
		t.Fatalf("project rooms=%v", rooms)
	}
	if rooms := s.Rooms(""); len(rooms) != 1 {
		t.Fatalf("all rooms=%v", rooms)
	}
	if _, ok := s.Room("missing"); ok {
		t.Fatal("missing room found")
	}

	messages := []domain.ChatMessage{
		{ID: "m1", RoomID: room.ID, Kind: "text", Body: "main", CreatedAt: base},
		{ID: "m2", RoomID: room.ID, Kind: domain.ChatKindDispatch, RunID: "run-live", Body: "dispatch", CreatedAt: base.Add(time.Minute)},
		{ID: "m3", RoomID: room.ID, Kind: domain.ChatKindApproval, NodeID: "gate", Body: "approve", CreatedAt: base.Add(2 * time.Minute)},
		{ID: "m4", RoomID: room.ID, Kind: "text", ThreadID: "thread-a", Body: "thread", Attachments: []domain.AssistantAttachment{{ID: "attachment-a"}}, CreatedAt: base.Add(3 * time.Minute)},
		{ID: "m5", RoomID: room.ID, Kind: domain.ChatKindDispatch, RunID: "run-done", RunFinal: "succeeded", Lane: 2, CreatedAt: base.Add(4 * time.Minute)},
	}
	for _, message := range messages {
		if err := s.PutMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Messages("", 2); len(got) != 0 {
		t.Fatalf("empty messages=%v", got)
	}
	if got := s.Messages(room.ID, 2); len(got) != 2 || got[0].ID != "m4" || got[1].ID != "m5" {
		t.Fatalf("limited messages=%v", got)
	}
	if got := s.Messages(room.ID, -1); len(got) != len(messages) {
		t.Fatalf("default messages=%v", got)
	}
	if got := s.TimelineMessages(room.ID, "", 0); len(got) != 4 {
		t.Fatalf("main timeline=%v", got)
	}
	if got := s.TimelineMessages(room.ID, "thread-a", 10); len(got) != 1 || got[0].ID != "m4" {
		t.Fatalf("thread timeline=%v", got)
	}
	if got := s.TimelineMessages("", "", 10); len(got) != 0 {
		t.Fatalf("empty timeline=%v", got)
	}
	if got, ok := s.DispatchMessage(room.ID, "run-live"); !ok || got.ID != "m2" {
		t.Fatalf("dispatch=%+v ok=%v", got, ok)
	}
	if _, ok := s.DispatchMessage("", "run-live"); ok {
		t.Fatal("empty dispatch matched")
	}
	if _, ok := s.DispatchMessage(room.ID, "missing"); ok {
		t.Fatal("missing dispatch matched")
	}
	if got := s.DispatchMessages(room.ID); len(got) != 2 || got[0].ID != "m2" {
		t.Fatalf("dispatches=%v", got)
	}
	if got, ok := s.Message(room.ID, "m3"); !ok || got.Kind != domain.ChatKindApproval {
		t.Fatalf("message=%+v ok=%v", got, ok)
	}
	if _, ok := s.Message("other", "m3"); ok {
		t.Fatal("cross-room message found")
	}
	if got, ok := s.MessageWithAttachment(room.ID, "attachment-a"); !ok || got.ID != "m4" {
		t.Fatalf("attachment message=%+v ok=%v", got, ok)
	}
	if _, ok := s.MessageWithAttachment(room.ID, "%"); ok {
		t.Fatal("unsafe attachment lookup")
	}
	if counts := s.ThreadMessageCounts(room.ID); counts["thread-a"] != 1 {
		t.Fatalf("thread counts=%v", counts)
	}
	signals := s.RoomSignals([]string{room.ID})
	if signals[room.ID].PendingApprovals != 1 || signals[room.ID].LiveRuns != 1 || signals[room.ID].Lanes != 2 {
		t.Fatalf("signals=%+v", signals)
	}
	if got := s.RoomSignals(nil); len(got) != 0 {
		t.Fatalf("empty signals=%v", got)
	}

	now := time.Now().UTC()
	if err := s.DB.Create(&chatRoomRow{ID: "bad-room", ProjectID: "platform", Name: "fallback", Payload: []byte("{"), Created: &now, Updated: &now}).Error; err != nil {
		t.Fatal(err)
	}
	rawRoom, _ := json.Marshal(domain.ChatRoom{})
	if err := s.DB.Create(&chatRoomRow{ID: "fallback-room", ProjectID: "platform", Name: "fallback", Payload: rawRoom, Created: &now, Updated: &now}).Error; err != nil {
		t.Fatal(err)
	}
	foundFallback := false
	for _, item := range s.Rooms("platform") {
		if item.ID == "fallback-room" && item.ProjectID == "platform" && item.Name == "fallback" {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatal("row fallbacks not applied")
	}
}

func TestRunPaginationTimingAndFilterHelpers(t *testing.T) {
	s := cvEStore(t)
	now := time.Now().UTC()
	logs := []domain.LogRecord{
		{Sequence: 1, Body: "one", Context: domain.TelemetryContext{NodeID: "a"}},
		{Sequence: 2, Body: "two", Context: domain.TelemetryContext{NodeID: "b"}},
		{Sequence: 3, Body: "three", Context: domain.TelemetryContext{NodeID: "a"}},
	}
	page := buildLogPage(logs, 3, "a", 0, 0, false, false, 1)
	if len(page.Logs) != 1 || page.Logs[0].Sequence != 3 || !page.HasMore {
		t.Fatalf("log page=%+v", page)
	}
	page = buildLogPage(logs, 3, "", 1, 3, true, true, 10)
	if len(page.Logs) != 2 || page.Logs[0].Sequence != 2 || page.Logs[1].Sequence != 3 {
		t.Fatalf("cursor log page=%+v", page)
	}
	events := []domain.TelemetryEvent{
		{Sequence: 1, NodeID: "a", Source: domain.TelemetrySource{Adapter: "cli"}},
		{Sequence: 2, NodeID: "b", Source: domain.TelemetrySource{Adapter: "runner"}},
		{Sequence: 3, NodeID: "a", Source: domain.TelemetrySource{Adapter: "cli"}},
	}
	telemetry := buildTelemetryPage(events, 3, "a", "cli", 0, 0, false, false, 1)
	if len(telemetry.Events) != 1 || telemetry.Events[0].Sequence != 3 || !telemetry.HasMore {
		t.Fatalf("telemetry page=%+v", telemetry)
	}
	if clampPageLimit(-1) != defaultLogPageSize || clampPageLimit(5000) != maxLogPageSize || clampPageLimit(25) != 25 {
		t.Fatal("page limit clamp")
	}
	if indexes, more := pageByCursor(3, 0, 0, false, false, 2, func(i int) int64 { return int64(i + 1) }); len(indexes) != 2 || !more {
		t.Fatalf("indexes=%v more=%v", indexes, more)
	}
	if indexes, more := pageByCursor(0, 0, 0, false, false, 2, func(int) int64 { return 0 }); len(indexes) != 0 || more {
		t.Fatalf("empty indexes=%v more=%v", indexes, more)
	}
	if nodeWhere, args := s.nodeIDFilter("payload", "node"); nodeWhere == "" || len(args) != 2 {
		t.Fatalf("node filter=%q %v", nodeWhere, args)
	}
	if nodeWhere, args := s.nodeIDFilter("payload", ""); nodeWhere != "" || args != nil {
		t.Fatalf("empty node filter=%q %v", nodeWhere, args)
	}
	if filter := adapterSQLFilter("runner"); filter.clause == "" || len(filter.args) != 1 {
		t.Fatalf("adapter filter=%+v", filter)
	}
	if filter := adapterSQLFilter(""); filter.clause != "" {
		t.Fatalf("empty adapter=%+v", filter)
	}

	start := now.Add(-10 * time.Minute)
	finish := now.Add(-2 * time.Minute)
	run := domain.Run{
		Status: "succeeded", CreatedAt: start, UpdatedAt: finish,
		Nodes: map[string]*domain.NodeRun{
			"a": {Status: "succeeded", StartedAt: start.Add(time.Minute), FinishedAt: start.Add(4 * time.Minute)},
			"b": {Status: "waiting", StartedAt: start.Add(5 * time.Minute)},
		},
		Events: []domain.RunEvent{
			{Type: "run.started", Timestamp: start},
			{Type: "run.waiting", Timestamp: start.Add(4 * time.Minute)},
			{Type: "run.resumed", Timestamp: start.Add(6 * time.Minute)},
			{Type: "run.succeeded", Timestamp: finish},
		},
		Artifacts: []domain.Artifact{{ID: "a"}, {ID: "b"}},
	}
	if runArtifactCount(run) != 2 || runDuration(run, now) <= 0 || runExecutionDuration(run) <= 0 || runActiveDuration(run, now) <= 0 || runWaitDuration(run, now) <= 0 {
		t.Fatalf("timing helpers failed")
	}
	if got := runEndedAt(domain.Run{Status: "running", UpdatedAt: finish}, now); !got.Equal(now) {
		t.Fatalf("live end=%v", got)
	}
	if err := parallel(func() error { return nil }, func() error { return errors.New("parallel failure") }); err == nil {
		t.Fatal("parallel error lost")
	}
	values := []int{3, 1, 2}
	sortSliceStable(values, func(a, b int) bool { return a < b })
	if values[0] != 1 || values[2] != 3 {
		t.Fatalf("sorted=%v", values)
	}
}

func TestCatalogValidationDeletionAndBuiltins(t *testing.T) {
	s := cvEStore(t)
	agents := []domain.AgentDefinition{
		{},
		{ID: "agent", Name: "Agent", Loop: domain.AgentLoopPolicy{Enabled: true, MaxIterations: 13}},
		{ID: "agent", Name: "Agent", ArtifactOutputs: []domain.ArtifactContract{{Name: "bad", Path: "../escape"}}},
		{ID: "agent", Name: "Agent", Interventions: []domain.HumanInterventionPoint{{ID: "point", Name: "Point", Stage: "invalid"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "../bad", Name: "Hook", Event: "Stop", Language: "shell", Script: "true"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "same", Name: "Hook", Event: "Stop", Language: "shell", Script: "true"}, {ID: "same", Name: "Hook", Event: "Stop", Language: "shell", Script: "true"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "hook", Event: "Stop", Language: "shell", Script: "true"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "hook", Name: "Hook", Event: "Bad", Language: "shell", Script: "true"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "hook", Name: "Hook", Event: "Stop", Language: "ruby", Script: "true"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "hook", Name: "Hook", Event: "Stop", Language: "shell"}}},
		{ID: "agent", Name: "Agent", Hooks: []domain.AgentHook{{ID: "hook", Name: "Hook", Event: "Stop", Language: "shell", Script: "true", TimeoutSec: 601}}},
	}
	for i, agent := range agents {
		if err := s.PutAgent(agent); err == nil {
			t.Fatalf("invalid agent %d accepted: %+v", i, agent)
		}
	}
	valid := domain.AgentDefinition{
		ID: "cve-catalog-agent", Name: "Catalog Agent", ProjectID: "platform",
		Loop:  domain.AgentLoopPolicy{Enabled: true, MaxIterations: 2},
		Hooks: []domain.AgentHook{{ID: "hook", Name: "Hook", Event: "Stop", Language: "SHELL", Script: " true "}},
	}
	if err := s.PutAgent(valid); err != nil {
		t.Fatal(err)
	}
	valid.Name = "Catalog Agent 2"
	valid.ProjectID = ""
	if err := s.PutAgent(valid); err != nil {
		t.Fatal(err)
	}
	gotAgent, ok := s.Agent(valid.ID)
	if !ok || gotAgent.ProjectID != "platform" || gotAgent.Loop.ExitCondition != "all_gates_pass" || gotAgent.Hooks[0].TimeoutSec != 10 {
		t.Fatalf("agent=%+v ok=%v", gotAgent, ok)
	}
	if err := s.DeleteAgent("missing"); err == nil {
		t.Fatal("missing agent deleted")
	}
	if err := s.DeleteAgent(valid.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.PutExecutor(domain.ExecutorDefinition{}); err == nil {
		t.Fatal("invalid executor accepted")
	}
	if _, ok := BuiltInExecutor("codex"); !ok {
		t.Fatal("built-in executor missing")
	}
	if _, ok := BuiltInExecutor("missing"); ok {
		t.Fatal("missing built-in found")
	}
	if _, ok := BuiltInExecutorForKind("claude-code"); !ok {
		t.Fatal("built-in kind missing")
	}
	if _, ok := BuiltInExecutorForKind("missing"); ok {
		t.Fatal("missing built-in kind found")
	}

	if err := s.PutSandbox(domain.SandboxProfile{}); err == nil {
		t.Fatal("invalid sandbox accepted")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "cve-sandbox", Name: "Sandbox", Driver: "unsupported"}); err == nil {
		t.Fatal("unsupported sandbox accepted")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "cve-sandbox", Name: "Sandbox", Driver: "docker"}); err == nil {
		t.Fatal("docker sandbox without Dockerfile accepted")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "cve-sandbox", Name: "Sandbox", Driver: "docker", DockerfileID: "missing"}); err == nil {
		t.Fatal("missing Dockerfile accepted")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "cve-sandbox", Name: "Sandbox", Driver: "local", EnvironmentID: "missing"}); err == nil {
		t.Fatal("missing environment accepted")
	}
	if err := s.PutSandbox(domain.SandboxProfile{ID: "cve-local-sandbox", Name: "Local", Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSandbox("missing"); err == nil {
		t.Fatal("missing sandbox deleted")
	}
	if err := s.DeleteSandbox("cve-local-sandbox"); err != nil {
		t.Fatal(err)
	}

	rule := domain.RuleDefinition{ID: "cve-rule", Name: "Rule"}
	if err := s.PutRule(rule); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRule(rule.ID); err == nil {
		t.Fatal("missing rule deleted")
	}
	skill := domain.SkillDefinition{ID: "cve-skill", Name: "Skill"}
	if err := s.PutSkill(skill); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSkill(skill.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSkill(skill.ID); err == nil {
		t.Fatal("missing skill deleted")
	}
}
