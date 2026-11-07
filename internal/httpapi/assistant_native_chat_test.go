package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeChatProjectBindingPreservesHistoricalImages(t *testing.T) {
	h, u := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "different-owner"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("different-owner")
	if err := h.repo.PutProject(domain.Project{ID: "private-project", Name: "Private", OwnerID: other.ID}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	attachment, err := saveWorkbenchImage(u.ID, "original.png", "image/png", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	session := domain.AssistantSession{ID: "native-bind", Title: "Existing drawing", Messages: []domain.AssistantMessage{{Role: "user", Content: "Draw"}, {Role: "assistant", Content: `{"message":"Done","drafts":[]}`, Attachments: []domain.AssistantAttachment{attachment}}}}
	if err = h.repo.PutAssistantSession(u.ID, session); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutProject(domain.Project{ID: "another-owned-project", Name: "Other", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		project string
		status  int
	}{{"private-project", 403}, {"workbench", 200}, {"private-project", 403}, {"another-owned-project", 409}} {
		stored, _ := h.repo.AssistantSession(u.ID, session.ID)
		stored.ProjectID = tc.project
		body, _ := json.Marshal(stored)
		c, rec := workbenchContext(u, "PUT", "/", bytes.NewBuffer(body))
		c.Params = gin.Params{{Key: "id", Value: session.ID}}
		h.session(c)
		if rec.Code != tc.status {
			t.Fatalf("binding %s status %d: %s", tc.project, rec.Code, rec.Body.String())
		}
	}
	saved, _ := h.repo.AssistantSession(u.ID, session.ID)
	if saved.ProjectID != "workbench" || saved.Messages[1].Attachments[0].ID != attachment.ID {
		t.Fatal("Binding changed conversation history or image identity")
	}
	req := assistantRequest{SessionID: session.ID, ProjectID: "workbench", Messages: append(saved.Messages, domain.AssistantMessage{Role: "user", Content: "Explain this drawing"})}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBufferString(`{}`))
	if err = h.prepareWorkbenchContext(c, &req, u); err != nil {
		t.Fatalf("Historical image lost after project binding: %v", err)
	}
	fresh := assistantRequest{SessionID: "new-session", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "Injected image", Attachments: []domain.AssistantAttachment{attachment}}}}
	if err = h.prepareWorkbenchContext(c, &fresh, u); err == nil {
		t.Fatal("Unrelated session silently accepted attachment from personal scope")
	}
}

func TestNativeChatGeneratedImagesKeepProjectScope(t *testing.T) {
	_, u := workbenchFixture(t)
	dir := filepath.Join(assistantCLIWorkspaceForUser(u.ID), "generated-images")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drawing.png"), data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	images := workspaceImages(u.ID, time.Time{}, time.Time{}, "workbench")
	if len(images) != 1 {
		t.Fatalf("Expected generated image, got %d", len(images))
	}
	record, _, err := loadWorkbenchAttachment(u.ID, images[0].ID)
	if err != nil || record.ProjectID != "workbench" {
		t.Fatalf("Missing generated image project scope: %+v %v", record, err)
	}
}
func TestNativeChatLocalImageMarkdownIsRemovedAsAWhole(t *testing.T) {
	text := stripLocalMediaPaths("画好了\n![preview](/private/generated-images/drawing.png)\n继续")
	if strings.Contains(text, "](") || strings.Contains(text, "/private") || !strings.Contains(text, "继续") {
		t.Fatalf("Broken image Markdown: %s", text)
	}
}

func TestNativeChatUserTitleSurvivesGenerationUpdates(t *testing.T) {
	h, u := workbenchFixture(t)
	initial := domain.AssistantSession{ID: "native-title", Title: "First prompt", Messages: []domain.AssistantMessage{{Role: "user", Content: "First prompt"}}}
	if err := h.repo.PutAssistantSession(u.ID, initial); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.repo.AssistantSession(u.ID, initial.ID)
	stored.Title = "My own title"
	body, _ := json.Marshal(stored)
	c, rec := workbenchContext(u, "PUT", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: initial.ID}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatalf("rename failed: %s", rec.Body.String())
	}
	pending := domain.AssistantSession{ID: initial.ID, Title: "First prompt", Messages: stored.Messages}
	if err := h.repo.PutAssistantSession(u.ID, pending); err != nil {
		t.Fatal(err)
	}
	current, _ := h.repo.AssistantSession(u.ID, initial.ID)
	if current.Title != "My own title" || !current.TitleEdited {
		t.Fatalf("Generation overwrote user title: %+v", current)
	}
	current.Title = "Renamed again"
	body, _ = json.Marshal(current)
	c, rec = workbenchContext(u, "PUT", "/", bytes.NewBuffer(body))
	c.Params = gin.Params{{Key: "id", Value: initial.ID}}
	h.session(c)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	current, _ = h.repo.AssistantSession(u.ID, initial.ID)
	if current.Title != "Renamed again" {
		t.Fatalf("second rename ignored: %s", current.Title)
	}
}

func TestNativeChatCloneDraftIncludesConversationProject(t *testing.T) {
	h, u := workbenchFixture(t)
	drafts := h.resolveAssistantDrafts(context.Background(), u, "workbench", []assistantDraft{{Tool: "workflows_clone", Path: map[string]any{"id": "source"}, Body: json.RawMessage(`{"id":"copy","name":"Copy"}`)}}, AssistantApprovalConfirm)
	var body map[string]any
	if len(drafts) != 1 || json.Unmarshal(drafts[0].Body, &body) != nil || body["projectId"] != "workbench" || drafts[0].Auto {
		t.Fatalf("Project missing from reviewable clone draft: %+v", drafts)
	}
	if err := validateAssistantDraft(&drafts[0], "workbench"); err != nil {
		t.Fatalf("MCP contract rejects project-scoped clone: %v", err)
	}
}

func TestNativeChatWorkflowClonePreservesSourceProject(t *testing.T) {
	h, _ := workbenchFixture(t)
	source := domain.WorkflowDefinition{ID: "native-clone-source", Name: "Source", ProjectID: "workbench", Nodes: []domain.WorkflowNode{{ID: "step", Name: "Step", Type: "script", ScriptLanguage: "shell", Script: "true"}}}
	if err := h.repo.PutWorkflow(source); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	catalog := &catalogHandlers{deps: h.deps}
	router.POST("/api/workflows/:id/clone", catalog.workflow)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/native-clone-source/clone", strings.NewReader(`{"id":"native-clone-copy","name":"Copy"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("Clone failed: %d %s", rec.Code, rec.Body.String())
	}
	saved, ok := h.repo.Workflow("native-clone-copy")
	if !ok || saved.ProjectID != "workbench" {
		t.Fatalf("Clone escaped source project: %+v", saved)
	}
}
