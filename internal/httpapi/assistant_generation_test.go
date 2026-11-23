package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGenerationSurvivesRequestAndCanBeCancelled(t *testing.T) {
	h, u := workbenchFixture(t)
	entered := make(chan struct{}, 4)
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	start := func(id string) *httptest.ResponseRecorder {
		c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"`+id+`","messages":[{"role":"user","content":"登录"}]}`))
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		h.startGeneration(c)
		cancel()
		return rec
	}
	for _, id := range []string{"session-one", "session-two"} {
		response := start(id)
		if response.Code != 202 {
			t.Fatalf("start: %d %s", response.Code, response.Body.String())
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("generation never started")
		}
		s, ok := h.repo.AssistantSession(u.ID, id)
		if !ok || len(s.Messages) != 1 {
			t.Fatal("user turn was not persisted")
		}
		g, ok := h.repo.AssistantGeneration(u.ID, id)
		if !ok || g.Status != "running" {
			t.Fatalf("browser cancellation stopped execution: %+v", g)
		}
	}
	if response := start("session-one"); response.Code != 409 {
		t.Fatalf("same-session duplicate: %d", response.Code)
	}
	for _, id := range []string{"session-one", "session-two"} {
		g, _ := h.repo.AssistantGeneration(u.ID, id)
		if err := h.repo.CancelAssistantGeneration(u.ID, id, g.ID); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		one, _ := h.repo.AssistantGeneration(u.ID, "session-one")
		two, _ := h.repo.AssistantGeneration(u.ID, "session-two")
		if one.Status == "cancelled" && two.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancel did not complete: %s %s", one.Status, two.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReferencesGlobalSearchAndPermission(t *testing.T) {
	h, u := workbenchFixture(t)
	h.repo.PutProject(domain.Project{ID: "another", Name: "另一项目", OwnerID: u.ID})
	h.repo.PutProject(domain.Project{ID: "hidden", Name: "私有", OwnerID: 999})
	h.repo.PutIssue(domain.Issue{ID: "login", ProjectID: "another", Title: "登录体验"})
	h.repo.PutIssue(domain.Issue{ID: "secret", ProjectID: "hidden", Title: "登录私有"})
	h.repo.PutAgent(domain.AgentDefinition{ID: "login-role", ProjectID: "workbench", Name: "登录开发"})
	c, rec := workbenchContext(u, "GET", "/api/assistant/references?scope=all&q=登录", bytes.NewBuffer(nil))
	h.workbenchReferences(c)
	var result struct {
		Items []domain.AssistantReference `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("global search: %s", rec.Body.String())
	}
	c, rec = workbenchContext(u, "GET", "/api/assistant/references?scope=all&type=issue&q=登录", bytes.NewBuffer(nil))
	h.workbenchReferences(c)
	json.Unmarshal(rec.Body.Bytes(), &result)
	if len(result.Items) != 1 || result.Items[0].ID != "login" {
		t.Fatalf("typed search: %s", rec.Body.String())
	}
	req := assistantRequest{ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "比较", References: []domain.AssistantReference{{Type: "issue", ID: "login", ProjectID: "hidden"}}}}}
	c, _ = workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &req, u); err != errForeignReference {
		t.Fatalf("project conversation referenced another project: %v", err)
	}
	// Personal conversations keep cross-project references.
	req.ProjectID = ""
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatal(err)
	}
	if req.Messages[0].References[0].ProjectID != "another" {
		t.Fatal("trusted forged project metadata")
	}
	req.Messages[0].References = []domain.AssistantReference{{Type: "issue", ID: "secret"}}
	if err := h.prepareWorkbenchContext(c, &req, u); err == nil {
		t.Fatal("private reference accepted")
	}
	// Generation snapshots remain private even if another user knows the ID.
	c, rec = workbenchContext(domain.User{ID: 999}, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: "session-one"}}
	h.generation(c)
	if rec.Code != 404 {
		t.Fatalf("private snapshot: %d", rec.Code)
	}
}

func TestGenerationCompletedReplyAndSnapshot(t *testing.T) {
	h, u := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"已分析登录需求","drafts":[]}`, nil
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"completed-chat","clientRequestId":"completion-request","messages":[{"role":"user","content":"分析登录需求"}]}`))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %s", rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		g, _ := h.repo.AssistantGeneration(u.ID, "completed-chat")
		if g.Status == "completed" {
			if !bytes.Contains(g.Reply, []byte("已分析登录需求")) {
				t.Fatalf("reply: %s", g.Reply)
			}
			s, ok := h.repo.AssistantSession(u.ID, "completed-chat")
			if !ok || len(s.Messages) != 2 {
				t.Fatal("completed transcript missing")
			}
			c, rec = workbenchContext(u, "GET", "/api/assistant/sessions/completed-chat/generation?after=99999", bytes.NewBuffer(nil))
			c.Params = gin.Params{{Key: "id", Value: "completed-chat"}}
			h.generation(c)
			var snapshot domain.AssistantGeneration
			json.Unmarshal(rec.Body.Bytes(), &snapshot)
			if rec.Code != 200 || snapshot.Status != "completed" || len(snapshot.Events) != 0 ||
				len(snapshot.Reply) == 0 || snapshot.ClientRequestID != "completion-request" {
				t.Fatalf("snapshot: %s", rec.Body.String())
			}
			break
		}
		if g.Status != "running" {
			t.Fatalf("failed: %+v", g)
		}
		if time.Now().After(deadline) {
			t.Fatal("completion timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAssistantGenerationKeepsAcceptanceAfterNextTurn(t *testing.T) {
	h, user := workbenchFixture(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"回复","drafts":[]}`, nil
	}
	const sessionID = "receipt-chat"
	var messages []assistantMessage
	for _, requestID := range []string{"first-request", "second-request"} {
		messages = append(messages, assistantMessage{Role: "user", Content: "继续"})
		body, err := json.Marshal(gin.H{
			"projectId": "workbench", "sessionId": sessionID,
			"clientRequestId": requestID, "messages": messages,
		})
		if err != nil {
			t.Fatal(err)
		}
		c, rec := workbenchContext(user, http.MethodPost, "/api/assistant/generations", bytes.NewBuffer(body))
		h.startGeneration(c)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("start %s: %d %s", requestID, rec.Code, rec.Body)
		}
		messages = waitForCompletedGeneration(t, h, user.ID, sessionID).Messages
	}
	generation, _ := h.repo.AssistantGeneration(user.ID, sessionID)
	if generation.ClientRequestID != "second-request" {
		t.Fatal("latest generation did not replace the first", generation.ClientRequestID)
	}
	c, rec := workbenchContext(user, http.MethodGet, "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: sessionID}}
	h.session(c)
	var session domain.AssistantSession
	if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil || rec.Code != http.StatusOK {
		t.Fatal("could not load acceptance receipts", rec.Code, rec.Body.String(), err)
	}
	receipts := session.RecentClientRequestIDs
	if len(receipts) != 2 || receipts[0] != "first-request" || receipts[1] != "second-request" {
		t.Fatal("the first send lost its receipt", receipts)
	}
}

func TestAssistantComposerModelRefusesCatalogSubstitution(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "available-model")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "available-model")
	h, user := workbenchFixture(t)
	stubCLIModelDiscovery(t)
	h.generate = func(context.Context, string, domain.User) (string, error) {
		return `{"message":"回复","drafts":[]}`, nil
	}
	send := func(id, model string, exact bool, handler func(*gin.Context), refs ...domain.AssistantReference) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(assistantRequest{
			SessionID: id, ProjectID: "workbench", Model: model, RequireExactModel: exact,
			Messages: []assistantMessage{{Role: "user", Content: "继续", References: refs}},
		})
		if err != nil {
			t.Fatal(err)
		}
		c, rec := workbenchContext(user, http.MethodPost, "/", bytes.NewBuffer(body))
		handler(c)
		return rec
	}
	const explicitID = "explicit-model"
	issue := domain.Issue{ID: "model-issue", ProjectID: "workbench", Title: "待讨论需求"}
	if err := h.repo.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(user.ID, domain.AssistantSession{ID: explicitID, ProjectID: issue.ProjectID}); err != nil {
		t.Fatal(err)
	}
	ref := domain.AssistantReference{Type: "issue", ID: issue.ID, Name: issue.Title, ProjectID: issue.ProjectID}
	for _, handler := range []func(*gin.Context){h.startGeneration, h.chat} {
		rec := send(explicitID, "retired-model", true, handler, ref)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "请重新选择模型") {
			t.Fatal("the explicit model was silently replaced", rec.Code, rec.Body.String())
		}
		if saved, _ := h.repo.AssistantSession(user.ID, explicitID); saved.IssueID != "" {
			t.Fatal("the rejected send changed the conversation's linked issue", saved.IssueID)
		}
	}
	if _, ok := h.repo.AssistantGeneration(user.ID, explicitID); ok {
		t.Fatal("the unavailable model admitted a generation")
	}
	for _, tc := range []struct {
		id, model string
		exact     bool
	}{
		{id: "legacy-model", model: "retired-model"},
		{id: "default-alias", model: assistantDefaultModel, exact: true},
		{id: "listed-model", model: "available-model", exact: true},
	} {
		rec := send(tc.id, tc.model, tc.exact, h.startGeneration)
		if rec.Code != http.StatusAccepted {
			t.Fatal("could not use the current model", tc.id, rec.Code, rec.Body.String())
		}
		if saved := waitForCompletedGeneration(t, h, user.ID, tc.id); saved.Model != "available-model" {
			t.Fatal("unexpected selected model", tc.id, saved.Model)
		}
	}

	const runningID = "running-model"
	running := domain.AssistantGeneration{ID: "running-turn", SessionID: runningID, UserID: user.ID, Status: "running"}
	pending := domain.AssistantSession{ID: runningID, ProjectID: "workbench", Model: "available-model",
		Messages: []assistantMessage{{Role: "user", Content: "第一条"}}}
	if err := h.repo.BeginAssistantGeneration(running, pending); err != nil {
		t.Fatal(err)
	}
	c, rec := workbenchContext(user, http.MethodPost, "/?generationId="+running.ID, bytes.NewBufferString(
		`{"content":"请修改方向","model":"retired-model","requireExactModel":true}`))
	c.Params = gin.Params{{Key: "id", Value: runningID}}
	h.steerGeneration(c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "请重新选择模型") {
		t.Fatal("steering silently changed the requested model", rec.Code, rec.Body.String())
	}
	if g, _ := h.repo.PeekAssistantGeneration(user.ID, runningID); g.CancelRequested || g.Steer != nil {
		t.Fatal("rejected model interrupted the original reply", g)
	}
}

func TestStartGenerationAcceptsLargeRequestBody(t *testing.T) {
	h, u := workbenchFixture(t)
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		return "回复", nil
	}
	largeActivityText := strings.Repeat("thinking step\n", 200000)
	largeMessages := []assistantMessage{
		{Role: "user", Content: "帮我做实验"},
		{
			Role:    "assistant",
			Content: "好的，正在执行实验",
			Activity: []domain.AssistantActivityEvent{
				{Kind: "thinking", Text: largeActivityText},
			},
		},
		{Role: "user", Content: "继续推进"},
	}
	_ = saveAssistantConversation(h.repo, u.ID, assistantConversation{
		ID:        "large-session",
		ProjectID: "workbench",
		Messages:  largeMessages[:2],
	})
	reqBody, err := json.Marshal(assistantRequest{
		ProjectID: "workbench",
		SessionID: "large-session",
		Messages:  largeMessages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqBody) <= 2<<20 {
		t.Fatalf("expected payload > 2MB, got %d", len(reqBody))
	}
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBuffer(reqBody))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("expected 202 Accepted, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionUpdateAcceptsLargeRequestBody(t *testing.T) {
	h, u := workbenchFixture(t)
	largeActivityText := strings.Repeat("thinking step\n", 200000)
	largeMessages := []assistantMessage{
		{Role: "user", Content: "帮我做实验"},
		{
			Role:    "assistant",
			Content: "好的，正在执行实验",
			Activity: []domain.AssistantActivityEvent{
				{Kind: "thinking", Text: largeActivityText},
			},
		},
	}
	_ = saveAssistantConversation(h.repo, u.ID, assistantConversation{
		ID:        "large-session-put",
		ProjectID: "workbench",
		Title:     "原标题",
		Messages:  largeMessages,
	})
	saved, _ := loadAssistantConversation(h.repo, u.ID, "large-session-put")
	body := struct {
		assistantConversation
		Title string `json:"title"`
	}{
		assistantConversation: assistantConversation{
			ID:        "large-session-put",
			ProjectID: "workbench",
			Title:     "新标题",
			Messages:  largeMessages,
			UpdatedAt: saved.UpdatedAt,
		},
		Title: "新标题",
	}
	reqBody, _ := json.Marshal(body)
	if len(reqBody) <= 2<<20 {
		t.Fatalf("expected payload > 2MB, got %d", len(reqBody))
	}
	c, rec := workbenchContext(u, "PUT", "/api/assistant/sessions/large-session-put", bytes.NewBuffer(reqBody))
	c.Params = gin.Params{{Key: "id", Value: "large-session-put"}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatalf("expected 200 OK, got %d %s", rec.Code, rec.Body.String())
	}
}
