package httpapi

import (
	"fmt"
	"log"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// conversationRequirement is the requirement Main Bot identified in an
// ordinary conversation that changes code directly, without an SOP.
type conversationRequirement struct {
	Title       string
	Description string
}

func parseConversationRequirement(args map[string]any) (conversationRequirement, error) {
	raw, ok := args["requirement"]
	if !ok || raw == nil {
		return conversationRequirement{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return conversationRequirement{}, fmt.Errorf("requirement 必须是包含 title 的对象")
	}
	title, _ := obj["title"].(string)
	description, _ := obj["description"].(string)
	req := conversationRequirement{Title: strings.TrimSpace(title), Description: strings.TrimSpace(description)}
	if req.Title == "" || len([]rune(req.Title)) > 200 {
		return conversationRequirement{}, fmt.Errorf("requirement.title 必填，最多 200 字")
	}
	if len([]rune(req.Description)) > 20000 {
		return conversationRequirement{}, fmt.Errorf("requirement.description 最多 20000 字")
	}
	return req, nil
}

// openConversationIssue returns the requirement in the project that a
// conversation workspace may keep working on.
func (h *agentToolGateway) openConversationIssue(projectID, issueID string) (domain.Issue, bool) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return domain.Issue{}, false
	}
	issue, ok := h.repo.Issue(issueID)
	if !ok || issue.ProjectID != projectID || issue.Completed() || strings.EqualFold(issue.Status, "cancelled") {
		return domain.Issue{}, false
	}
	return issue, true
}

// linkConversationRequirement ties a conversation workspace to the
// requirement it works on, so it shows up in the requirement list, delivery
// and analytics instead of as an anonymous workspace. A workspace keeps the
// requirement it already has. Otherwise an explicitly identified requirement
// is created; with preferSession the conversation's own open requirement is
// reused first. Without a title nothing is created. SOP runs are never
// relinked: their requirement is set when they are dispatched.
func (h *agentToolGateway) linkConversationRequirement(s *AgentToolSession, run domain.Run, req conversationRequirement, preferSession bool) (domain.Issue, bool, error) {
	if !run.IsWorkspace() || run.ExecutionContext.ProjectID != s.ProjectID {
		issue, _ := h.repo.Issue(run.ExecutionContext.IssueID)
		return issue, false, nil
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if current, ok := h.repo.RunState(run.ID); ok {
		run = current
	}
	if issue, ok := h.repo.Issue(run.ExecutionContext.IssueID); ok && run.ExecutionContext.IssueID != "" {
		return issue, false, nil
	}
	var conv domain.AssistantSession
	if s.ChatSessionID != "" {
		conv, _ = h.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID)
	}
	if req.Title == "" || preferSession {
		if issue, ok := h.openConversationIssue(s.ProjectID, conv.IssueID); ok {
			if linked, err := h.repo.LinkConversationRunIssue(run.ID, issue.ID); err != nil || !linked {
				return issue, false, err
			}
			return issue, false, nil
		}
	}
	if req.Title == "" {
		return domain.Issue{}, false, nil
	}
	issue := conversationRequirementIssue(s, run, conv, req)
	if err := h.repo.PutIssue(issue); err != nil {
		return domain.Issue{}, false, err
	}
	if _, err := h.repo.LinkConversationRunIssue(run.ID, issue.ID); err != nil {
		return issue, true, err
	}
	if s.ChatSessionID != "" && s.ChatOwnerID != 0 {
		// A new conversation is saved only when its first turn ends;
		// attributeConversationUsage links it then.
		_, _ = h.repo.LinkAssistantSessionIssue(s.ChatOwnerID, s.ChatSessionID, issue.ID)
	}
	stored, ok := h.repo.Issue(issue.ID)
	if !ok {
		stored = issue
	}
	return stored, true, nil
}

// conversationRequirementIssue builds the requirement a conversation
// workspace creates for req: the default status, source and assignee, the
// run's repositories, and the create operation naming the conversation.
func conversationRequirementIssue(s *AgentToolSession, run domain.Run, conv domain.AssistantSession, req conversationRequirement) domain.Issue {
	issue := domain.Issue{
		ID:                    fmt.Sprintf("issue-%d", time.Now().UnixNano()),
		ProjectID:             s.ProjectID,
		Title:                 truncateRunes(req.Title, 200),
		Description:           req.Description,
		Status:                "in_progress",
		Priority:              "medium",
		Kind:                  "requirement",
		Source:                "local",
		AutomationState:       "ignored",
		AssigneeUserID:        s.UserID,
		ConversationSessionID: s.ChatSessionID,
	}
	for _, rc := range run.RepositoryContexts() {
		if rc.RepositoryID != "" {
			issue.RepositoryIDs = append(issue.RepositoryIDs, rc.RepositoryID)
		}
	}
	if len(issue.RepositoryIDs) > 0 {
		issue.RepositoryID = issue.RepositoryIDs[0]
	}
	actor := strings.TrimSpace(s.User.ChineseName)
	if actor == "" {
		actor = s.User.LoginName
	}
	detail := "对话中识别为需求并创建，在对话工作区 " + run.ID + " 直接改代码"
	if title := strings.TrimSpace(conv.Title); title != "" {
		detail = "对话「" + truncateRunes(title, 40) + "」中识别为需求并创建，在对话工作区 " + run.ID + " 直接改代码"
	}
	issue.AppendOperation(domain.IssueOperation{Actor: actor, ActorUserID: s.UserID, Action: "create", Detail: detail})
	return issue
}

// recordConversationMRs registers the MRs a conversation workspace submitted
// as the requirement's delivery MRs. MR polling then marks the requirement
// delivered once they merge, and analytics counts them like SOP deliveries.
func (h *agentToolGateway) recordConversationMRs(issueID string, results []workspaceMRResult, repos []domain.RepositoryContext) error {
	if strings.TrimSpace(issueID) == "" {
		return nil
	}
	mrs := []domain.DeliveryMR{}
	direct := 0
	for _, r := range results {
		project, iid, _, ok := domain.ParseGongfengMR(r.MRURL)
		if r.MRURL == "" || !ok {
			if r.Direct {
				// A direct push to a shared branch delivers work without an MR, so
				// the requirement still needs review; only the MR list stays empty.
				direct++
			}
			continue
		}
		mr := domain.DeliveryMR{Repository: r.Repository, Project: project, IID: iid, URL: r.MRURL, State: "opened", TargetBranch: r.TargetBranch}
		for _, rc := range repos {
			if rc.Name == r.Repository {
				mr.RepositoryID = rc.RepositoryID
			}
		}
		if err := h.repo.EnsureMRDeliveryState(project, iid, r.MRURL, issueID); err != nil {
			return err
		}
		mrs = append(mrs, mr)
	}
	if len(mrs) == 0 && direct == 0 {
		return nil
	}
	_, err := h.repo.PatchIssue(issueID, func(issue *domain.Issue) error {
		list := issue.DeliveryMRList()
		for _, mr := range mrs {
			if !issue.HasDeliveryMR(mr.URL) {
				list = append(list, mr)
			}
		}
		issue.SetDeliveryMRs(list)
		if !issue.Completed() && !strings.EqualFold(issue.Status, "in_review") {
			issue.Status = "in_review"
		}
		return nil
	})
	return err
}

// attributeConversationUsage runs after a turn is saved. It adds the turn's
// model usage to the conversation workspace the turn worked in, and links the
// conversation to that workspace's requirement: a conversation's first turn
// is saved only when it ends, after its tools created the requirement. SOP
// runs a conversation borrowed keep only their own usage.
func attributeConversationUsage(d *deps, s *AgentToolSession, model string, usage *domain.TokenUsage) {
	if s == nil {
		return
	}
	runID := s.conversationWorkspace()
	if runID == "" {
		return
	}
	if usage != nil {
		if err := d.repo.AddConversationRunUsage(runID, model, *usage); err != nil {
			log.Printf("add conversation usage to workspace %s: %v", runID, err)
		}
	}
	if s.ChatSessionID == "" || s.ChatOwnerID == 0 {
		return
	}
	run, ok := d.repo.RunState(runID)
	if !ok || !run.IsWorkspace() || run.ExecutionContext.ProjectID != s.ProjectID || strings.TrimSpace(run.ExecutionContext.IssueID) == "" {
		return
	}
	if conv, ok := d.repo.AssistantSession(s.ChatOwnerID, s.ChatSessionID); !ok || conv.IssueID != "" {
		return
	}
	if _, err := d.repo.LinkAssistantSessionIssue(s.ChatOwnerID, s.ChatSessionID, run.ExecutionContext.IssueID); err != nil {
		log.Printf("link conversation %s to its requirement %s: %v", s.ChatSessionID, run.ExecutionContext.IssueID, err)
	}
}
