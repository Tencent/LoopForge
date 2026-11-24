package httpapi

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestCreateRequirementWithAttachments(t *testing.T) {
	h, user := workbenchFixture(t)
	file, err := saveWorkbenchImage(user.ID, "需求.zip", "application/zip", []byte("archive"), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ProjectID: "workbench", Attachments: []domain.IssueAttachment{{AssistantAttachment: domain.AssistantAttachment{ID: file.ID, Name: "forged"}, UploadedBy: 999}}}
	c, _ := workbenchContext(user, "POST", "/api/issues", bytes.NewBuffer(nil))
	ih := issuesHandlers{deps: h.deps}
	if err := ih.canonicalIssueAttachments(c, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Attachments[0].Name != file.Name || issue.Attachments[0].UploadedBy != user.ID {
		t.Fatalf("not canonical: %+v", issue.Attachments)
	}
	foreign := issue
	foreign.ProjectID = "platform"
	if err := ih.canonicalIssueAttachments(c, &foreign); err == nil {
		t.Fatal("cross-project attachment accepted")
	}
	other := user
	other.ID++
	c, _ = workbenchContext(other, "POST", "/api/issues", bytes.NewBuffer(nil))
	if err := ih.canonicalIssueAttachments(c, &issue); err == nil {
		t.Fatal("other uploader accepted")
	}
}

func TestRequirementFilesReachConversationAndSurviveSweep(t *testing.T) {
	h, user := workbenchFixture(t)
	file, err := saveWorkbenchImage(user.ID, "spec.md", "text/plain", []byte("acceptance requirements"), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "with-attachments", ProjectID: "workbench", Title: "Requirement", Attachments: []domain.IssueAttachment{{AssistantAttachment: file, UploadedBy: user.ID}}}
	if err := h.repo.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	req := assistantRequest{ProjectID: "workbench", Messages: []assistantMessage{{Role: "user", Content: "clarify", References: []domain.AssistantReference{{Type: "issue", ID: issue.ID}}}}}
	c, _ := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &req, user); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := writeConversationAttachments(root, req); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, conversationAttachmentPath(file.ID, file.Name)))
	if err != nil || string(data) != "acceptance requirements" {
		t.Fatalf("not materialized: %v %s", err, data)
	}
	if !strings.Contains(conversationAttachmentNote(root, req, "codex"), file.Name) {
		t.Fatal("model missing attachment note")
	}
	h.queueConversationAttachments(domain.AssistantSession{UserID: user.ID, Messages: []domain.AssistantMessage{{Role: "user", Attachments: []domain.AssistantAttachment{file}}}})
	h.sweepQueuedAttachments()
	if !testAttachmentExists(user.ID, file.ID) {
		t.Fatal("requirement attachment swept")
	}
	c, rec := workbenchContext(user, "GET", "/api/assistant/attachments/"+file.ID+"?issueId="+issue.ID, bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: file.ID}}
	h.downloadAttachment(c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "acceptance requirements") {
		t.Fatalf("download: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequirementAttachmentsSharedConversationAndSync(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "attachment-member"}); err != nil {
		t.Fatal(err)
	}
	member, _ := h.repo.UserByLoginName("attachment-member")
	project, _ := h.repo.Project("workbench")
	project.Members = []domain.ProjectMember{{UserID: member.ID, Role: "member"}}
	h.repo.PutProject(project)
	image, err := saveWorkbenchImage(owner.ID, "design.png", "image/png", conversationTestPNG(t), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "shared-evidence", ProjectID: project.ID, Title: "Image reference", Attachments: []domain.IssueAttachment{{AssistantAttachment: image, UploadedBy: owner.ID}}}
	if err := h.repo.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutSyncedIssue(domain.Issue{ID: issue.ID, ProjectID: project.ID, Title: "Tracker refresh"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := h.repo.Issue(issue.ID)
	if len(saved.Attachments) != 1 {
		t.Fatal("sync dropped attachments")
	}
	if err := h.repo.PutAssistantSession(member.ID, domain.AssistantSession{ID: "member-followup", ProjectID: project.ID, IssueID: issue.ID}); err != nil {
		t.Fatal(err)
	}
	req := assistantRequest{ProjectID: project.ID, SessionID: "member-followup", Messages: []assistantMessage{{Role: "user", Content: "continue clarifying"}}}
	c, _ := workbenchContext(member, "POST", "/api/assistant/generations", bytes.NewBuffer(nil))
	if err := h.prepareWorkbenchContext(c, &req, member); err != nil {
		t.Fatal(err)
	}
	if req.attachmentOwners[image.ID] != owner.ID || !conversationHasImageAttachments(req) {
		t.Fatal("shared image not routed from original uploader")
	}
	root := t.TempDir()
	if err := writeConversationAttachments(root, req); err != nil {
		t.Fatal(err)
	}
	if len(conversationTurnImagePaths(root, req)) != 1 {
		t.Fatal("image input lost on followup")
	}
	c, rec := workbenchContext(member, "GET", "/api/assistant/attachments/"+image.ID+"?issueId="+issue.ID, bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: image.ID}}
	h.downloadAttachment(c)
	if rec.Code != 200 {
		t.Fatalf("member download: %d %s", rec.Code, rec.Body.String())
	}
	outsider := member
	outsider.ID += 10000
	c, rec = workbenchContext(outsider, "GET", "/api/assistant/attachments/"+image.ID+"?issueId="+issue.ID, bytes.NewBuffer(nil))
	c.Params = gin.Params{{Key: "id", Value: image.ID}}
	h.downloadAttachment(c)
	if rec.Code != 403 {
		t.Fatalf("nonmember download: %d", rec.Code)
	}
}

func TestAttachmentOnlyRequirementDerivesTitleAndDescription(t *testing.T) {
	h, user := workbenchFixture(t)
	a, err := saveWorkbenchImage(user.ID, "需求说明.md", "text/markdown", []byte("# 需求\n实现文档中的功能"), "workbench")
	if err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ProjectID: "workbench", Attachments: []domain.IssueAttachment{{AssistantAttachment: a}}}
	c, _ := workbenchContext(user, "POST", "/api/issues", bytes.NewBuffer(nil))
	if err := (&issuesHandlers{deps: h.deps}).canonicalIssueAttachments(c, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Title != "需求说明.md" || !strings.Contains(issue.Description, "附件正文") {
		t.Fatalf("attachment-only requirement missing context: %+v", issue)
	}
}

func TestRunnerRequirementDownloadRequiresCurrentTaskLease(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, handler := clarifyTestServer(t)
	a, err := saveWorkbenchImage(42, "requirements.pdf", "application/pdf", []byte{0, 255, 128, 10}, "platform")
	if err != nil {
		t.Fatal(err)
	}
	st.PutIssue(domain.Issue{ID: "download-issue", ProjectID: "platform", Attachments: []domain.IssueAttachment{{AssistantAttachment: a, UploadedBy: 42}}})
	st.PutRun(domain.Run{ID: "download-run", ExecutionContext: domain.TelemetryContext{ProjectID: "platform", IssueID: "download-issue"}})
	runnerID, session := registerTestRunner(t, handler)
	_, otherSession := registerTestRunner(t, handler)
	until := time.Now().Add(time.Minute)
	task := domain.RunnerTask{ID: "download-task", Provider: "mock", RunnerID: runnerID, LeaseOwner: runnerID, LeaseUntil: &until, Attempt: 2, Status: domain.RunnerTaskRunning, RunID: "download-run", Invocation: &domain.RunnerInvocation{RequirementAttachments: []domain.IssueAttachment{{AssistantAttachment: a, UploadedBy: 42}}}}
	if err := st.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	path := "/runners/tasks/" + task.ID + "/attachments/" + a.ID
	for _, tc := range []struct {
		token, attempt string
		status         int
	}{{session, "2", 200}, {otherSession, "2", 409}, {session, "1", 409}} {
		rec := requestWithHeaders(t, handler, "GET", path, nil, map[string]string{"Authorization": "Bearer " + tc.token, "X-Task-Attempt": tc.attempt})
		if rec.Code != tc.status {
			t.Fatalf("status=%d want=%d %s", rec.Code, tc.status, rec.Body.String())
		}
		if rec.Code == 200 && !bytes.Equal(rec.Body.Bytes(), []byte{0, 255, 128, 10}) {
			t.Fatal("download corrupted")
		}
	}
	rec := requestWithHeaders(t, handler, "GET", path+"different", nil, map[string]string{"Authorization": "Bearer " + session, "X-Task-Attempt": "2"})
	if rec.Code != 404 {
		t.Fatalf("other attachment: %d", rec.Code)
	}
	until = time.Now().Add(-time.Minute)
	task.LeaseUntil = &until
	st.PutRunnerTask(task)
	rec = requestWithHeaders(t, handler, "GET", path, nil, map[string]string{"Authorization": "Bearer " + session, "X-Task-Attempt": "2"})
	if rec.Code != 409 {
		t.Fatalf("expired lease: %d", rec.Code)
	}
}
