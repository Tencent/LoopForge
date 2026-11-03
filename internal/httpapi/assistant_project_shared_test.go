package httpapi

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Ordinary conversations bound to a project are visible to and continuable by
// every project member; unbound conversations remain private.
func TestProjectConversationsAreSharedWithMembers(t *testing.T) {
	h, a, b, out, _ := sharedWorkbenchFixture(t)
	now := time.Now().UTC()
	project := domain.AssistantSession{ID: "chat-project", ProjectID: "workbench", Title: "项目讨论", Messages: []domain.AssistantMessage{{Role: "user", Content: "第一问", AuthorID: a.ID, AuthorName: a.LoginName, CreatedAt: now}, {Role: "assistant", Content: `{"message":"第一答","drafts":[]}`, CreatedAt: now}}}
	private := domain.AssistantSession{ID: "chat-private", Title: "私人", Messages: []domain.AssistantMessage{{Role: "user", Content: "私事", CreatedAt: now}}}
	for _, s := range []domain.AssistantSession{project, private} {
		if err := h.repo.PutAssistantSession(a.ID, s); err != nil {
			t.Fatal(err)
		}
	}
	get := func(u domain.User, id string) int {
		c, w := workbenchContext(u, "GET", "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.session(c)
		return w.Code
	}
	if code := get(b, project.ID); code != 200 {
		t.Fatalf("member cannot open project conversation: %d", code)
	}
	if code := get(out, project.ID); code != 403 {
		t.Fatalf("non-member read project conversation: %d", code)
	}
	if code := get(b, private.ID); code != 404 {
		t.Fatalf("unbound conversation leaked: %d", code)
	}
	listed := func(u domain.User) map[string]bool {
		c, w := workbenchContext(u, "GET", "/", bytes.NewBuffer(nil))
		h.listSessions(c)
		var list []domain.AssistantSession
		json.Unmarshal(w.Body.Bytes(), &list)
		ids := map[string]bool{}
		for _, item := range list {
			ids[item.ID] = true
		}
		return ids
	}
	if ids := listed(b); !ids[project.ID] || ids[private.ID] {
		t.Fatalf("member list wrong: %v", ids)
	}
	if ids := listed(out); ids[project.ID] || ids[private.ID] {
		t.Fatalf("outsider list wrong: %v", ids)
	}

	actor := int64(0)
	h.generate = func(_ context.Context, _ string, u domain.User) (string, error) {
		actor = u.ID
		return "第二答", nil
	}
	stored, _ := h.repo.AssistantSession(a.ID, project.ID)
	forged := append(append([]assistantMessage{}, stored.Messages...), assistantMessage{Role: "assistant", Content: "伪造"}, assistantMessage{Role: "user", Content: "第二问"})
	body, _ := json.Marshal(assistantRequest{ProjectID: "workbench", SessionID: project.ID, Messages: forged})
	c, w := workbenchContext(b, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if w.Code != 400 {
		t.Fatalf("forged reply accepted: %d %s", w.Code, w.Body.String())
	}
	next := append(append([]assistantMessage{}, stored.Messages...), assistantMessage{Role: "user", Content: "第二问", AuthorID: a.ID, AuthorName: "forged"})
	body, _ = json.Marshal(assistantRequest{ProjectID: "workbench", SessionID: project.ID, Messages: next})
	c, w = workbenchContext(b, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, _ := h.repo.AssistantSession(a.ID, project.ID)
	if actor != b.ID || len(saved.Messages) != 4 || saved.UserID != a.ID || saved.Shared {
		t.Fatalf("member turn not stored in shared conversation: actor=%d %+v", actor, saved)
	}
	if saved.Title != project.Title {
		t.Fatalf("a member turn blanked the conversation title: %q", saved.Title)
	}
	if m := saved.Messages[2]; m.AuthorID != b.ID || m.AuthorName != b.LoginName {
		t.Fatalf("member turn attributed to %d %q", m.AuthorID, m.AuthorName)
	}
	if len(saved.Participants) != 2 {
		t.Fatalf("participants not updated: %+v", saved.Participants)
	}

	// Members manage the same conversation, regardless of who created it.
	saved.Title = "改名"
	raw, _ := json.Marshal(saved)
	c, w = workbenchContext(b, "PUT", "/", bytes.NewBuffer(raw))
	c.Params = gin.Params{{Key: "id", Value: project.ID}}
	h.session(c)
	if w.Code != 200 {
		t.Fatalf("member rename failed: %d %s", w.Code, w.Body.String())
	}
	renamed, _ := h.repo.AssistantSession(a.ID, project.ID)
	if renamed.Title != "改名" || len(renamed.Messages) != 4 {
		t.Fatalf("rename lost data: %+v", renamed)
	}
	truncated := renamed
	truncated.Messages = truncated.Messages[:1]
	raw, _ = json.Marshal(truncated)
	c, w = workbenchContext(b, "PUT", "/", bytes.NewBuffer(raw))
	c.Params = gin.Params{{Key: "id", Value: project.ID}}
	h.session(c)
	if w.Code < 400 {
		t.Fatal("member deleted shared messages")
	}
	c, w = workbenchContext(b, "POST", "/", bytes.NewBufferString(`{"archived":true}`))
	c.Params = gin.Params{{Key: "id", Value: project.ID}}
	h.archiveSession(c)
	if w.Code != 200 {
		t.Fatalf("member could not archive project conversation: %d %s", w.Code, w.Body.String())
	}
	if archived, _ := h.repo.AssistantSession(a.ID, project.ID); !archived.Archived {
		t.Fatal("member archive did not update the shared storage slot")
	}

	// Members may edit any user turn; branching stays available.
	edit := func(u domain.User, index int) int {
		current, _ := h.repo.AssistantSession(a.ID, project.ID)
		raw, _ := json.Marshal(gin.H{"messageIndex": index, "content": "改过", "updatedAt": current.UpdatedAt})
		c, w := workbenchContext(u, "POST", "/", bytes.NewBuffer(raw))
		c.Params = gin.Params{{Key: "id", Value: project.ID}}
		h.editSession(c)
		return w.Code
	}
	current, _ := h.repo.AssistantSession(a.ID, project.ID)
	raw, _ = json.Marshal(gin.H{"messageIndex": 0, "content": "分支问题", "updatedAt": current.UpdatedAt})
	c, w = workbenchContext(b, "POST", "/", bytes.NewBuffer(raw))
	c.Params = gin.Params{{Key: "id", Value: project.ID}}
	h.forkSession(c)
	if w.Code != 201 {
		t.Fatalf("member fork failed: %d %s", w.Code, w.Body.String())
	}
	var branch domain.AssistantSession
	json.Unmarshal(w.Body.Bytes(), &branch)
	if branch.Shared || branch.UserID != b.ID || branch.ProjectID != "workbench" || len(branch.ID) > 0 && branch.ID[:4] == "sop-" {
		t.Fatalf("project branch became an SOP branch: %+v", branch)
	}
	if code := edit(b, 2); code != 200 {
		t.Fatalf("member cannot edit own turn: %d", code)
	}
	if code := edit(b, 0); code != 200 {
		t.Fatalf("member cannot edit creator's turn: %d", code)
	}
	if edited, _ := h.repo.AssistantSession(a.ID, project.ID); len(edited.Messages) != 1 || edited.Messages[0].AuthorID != b.ID || edited.UserID != a.ID {
		t.Fatalf("editing changed storage ownership or lost editor attribution: %+v", edited)
	}

	c, w = workbenchContext(b, "DELETE", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: project.ID}}
	h.session(c)
	if w.Code != 200 {
		t.Fatalf("member cannot delete: %d %s", w.Code, w.Body.String())
	}
	if _, exists := h.repo.AssistantSession(a.ID, project.ID); exists {
		t.Fatal("member delete did not remove the shared storage slot")
	}
}

// Accounts are self-registered, so a link alone never opens a project
// conversation: non-members can neither read nor change it.
func TestProjectConversationsArePrivateToMembers(t *testing.T) {
	h, a, b, out, _ := sharedWorkbenchFixture(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	inside, err := saveWorkbenchImage(a.ID, "inside.png", "image/png", data.Bytes(), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	outside, err := saveWorkbenchImage(a.ID, "outside.png", "image/png", data.Bytes(), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := domain.AssistantSession{ID: "chat-readonly", ProjectID: "workbench", Title: "只读", Messages: []domain.AssistantMessage{{Role: "user", Content: "问题", AuthorID: a.ID, CreatedAt: now}, {Role: "assistant", Content: `{"message":"回答","drafts":[]}`, Attachments: []domain.AssistantAttachment{inside}, CreatedAt: now}}}
	if err := h.repo.PutAssistantSession(a.ID, session); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.repo.AssistantSession(a.ID, session.ID)
	call := func(u domain.User, method string, handler func(*gin.Context), body any, query string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		if body == nil {
			raw = nil
		}
		c, w := workbenchContext(u, method, "/?"+query, bytes.NewBuffer(raw))
		c.Params = gin.Params{{Key: "id", Value: session.ID}}
		handler(c)
		return w
	}

	// Members read the full transcript; outsiders get nothing.
	for _, u := range []domain.User{a, b} {
		w := call(u, "GET", h.session, nil, "")
		var got struct {
			ReadOnly bool                      `json:"readOnly"`
			Messages []domain.AssistantMessage `json:"messages"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 200 || got.ReadOnly || len(got.Messages) != 2 {
			t.Fatalf("%s read: %d readOnly=%v %s", u.LoginName, w.Code, got.ReadOnly, w.Body.String())
		}
	}
	if w := call(out, "GET", h.session, nil, ""); w.Code != 403 || strings.Contains(w.Body.String(), "回答") || !strings.Contains(w.Body.String(), "加入项目") {
		t.Fatalf("outsider read: %d %s", w.Code, w.Body.String())
	}
	download := func(u domain.User, id string) int {
		c, w := workbenchContext(u, "GET", "/?sessionId="+session.ID, bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: id}}
		h.downloadAttachment(c)
		return w.Code
	}
	if code := download(b, inside.ID); code != 200 {
		t.Fatalf("member cannot open transcript image: %d", code)
	}
	for _, id := range []string{inside.ID, outside.ID} {
		if code := download(out, id); code != 403 {
			t.Fatalf("outsider fetched attachment %s: %d", id, code)
		}
	}

	// Generation progress is neither readable nor stoppable from outside.
	pending := stored
	pending.Messages = append(append([]domain.AssistantMessage{}, stored.Messages...), domain.AssistantMessage{Role: "user", Content: "继续", AuthorID: a.ID})
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "gen-readonly", SessionID: session.ID, UserID: a.ID, ActorID: a.ID, ProjectID: "workbench", Status: "running", StartedAt: now}, pending); err != nil {
		t.Fatal(err)
	}
	if w := call(b, "GET", h.generation, nil, ""); w.Code != 200 {
		t.Fatalf("member cannot follow generation: %d %s", w.Code, w.Body.String())
	}
	if w := call(out, "GET", h.generation, nil, ""); w.Code != 403 {
		t.Fatalf("outsider followed generation: %d %s", w.Code, w.Body.String())
	}
	if w := call(out, "POST", h.generation, map[string]any{}, "generationId=gen-readonly"); w.Code != 403 {
		t.Fatalf("viewer stopped generation: %d", w.Code)
	}
	if err := h.repo.CancelAssistantGeneration(a.ID, session.ID, "gen-readonly"); err != nil {
		t.Fatal(err)
	}
	stored, _ = h.repo.AssistantSession(a.ID, session.ID)

	// Every write path requires membership.
	next := append(append([]assistantMessage{}, stored.Messages...), assistantMessage{Role: "user", Content: "我也想问"})
	if w := call(out, "POST", h.chat, assistantRequest{ProjectID: "workbench", SessionID: session.ID, Messages: next}, ""); w.Code != 403 || !strings.Contains(w.Body.String(), "加入项目") {
		t.Fatalf("viewer continued conversation: %d %s", w.Code, w.Body.String())
	}
	renamed := stored
	renamed.Title = "改名"
	if w := call(out, "PUT", h.session, renamed, ""); w.Code != 403 {
		t.Fatalf("viewer renamed: %d", w.Code)
	}
	turn := gin.H{"messageIndex": 0, "content": "改过", "updatedAt": stored.UpdatedAt}
	for name, handler := range map[string]func(*gin.Context){"fork": h.forkSession, "edit": h.editSession} {
		if w := call(out, "POST", handler, turn, ""); w.Code != 403 {
			t.Fatalf("viewer %s: %d", name, w.Code)
		}
	}
	if w := call(out, "POST", h.archiveSession, gin.H{"archived": true}, ""); w.Code != 403 {
		t.Fatalf("viewer archived: %d", w.Code)
	}
	if w := call(out, "DELETE", h.session, nil, ""); w.Code != 403 {
		t.Fatalf("viewer deleted: %d", w.Code)
	}
	if w := call(out, "PUT", h.sessionIssue, gin.H{"issueId": "", "updatedAt": stored.UpdatedAt}, ""); w.Code != 403 {
		t.Fatalf("viewer changed issue: %d", w.Code)
	}
	after, _ := h.repo.AssistantSession(a.ID, session.ID)
	if after.Title != stored.Title || len(after.Messages) != len(stored.Messages) || !after.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("viewer changed the conversation: %+v", after)
	}

	// Other projects' conversations never show up in one's list.
	c, w := workbenchContext(out, "GET", "/", bytes.NewBuffer(nil))
	h.listSessions(c)
	if strings.Contains(w.Body.String(), session.ID) {
		t.Fatal("non-member list includes project conversation")
	}
}

// Platform admins are treated like members of every project: they see all
// project conversations in their list and may continue them.
func TestPlatformAdminSeesAndContinuesAllProjectConversations(t *testing.T) {
	h, a, _, _, _ := sharedWorkbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "platform-boss", CodeBuddyAPIKey: "boss-test-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.repo.GrantPlatformAdmin("platform-boss"); err != nil {
		t.Fatal(err)
	}
	admin, _ := h.repo.UserByLoginName("platform-boss")
	p, _ := h.repo.Project("workbench")
	if workbenchProjectAllowsUser(p, admin.ID, false) {
		t.Fatal("fixture: admin must not be a project member")
	}
	now := time.Now().UTC()
	session := domain.AssistantSession{ID: "chat-admin-view", ProjectID: "workbench", Title: "项目讨论", Messages: []domain.AssistantMessage{{Role: "user", Content: "问题", AuthorID: a.ID, CreatedAt: now}, {Role: "assistant", Content: `{"message":"回答","drafts":[]}`, CreatedAt: now}}}
	if err := h.repo.PutAssistantSession(a.ID, session); err != nil {
		t.Fatal(err)
	}

	c, w := workbenchContext(admin, "GET", "/", bytes.NewBuffer(nil))
	h.listSessions(c)
	if !strings.Contains(w.Body.String(), session.ID) {
		t.Fatal("admin list misses project conversation")
	}
	c, w = workbenchContext(admin, "GET", "/", bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: session.ID}}
	h.session(c)
	var got struct {
		ReadOnly bool `json:"readOnly"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.ReadOnly {
		t.Fatalf("admin open: %d readOnly=%v", w.Code, got.ReadOnly)
	}

	h.generate = func(_ context.Context, _ string, _ domain.User) (string, error) {
		return "管理员追问的回答", nil
	}
	stored, _ := h.repo.AssistantSession(a.ID, session.ID)
	next := append(append([]assistantMessage{}, stored.Messages...), assistantMessage{Role: "user", Content: "管理员追问"})
	body, _ := json.Marshal(assistantRequest{ProjectID: "workbench", SessionID: session.ID, Messages: next})
	c, w = workbenchContext(admin, "POST", "/api/assistant/chat", bytes.NewBuffer(body))
	h.chat(c)
	if w.Code != 200 {
		t.Fatalf("admin cannot continue: %d %s", w.Code, w.Body.String())
	}
	saved, _ := h.repo.AssistantSession(a.ID, session.ID)
	if len(saved.Messages) != 4 || saved.Messages[2].AuthorID != admin.ID {
		t.Fatalf("admin turn not stored: %+v", saved.Messages)
	}
}
