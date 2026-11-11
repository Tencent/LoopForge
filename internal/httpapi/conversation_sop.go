package httpapi

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"

	"agentflow/internal/domain"
)

// chatSessionHeader names the conversation a browser-confirmed tool call was
// drafted in. It only links the dispatched issue to that conversation; access
// is checked by the project operation that carries it.
const chatSessionHeader = "X-AgentFlow-Chat-Session"

type conversationSessionKey struct{}

func withConversationSession(ctx context.Context, sessionID string) context.Context {
	if strings.TrimSpace(sessionID) == "" {
		return ctx
	}
	return context.WithValue(ctx, conversationSessionKey{}, strings.TrimSpace(sessionID))
}

// requestConversation returns the ordinary conversation of
// projectID a request was made from.
func requestConversation(d *deps, r *http.Request, userID int64, projectID string) (domain.AssistantSession, bool) {
	id, _ := r.Context().Value(conversationSessionKey{}).(string)
	if id == "" {
		id = strings.TrimSpace(r.Header.Get(chatSessionHeader))
	}
	if id == "" || userID == 0 {
		return domain.AssistantSession{}, false
	}
	session, ok := d.repo.AssistantSession(assistantStorageOwner(d.repo, id, userID), id)
	if !ok || session.Shared || session.RunID != "" || session.ProjectID != projectID {
		return domain.AssistantSession{}, false
	}
	return session, true
}

// linkDispatchingConversation links the conversation an issue was dispatched
// from to that issue, so later turns offer Main Bot the issue's SOP workspace.
func linkDispatchingConversation(d *deps, r *http.Request, userID int64, issue domain.Issue) {
	session, ok := requestConversation(d, r, userID, issue.ProjectID)
	if !ok || session.IssueID != "" {
		return
	}
	if _, err := d.repo.LinkAssistantSessionIssue(session.UserID, session.ID, issue.ID); err != nil {
		log.Printf("link conversation %s to dispatched issue %s: %v", session.ID, issue.ID, err)
	}
}

// borrowableSOPRun reports whether a top-level SOP run of projectID can lend
// its workspace to an ordinary conversation.
func borrowableSOPRun(run domain.Run, projectID string) bool {
	return projectID != "" && run.ID != "" && !run.IsWorkspace() && !run.HistoricalGroup() && run.ExecutionContext.ParentRunID == "" && run.ExecutionContext.ProjectID == projectID
}

// ownWorkspaceRunIDs lists the workspaces conv opened itself. A workspace run
// bound before the list existed was necessarily opened by the conversation.
func ownWorkspaceRunIDs(conv domain.AssistantSession) []string {
	ids := append([]string{}, conv.OwnWorkspaceRunIDs...)
	if conv.WorkspaceRunID != "" && !slices.Contains(ids, conv.WorkspaceRunID) {
		ids = append(ids, conv.WorkspaceRunID)
	}
	return ids
}

// conversationMayBind reports whether conv may work in run: a workspace it
// opened that is still usable, or a top-level SOP run of its project.
func conversationMayBind(conv domain.AssistantSession, run domain.Run, projectID string) bool {
	if run.IsWorkspace() {
		return usableConversationWorkspace(run, projectID) && slices.Contains(ownWorkspaceRunIDs(conv), run.ID)
	}
	return borrowableSOPRun(run, projectID)
}

// loadTurnWorkspace starts a conversation turn in the workspace the
// conversation is bound to; Main Bot switches with workbench_workspace_open.
func loadTurnWorkspace(d *deps, s *AgentToolSession, conv domain.AssistantSession) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	s.workspaceRunID = ""
	if run, ok := d.repo.RunState(conv.WorkspaceRunID); ok && conversationMayBind(conv, run, s.ProjectID) {
		s.workspaceRunID = run.ID
	}
}

func lastMessageReferences(messages []assistantMessage) []domain.AssistantReference {
	if len(messages) == 0 {
		return nil
	}
	return messages[len(messages)-1].References
}

// turnIssues returns the issues of projectID the message mentions, then the
// conversation's linked issue, without duplicates.
func turnIssues(d *deps, projectID, linkedIssueID string, refs []domain.AssistantReference) []domain.Issue {
	ids := []string{}
	for _, ref := range refs {
		if ref.Type == "issue" {
			ids = append(ids, ref.ID)
		}
	}
	if linkedIssueID != "" {
		ids = append(ids, linkedIssueID)
	}
	issues := []domain.Issue{}
	seen := map[string]bool{}
	for _, id := range ids {
		if issue, ok := d.repo.Issue(id); ok && !seen[id] && issue.ProjectID == projectID {
			seen[id] = true
			issues = append(issues, issue)
		}
	}
	return issues
}

// issueSOPRun returns the newest SOP run of an issue that can lend its
// workspace to a conversation of projectID.
func issueSOPRun(d *deps, projectID, issueID string) (domain.Run, bool) {
	run, ok := d.repo.LatestIssueSOPRun(issueID)
	return run, ok && borrowableSOPRun(run, projectID)
}
