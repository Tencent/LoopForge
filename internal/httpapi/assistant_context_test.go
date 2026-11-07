package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"strings"
	"testing"
	"time"
)

func putContextIssue(t *testing.T, h *assistantHandlers, id, project, title string) {
	t.Helper()
	if err := h.repo.PutIssue(domain.Issue{ID: id, ProjectID: project, Title: title, Description: "需求正文", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
}
func TestSessionIssueBindingContextAndPrivacy(t *testing.T) {
	h, u := workbenchFixture(t)
	putContextIssue(t, h, "primary", "workbench", "主需求初版")
	if err := h.repo.PutProject(domain.Project{ID: "other", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	putContextIssue(t, h, "foreign", "other", "其他项目需求")
	if err := h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "bound-chat", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "讨论", AuthorID: u.ID}}}); err != nil {
		t.Fatal(err)
	}
	old, _ := h.repo.AssistantSession(u.ID, "bound-chat")
	update := func(actor domain.User, id string, stamp time.Time, want int) {
		t.Helper()
		raw, _ := json.Marshal(gin.H{"issueId": id, "updatedAt": stamp})
		c, w := workbenchContext(actor, "PUT", "/", bytes.NewBuffer(raw))
		c.Params = gin.Params{{Key: "id", Value: old.ID}}
		h.sessionIssue(c)
		if w.Code != want {
			t.Fatalf("bind %s: %d %s", id, w.Code, w.Body)
		}
	}
	update(u, "foreign", old.UpdatedAt, 403)
	update(u, "missing", old.UpdatedAt, 403)
	attacker := u
	attacker.ID += 10000
	update(attacker, "primary", old.UpdatedAt, 403)
	update(u, "primary", old.UpdatedAt, 200)
	bound, _ := h.repo.AssistantSession(u.ID, old.ID)
	if bound.IssueID != "primary" || bound.HistoryRevision != old.HistoryRevision+1 || bound.Shared {
		t.Fatalf("bad binding: %+v", bound)
	}
	update(u, "", old.UpdatedAt, 409)
	// A stale generation snapshot cannot overwrite the binding.
	pending := old
	pending.Messages = append(pending.Messages, domain.AssistantMessage{Role: "user", Content: "过期请求"})
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "stale-context", SessionID: old.ID, UserID: u.ID, Status: "running"}, pending); err == nil {
		t.Fatal("stale context started")
	}
	if err := h.repo.PutAssistantSession(u.ID, old); err != nil {
		t.Fatal(err)
	}
	bound, _ = h.repo.AssistantSession(u.ID, old.ID)
	if bound.IssueID != "primary" {
		t.Fatal("transcript save lost binding")
	}
	req := assistantRequest{SessionID: old.ID, ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "继续"}}}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil || !strings.Contains(req.contextText, "主需求初版") {
		t.Fatal(err, req.contextText)
	}
	putContextIssue(t, h, "primary", "workbench", "主需求最新版")
	if err := h.prepareWorkbenchContext(c, &req, u); err != nil || !strings.Contains(req.contextText, "主需求最新版") || strings.Contains(req.contextText, "主需求初版") {
		t.Fatal(err, req.contextText)
	}
	// Project conversations linked to an issue are visible to project members.
	if err := h.repo.PutUser(domain.User{LoginName: "context-peer"}); err != nil {
		t.Fatal(err)
	}
	peer, _ := h.repo.UserByLoginName("context-peer")
	p, _ := h.repo.Project("workbench")
	p.Members = append(p.Members, domain.ProjectMember{UserID: peer.ID, Role: "member"})
	_ = h.repo.PutProject(p)
	for _, tc := range []struct {
		actor domain.User
		want  int
	}{{u, 1}, {peer, 1}} {
		c, w := workbenchContext(tc.actor, "GET", "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: "primary"}}
		h.issueConversations(c)
		var rows []domain.AssistantSession
		_ = json.Unmarshal(w.Body.Bytes(), &rows)
		if w.Code != 200 || len(rows) != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	update(u, "", bound.UpdatedAt, 200)
	s, _ := h.repo.AssistantSession(u.ID, old.ID)
	if s.IssueID != "" {
		t.Fatal("unbind failed")
	}
}
func TestSessionIssueCreationAndSharedRunAssociation(t *testing.T) {
	h, u, peer, out, shared := sharedWorkbenchFixture(t)
	putContextIssue(t, h, "run-issue", "workbench", "运行需求")
	run, _ := h.repo.RunState(shared.RunID)
	run.ExecutionContext.IssueID = "run-issue"
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actor domain.User
		want  int
	}{{u, 1}, {peer, 1}, {out, 0}} {
		c, w := workbenchContext(tc.actor, "GET", "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: "run-issue"}}
		h.issueConversations(c)
		var rows []domain.AssistantSession
		_ = json.Unmarshal(w.Body.Bytes(), &rows)
		if tc.want == 0 {
			if w.Code != 403 {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 200 || len(rows) != tc.want || rows[0].ID != repo.RunAssistantSessionID(run.ID) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	c, w := workbenchContext(u, "PUT", "/", bytes.NewBufferString(`{"issueId":"run-issue"}`))
	c.Params = gin.Params{{Key: "id", Value: shared.ID}}
	h.sessionIssue(c)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		issue string
		want  int
	}{{"run-issue", 200}, {"absent", 403}} {
		raw, _ := json.Marshal(domain.AssistantSession{ProjectID: "workbench", IssueID: tc.issue})
		c, w := workbenchContext(u, "PUT", "/", bytes.NewBuffer(raw))
		c.Params = gin.Params{{Key: "id", Value: "new-" + tc.issue}}
		h.session(c)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestSessionIssueCannotChangeDuringGeneration(t *testing.T) {
	h, u := workbenchFixture(t)
	putContextIssue(t, h, "a", "workbench", "A")
	_ = h.repo.PutAssistantSession(u.ID, domain.AssistantSession{ID: "active-binding", ProjectID: "workbench"})
	s, _ := h.repo.AssistantSession(u.ID, "active-binding")
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{ID: "active-g", SessionID: s.ID, UserID: u.ID, Status: "running"}, s); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(gin.H{"issueId": "a", "updatedAt": s.UpdatedAt})
	c, w := workbenchContext(u, "PUT", "/", bytes.NewBuffer(raw))
	c.Params = gin.Params{{Key: "id", Value: s.ID}}
	h.sessionIssue(c)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSessionReceiptCannotForgeParticipants(t *testing.T) {
	h, u := workbenchFixture(t)
	raw := `{"messages":[{"role":"user","content":"hello","authorId":999,"authorName":"forged"}],"participants":[{"id":999}],"participantsVersion":1}`
	c, w := workbenchContext(u, "PUT", "/", bytes.NewBufferString(raw))
	c.Params = gin.Params{{Key: "id", Value: "author-check"}}
	h.session(c)
	s, _ := h.repo.AssistantSession(u.ID, "author-check")
	if w.Code != 200 || len(s.Participants) != 1 || s.Participants[0].ID != u.ID || s.Messages[0].AuthorID != u.ID {
		t.Fatal(w.Code, w.Body.String())
	}
}
