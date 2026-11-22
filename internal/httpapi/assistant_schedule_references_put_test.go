package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func scheduledRefOwnerFixture(t *testing.T, login string) (*assistantHandlers, domain.User) {
	t.Helper()
	st := cvAStore(t)
	if err := st.PutUser(domain.User{LoginName: login, CodeBuddyAPIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName(login)
	if err := st.PutProject(domain.Project{ID: "ref-put-home", Name: "Home", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "ref-put-issue", ProjectID: "ref-put-home", Title: "Home Issue"}); err != nil {
		t.Fatal(err)
	}
	return &assistantHandlers{deps: &deps{repo: st}}, user
}

func TestPutAssistantTaskStoresNormalizedReferences(t *testing.T) {
	h, user := scheduledRefOwnerFixture(t, "ref-put-owner")

	c, rec := cvAContext(t, http.MethodPut, "/api/assistant/tasks", `{"id":"ref-job","name":"job","prompt":"do it","projectId":"ref-put-home","intervalSec":60,"references":[{"type":" issue ","id":"ref-put-issue","name":"forged","projectId":"elsewhere"},{"type":"issue","id":"ref-put-issue"}]}`, &user)
	h.putTask(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d %s", rec.Code, rec.Body.String())
	}
	stored, err := loadAssistantTask(h.repo, user.ID, "ref-job")
	if err != nil || len(stored.References) != 1 || stored.References[0] != (domain.AssistantReference{Type: "issue", ID: "ref-put-issue"}) {
		t.Fatalf("stored references = %+v err=%v", stored.References, err)
	}

	c, rec = cvAContext(t, http.MethodPut, "/api/assistant/tasks", `{"id":"ref-bad","name":"job","prompt":"do it","projectId":"ref-put-home","intervalSec":60,"references":[{"type":"element","id":"x"}]}`, &user)
	h.putTask(c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "无效") {
		t.Fatalf("invalid reference put = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := loadAssistantTask(h.repo, user.ID, "ref-bad"); err == nil {
		t.Fatal("task with an invalid reference was saved")
	}
}

func TestScheduledTaskWithMissingReferenceFailsBeforeTurn(t *testing.T) {
	h, user := scheduledRefOwnerFixture(t, "ref-put-runner")
	called := false
	h.generate = func(context.Context, string, domain.User) (string, error) {
		called = true
		return "", nil
	}
	task := assistantTask{ID: "ref-missing", Name: "巡检", Prompt: "检查", ProjectID: "ref-put-home", IntervalSec: 60, Enabled: true,
		References: []domain.AssistantReference{{Type: "issue", ID: "deleted-issue"}}}
	if err := h.executeAssistantTask(context.Background(), user, &task); err == nil {
		t.Fatal("missing reference did not fail the run")
	}
	if called || task.LastStatus != "error" || !strings.Contains(task.LastError, "deleted-issue") || task.SessionID != "" {
		t.Fatalf("task after failed run = %+v called=%v", task, called)
	}
}
