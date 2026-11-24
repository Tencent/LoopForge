package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func callRenameSession(h *assistantHandlers, user domain.User, id, title string) (int, string) {
	body, _ := json.Marshal(gin.H{"title": title})
	c, rec := workbenchContext(user, "PUT", "/api/assistant/sessions/"+id+"/title", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.renameSession(c)
	return rec.Code, rec.Body.String()
}

func TestRenameAssistantSessionDuringGeneration(t *testing.T) {
	h, owner, peer, _, _ := sharedWorkbenchFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.generate = func(turnCtx context.Context, _ string, _ domain.User) (string, error) {
		close(entered)
		select {
		case <-release:
			return `{"message":"生成完成","drafts":[]}`, nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-turnCtx.Done():
			return "", turnCtx.Err()
		}
	}

	const id = "running-title"
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{
		ID: id, ProjectID: "workbench", Title: "之前手动设置的名称", TitleEdited: true,
	}); err != nil {
		t.Fatal(err)
	}
	c, rec := workbenchContext(owner, "POST", "/api/assistant/generations", bytes.NewBufferString(
		`{"sessionId":"running-title","projectId":"workbench","messages":[{"role":"user","content":"生成期间请求改名"}]}`,
	))
	h.startGeneration(c)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}

	before, _ := h.repo.AssistantSession(owner.ID, id)
	title := strings.Repeat("会话", 30)
	code, response := callRenameSession(h, peer, id, "  "+title+"  ")
	if code != 200 {
		t.Fatalf("rename: %d %s", code, response)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(response), &saved); err != nil || len(saved) != 2 || saved["title"] != title || saved["titleEdited"] != true {
		t.Fatalf("rename response: %s", response)
	}
	after, _ := h.repo.AssistantSession(owner.ID, id)
	before.Title = title
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rename changed more than the title: got %+v, want %+v", after, before)
	}
	g, _ := h.repo.PeekAssistantGeneration(owner.ID, id)
	if g.Status != "running" {
		t.Fatalf("rename changed the running turn to %q", g.Status)
	}
	transcript, _ := json.Marshal(after)
	if code, _ := callSession(h, peer, "PUT", id, transcript); code != 409 {
		t.Fatalf("full conversation PUT during a generation returned %d", code)
	}

	close(release)
	completed := waitForCompletedGeneration(t, h, owner.ID, id)
	if completed.Title != title || !completed.TitleEdited || len(completed.Messages) != 2 {
		t.Fatalf("reply did not preserve the chosen title and transcript: %+v", completed)
	}
	for _, row := range h.repo.ListAssistantSessions(owner.ID) {
		if row.ID == id && row.Title == title && row.TitleEdited {
			return
		}
	}
	t.Fatal("the history list did not show the renamed session")
}

func TestRenameAssistantSessionPermissionsAndValidation(t *testing.T) {
	h, owner, peer, outsider, flowRun := sharedWorkbenchFixture(t)
	for _, session := range []domain.AssistantSession{
		{ID: "private-title", Title: "私有会话"},
		{ID: "project-title", Title: "项目会话", ProjectID: "workbench"},
	} {
		if err := h.repo.PutAssistantSession(owner.ID, session); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.repo.PutAssistantSession(outsider.ID, domain.AssistantSession{
		ID: "old-run-title", ProjectID: "workbench", RunID: flowRun.RunID,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		user  domain.User
		id    string
		title string
		code  int
	}{
		{"private owner", owner, "private-title", strings.Repeat("🙂", 128), 200},
		{"private outsider", peer, "private-title", "改名", 404},
		{"project member", peer, "project-title", "项目新名称", 200},
		{"project outsider", outsider, "project-title", "改名", 403},
		{"legacy run owner outside the project", outsider, "old-run-title", "改名", 403},
		{"shared flow run", owner, flowRun.ID, "改名", 403},
		{"missing conversation", owner, "missing-title", "改名", 404},
		{"blank title", owner, "private-title", " \t　", 400},
		{"long title", owner, "private-title", strings.Repeat("会", 129), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, response := callRenameSession(h, tc.user, tc.id, tc.title); code != tc.code {
				t.Fatalf("rename: %d %s, want %d", code, response, tc.code)
			}
		})
	}
}
