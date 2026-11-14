package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestNormalizeScheduledReferencesLimitDedupAndTrim(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "ref-norm", Name: "Norm"})
	_ = st.PutIssue(domain.Issue{ID: "ref-norm-issue", ProjectID: "ref-norm", Title: "需求"})
	h := &assistantHandlers{deps: &deps{repo: st}}

	tooMany := make([]domain.AssistantReference, 13)
	for i := range tooMany {
		tooMany[i] = domain.AssistantReference{Type: "issue", ID: fmt.Sprintf("x-%d", i)}
	}
	if _, err := h.normalizeScheduledReferences("ref-norm", tooMany); err == nil || !strings.Contains(err.Error(), "12") {
		t.Fatalf("13 references should be rejected by the limit, err = %v", err)
	}
	if refs, err := h.normalizeScheduledReferences("ref-norm", nil); err != nil || refs != nil {
		t.Fatalf("empty references = %+v, %v", refs, err)
	}
	if _, err := h.normalizeScheduledReferences("ref-norm", []domain.AssistantReference{{Type: "issue", ID: "  "}}); err == nil {
		t.Fatal("blank reference ID should be rejected")
	}

	refs, err := h.normalizeScheduledReferences("ref-norm", []domain.AssistantReference{
		{Type: " issue ", ID: " ref-norm-issue ", Name: "伪造", ProjectID: "other"},
		{Type: "issue", ID: "ref-norm-issue"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != (domain.AssistantReference{Type: "issue", ID: "ref-norm-issue"}) {
		t.Fatalf("trimmed, deduplicated identity-only references = %+v", refs)
	}
}

func TestScheduledMessageReferencesResolveOwnerProject(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "ref-home", Name: "Home"})
	_ = st.PutProject(domain.Project{ID: "ref-lib", Name: "Library"})
	_ = st.PutRepository(domain.Repository{ID: "ref-repo", ProjectID: "ref-home", Name: "app"})
	_ = st.PutAgent(domain.AgentDefinition{ID: "ref-agent", ProjectID: "ref-lib", Name: "共享 Agent", Shared: true})
	_ = st.PutSkill(domain.SkillDefinition{ID: "ref-skill", ProjectID: "ref-lib", Name: "共享技能", Shared: true})
	_ = st.PutRun(domain.Run{ID: "ref-run", Status: "succeeded", WorkflowSnapshot: domain.WorkflowDefinition{Name: "巡检"}, ExecutionContext: domain.TelemetryContext{ProjectID: "ref-home"}})
	h := &assistantHandlers{deps: &deps{repo: st}}

	got, err := h.scheduledMessageReferences("ref-home", []domain.AssistantReference{
		{Type: "repository", ID: "ref-repo"},
		{Type: "run", ID: "ref-run"},
		{Type: "agent", ID: "ref-agent"},
		{Type: "skill", ID: "ref-skill"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.AssistantReference{
		{Type: "repository", ID: "ref-repo", Name: "app", ProjectID: "ref-home", ProjectName: "Home"},
		{Type: "run", ID: "ref-run", Name: "巡检 · ref-run", ProjectID: "ref-home", ProjectName: "Home"},
		{Type: "agent", ID: "ref-agent", Name: "共享 Agent", ProjectID: "ref-lib", ProjectName: "Library"},
		{Type: "skill", ID: "ref-skill", Name: "共享技能", ProjectID: "ref-lib", ProjectName: "Library"},
	}
	if len(got) != len(want) {
		t.Fatalf("references = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reference %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if _, err := h.scheduledMessageReferences("ref-home", []domain.AssistantReference{{Type: "repository", ID: "gone"}}); err == nil {
		t.Fatal("missing reference should fail message construction")
	}
}

func TestAssistantTaskReferenceSaveRequiresProjectAccess(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "ref-owner"})
	_ = st.PutUser(domain.User{LoginName: "ref-outsider"})
	owner, _ := st.UserByLoginName("ref-owner")
	outsider, _ := st.UserByLoginName("ref-outsider")
	_ = st.PutProject(domain.Project{ID: "private-ref-project", Name: "Private", OwnerID: owner.ID})
	_ = st.PutIssue(domain.Issue{ID: "private-ref-issue", ProjectID: "private-ref-project", Title: "secret issue"})
	h := &assistantHandlers{deps: &deps{repo: st}}

	c, rec := cvAContext(t, http.MethodPut, "/api/assistant/tasks", `{"name":"job","prompt":"inspect","projectId":"private-ref-project","intervalSec":60,"references":[{"type":"issue","id":"private-ref-issue"}]}`, &outsider)
	h.putTask(c)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outsider save = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "不可用") || strings.Contains(rec.Body.String(), "不存在") {
		t.Fatalf("permission response reveals reference existence: %s", rec.Body.String())
	}
}
