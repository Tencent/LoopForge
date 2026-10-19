package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestIssueClarifyContinuesAfterClientDisconnect(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	setClarifyExecForTest(func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		if emit != nil {
			emit("thought", map[string]string{"kind": "thinking", "text": "still going"})
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			t.Fatal("job context was cancelled after the client disconnected")
		}
		parsed := map[string]any{
			"summary":   "请补充验收标准",
			"questions": []any{map[string]any{"id": "q1", "question": "怎么验收？", "options": []any{"接口回归", "手工验收"}, "multiSelect": false}},
			"ready":     false,
		}
		raw, _ := json.Marshal(parsed)
		return parsed, string(raw), map[string]any{"analyzed": false}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	s, h := clarifyTestServer(t)
	if err := s.PutIssue(domain.Issue{ID: "iss-clarify-bg", ProjectID: "platform", Title: "需要澄清", Description: "短", Kind: "requirement", Status: "backlog", Clarification: domain.ClarificationAssessment{Status: domain.ClarificationNeedsUser, Score: 10}}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"message": domain.ClarificationKickoff})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/issues/iss-clarify-bg/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("clarify job did not start")
	}
	startedIssue, ok := s.Issue("iss-clarify-bg")
	if !ok || startedIssue.Clarification.StartedAt.IsZero() {
		t.Fatalf("clarification start was not persisted: %+v", startedIssue.Clarification)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, ok := s.Issue("iss-clarify-bg")
		if ok && stored.Clarification.Status == domain.ClarificationInProgress && len(stored.Clarification.Questions) > 0 {
			if !stored.Clarification.StartedAt.Equal(startedIssue.Clarification.StartedAt) {
				t.Fatalf("clarification start changed after outcome: %+v", stored.Clarification)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	stored, _ := s.Issue("iss-clarify-bg")
	t.Fatalf("job did not persist questions after disconnect: %+v", stored.Clarification)
}

func TestIssueClarifySSEReplayAfterAttach(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	setClarifyExecForTest(func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		emit("thought", map[string]string{"kind": "thinking", "text": "replay-me"})
		close(started)
		<-release
		parsed := map[string]any{"summary": "还缺范围", "questions": []any{map[string]any{"id": "q1", "question": "改哪个模块？", "options": []any{"A", "B"}}}, "ready": false}
		raw, _ := json.Marshal(parsed)
		return parsed, string(raw), map[string]any{"analyzed": false}, nil
	})
	t.Cleanup(func() {
		setClarifyExecForTest(nil)
		select {
		case <-release:
		default:
			close(release)
		}
	})

	s, h := clarifyTestServer(t)
	if err := s.PutIssue(domain.Issue{ID: "iss-clarify-sse", ProjectID: "platform", Title: "SSE", Kind: "requirement", Status: "backlog"}); err != nil {
		t.Fatal(err)
	}
	startBody, _ := json.Marshal(map[string]any{"message": domain.ClarificationKickoff})
	startReq := httptest.NewRequest(http.MethodPost, "/api/issues/iss-clarify-sse/chat-clarify", bytes.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startRec := httptest.NewRecorder()
	go h.ServeHTTP(startRec, startReq)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}
	attach := httptest.NewRequest(http.MethodGet, "/api/issues/iss-clarify-sse/chat-clarify", nil)
	attach.Header.Set("Accept", "text/event-stream")
	attachRec := newFlushRecorder()
	go h.ServeHTTP(attachRec, attach)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(attachRec.Body.String(), "replay-me") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("SSE attach did not replay thought: %s", attachRec.Body.String())
}

func TestIssueClarifySanitizesJSONDumpOptions(t *testing.T) {
	dump := `{"type":"conversation_record","id":"tapd-chat","payload":{"comments":[{"body":"DescribeDomainLastPackageInfo"}]}}`
	setClarifyExecForTest(func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		parsed := map[string]any{
			"summary": "查不到套餐时接口返回了参数不合法",
			"questions": []any{
				map[string]any{
					"id":       "q1",
					"question": "查不到套餐时应该返回什么？",
					"options": []any{
						"返回空数据",
						map[string]any{"label": "保持参数不合法错误"},
						dump,
					},
				},
			},
			"ready": false,
		}
		raw, _ := json.Marshal(parsed)
		return parsed, string(raw), map[string]any{"analyzed": false}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	s, h := clarifyTestServer(t)
	if err := s.PutIssue(domain.Issue{ID: "iss-clarify-json-opt", ProjectID: "platform", Title: "DescribeDomainLastPackageInfo 返回错误", Kind: "requirement", Status: "backlog"}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"message": domain.ClarificationKickoff})
	req := httptest.NewRequest(http.MethodPost, "/api/issues/iss-clarify-json-opt/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Parsed map[string]any `json:"parsed"`
		Issue  domain.Issue   `json:"issue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	assertClarifyOptionsClean(t, payload.Issue.Clarification.Questions)
	rawOpts, _ := payload.Parsed["questions"].([]any)
	if len(rawOpts) != 1 {
		t.Fatalf("parsed questions=%v", payload.Parsed["questions"])
	}
	stored, ok := s.Issue("iss-clarify-json-opt")
	if !ok {
		t.Fatal("issue missing")
	}
	assertClarifyOptionsClean(t, stored.Clarification.Questions)
}

func assertClarifyOptionsClean(t *testing.T, questions []domain.ClarificationQuestion) {
	t.Helper()
	if len(questions) != 1 {
		t.Fatalf("questions=%+v", questions)
	}
	opts := questions[0].Options
	if len(opts) != 2 || opts[0] != "返回空数据" || opts[1] != "保持参数不合法错误" {
		t.Fatalf("options=%v", opts)
	}
	for _, opt := range opts {
		if strings.Contains(opt, "conversation_record") || strings.Contains(opt, "{") {
			t.Fatalf("json dump leaked into option %q", opt)
		}
	}
}

func clarifyTestServer(t *testing.T) (*repo.Store, http.Handler) {
	t.Helper()
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	return s, h
}

type flushRecorder struct{ *httptest.ResponseRecorder }

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (f *flushRecorder) Flush() {}

func TestPersistClarifyEmptyQuestionsRejected(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{
		ID: "iss-mcp-round", ProjectID: "platform", Title: "MCP round", Kind: "requirement", Source: "tapd",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "202"},
	}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s, sync: tracker.NewService()}}
	if _, err := h.persistClarifyOutcome("iss-mcp-round", map[string]any{
		"summary":   "正在读 TAPD 原单",
		"questions": []any{},
		"ready":     false,
		"mcpCalls":  []any{map[string]any{"tool": "stories_get"}},
	}, ""); err == nil {
		t.Fatal("empty questions without ready should not persist")
	}
	stored, ok := s.Issue("iss-mcp-round")
	if !ok {
		t.Fatal("issue missing")
	}
	if stored.Clarification.Status == domain.ClarificationReady {
		t.Fatalf("status=%s", stored.Clarification.Status)
	}
}

func TestPersistClarifyReadyPromotesBacklogToTodo(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{
		ID: "iss-clarify-todo", ProjectID: "platform", Title: "待澄清需求", Kind: "requirement", Status: "backlog",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{
		ID: "iss-clarify-blocked", ProjectID: "platform", Title: "失败重澄清", Kind: "requirement", Status: "blocked",
	}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s, sync: tracker.NewService()}}
	stored, err := h.persistClarifyOutcome("iss-clarify-todo", map[string]any{
		"summary": "已确认返回空数据的语义", "questions": []any{}, "ready": true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "todo" {
		t.Fatalf("clarified backlog requirement status=%q want todo", stored.Status)
	}
	blocked, err := h.persistClarifyOutcome("iss-clarify-blocked", map[string]any{
		"summary": "失败后重新澄清", "questions": []any{}, "ready": true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "blocked" {
		t.Fatalf("clarified in-flight requirement status=%q want blocked (no downgrade)", blocked.Status)
	}
}

func TestPersistClarifyReadyWaitsForUserConfirm(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{
		ID: "iss-ready-confirm", ProjectID: "platform", Title: "ready", Kind: "requirement", Source: "tapd",
		Description: "原 TAPD 描述",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "202"},
	}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s, sync: tracker.NewService()}}
	stored, err := h.persistClarifyOutcome("iss-ready-confirm", map[string]any{
		"summary":   "查不到套餐时应返回空数据",
		"questions": []any{},
		"ready":     true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Clarification.Status != domain.ClarificationReady {
		t.Fatalf("status=%s", stored.Clarification.Status)
	}
	if stored.Clarification.WriteBackStatus != "pending" {
		t.Fatalf("writeBack=%s want pending", stored.Clarification.WriteBackStatus)
	}
	if !strings.Contains(stored.Description, "原 TAPD 描述") || !strings.Contains(stored.Description, "## AgentFlow 澄清结论") || !strings.Contains(stored.Description, "查不到套餐时应返回空数据") {
		t.Fatalf("platform description missing conclusion: %q", stored.Description)
	}
	again, err := h.persistClarifyOutcome("iss-ready-confirm", map[string]any{
		"summary":   "第二轮结论：返回空数据并记日志",
		"questions": []any{},
		"ready":     true,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(again.Description, "## AgentFlow 澄清结论") != 1 {
		t.Fatalf("conclusion block should be replaced, not stacked: %q", again.Description)
	}
	if !strings.Contains(again.Description, "第二轮结论：返回空数据并记日志") || strings.Contains(again.Description, "查不到套餐时应返回空数据") {
		t.Fatalf("stale conclusion kept: %q", again.Description)
	}
}

func TestClarifyStreamEventsDropsProtocolJSONThoughts(t *testing.T) {
	line := func(kind, field, text string) string {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": kind, field: text}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, dump := range []string{
		line("thinking", "thinking", `{"summary":"x","questions":[],"mcpCalls":[{"tool":"stories_get"}]}`),
		line("text", "text", `{"summary":"x","questions":[],"ready":false}`),
		line("thinking", "thinking", "```json\n{\"mcpCalls\":[]}\n```"),
		line("thinking", "thinking", `先读原单 {"summary":"x","questions":[],"mcpCalls":[]}`),
	} {
		if got := clarifyStreamEvents(dump); len(got) != 0 {
			t.Fatalf("protocol json leaked from %s: %+v", dump, got)
		}
	}
	got := clarifyStreamEvents(line("thinking", "thinking", "正在读 TAPD 原单"))
	if len(got) != 1 || got[0]["text"] != "正在读 TAPD 原单" {
		t.Fatalf("got %+v", got)
	}
	got = clarifyStreamEvents(line("text", "text", "我先看 TAPD 原单和评论"))
	if len(got) != 1 || got[0]["text"] != "我先看 TAPD 原单和评论" {
		t.Fatalf("plain text got %+v", got)
	}
}

func TestPersistClarifyRejectsRawDump(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{
		ID: "iss-dump", ProjectID: "platform", Title: "dump", Kind: "requirement",
	}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s}}
	if _, err := h.persistClarifyOutcome("iss-dump", nil, "正在思考\n{\"type\":\"conversation_record\"}"); err == nil {
		t.Fatal("raw dump should not persist")
	}
	stored, ok := s.Issue("iss-dump")
	if !ok {
		t.Fatal("issue missing")
	}
	if stored.Clarification.Summary != "" {
		t.Fatalf("dump leaked into summary %q", stored.Clarification.Summary)
	}
}

func TestPersistClarifyKeepsSessionID(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutIssue(domain.Issue{
		ID: "iss-session", ProjectID: "platform", Title: "session", Kind: "requirement",
		Clarification: domain.ClarificationAssessment{SessionID: "af-clarify-1", SessionUserID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	h := &streamHandlers{deps: &deps{repo: s}}
	stored, err := h.persistClarifyOutcome("iss-session", map[string]any{
		"summary":   "请补充验收标准",
		"questions": []any{map[string]any{"id": "q1", "question": "怎么验收？", "options": []any{"接口回归", "手工验收"}}},
		"ready":     false,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Clarification.SessionID != "af-clarify-1" {
		t.Fatalf("sessionId=%q", stored.Clarification.SessionID)
	}
	if stored.Clarification.SessionUserID != 1 {
		t.Fatalf("sessionUserId=%d", stored.Clarification.SessionUserID)
	}
	h.rememberClarifySession("iss-session", "new-session", 13)
	updated, _ := s.Issue("iss-session")
	if updated.Clarification.SessionID != "new-session" || updated.Clarification.SessionUserID != 13 {
		t.Fatalf("session binding not updated: %+v", updated.Clarification)
	}
	if len(updated.Clarification.History) != len(stored.Clarification.History) || updated.Clarification.Summary != stored.Clarification.Summary {
		t.Fatal("rebinding session must preserve clarification history and summary")
	}
}

func TestComposerClarifyContinuesAfterClientDisconnect(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	setClarifyExecForTest(func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		if emit != nil {
			emit("thought", map[string]string{"kind": "thinking", "text": "composer still going"})
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			t.Fatal("composer job context was cancelled after the client disconnected")
		}
		parsed := map[string]any{
			"summary":   "请补充验收标准",
			"questions": []any{map[string]any{"id": "q1", "question": "怎么验收？", "options": []any{"接口回归", "手工验收"}, "multiSelect": false}},
			"ready":     false,
		}
		raw, _ := json.Marshal(parsed)
		return parsed, string(raw), map[string]any{"analyzed": false, "sessionId": "composer-sess"}, nil
	})
	t.Cleanup(func() { setClarifyExecForTest(nil) })

	_, h := clarifyTestServer(t)
	body, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加个徽章", "jobId": "composer-test-1"})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req = req.WithContext(ctx)
	rec := newFlushRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("composer job did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client disconnect")
	}
	close(release)
	attach := httptest.NewRequest(http.MethodGet, "/api/chat-clarify?jobId=composer-test-1", nil)
	attach.Header.Set("Accept", "text/event-stream")
	attachRec := newFlushRecorder()
	go h.ServeHTTP(attachRec, attach)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body := attachRec.Body.String()
		if strings.Contains(body, "请补充验收标准") && strings.Contains(body, `"jobId":"composer-test-1"`) && strings.Contains(body, `"startedAt":"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("composer attach did not replay done: %s", attachRec.Body.String())
}

func TestComposerClarifySSEReplayAfterAttach(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	setClarifyExecForTest(func(ctx context.Context, issue domain.Issue, request chatClarifyRequest, emit func(string, any)) (any, string, map[string]any, error) {
		emit("thought", map[string]string{"kind": "thinking", "text": "composer-replay-me"})
		close(started)
		<-release
		parsed := map[string]any{"summary": "还缺范围", "questions": []any{map[string]any{"id": "q1", "question": "改哪个模块？", "options": []any{"A", "B"}}}, "ready": false}
		raw, _ := json.Marshal(parsed)
		return parsed, string(raw), map[string]any{"analyzed": false}, nil
	})
	t.Cleanup(func() {
		setClarifyExecForTest(nil)
		select {
		case <-release:
		default:
			close(release)
		}
	})

	_, h := clarifyTestServer(t)
	startBody, _ := json.Marshal(map[string]any{"projectId": "platform", "message": "加徽章", "jobId": "composer-sse-1"})
	startReq := httptest.NewRequest(http.MethodPost, "/api/chat-clarify", bytes.NewReader(startBody))
	startReq.Header.Set("Content-Type", "application/json")
	startReq.Header.Set("Accept", "text/event-stream")
	startRec := newFlushRecorder()
	go h.ServeHTTP(startRec, startReq)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("composer job did not start")
	}
	attach := httptest.NewRequest(http.MethodGet, "/api/chat-clarify?jobId=composer-sse-1", nil)
	attach.Header.Set("Accept", "text/event-stream")
	attachRec := newFlushRecorder()
	go h.ServeHTTP(attachRec, attach)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(attachRec.Body.String(), "composer-replay-me") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("composer SSE attach did not replay thought: %s", attachRec.Body.String())
}
