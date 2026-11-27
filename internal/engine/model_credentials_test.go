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

// A user without a key of their own runs LOOPs on the sponsor's key, marked
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
	if got := resolveCodeBuddyCredential(s, 22); len(got.SponsoredModels) != 1 || got.SponsoredModels[0] != repo.SponsoredCodeBuddyModel {
		t.Fatalf("legacy sponsor models = %+v", got)
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "nobody")
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "" || got.Sponsored {
		t.Fatalf("missing sponsor account = %+v", got)
	}
}

// The key configured under 平台管理 wins over the legacy sponsor account and
// carries the administrator's model list.
func TestResolveCodeBuddyCredentialUsesPlatformSharedKey(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, user := range []domain.User{
		{ID: 21, LoginName: "owner", CodeBuddyAPIKey: "ck_user"},
		{ID: 22, LoginName: "newcomer"},
		{ID: 23, LoginName: "sponsor", CodeBuddyAPIKey: "ck_sponsor"},
	} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "sponsor")
	if err := s.SetPlatformCodeBuddyKey(repo.PlatformCodeBuddyKeyUpdate{APIKey: "ck_platform", InternetEnvironment: "internal", Models: []string{"glm-5.1", "kimi-k2.6"}}, 21); err != nil {
		t.Fatal(err)
	}
	got := resolveCodeBuddyCredential(s, 22)
	if got.APIKey != "ck_platform" || got.InternetEnvironment != "internal" || !got.Sponsored || strings.Join(got.SponsoredModels, ",") != "glm-5.1,kimi-k2.6" {
		t.Fatalf("platform shared credential = %+v", got)
	}
	if got := resolveCodeBuddyCredential(s, 23); got.APIKey != "ck_sponsor" || got.Sponsored {
		t.Fatalf("a user with a key keeps it, got %+v", got)
	}
	if err := s.ClearPlatformCodeBuddyKey(); err != nil {
		t.Fatal(err)
	}
	if got := resolveCodeBuddyCredential(s, 22); got.APIKey != "ck_sponsor" || !got.Sponsored {
		t.Fatalf("clearing the platform key falls back to the legacy sponsor, got %+v", got)
	}
}

// A shared-key CodeBuddy node keeps the role's model when the administrator
// allowed it.
func TestSponsoredExecutorKeepsAllowedRoleModel(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, runtime.NewManager(t.TempDir()))
	var observed []domain.TelemetryEvent
	sink := func(event domain.TelemetryEvent) { observed = append(observed, event) }
	got := e.sponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codebuddy", Model: "kimi-k2.6"}, domain.SharedModelProviderCodeBuddy, []string{"glm-5.1", "kimi-k2.6"}, sink)
	if got.Model != "kimi-k2.6" || len(observed) != 1 || strings.Contains(observed[0].Message, "不生效") {
		t.Fatalf("allowed role model = %+v, notice %+v", got, observed)
	}
	got = e.sponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codebuddy"}, domain.SharedModelProviderCodeBuddy, []string{"glm-5.1", "kimi-k2.6"}, sink)
	if got.Model != "glm-5.1" {
		t.Fatalf("role without a model uses the shared default, got %+v", got)
	}
}

// A sponsored CodeBuddy node runs an allowed model whatever its role
// configures, says so on the node, and notes it once on the run.
func TestSponsoredExecutorPinsModelAndExplains(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	wf := domain.WorkflowDefinition{ID: "sponsored", Name: "Sponsored", SandboxProfileID: "test-native", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
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
		got := e.sponsoredExecutor(run.ID, domain.ExecutorDefinition{ID: "codebuddy", Kind: "codebuddy", Model: "claude-opus-5", Effort: "high"}, domain.SharedModelProviderCodeBuddy, []string{repo.SponsoredCodeBuddyModel}, sink)
		if got.Model != repo.SponsoredCodeBuddyModel || got.Effort != "high" || got.Kind != "codebuddy" {
			t.Fatalf("executor = %+v", got)
		}
	}
	if len(observed) != 2 || observed[0].Model != repo.SponsoredCodeBuddyModel || !strings.Contains(observed[0].Message, "共享 Key") || !strings.Contains(observed[0].Message, "claude-opus-5") || !strings.Contains(observed[0].Message, "不生效") {
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

// A user without a Codex or Claude Code key of their own runs on the shared
// key of that CLI, marked sponsored so the node is limited to its models.
func TestResolveProviderCredentialFallsBackToTheSharedKey(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "")
	for _, user := range []domain.User{
		{ID: 31, LoginName: "owner", CodexAPIKey: "sk_own", ClaudeCodeAPIKey: "sk_own_claude"},
		{ID: 32, LoginName: "newcomer"},
	} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	for _, update := range []repo.PlatformModelKeyUpdate{
		{Provider: domain.SharedModelProviderCodex, APIKey: "sk_codex", BaseURL: "https://gateway.example.com/v1", Models: []string{"gpt-6.1-terra", "gpt-6.1-sol"}},
		{Provider: domain.SharedModelProviderClaude, APIKey: "sk_claude", Models: []string{"claude-opus-5"}},
	} {
		if err := s.SetPlatformModelKey(update, 31); err != nil {
			t.Fatal(err)
		}
	}
	codex := resolveProviderCredential(s, 32, domain.SharedModelProviderCodex, runtime.ProviderCredential{})
	if codex.APIKey != "sk_codex" || codex.BaseURL != "https://gateway.example.com/v1" || !codex.Sponsored || strings.Join(codex.SponsoredModels, ",") != "gpt-6.1-terra,gpt-6.1-sol" {
		t.Fatalf("shared codex credential = %+v", codex)
	}
	claude := resolveProviderCredential(s, 32, domain.SharedModelProviderClaude, runtime.ProviderCredential{})
	if claude.APIKey != "sk_claude" || claude.BaseURL != "" || !claude.Sponsored || len(claude.SponsoredModels) != 1 {
		t.Fatalf("shared claude credential = %+v", claude)
	}
	if own := resolveProviderCredential(s, 31, domain.SharedModelProviderCodex, runtime.ProviderCredential{APIKey: "sk_own"}); own.Sponsored || own.APIKey != "sk_own" {
		t.Fatalf("a personal key wins, got %+v", own)
	}
	if missing := resolveProviderCredential(s, 999, domain.SharedModelProviderCodex, runtime.ProviderCredential{}); missing.APIKey != "" || missing.Sponsored {
		t.Fatalf("unknown user must not borrow, got %+v", missing)
	}
	if none := resolveProviderCredential(s, 32, "kimi", runtime.ProviderCredential{}); none.APIKey != "" {
		t.Fatalf("agent CLIs have no shared credential, got %+v", none)
	}
}

// A Codex or Claude Code node on the shared key runs an allowed model and says
// why the role's model did not apply.
func TestSponsoredExecutorPinsModelsForEverySharedCLI(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, runtime.NewManager(t.TempDir()))
	var observed []domain.TelemetryEvent
	sink := func(event domain.TelemetryEvent) { observed = append(observed, event) }
	got := e.sponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codex", Model: "gpt-5.5"}, domain.SharedModelProviderCodex, []string{"gpt-6.1-terra", "gpt-6.1-sol"}, sink)
	if got.Model != "gpt-6.1-terra" || !strings.Contains(observed[0].Message, "个人 Codex API Key") || !strings.Contains(observed[0].Message, "gpt-5.5") || !strings.Contains(observed[0].Message, "不生效") {
		t.Fatalf("codex executor = %+v, notice %+v", got, observed)
	}
	observed = nil
	got = e.sponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "claude-code", Model: "claude-opus-5"}, domain.SharedModelProviderClaude, []string{"claude-opus-5", "claude-haiku-5-5"}, sink)
	if got.Model != "claude-opus-5" || !strings.Contains(observed[0].Message, "Claude Code") || strings.Contains(observed[0].Message, "不生效") {
		t.Fatalf("claude executor = %+v, notice %+v", got, observed)
	}

	// The snapshot decides: a personal or runner-login credential is kept.
	sponsored := runtime.RunCredentials{Codex: runtime.ProviderCredential{APIKey: "sk", Sponsored: true, SponsoredModels: []string{"gpt-6.1-sol"}}}
	if ex := e.applySponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codex", Model: "gpt-5.5"}, sponsored, sink); ex.Model != "gpt-6.1-sol" {
		t.Fatalf("sponsored snapshot must pin the model, got %+v", ex)
	}
	if ex := e.applySponsoredExecutor("missing-run", domain.ExecutorDefinition{Kind: "codex", Model: "gpt-5.5"}, runtime.RunCredentials{}, sink); ex.Model != "gpt-5.5" {
		t.Fatalf("an own credential keeps the role model, got %+v", ex)
	}
}

// A LOOP node can be scheduled onto a CLI the platform lends a key for.
func TestSharedKeyCountsAsExecutorCredential(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Setenv(repo.CodeBuddySponsorLoginEnv, "")
	for _, user := range []domain.User{
		{ID: 41, LoginName: "owner"},
		{ID: 42, LoginName: "newcomer"},
	} {
		if err := st.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPlatformModelKey(repo.PlatformModelKeyUpdate{
		Provider: domain.SharedModelProviderCodex, APIKey: "sk_codex",
		BaseURL: "https://gateway.example.com/v1", Models: []string{"gpt-6.1-terra"},
	}, 41); err != nil {
		t.Fatal(err)
	}
	access := newSOPExecutorAccess(st, domain.TelemetryContext{ResponsibleUserID: 42})
	if !access.hasCredential(domain.SharedModelProviderCodex) {
		t.Fatal("a shared Codex key must count as a credential")
	}
	if access.hasCredential(domain.SharedModelProviderClaude) {
		t.Fatal("a CLI without a shared key must stay unconfigured")
	}
	if got := access.defaults().Executor; got != domain.SharedModelProviderCodex {
		t.Fatalf("default executor = %q, want codex", got)
	}
}
