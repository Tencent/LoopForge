package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
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
	c, rec := workbenchContext(u, "POST", "/api/assistant/generations", bytes.NewBufferString(`{"projectId":"workbench","sessionId":"completed-chat","messages":[{"role":"user","content":"分析登录需求"}]}`))
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
			if rec.Code != 200 || snapshot.Status != "completed" || len(snapshot.Events) != 0 || len(snapshot.Reply) == 0 {
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
