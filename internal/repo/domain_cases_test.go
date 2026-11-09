package repo

import (
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func cvEStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPreviewLifecycleQueriesSessionsAndBlobs(t *testing.T) {
	s := cvEStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	p := domain.RunPreview{
		RunID: "cve-run", ProjectID: "platform", Kind: domain.PreviewKindStatic,
		Status: domain.PreviewPending, SourcePath: "dist", UploadTokenHash: "secret",
		UploadExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour),
	}
	if err := s.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	p, ok := s.PreviewByRun("cve-run")
	if !ok || p.ID != "preview-cve-run" || p.CreatedAt.IsZero() {
		t.Fatalf("preview = %+v, %v", p, ok)
	}
	if got := s.PreviewsByStatus(domain.PreviewPending); len(got) != 1 {
		t.Fatalf("pending = %d", len(got))
	}
	claimed, ok, err := s.ClaimPreviewArchive(p.ID, now, now.Add(time.Minute))
	if err != nil || !ok || claimed.Status != domain.PreviewArchiving {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if _, ok, err = s.ClaimPreviewArchive(p.ID, now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("live lease reclaimed: %v %v", ok, err)
	}
	claimed.StorageKey = "objects/cve"
	claimed.ContentSHA256 = strings.Repeat("a", 64)
	claimed.ExpiresAt = now.Add(-time.Minute)
	if ok, err = s.MarkPreviewReadyIfArchiving(claimed); err != nil || !ok {
		t.Fatalf("ready = %v %v", ok, err)
	}
	if ok, err = s.FailPreviewIfOpen(p.ID, "late", now); err != nil || ok {
		t.Fatalf("ready preview failed: %v %v", ok, err)
	}
	if got := s.ExpiredReadyPreviews(now); len(got) != 1 {
		t.Fatalf("expired = %d", len(got))
	}
	if got := s.ReadyPreviewsByStorageKey("objects/cve"); len(got) != 1 {
		t.Fatalf("storage query = %d", len(got))
	}
	if n, err := s.CountReadyStorageKey("objects/cve"); err != nil || n != 1 {
		t.Fatalf("count = %d %v", n, err)
	}
	if err := s.MarkPreviewAccessed(p.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePreviewBackendURL("", "x"); err == nil {
		t.Fatal("empty preview ID accepted")
	}

	session := domain.PreviewSession{PreviewID: p.ID, TokenHash: "cve-hash", BackendURL: "http://old", ExpiresAt: now.Add(time.Hour)}
	if err := s.PutPreviewSession(session); err != nil {
		t.Fatal(err)
	}
	session, ok = s.PreviewSessionByTokenHash("cve-hash")
	if !ok || !strings.HasPrefix(session.ID, "ps-") {
		t.Fatalf("session = %+v %v", session, ok)
	}
	if err := s.UpdatePreviewBackendURL(p.ID, "http://new"); err != nil {
		t.Fatal(err)
	}
	session, ok = s.PreviewSession(session.ID)
	if !ok || session.BackendURL != "http://new" {
		t.Fatalf("updated session = %+v", session)
	}
	if err := s.TouchPreviewSession(session.ID, now.Add(2*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokePreviewSession(session.ID, now); err != nil {
		t.Fatal(err)
	}

	sha := strings.Repeat("b", 64)
	if err := s.PutPreviewBlob("short", []byte("x")); err == nil {
		t.Fatal("bad sha accepted")
	}
	if err := s.PutPreviewBlob(strings.ToUpper(sha), []byte("payload")); err != nil {
		t.Fatal(err)
	}
	body, size, ok := s.PreviewBlob(sha)
	if !ok || string(body) != "payload" || size != 7 {
		t.Fatalf("blob = %q %d %v", body, size, ok)
	}
	if err := s.DeletePreviewBlob(sha); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.PreviewBlob(sha); ok {
		t.Fatal("blob remained")
	}
	if got := PreviewNotFound("r").Error(); !strings.Contains(got, "r") {
		t.Fatal(got)
	}
	if NewPreviewID(" ") != "preview" {
		t.Fatal("empty preview ID")
	}
	if err := s.DeletePreview(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Preview(p.ID); ok {
		t.Fatal("preview remained")
	}
	if _, ok := s.PreviewSession(session.ID); ok {
		t.Fatal("session remained")
	}
}

func TestPreviewFailureAndExpiredLease(t *testing.T) {
	s := cvEStore(t)
	now := time.Now().UTC()
	if ok, err := s.FailPreviewIfOpen("", "x", now); err != nil || ok {
		t.Fatalf("empty fail = %v %v", ok, err)
	}
	if _, ok, err := s.ClaimPreviewArchive("", now, now); err != nil || ok {
		t.Fatalf("empty claim = %v %v", ok, err)
	}
	p := domain.RunPreview{ID: "cve-fail", RunID: "r-fail", Status: domain.PreviewPending}
	if err := s.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.FailPreviewIfOpen(p.ID, "boom", now); err != nil || !ok {
		t.Fatalf("fail = %v %v", ok, err)
	}
	got, _ := s.Preview(p.ID)
	if got.Status != domain.PreviewFailed || got.ErrorText != "boom" {
		t.Fatalf("failed preview = %+v", got)
	}
	p = domain.RunPreview{ID: "cve-reclaim", RunID: "r-reclaim", Status: domain.PreviewArchiving, ArchiveLeaseUntil: now.Add(-time.Minute)}
	if err := s.PutPreview(p); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimPreviewArchive(p.ID, now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("expired lease = %v %v", ok, err)
	}
}

func TestRunEvalCRUDAndTrim(t *testing.T) {
	s := cvEStore(t)
	if err := s.PutRunEval(domain.RunEvalRecord{}); err == nil {
		t.Fatal("invalid eval accepted")
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < maxRunEvalsPerRun+2; i++ {
		rec := domain.RunEvalRecord{
			ID: "cve-eval-" + string(rune('a'+i)), RunID: "cve-run", Status: domain.RunEvalSucceeded,
			Score: i, Passed: true, Model: "model", CreatedBy: 7, CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if i == maxRunEvalsPerRun+1 {
			rec.Status = domain.RunEvalRunning
			rec.FinishedAt = base.Add(2 * time.Hour)
		}
		if err := s.PutRunEval(rec); err != nil {
			t.Fatal(err)
		}
	}
	list := s.ListRunEvals("cve-run")
	if len(list) != maxRunEvalsPerRun {
		t.Fatalf("eval count = %d", len(list))
	}
	latest, ok := s.LatestRunEval("cve-run")
	if !ok || latest.Status != domain.RunEvalRunning {
		t.Fatalf("latest = %+v %v", latest, ok)
	}
	active, ok := s.ActiveRunEval("cve-run")
	if !ok || active.ID != latest.ID {
		t.Fatalf("active = %+v %v", active, ok)
	}
	got, ok := s.RunEval(latest.ID)
	if !ok || got.Model != "model" || got.CreatedBy != 7 || got.FinishedAt.IsZero() {
		t.Fatalf("eval = %+v %v", got, ok)
	}
	if _, ok := s.RunEval("missing"); ok {
		t.Fatal("missing eval found")
	}
}

func TestBotConnectionConversationAndCardTransitions(t *testing.T) {
	s := cvEStore(t)
	now := time.Now().UTC()
	if ok, err := s.ClaimBotConnection("cve-lease", "owner", now); err != nil || !ok {
		t.Fatalf("claim = %v %v", ok, err)
	}
	if ok, err := s.RenewBotConnection("cve-lease", "owner", "connected", "ok", now); err != nil || !ok {
		t.Fatalf("renew = %v %v", ok, err)
	}
	state, err := s.BotConnectionState("cve-lease")
	if err != nil || state.State != "connected" {
		t.Fatalf("state = %+v %v", state, err)
	}
	if err := s.ReleaseBotConnection("cve-lease", "owner"); err != nil {
		t.Fatal(err)
	}
	state, _ = s.BotConnectionState("cve-lease")
	if state.State != "offline" {
		t.Fatalf("released state = %+v", state)
	}

	item := domain.BotInteraction{ID: BotMessageID("cve-bot", "m"), BotID: "cve-bot", SenderID: "u", Content: "hello"}
	if inserted, err := s.EnqueueBotInteraction(item, "group", "chat"); err != nil || !inserted {
		t.Fatalf("enqueue = %v %v", inserted, err)
	}
	convID := BotConversationID("cve-bot", "group", "chat")
	conv, err := s.BotConversation(convID)
	if err != nil || conv.SessionID == "" {
		t.Fatalf("conversation = %+v %v", conv, err)
	}
	convs, err := s.BotConversations("cve-bot")
	if err != nil || len(convs) != 1 {
		t.Fatalf("conversations = %d %v", len(convs), err)
	}
	records, err := s.BotInteractionRecords("cve-bot")
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %d %v", len(records), err)
	}
	if err := s.NewBotConversation(records[0]); err != nil {
		t.Fatal(err)
	}

	card := domain.BotInteraction{ID: "cve-card", BotID: "cve-bot", ConversationID: convID, Status: "ready", Action: "notification", CreatedAt: now, UpdatedAt: now}
	if err := s.DB.Create(&card).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SetBotCardTaskID(card.ID, "opaque"); err != nil {
		t.Fatal(err)
	}
	if err := s.FailBotReply(card.ID, "wire"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequeueBotCard(card.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireBotCard(card.ID); err != nil {
		t.Fatal(err)
	}
	card, err = s.BotInteraction(card.ID)
	if err != nil || card.Status != "replied" || card.CardTaskID != "opaque" {
		t.Fatalf("card = %+v %v", card, err)
	}
	if err := s.SetBotCardTaskID("missing", "x"); err == nil {
		t.Fatal("missing card task update succeeded")
	}
	history, err := s.BotHistory(conv.SessionID)
	if err != nil || len(history) != 0 {
		t.Fatalf("history = %d %v", len(history), err)
	}
}

func TestCloneParseSortAndJSONHelpers(t *testing.T) {
	cases := map[string]struct {
		repo string
		iid  int
	}{
		"gf-owner/repo-42": {"owner/repo", 42},
		"gf-a-0":           {"", 0},
		"gf-a-x":           {"", 0},
		"plain":            {"", 0},
		"gf--1":            {"", 0},
	}
	for in, want := range cases {
		gotRepo, gotIID := parseStoredGongfengIssueID(in)
		if gotRepo != want.repo || gotIID != want.iid {
			t.Fatalf("%q = %q,%d", in, gotRepo, gotIID)
		}
	}
	run := domain.Run{
		Nodes:  map[string]*domain.NodeRun{"n": {NodeID: "n"}, "nil": nil},
		Events: []domain.RunEvent{{Type: "x"}}, Logs: []domain.LogRecord{{Body: "x"}},
		Artifacts: []domain.Artifact{{Name: "a"}}, Telemetry: []domain.TelemetryEvent{{Name: "x"}},
	}
	cp := cloneRun(run)
	cp.Nodes["n"].Status = "changed"
	cp.Events[0].Type = "changed"
	if run.Nodes["n"].Status != "" || run.Events[0].Type != "x" || cp.Nodes["nil"] != nil {
		t.Fatal("clone aliases input")
	}
	values := []int{3, 1, 2}
	sortSlice(values, func(a, b int) bool { return a < b })
	if values[0] != 1 || values[2] != 3 {
		t.Fatalf("sort = %v", values)
	}
	if string(mustJSON(func() {})) != "{}" {
		t.Fatal("marshal failure fallback")
	}
}

func TestKnowledgeMCPFilesSpacesAndAssistantTasks(t *testing.T) {
	s := cvEStore(t)
	if err := s.PutKnowledgeSettings(domain.KnowledgeSettings{Enabled: true, URL: "https://kb", Token: "secret", DefaultSources: []string{"docs"}}); err != nil {
		t.Fatal(err)
	}
	settings := s.KnowledgeSettings()
	if !settings.Enabled || settings.Token != "secret" || !settings.TokenConfigured {
		t.Fatalf("settings = %+v", settings)
	}
	if err := s.PutKnowledgeSubmission(domain.KnowledgeSubmission{}); err == nil {
		t.Fatal("empty submission accepted")
	}
	sub := domain.KnowledgeSubmission{ID: "cve-sub", ProjectID: "platform", RunID: "r", ArtifactID: "a", Title: "Doc", Content: "body"}
	if err := s.PutKnowledgeSubmission(sub); err != nil {
		t.Fatal(err)
	}
	gotSub, ok := s.KnowledgeSubmission(sub.ID)
	if !ok || gotSub.Status != domain.KnowledgeSubmissionPending || gotSub.Content != "body" {
		t.Fatalf("submission = %+v %v", gotSub, ok)
	}
	list := s.KnowledgeSubmissions("platform", domain.KnowledgeSubmissionPending, 0)
	if len(list) != 1 || list[0].Content != "" {
		t.Fatalf("submissions = %+v", list)
	}
	if ids := s.KnowledgeSubmissionIDsByArtifact([]string{"a", "missing"}); ids["a"] != sub.ID {
		t.Fatalf("artifact IDs = %v", ids)
	}
	if err := s.DeleteKnowledgeSubmission(sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKnowledgeSubmission(sub.ID); err == nil {
		t.Fatal("missing submission deleted")
	}

	mcp := domain.MCPServerDefinition{ID: "cve-mcp", Name: "MCP", Transport: "stdio", Command: "tool"}
	if err := s.PutMCPServer(mcp); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMCPFiles("", []SkillFileInput{{Path: "x", Content: []byte("x")}}); err == nil {
		t.Fatal("empty MCP accepted")
	}
	if err := s.PutMCPFiles(mcp.ID, nil); err == nil {
		t.Fatal("empty files accepted")
	}
	files := []SkillFileInput{{Path: "main.go", Content: []byte("package main"), Mode: 0o755}, {Path: "README.md", Content: []byte("readme")}}
	if err := s.PutMCPFiles(mcp.ID, files); err != nil {
		t.Fatal(err)
	}
	if refs := s.MCPFileRefs(mcp.ID); len(refs) != 2 || refs[0].Path != "README.md" {
		t.Fatalf("refs = %+v", refs)
	}
	if body, ok := s.MCPFileContent(mcp.ID, "main.go"); !ok || string(body) != "package main" {
		t.Fatalf("content = %q %v", body, ok)
	}
	if contents := s.MCPFileContents(mcp.ID); len(contents) != 2 {
		t.Fatalf("contents = %v", contents)
	}
	if err := s.DeleteMCPFile("", "x"); err == nil {
		t.Fatal("empty delete accepted")
	}
	if err := s.DeleteMCPFile(mcp.ID, "main.go"); err != nil {
		t.Fatal(err)
	}

	space := domain.RequirementSpace{ID: "cve-space", ProjectID: "platform", IssueID: "issue"}
	if err := s.PutSpace(space); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.SpaceByIssue("issue"); !ok || got.ID != space.ID {
		t.Fatalf("space = %+v %v", got, ok)
	}
	member := domain.SpaceMember{SpaceID: space.ID, UserID: 7, Name: "User"}
	if err := s.PutSpaceMember(member); err != nil {
		t.Fatal(err)
	}
	if got := s.SpaceMembers(space.ID); len(got) != 1 {
		t.Fatalf("members = %+v", got)
	}
	if err := s.DeleteSpaceMember(space.ID, member.UserID); err != nil {
		t.Fatal(err)
	}
	artifact := domain.SpaceArtifact{ID: "cve-art", SpaceID: space.ID, Kind: "doc", Title: "Document", Body: "v1"}
	artifact, err := s.PutSpaceArtifact(artifact, 0)
	if err != nil || artifact.Version != 1 {
		t.Fatalf("artifact = %+v %v", artifact, err)
	}
	if _, err := s.PutSpaceArtifact(artifact, 0); err != ErrArtifactConflict {
		t.Fatalf("stale artifact = %v", err)
	}
	artifact.Body = "v2"
	artifact, err = s.PutSpaceArtifact(artifact, 1)
	if err != nil || artifact.Version != 2 {
		t.Fatalf("artifact v2 = %+v %v", artifact, err)
	}
	if got := s.SpaceArtifacts(space.ID); len(got) != 1 {
		t.Fatalf("artifacts = %+v", got)
	}
	if revisions := s.SpaceArtifactRevisions(space.ID, artifact.ID); len(revisions) != 2 || revisions[0].Body != "" {
		t.Fatalf("revisions = %+v", revisions)
	}
	if revision, ok := s.SpaceArtifactRevision(space.ID, artifact.ID, 1); !ok || revision.Body != "v1" {
		t.Fatalf("revision = %+v %v", revision, ok)
	}
	thread := domain.SpaceThread{ID: "cve-thread", SpaceID: space.ID, RunID: "run", Title: "Thread"}
	if err := s.PutSpaceThread(thread); err != nil {
		t.Fatal(err)
	}
	if got := s.SpaceThreads(space.ID); len(got) != 1 {
		t.Fatalf("threads = %+v", got)
	}
	if got, ok := s.SpaceRunThread(space.ID, "run"); !ok || got.ID != thread.ID {
		t.Fatalf("run thread = %+v %v", got, ok)
	}
	if err := s.DeleteSpaceArtifact(space.ID, artifact.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SpaceArtifact(space.ID, artifact.ID); ok {
		t.Fatal("artifact remained")
	}

	task := domain.AssistantTask{ID: "cve-task", ProjectID: "platform", Name: "Task", Enabled: true, IntervalSec: 60}
	if err := s.PutAssistantTask(0, task); err == nil {
		t.Fatal("zero user accepted")
	}
	if err := s.PutAssistantTask(1, task); err != nil {
		t.Fatal(err)
	}
	if got := s.ListAssistantTasks(1); len(got) != 1 || got[0].ID != task.ID {
		t.Fatalf("tasks = %+v", got)
	}
	if got := s.EnabledAssistantTasks(); len(got) != 1 {
		t.Fatalf("enabled tasks = %+v", got)
	}
	if !s.AssistantTaskExists("platform", task.ID) {
		t.Fatal("task does not exist")
	}
	if err := s.DeleteAssistantTask(1, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAssistantTask(1, task.ID); err == nil {
		t.Fatal("missing task deleted")
	}
}

func TestRunnerUtilityAndEmptyQueryBranches(t *testing.T) {
	if !containsFold([]string{"One", "TWO"}, "two") || containsFold([]string{"one"}, "three") {
		t.Fatal("containsFold")
	}
	now := time.Now().UTC()
	tasks := []domain.RunnerTask{{ID: "late", CreatedAt: now}, {ID: "early", CreatedAt: now.Add(-time.Minute)}}
	sortRunnerTasksByCreated(tasks)
	if tasks[0].ID != "early" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if nullableInt64(0) != nil || nullableInt64(3) != int64(3) {
		t.Fatal("nullableInt64")
	}
	s := cvEStore(t)
	if active, err := s.ActiveRunnerTasksForRun(""); err != nil || active != nil {
		t.Fatalf("empty active tasks = %+v %v", active, err)
	}
	if got := s.ListAssistantTasks(0); len(got) != 0 {
		t.Fatalf("zero-user tasks = %+v", got)
	}
	if got := s.KnowledgeSubmissionIDsByArtifact(nil); len(got) != 0 {
		t.Fatalf("empty artifact map = %v", got)
	}
	if _, ok := s.SpaceByIssue(""); ok {
		t.Fatal("empty issue matched a space")
	}
	if states, err := s.ListMRDeliveryStates(); err != nil || len(states) != 0 {
		t.Fatalf("delivery states = %+v %v", states, err)
	}
	if err := s.DiscoverMRDeliveryStatesFromArtifacts(); err != nil {
		t.Fatal(err)
	}
	if got := s.MRDeliveryStatesMap([]domain.MRDeliveryKey{{}, {Project: "p"}}); len(got) != 0 {
		t.Fatalf("invalid delivery keys = %v", got)
	}
	if issueIDFromExecutionContext("") != "" || issueIDFromExecutionContext("{bad") != "" {
		t.Fatal("invalid execution context parsed")
	}
	raw := `{"issueId":"cve-issue"}`
	if got := issueIDFromExecutionContext(raw); got != "cve-issue" {
		t.Fatalf("issue ID = %q", got)
	}
	finished := now.Add(time.Minute)
	row := runEvalRow{
		ID: "raw", RunID: "run", Status: domain.RunEvalFailed, Score: 12, Model: "m",
		CreatedBy: 9, Payload: []byte("{bad"), Created: &now, Finished: &finished,
	}
	rec, ok := runEvalFromRow(row)
	if !ok || rec.ID != "raw" || rec.RunID != "run" || rec.Status != domain.RunEvalFailed ||
		rec.Score != 12 || rec.Model != "m" || rec.CreatedBy != 9 || rec.CreatedAt.IsZero() || rec.FinishedAt.IsZero() {
		t.Fatalf("raw eval = %+v %v", rec, ok)
	}
}
