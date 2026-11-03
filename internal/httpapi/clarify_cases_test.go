package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestClarifyHubLifecycleAndPersistence(t *testing.T) {
	st := cvAStore(t)
	projectID := "cva-clarify-p"
	issueID := "cva-clarify-i"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "P"})
	_ = st.PutIssue(domain.Issue{ID: issueID, ProjectID: projectID, Title: "Need"})
	h := &streamHandlers{deps: &deps{repo: st}}
	h.jobs = newClarifyHub(h)
	h.jobs.exec = func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		emit("thought", map[string]string{"text": "thinking"})
		emit("open", map[string]string{"path": "x"})
		parsed := map[string]any{
			"summary": "",
			"ready":   false,
			"questions": []any{
				map[string]any{"id": "q1", "question": "Pick?", "options": []any{"A", "B"}},
			},
		}
		return parsed, `{"summary":"","ready":false,"questions":[{"id":"q1","question":"Pick?","options":["A","B"]}]}`, map[string]any{"sessionId": "sid"}, nil
	}
	if h.jobRunning("") || h.jobRunning("missing") {
		t.Fatal("missing job running")
	}
	if got := historyToClarify([]domain.ClarificationMessage{{Role: "user", Content: "x"}}); len(got) != 1 || got[0].Content != "x" {
		t.Fatalf("history=%v", got)
	}
	if key := composerJobKey(" composer:a !b "); key != "composer:ab" || composerPublicJobID(key) != "ab" {
		t.Fatalf("composer key=%q", key)
	}
	if len(composerJobKey(strings.Repeat("a", 100))) != len(composerJobPrefix)+80 {
		t.Fatal("composer key not clipped")
	}

	job, err := h.ensureClarifyJob(domain.Issue{ID: issueID}, chatClarifyRequest{Message: "start"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("clarify job did not finish")
	}
	if !job.finished() || h.jobRunning(issueID) {
		t.Fatal("finished job reported running")
	}
	saved, _ := st.Issue(issueID)
	if saved.Clarification.Status != domain.ClarificationInProgress || len(saved.Clarification.Questions) != 1 {
		t.Fatalf("saved clarification=%+v", saved.Clarification)
	}

	manualCtx, manualCancel := context.WithCancel(context.Background())
	manual := &clarifyJob{issueID: "manual", done: make(chan struct{}), cancel: manualCancel, subs: map[chan clarifyJobEvent]struct{}{}}
	for i := 0; i < clarifyThoughtCap+5; i++ {
		manual.publish("thought", i)
	}
	ch, replay, _, _, done := manual.subscribe()
	if done || len(replay) != clarifyThoughtCap {
		t.Fatalf("subscribe replay=%d done=%v", len(replay), done)
	}
	manual.publish("done", map[string]any{"ok": true})
	select {
	case event := <-ch:
		if event.Type != "done" {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
	manual.unsubscribe(ch)
	manual.fail("boom")
	manualCancel()
	close(manual.done)
	if !manual.finished() {
		t.Fatal("closed job not finished")
	}
	_ = manualCtx

	_ = st.PutIssue(domain.Issue{ID: "cva-revert", ProjectID: projectID, Title: "R", Clarification: domain.ClarificationAssessment{
		Status: domain.ClarificationAnalyzing, Questions: []domain.ClarificationQuestion{{ID: "q"}},
	}})
	h.revertAnalyzing("cva-revert", errors.New("failure"))
	reverted, _ := st.Issue("cva-revert")
	if reverted.Clarification.Status != domain.ClarificationInProgress || reverted.Clarification.WriteBackError != "failure" {
		t.Fatalf("reverted=%+v", reverted.Clarification)
	}
	_ = st.PutIssue(domain.Issue{ID: "cva-revert-empty", ProjectID: projectID, Title: "R", Clarification: domain.ClarificationAssessment{Status: domain.ClarificationAnalyzing}})
	h.revertAnalyzing("cva-revert-empty", context.Canceled)
	reverted, _ = st.Issue("cva-revert-empty")
	if reverted.Clarification.Status != domain.ClarificationNeedsUser || reverted.Clarification.WriteBackError != "" {
		t.Fatalf("empty reverted=%+v", reverted.Clarification)
	}

	if _, err := h.persistClarifyOutcome("missing", nil, "bad"); err == nil {
		t.Fatal("invalid clarify outcome persisted")
	}
	readyParsed := map[string]any{"summary": "Complete", "ready": true, "questions": []any{}}
	ready, err := h.persistClarifyOutcome(issueID, readyParsed, `{"summary":"Complete","ready":true,"questions":[]}`)
	if err != nil || ready.Clarification.Status != domain.ClarificationReady || ready.Clarification.WriteBackStatus != "skipped" {
		t.Fatalf("ready=%+v %v", ready.Clarification, err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelJob := &clarifyJob{issueID: issueID, done: make(chan struct{}), cancel: func() {}, subs: map[chan clarifyJobEvent]struct{}{}}
	h.runClarifyJob(cancelCtx, cancelJob, issueID, chatClarifyRequest{})
	if !cancelJob.finished() {
		t.Fatal("cancelled direct job not finished")
	}
}

func TestClarifyComposerAndHTTPBranches(t *testing.T) {
	h := &streamHandlers{}
	if _, err := h.ensureComposerJob(domain.Issue{}, chatClarifyRequest{}); err == nil {
		t.Fatal("composer without hub accepted")
	}
	h.jobs = newClarifyHub(h)
	h.jobs.exec = func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		return map[string]any{"ready": true, "summary": "ok"}, `{"ready":true,"summary":"ok"}`, map[string]any{"sessionId": "s"}, nil
	}
	job, err := h.ensureComposerJob(domain.Issue{Title: "draft"}, chatClarifyRequest{JobID: "cva-job"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("composer job timeout")
	}
	_, _, result, errMsg, done := job.subscribe()
	if !done || errMsg != "" || result["jobId"] != "cva-job" {
		t.Fatalf("composer result=%v err=%q done=%v", result, errMsg, done)
	}

	c, rec := cvAContext(t, http.MethodGet, "/attach", "", nil)
	h.attachChatClarify(c)
	if rec.Code != 400 {
		t.Fatalf("missing job id=%d", rec.Code)
	}
	c, rec = cvAContext(t, http.MethodGet, "/attach?jobId=missing", "", nil)
	h.attachChatClarify(c)
	if rec.Code != 404 {
		t.Fatalf("missing composer=%d", rec.Code)
	}
	nilHub := &streamHandlers{}
	c, rec = cvAContext(t, http.MethodGet, "/attach?jobId=x", "", nil)
	nilHub.attachChatClarify(c)
	if rec.Code != 404 {
		t.Fatalf("nil hub=%d", rec.Code)
	}

	failing := &streamHandlers{}
	failing.jobs = newClarifyHub(failing)
	failing.jobs.exec = func(context.Context, domain.Issue, chatClarifyRequest, func(string, any)) (any, string, map[string]any, error) {
		return nil, "", nil, errors.New("failed")
	}
	c, rec = cvAContext(t, http.MethodPost, "/composer", "", nil)
	failing.handleComposerClarify(c, domain.Issue{Title: "draft"}, chatClarifyRequest{JobID: "fail"})
	if rec.Code != 500 {
		t.Fatalf("failed composer=%d %s", rec.Code, rec.Body.String())
	}
}

func TestClarifyMCPInstructionsAndOwnership(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "cva-mcp-owner", TaihuPAT: "tai_pat_owner"})
	owner, _ := st.UserByLoginName("cva-mcp-owner")
	_ = st.PutUser(domain.User{LoginName: "cva-mcp-worker"})
	worker, _ := st.UserByLoginName("cva-mcp-worker")
	projectID := "cva-mcp-p"
	_ = st.PutProject(domain.Project{ID: projectID, Name: "P", OwnerID: owner.ID, TAPDWorkspaceID: "ws", DefaultRepositoryID: "cva-mcp-r"})
	_ = st.PutRepository(domain.Repository{ID: "cva-mcp-r", ProjectID: projectID, Name: "R", GongfengProjectID: "group/repo"})
	for _, server := range []domain.MCPServerDefinition{
		{ID: "cva-tapd", Name: "TAPD", ProjectID: projectID, Scope: "project", MarketID: "70", Transport: "http", URL: "https://example.test/tapd"},
		{ID: "cva-gf", Name: "Gongfeng", ProjectID: projectID, Scope: "project", MarketID: "61", Transport: "http", URL: "https://example.test/gf"},
		{ID: "cva-iwiki", Name: "iWiki", ProjectID: projectID, Scope: "project", MarketID: "58", Transport: "http", URL: "https://example.test/iwiki"},
		{ID: "cva-other", Name: "Other", ProjectID: projectID, Scope: "project", Transport: "http", URL: "https://example.test/other"},
		{ID: "cva-stdio", Name: "stdio", ProjectID: projectID, Scope: "project", Transport: "stdio", Command: "x"},
	} {
		if err := st.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	h := &streamHandlers{deps: &deps{repo: st}}
	tapd := domain.Issue{ID: "tapd-i", ProjectID: projectID, Source: "tapd", Description: "https://iwiki.example.com/pages/12345", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "9", ExternalType: "bug", ExternalURL: "https://tapd.example.com/ws/bugtrace/bugs/view?bug_id=9"}}
	text := h.clarifyMCPInstructionsFor(tapd, worker, false)
	if !strings.Contains(text, "bugs_get") || !strings.Contains(text, "docid=12345") || !strings.Contains(text, "其他已安装") {
		t.Fatalf("tapd instructions=%s", text)
	}
	if resumed := h.clarifyMCPInstructionsFor(tapd, worker, true); !strings.Contains(resumed, "不要再重拉") {
		t.Fatalf("resumed instructions=%s", resumed)
	}
	gf := domain.Issue{ID: "gf-i", ProjectID: projectID, Source: "gongfeng", RepositoryID: "cva-mcp-r", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "12", ExternalIID: 12, ExternalType: "issue"}}
	if text := h.clarifyMCPInstructions(gf, worker, false); !strings.Contains(text, "group/repo") || !strings.Contains(text, "issue_iid=12") {
		t.Fatalf("gf instructions=%s", text)
	}
	draft := domain.Issue{ProjectID: projectID, Description: "draft"}
	if text := h.clarifyMCPInstructionsFor(draft, worker, false); !strings.Contains(text, "尚未建单") {
		t.Fatalf("draft instructions=%s", text)
	}
	if got := h.clarifyMCPUser(domain.Issue{ProjectID: projectID}, worker); got.ID != owner.ID {
		t.Fatalf("clarify owner=%+v", got)
	}
	if len(h.clarifyHTTPMCPs(projectID, worker.ID)) != 4 {
		t.Fatalf("HTTP MCPs=%v", h.clarifyHTTPMCPs(projectID, worker.ID))
	}
	if clarifyTAPDWorkspace(tapd, domain.Project{TAPDWorkspaceID: "fallback"}) == "" {
		t.Fatal("tapd workspace")
	}
	entryType, entryID := clarifyTAPDEntry(tapd)
	if entryType != "bug" || entryID != "9" {
		t.Fatalf("tapd entry=%s/%s", entryType, entryID)
	}
	if h.clarifyGongfengProjectID(gf) != "group/repo" {
		t.Fatal("gongfeng project fallback")
	}
	if texts := clarifyRequestTexts(chatClarifyRequest{Title: "t", Description: "d", Message: "m"}); len(texts) != 3 {
		t.Fatalf("request texts=%v", texts)
	}
}
