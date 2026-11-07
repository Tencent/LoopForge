package engine

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestResolveCodeBuddyAPIKeyFromUserOnly(t *testing.T) {
	t.Setenv(codeBuddyAPIKeyEnv, "ck_host")
	t.Setenv(codeBuddyInternetEnv, "ioa")
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user", CodeBuddyInternetEnvironment: "private-net"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{ID: 22, LoginName: "newcomer"}); err != nil {
		t.Fatal(err)
	}
	if got := resolveCodeBuddyCredential(s, 21); got.APIKey != "ck_user" || got.InternetEnvironment != "private-net" || got.Sponsored {
		t.Fatalf("typed user credential = %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "" || got.Sponsored {
		t.Fatalf("user without key and no sponsor must not use host env, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 999); got.APIKey != "" || got.InternetEnvironment != "" {
		t.Fatalf("missing user must not use host env, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(nil, 21); got.APIKey != "" {
		t.Fatalf("nil store = %+v", got)
	}
}

// A user without a key of their own runs SOPs on the sponsor's key, marked
// sponsored so the engine pins the sponsored model.
func TestResolveCodeBuddyCredentialFallsBackToSponsor(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, user := range []domain.User{
		{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user"},
		{ID: 22, LoginName: "newcomer", CodeBuddyInternetEnvironment: "ioa"},
		{ID: 23, LoginName: "sponsor", CodeBuddyAPIKey: "ck_sponsor", CodeBuddyInternetEnvironment: "internal"},
	} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "sponsor")
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "ck_sponsor" || got.InternetEnvironment != "internal" || !got.Sponsored {
		t.Fatalf("newcomer credential = %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 21); got.APIKey != "ck_user" || got.Sponsored {
		t.Fatalf("own key must win over sponsor, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 23); got.APIKey != "ck_sponsor" || got.Sponsored {
		t.Fatalf("sponsor runs on its own key, got %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 999); got.APIKey != "" {
		t.Fatalf("unknown user must not borrow the sponsor key, got %+v", got)
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "nobody")
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "" || got.Sponsored {
		t.Fatalf("missing sponsor account = %+v", got)
	}
}

// A sponsored CodeBuddy node runs the sponsored model whatever its role
// configures, says so on the node, and notes it once on the run.
func TestSponsoredExecutorPinsModelAndExplains(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wf := domain.WorkflowDefinition{ID: "sponsored", Name: "Sponsored", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
	if err = st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(st, runtime.NewManager(t.TempDir()))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "waiting")
	defer func() { _ = e.Cancel(run.ID) }()
	var observed []domain.TelemetryEvent
	sink := func(event domain.TelemetryEvent) { observed = append(observed, event) }
	for range 2 {
		got := e.sponsoredExecutor(run.ID, domain.ExecutorDefinition{ID: "codebuddy", Kind: "codebuddy", Model: "claude-opus-5", Effort: "high"}, sink)
		if got.Model != repo.SponsoredCodeBuddyModel || got.Effort != "high" || got.Kind != "codebuddy" {
			t.Fatalf("executor = %+v", got)
		}
	}
	if len(observed) != 2 || observed[0].Model != repo.SponsoredCodeBuddyModel || !strings.Contains(observed[0].Message, "共享模型") || !strings.Contains(observed[0].Message, "claude-opus-5") {
		t.Fatalf("node notice = %+v", observed)
	}
	current, _ := st.RunState(run.ID)
	notes := 0
	for _, event := range current.Events {
		if event.Type == sponsoredRunEvent {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("run notes = %d, events %+v", notes, current.Events)
	}
}
