package httpapi

import (
	"bytes"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

const (
	sessionIssueNote  = "当前会话关联的主需求"
	sessionBranchNote = "这是编辑消息后的讨论分支"
)

// storedSessionGuardFixture adds a project member whose project conversations
// are stored in the member's own slot, plus a second project the owner may use.
func storedSessionGuardFixture(t *testing.T) (*assistantHandlers, domain.User, domain.User) {
	t.Helper()
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "stored-session-peer"}); err != nil {
		t.Fatal(err)
	}
	peer, _ := h.repo.UserByLoginName("stored-session-peer")
	p, _ := h.repo.Project("workbench")
	p.Members = append(p.Members, domain.ProjectMember{UserID: peer.ID, Role: "member"})
	if err := h.repo.PutProject(p); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutProject(domain.Project{ID: "another", Name: "Another", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	return h, owner, peer
}

func prepareStoredSessionTurn(t *testing.T, h *assistantHandlers, u domain.User, req assistantRequest) (assistantRequest, error) {
	t.Helper()
	if len(req.Messages) == 0 {
		req.Messages = []assistantMessage{{Role: "user", Content: "继续"}}
	}
	c, _ := workbenchContext(u, "POST", "/", bytes.NewBuffer(nil))
	err := h.prepareWorkbenchContext(c, &req, u)
	return req, err
}

func TestWorkbenchContextRejectsStoredConversationOfAnotherProject(t *testing.T) {
	h, owner, peer := storedSessionGuardFixture(t)
	// A project conversation opened by the peer lives in the peer's slot; the
	// owner's request must still see its project binding.
	if err := h.repo.PutAssistantSession(peer.ID, domain.AssistantSession{ID: "peer-project-chat", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "讨论", AuthorID: peer.ID}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "own-project-chat", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"peer-project-chat", "own-project-chat"} {
		for _, project := range []string{"another", ""} {
			if _, err := prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: id, ProjectID: project}); err == nil || !strings.Contains(err.Error(), "会话属于其他项目") {
				t.Fatalf("session %s reused from project %q: %v", id, project, err)
			}
		}
		if _, err := prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: id, ProjectID: "workbench"}); err != nil {
			t.Fatalf("session %s rejected in its own project: %v", id, err)
		}
	}
	// A new conversation has nothing stored yet and may start in any allowed project.
	if _, err := prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: "not-yet-stored", ProjectID: "another"}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkbenchContextCarriesStoredIssueAndBranchNotes(t *testing.T) {
	h, owner, peer := storedSessionGuardFixture(t)
	putContextIssue(t, h, "primary", "workbench", "存量主需求")
	putContextIssue(t, h, "moved", "another", "已迁走的需求")
	sessions := []domain.AssistantSession{
		{ID: "peer-issue-branch", ProjectID: "workbench", IssueID: "primary", ParentSessionID: "peer-root", RootSessionID: "peer-root"},
		{ID: "peer-plain", ProjectID: "workbench"},
		{ID: "peer-moved-issue", ProjectID: "workbench", IssueID: "moved"},
	}
	for _, s := range sessions {
		if err := h.repo.PutAssistantSession(peer.ID, s); err != nil {
			t.Fatal(err)
		}
	}
	// The owner reads the peer's conversation through its storage owner.
	req, err := prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: "peer-issue-branch", ProjectID: "workbench"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.contextText, sessionIssueNote) || !strings.Contains(req.contextText, "存量主需求") {
		t.Fatalf("stored issue missing from context: %s", req.contextText)
	}
	if !strings.Contains(req.contextText, sessionBranchNote) {
		t.Fatalf("branch note missing from context: %s", req.contextText)
	}
	req, err = prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: "peer-plain", ProjectID: "workbench"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.contextText, sessionIssueNote) || strings.Contains(req.contextText, sessionBranchNote) {
		t.Fatalf("plain conversation got issue or branch notes: %s", req.contextText)
	}
	// An unknown conversation carries neither note either.
	req, err = prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: "not-yet-stored", ProjectID: "workbench"})
	if err != nil || strings.Contains(req.contextText, sessionIssueNote) || strings.Contains(req.contextText, sessionBranchNote) {
		t.Fatalf("new conversation got stored notes: %v %s", err, req.contextText)
	}
	// The stored issue is re-validated against the current project every turn.
	if _, err = prepareStoredSessionTurn(t, h, owner, assistantRequest{SessionID: "peer-moved-issue", ProjectID: "workbench"}); err == nil || !strings.Contains(err.Error(), "需求不存在或不属于当前项目") {
		t.Fatalf("issue of another project accepted: %v", err)
	}
}

func TestWorkbenchContextResolvesEarlierAttachmentsToTheirAuthor(t *testing.T) {
	h, owner, peer := storedSessionGuardFixture(t)
	fromPeer, err := saveWorkbenchImage(peer.ID, "peer.txt", "text/plain", []byte("peer"), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	fromOwner, err := saveWorkbenchImage(owner.ID, "owner.txt", "text/plain", []byte("owner"), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	// The conversation is stored under the owner, but the first message was
	// written by the peer, whose upload lives in the peer's attachment folder.
	session := domain.AssistantSession{ID: "mixed-authors", ProjectID: "workbench", Messages: []domain.AssistantMessage{
		{Role: "user", Content: "看附件", AuthorID: peer.ID, Attachments: []domain.AssistantAttachment{fromPeer}},
		{Role: "assistant", Content: "好的"},
		{Role: "user", Content: "再看这个", AuthorID: owner.ID, Attachments: []domain.AssistantAttachment{fromOwner}},
	}}
	if err = h.repo.PutAssistantSession(owner.ID, session); err != nil {
		t.Fatal(err)
	}
	saved, _ := h.repo.AssistantSession(owner.ID, session.ID)
	if len(saved.Messages) != 3 || saved.Messages[0].AuthorID != peer.ID {
		t.Fatalf("unexpected stored transcript: %+v", saved.Messages)
	}
	for _, caller := range []domain.User{owner, peer} {
		messages := append(append([]assistantMessage(nil), saved.Messages...), assistantMessage{Role: "user", Content: "继续"})
		req, err := prepareStoredSessionTurn(t, h, caller, assistantRequest{SessionID: session.ID, ProjectID: "workbench", Messages: messages})
		if err != nil {
			t.Fatalf("caller %d: %v", caller.ID, err)
		}
		if req.attachmentOwners[fromPeer.ID] != peer.ID || req.attachmentOwners[fromOwner.ID] != owner.ID || !req.attachmentIDs[fromPeer.ID] || !req.attachmentIDs[fromOwner.ID] {
			t.Fatalf("caller %d resolved owners %+v ids %+v", caller.ID, req.attachmentOwners, req.attachmentIDs)
		}
	}
}
