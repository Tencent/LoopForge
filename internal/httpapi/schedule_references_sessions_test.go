package httpapi

import (
	"context"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestScheduleFixedSessionContinuesAfterRemovingAndDeletingIssueReference(t *testing.T) {
	stubCLIModelDiscovery(t)
	discoverCLIModels = func(context.Context, string, string) []string { return []string{"glm-5.1"} }
	h, user, job := scheduleSessionFixture(t)
	job.SessionMode = domain.ScheduleSessionFixed
	job.AssistantModel = "glm-5.1"
	issue := domain.Issue{ID: "removed-session-reference", ProjectID: job.ProjectID, Title: "Referenced issue"}
	if err := h.repo.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	job.References = []domain.AssistantReference{{Type: "issue", ID: issue.ID}}
	calls := 0
	h.generate = func(_ context.Context, prompt string, _ domain.User) (string, error) {
		calls++
		if calls == 1 && !strings.Contains(prompt, issue.Title) {
			t.Fatal("first execution did not resolve the issue reference")
		}
		if calls > 1 && strings.Contains(prompt, "当前会话关联的主需求（每轮从平台读取的最新数据）") {
			t.Fatal("removed reference was injected as the session's linked issue")
		}
		return "done", nil
	}
	_, sessionID, err := h.runScheduleJobPrompt(job)
	if err != nil || sessionID == "" {
		t.Fatalf("first execution: session=%q err=%v", sessionID, err)
	}
	job.FixedSessionID = sessionID
	job.References = nil
	if err := h.repo.DeleteIssue(issue.ID); err != nil {
		t.Fatal(err)
	}
	_, nextSessionID, err := h.runScheduleJobPrompt(job)
	if err != nil || nextSessionID != sessionID {
		t.Fatalf("execution after removing and deleting the reference: session=%q want=%q err=%v", nextSessionID, sessionID, err)
	}
	session, ok := h.repo.AssistantSession(user.ID, sessionID)
	if !ok || session.IssueID != "" || len(session.Messages) != 4 || calls != 2 {
		t.Fatalf("fixed session: found=%v issue=%q messages=%d calls=%d", ok, session.IssueID, len(session.Messages), calls)
	}
	if refs := session.Messages[2].References; len(refs) != 0 {
		t.Fatalf("removed reference persisted in the new message: %+v", refs)
	}
}

func TestScheduleReferencesWithSessionModesAndSharedKey(t *testing.T) {
	stubCLIModelDiscovery(t)
	discoverCLIModels = func(context.Context, string, string) []string { return []string{"glm-5.1"} }
	for _, mode := range []string{"", domain.ScheduleSessionFixed} {
		for _, shared := range []bool{false, true} {
			name := "new/personal"
			if mode != "" {
				name = "fixed/personal"
			}
			if shared {
				name = strings.ReplaceAll(name, "personal", "shared")
			}
			t.Run(name, func(t *testing.T) {
				h, user, job := scheduleSessionFixture(t)
				job.SessionMode = mode
				job.AssistantModel = "glm-5.1"
				if shared {
					if err := h.repo.PutUser(domain.User{LoginName: "reference-member"}); err != nil {
						t.Fatal(err)
					}
					user, _ = h.repo.UserByLoginName("reference-member")
					project, _ := h.repo.Project(job.ProjectID)
					project.Members = []domain.ProjectMember{{UserID: user.ID, Role: "member"}}
					if err := h.repo.PutProject(project); err != nil {
						t.Fatal(err)
					}
					if err := h.repo.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "shared-test-key", Models: []string{"glm-5.1"}}, 0); err != nil {
						t.Fatal(err)
					}
				}
				job.RunAsUserID = user.ID
				issue := domain.Issue{ID: "session-reference", ProjectID: job.ProjectID, Title: "Original asset title"}
				job.References = []domain.AssistantReference{{Type: "issue", ID: issue.ID, Name: "Forged asset title"}}
				calls := 0
				h.generate = func(_ context.Context, prompt string, runner domain.User) (string, error) {
					calls++
					if runner.ID != user.ID || (shared && runner.CodeBuddyAPIKey != "shared-test-key") {
						t.Fatalf("unexpected execution identity: user=%d", runner.ID)
					}
					if !strings.Contains(prompt, issue.Title) || strings.Contains(prompt, "Forged asset title") {
						t.Fatal("execution did not resolve the current asset")
					}
					return "done", nil
				}
				var first string
				for run := 0; run < 2; run++ {
					if err := h.repo.PutIssue(issue); err != nil {
						t.Fatal(err)
					}
					var opened string
					_, sessionID, err := h.runScheduleJobPromptReporting(job, func(id string) { opened = id })
					if err != nil || sessionID == "" || opened != sessionID {
						t.Fatalf("run %d: session=%q opened=%q err=%v", run, sessionID, opened, err)
					}
					if run == 0 {
						first = sessionID
					} else if (sessionID == first) != job.UsesFixedSession() {
						t.Fatalf("session reuse does not match mode %q", mode)
					}
					conversation, ok := h.repo.AssistantSession(user.ID, sessionID)
					wantMessages := 2
					if run == 1 && job.UsesFixedSession() {
						wantMessages = 4
					}
					if !ok || len(conversation.Messages) != wantMessages {
						t.Fatalf("run %d: found=%v messages=%d", run, ok, len(conversation.Messages))
					}
					refs := conversation.Messages[wantMessages-2].References
					if len(refs) != 1 || refs[0].ID != issue.ID || refs[0].Name != issue.Title {
						t.Fatalf("run %d: message references=%+v", run, refs)
					}
					job.FixedSessionID = sessionID
					issue.Title = "Updated asset title"
				}
				job.References[0].ID = "missing-asset"
				opened := false
				_, id, err := h.runScheduleJobPromptReporting(job, func(string) { opened = true })
				if err == nil || id != "" || opened || calls != 2 {
					t.Fatalf("missing reference reached a turn: id=%q opened=%v calls=%d err=%v", id, opened, calls, err)
				}
			})
		}
	}
}

func TestScheduleRejectsInvalidReferencesBeforeOpeningConversation(t *testing.T) {
	stubCLIModelDiscovery(t)
	discoverCLIModels = func(context.Context, string, string) []string { return []string{"glm-5.1"} }
	h, _, job := scheduleSessionFixture(t)
	job.AssistantModel = "glm-5.1"
	h.generate = func(context.Context, string, domain.User) (string, error) {
		t.Fatal("unsupported reference reached the model")
		return "", nil
	}
	for _, typ := range []string{"element", "unknown"} {
		job.References = []domain.AssistantReference{{Type: typ, ID: "legacy-reference"}}
		opened := false
		_, sessionID, err := h.runScheduleJobPromptReporting(job, func(string) { opened = true })
		if err == nil || opened || sessionID != "" {
			t.Fatalf("unsupported %s: opened=%v session=%q err=%v", typ, opened, sessionID, err)
		}
	}
}
