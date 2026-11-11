package httpapi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func openChoice(t *testing.T, args map[string]any) (workspaceOpenChoice, error) {
	t.Helper()
	args["goal"], args["new"] = "改代码", true
	return parseWorkspaceOpenChoice(args)
}

func branchChoiceRepositories() []domain.RepositoryContext {
	return []domain.RepositoryContext{
		{RepositoryID: "repo-web", Name: "web", BaseBranch: "master", DefaultBranch: "master"},
		{RepositoryID: "repo-api", Name: "api", BaseBranch: "master", DefaultBranch: "master"},
	}
}

// One call can review someone's branch in one repository and create a named
// branch in another: a conversation only binds one workspace at a time.
func TestWorkspaceOpenPerRepositoryBranchModes(t *testing.T) {
	choice, err := openChoice(t, map[string]any{"repositoryBranches": map[string]any{
		"api": map[string]any{"branch": "feature/review", "branchMode": "continue"},
		"web": map[string]any{"branch": "feature/new", "branchMode": "create", "baseBranch": "release/1.2"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	selected, picked, err := selectWorkspaceBranches(branchChoiceRepositories(), choice)
	if err != nil || len(picked) != 2 {
		t.Fatalf("per-repository modes = %+v %v %v", selected, picked, err)
	}
	web, api := selected[0], selected[1]
	if !web.CreateNewWorkBranch || web.RequireExistingWorkBranch || web.WorkBranch != "feature/new" || web.BaseBranch != "release/1.2" {
		t.Fatalf("web = %+v", web)
	}
	if !api.RequireExistingWorkBranch || api.CreateNewWorkBranch || api.WorkBranch != "feature/review" || api.BaseBranch != "master" {
		t.Fatalf("api = %+v", api)
	}
	// A plain string still inherits the call's branchMode.
	choice, err = openChoice(t, map[string]any{"branchMode": "create", "repositoryBranches": map[string]any{"api": "feature/x", "web": map[string]any{"branch": "feature/y", "branchMode": "continue"}}})
	if err != nil {
		t.Fatal(err)
	}
	selected, _, err = selectWorkspaceBranches(branchChoiceRepositories(), choice)
	if err != nil || !selected[1].CreateNewWorkBranch || !selected[0].RequireExistingWorkBranch {
		t.Fatalf("string entries inherit, objects override: %+v %v", selected, err)
	}
	for name, args := range map[string]map[string]any{
		"unknown field":      {"repositoryBranches": map[string]any{"api": map[string]any{"ref": "x"}}},
		"bad entry mode":     {"repositoryBranches": map[string]any{"api": map[string]any{"branch": "feature/x", "branchMode": "fork"}}},
		"auto with a branch": {"repositoryBranches": map[string]any{"api": map[string]any{"branch": "feature/x", "branchMode": "auto"}}},
		"create without one": {"repositoryBranches": map[string]any{"api": map[string]any{"branchMode": "create"}}},
	} {
		choice, err := openChoice(t, args)
		if err == nil {
			_, _, err = selectWorkspaceBranches(branchChoiceRepositories(), choice)
		}
		if err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// The base is where the work branch starts and what its MR targets, so a
// hotfix against a release branch carries no commits the release lacks.
func TestWorkspaceOpenBaseBranch(t *testing.T) {
	choice, err := openChoice(t, map[string]any{"branch": "hotfix/login", "branchMode": "create", "baseBranch": "release/1.2"})
	if err != nil {
		t.Fatal(err)
	}
	selected, picked, err := selectWorkspaceBranches(branchChoiceRepositories()[:1], choice)
	if err != nil || selected[0].BaseBranch != "release/1.2" || selected[0].WorkBranch != "hotfix/login" || !strings.Contains(picked[0], "起点 release/1.2") {
		t.Fatalf("create from a release base = %+v %v %v", selected, picked, err)
	}
	// baseBranch alone keeps the auto name but moves its start point, for
	// every repository of the call.
	choice, err = openChoice(t, map[string]any{"baseBranch": "release/1.2"})
	if err != nil || choice.BranchMode != workspaceBranchModeAuto {
		t.Fatalf("baseBranch alone = %+v %v", choice, err)
	}
	selected, picked, err = selectWorkspaceBranches(branchChoiceRepositories(), choice)
	if err != nil || len(picked) != 2 || selected[0].BaseBranch != "release/1.2" || selected[1].BaseBranch != "release/1.2" || selected[0].WorkBranch != "" {
		t.Fatalf("auto from a release base = %+v %v %v", selected, picked, err)
	}
	// The base itself is never the work branch.
	choice, _ = openChoice(t, map[string]any{"branch": "release/1.2", "branchMode": "continue", "baseBranch": "release/1.2"})
	if _, _, err := selectWorkspaceBranches(branchChoiceRepositories()[:1], choice); err == nil || !strings.Contains(err.Error(), "共享分支") {
		t.Fatalf("working on the base must be refused: %v", err)
	}
	choice, _ = openChoice(t, map[string]any{"baseBranch": "bad..name"})
	if _, _, err := selectWorkspaceBranches(branchChoiceRepositories()[:1], choice); err == nil {
		t.Fatal("an invalid base must be refused")
	}
}

func TestWorkspaceOpenReadOnlyCheckout(t *testing.T) {
	choice, err := openChoice(t, map[string]any{"branch": "preview", "branchMode": "readonly"})
	if err != nil {
		t.Fatal(err)
	}
	selected, picked, err := selectWorkspaceBranches(branchChoiceRepositories()[:1], choice)
	if err != nil || !selected[0].ReadOnly || selected[0].BaseBranch != "preview" || selected[0].WorkBranch != "" || !strings.Contains(picked[0], "只读") {
		t.Fatalf("read-only preview = %+v %v %v", selected, picked, err)
	}
	// Shared baselines are fine read-only, next to a writable repository.
	choice, err = openChoice(t, map[string]any{"baseBranch": "release/1.2", "repositoryBranches": map[string]any{"web": map[string]any{"branch": "master", "branchMode": "readonly"}}})
	if err != nil {
		t.Fatal(err)
	}
	selected, _, err = selectWorkspaceBranches(branchChoiceRepositories(), choice)
	if err != nil || !selected[0].ReadOnly || selected[0].BaseBranch != "master" || selected[1].ReadOnly || selected[1].BaseBranch != "release/1.2" {
		t.Fatalf("read-only next to writable = %+v %v", selected, err)
	}
	choice, _ = openChoice(t, map[string]any{"repositoryBranches": map[string]any{"web": map[string]any{"branch": "preview", "branchMode": "readonly", "baseBranch": "master"}}})
	if _, _, err := selectWorkspaceBranches(branchChoiceRepositories(), choice); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("read-only with its own base must be refused: %v", err)
	}
	// A top-level base that only read-only repositories would see has no
	// effect, so it is refused rather than silently dropped.
	choice, _ = openChoice(t, map[string]any{"branch": "preview", "branchMode": "readonly", "baseBranch": "master"})
	if _, _, err := selectWorkspaceBranches(branchChoiceRepositories()[:1], choice); err == nil || !strings.Contains(err.Error(), "baseBranch=master") {
		t.Fatalf("an unused top-level base must be refused: %v", err)
	}
	choice, _ = openChoice(t, map[string]any{"branchMode": "readonly", "baseBranch": "master", "repositoryBranches": map[string]any{"web": "preview", "api": "preview"}})
	if _, _, err := selectWorkspaceBranches(branchChoiceRepositories(), choice); err == nil {
		t.Fatal("a base no repository starts from must be refused")
	}
	if _, err := openChoice(t, map[string]any{"branchMode": "readonly"}); err == nil {
		t.Fatal("readonly without a branch must be refused")
	}
}

func TestMissingBranchHintNamesRepository(t *testing.T) {
	repositories := []domain.RepositoryContext{{Name: "web", BaseBranch: "release/doesnotexist"}, {Name: "api", BaseBranch: "master"}}
	raw := errors.New("git clone https://git.example/web.git: exit status 128 (fatal: Remote branch release/doesnotexist not found in upstream origin)")
	if hint := missingBranchHint(raw, repositories); !strings.Contains(hint, "仓库 web") || !strings.Contains(hint, "release/doesnotexist") || strings.Contains(hint, "api") {
		t.Fatalf("hint = %q", hint)
	}
	if hint := missingBranchHint(errors.New("permission denied"), repositories); hint != "" {
		t.Fatalf("unrelated failures keep their own message: %q", hint)
	}
}

func TestWorkspaceOpenScratch(t *testing.T) {
	choice, err := openChoice(t, map[string]any{"repositories": []any{}})
	if err != nil || !choice.Scratch {
		t.Fatalf("empty repositories = %+v %v", choice, err)
	}
	if choice, _ := openChoice(t, map[string]any{}); choice.Scratch {
		t.Fatal("omitted repositories still means every repository")
	}
	if _, err := openChoice(t, map[string]any{"repositories": []any{}, "branch": "feature/x"}); err == nil {
		t.Fatal("a scratch workspace has no branch to choose")
	}
	got, err := selectConversationRepositories(domain.Project{ID: "none", Name: "空项目"}, nil, nil, true)
	if err != nil || len(got) != 0 {
		t.Fatalf("scratch in a project without repositories = %+v %v", got, err)
	}
	if got, err := selectConversationRepositories(domain.Project{ID: "none", Name: "空项目"}, nil, nil, false); err != nil || len(got) != 0 {
		t.Fatalf("a project without repositories should open a scratch workspace: %v %v", got, err)
	}
}

// Creating a branch an earlier workspace of the conversation already works on
// would start over from the base and drop its unpushed commits.
func TestWorkspaceOpenRefusesRecreatingEarlierBranch(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	earlier := domain.Run{ID: "run-earlier", Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID}, ExecutionContext: domain.TelemetryContext{ProjectID: "platform"},
		Repositories: []domain.RepositoryContext{{RepositoryID: "repo-api", Name: "api", BaseBranch: "master", WorkBranch: "feature/mine", CreateNewWorkBranch: true}}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(earlier); err != nil {
		t.Fatal(err)
	}
	h := &agentToolGateway{deps: &deps{repo: st}}
	conv := domain.AssistantSession{OwnWorkspaceRunIDs: []string{earlier.ID}}
	again := []domain.RepositoryContext{{RepositoryID: "repo-api", Name: "api", WorkBranch: "feature/mine", CreateNewWorkBranch: true}}
	if err := h.refuseRecreatedBranch(conv, "platform", again); err == nil || !strings.Contains(err.Error(), "workspaceRunId="+earlier.ID) {
		t.Fatalf("recreating an earlier branch must point back to its workspace: %v", err)
	}
	if err := h.refuseRecreatedBranch(conv, "platform", []domain.RepositoryContext{{RepositoryID: "repo-api", WorkBranch: "feature/other", CreateNewWorkBranch: true}}); err != nil {
		t.Fatalf("another name is fine: %v", err)
	}
	if err := h.refuseRecreatedBranch(domain.AssistantSession{}, "platform", again); err != nil {
		t.Fatalf("other conversations' workspaces are not consulted: %v", err)
	}
	earlier.Status = "cancelled"
	if err := st.PutRun(earlier); err != nil {
		t.Fatal(err)
	}
	if err := h.refuseRecreatedBranch(conv, "platform", again); err != nil {
		t.Fatalf("a discarded workspace cannot be switched back to, so it does not block: %v", err)
	}
}

func TestWorkspaceSubmitMRRefusesReadOnlyOnlyWorkspace(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "alice", GitToken: "token"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("alice")
	run := domain.Run{ID: "run-readonly", Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID}, ExecutionContext: domain.TelemetryContext{ProjectID: "platform"},
		Repositories: []domain.RepositoryContext{{RepositoryID: "repo-1", Name: "app", BaseBranch: "preview", DefaultBranch: "master", ReadOnly: true}}, Nodes: map[string]*domain.NodeRun{}}
	if err := st.PutRun(run); err != nil {
		t.Fatal(err)
	}
	h := &agentToolGateway{deps: &deps{repo: st, engine: engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))}}
	s := &AgentToolSession{RunID: run.ID, ProjectID: "platform", UserID: user.ID, User: user, Mode: AgentToolAssistant}
	if _, err := h.workspaceSubmitMR(context.Background(), s, map[string]any{"title": "x"}); err == nil || !strings.Contains(err.Error(), "只读检出") {
		t.Fatalf("a read-only workspace has nothing to submit: %v", err)
	}
	// Direct push to the shared branch a repository only reads is not a push
	// to that repository's base.
	if _, err := h.workspaceSubmitMR(context.Background(), s, map[string]any{"title": "x", "mode": "direct", "targetBranch": "preview"}); err == nil || strings.Contains(err.Error(), "不能直推基线分支") {
		t.Fatalf("read-only repositories must not block direct pushes: %v", err)
	}
}
