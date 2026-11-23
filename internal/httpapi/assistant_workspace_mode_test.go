package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

func TestRuntimeModeSwitchDoesNotNarrateInternalHandoff(t *testing.T) {
	s := &AgentToolSession{}
	var filter assistantModeEventFilter
	if !filter.visible(s, assistantStreamEvent{Kind: "answer", Text: "task progress"}) {
		t.Fatal("normal reply hidden")
	}
	if !filter.visible(s, assistantStreamEvent{Kind: "tool", ID: "workspace-open", Tool: "workbench_workspace_open"}) {
		t.Fatal("workspace-opening tool hidden")
	}
	s.requestMode(domain.AssistantModeWorkspace)
	for _, kind := range []string{"answer", "text", "thinking", "thinkingStart", "status"} {
		if filter.visible(s, assistantStreamEvent{Kind: kind, Text: "internal transition"}) {
			t.Fatalf("%s transition exposed", kind)
		}
	}
	if !filter.visible(s, assistantStreamEvent{Kind: "toolDone", ID: "workspace-open"}) {
		t.Fatal("lost tool completion")
	}
	for _, ev := range []assistantStreamEvent{
		{Kind: "tool", ID: "old-write", Tool: "workbench_workspace_write"},
		{Kind: "toolDone", ID: "old-write", Failed: true},
		{Kind: "toolDone", ID: "workspace-open"},
	} {
		if filter.visible(s, ev) {
			t.Fatalf("internal stale-CLI event exposed: %+v", ev)
		}
	}
	s.requestMode("")
	if !filter.visible(s, assistantStreamEvent{Kind: "tool", ID: "native-write", Tool: "Write"}) || !filter.visible(s, assistantStreamEvent{Kind: "toolDone", ID: "native-write"}) {
		t.Fatal("new phase native operation hidden")
	}
}

func TestWorkspaceRepositoryPromptRefreshesOnlyWhenNeeded(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutProject(domain.Project{ID: "another-project", Name: "Another project", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	for _, repository := range []domain.Repository{
		{ID: "other-repo", Name: "Other project repo", ProjectID: "another-project", CloneURL: "https://git.example.com/other/repo.git"},
		{ID: "old-repo", Name: "Old repo", ProjectID: "workbench", CloneURL: "https://old-user:fixture-password@git.example.com/team/old.git?signature=clone-query-fixture#clone-fragment-fixture", DefaultBranch: "develop"},
	} {
		if err := h.repo.PutRepository(repository); err != nil {
			t.Fatal(err)
		}
	}

	session := &AgentToolSession{ProjectID: "workbench", workspaceRunID: "first-workspace"}
	root := t.TempDir()
	render := func(s *AgentToolSession, hash string) (string, string) {
		t.Helper()
		prompt, next, err := h.workspaceRepositoryPrompt(s, root, hash, 0)
		if err != nil {
			t.Fatal(err)
		}
		return prompt, next
	}
	repositories := func(prompt string) []workspaceRepositoryHint {
		t.Helper()
		data, found := strings.CutPrefix(prompt, workspaceRepositoryHeading)
		if !found {
			t.Fatalf("missing repository context: %s", prompt)
		}
		var hints []workspaceRepositoryHint
		if err := json.Unmarshal([]byte(data), &hints); err != nil {
			t.Fatal(err)
		}
		return hints
	}
	prompt, firstHash := render(session, "")
	got := repositories(prompt)
	want := workspaceRepositoryHint{ID: "old-repo", Name: "Old repo", CloneURL: "https://git.example.com/team/old.git", DefaultBranch: "develop"}
	if firstHash == "" || len(got) != 1 || got[0] != want {
		t.Fatalf("unexpected project repository context: %+v", got)
	}
	if prompt, hash := render(session, firstHash); prompt != "" || hash != "" {
		t.Fatal("an unchanged catalog was sent again")
	}
	if err := h.repo.DeleteRepository("other-repo"); err != nil {
		t.Fatal(err)
	}
	if prompt, hash := render(session, firstHash); prompt != "" || hash != "" {
		t.Fatal("another project's change refreshed the catalog")
	}
	if err := h.repo.PutRepository(domain.Repository{ID: "new-repo", Name: "New repo", ProjectID: "workbench", CloneURL: "git@git.example.com:team/new.git"}); err != nil {
		t.Fatal(err)
	}
	prompt, updatedHash := render(session, firstHash)
	got = repositories(prompt)
	if updatedHash == "" || updatedHash == firstHash || len(got) != 2 || got[0].ID != "new-repo" || got[1] != want {
		t.Fatalf("did not send the full updated catalog: %+v", got)
	}
	session.workspaceRunID = "second-workspace"
	prompt, switchedHash := render(session, updatedHash)
	if switchedHash == "" || switchedHash == updatedHash || len(repositories(prompt)) != 2 {
		t.Fatal("the new workspace did not receive its catalog")
	}
	for _, id := range []string{"old-repo", "new-repo"} {
		if err := h.repo.DeleteRepository(id); err != nil {
			t.Fatal(err)
		}
	}
	prompt, emptyHash := render(session, switchedHash)
	if emptyHash == "" || emptyHash == switchedHash || prompt != workspaceRepositoryHeading+"[]\n" {
		t.Fatal("removing all repositories did not clear the catalog")
	}
	if prompt, hash := render(&AgentToolSession{}, ""); prompt != "" || hash != "" {
		t.Fatal("personal workspace received project data")
	}
}

func TestWorkspaceRepositoryHintsStripURLSecrets(t *testing.T) {
	hints := projectRepositoryHints([]domain.Repository{
		{ID: "ssh", Name: "A", ProjectID: "p", CloneURL: "ssh://git:fixture-password@git.example.test:2222/team/repo.git?signature=fixture#marker"},
		{ID: "web", Name: "B", ProjectID: "p", WebURL: "https://fixture-user@git.example.test/team/repo%20name?signature=fixture#marker"},
		{ID: "scp", Name: "C", ProjectID: "p", CloneURL: "git@git.example.test:team/repo.git"},
	}, "p")
	if len(hints) != 3 || hints[0].CloneURL != "ssh://git@git.example.test:2222/team/repo.git" ||
		hints[1].WebURL != "https://git.example.test/team/repo%20name" ||
		hints[2].CloneURL != "git@git.example.test:team/repo.git" {
		t.Fatalf("unexpected URLs in model data: %+v", hints)
	}
}

func TestWorkspaceRepositoryHintsPreserveGitPaths(t *testing.T) {
	for _, path := range []string{
		"/srv/repos/billing#core.git",
		"./billing?core.git",
		"file:///srv/repos/billing#core?.git",
		"git@git.example.test://srv/repos/billing#core?.git",
	} {
		hints := projectRepositoryHints([]domain.Repository{
			{ID: "billing", Name: "Billing", ProjectID: "p", CloneURL: path},
		}, "p")
		if len(hints) != 1 || hints[0].CloneURL != path {
			t.Errorf("catalog changed Git path %q: %+v", path, hints)
		}
	}
}

func TestLargeWorkspaceRepositoryCatalogReachableOnResumeAndRebuild(t *testing.T) {
	h, _ := workbenchFixture(t)
	for i := range 1000 {
		suffix := fmt.Sprintf("%04d", i)
		if err := h.repo.PutRepository(domain.Repository{
			ID: "repo-" + suffix, Name: "collaboration-component-" + suffix, ProjectID: "workbench",
			CloneURL:      "https://git.example.test/tools/collaboration-platform/component-" + suffix + ".git?signature=fixture",
			DefaultBranch: "main",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if size := len(jsonObject(projectRepositoryHints(h.repo.Repositories(), "workbench"))); size < 128<<10 {
		t.Fatalf("fixture is too small to exercise the Linux argument limit: %d", size)
	}
	root := t.TempDir()
	rel := "attachments/workspace-repositories.jsonl"
	path := filepath.Join(root, filepath.FromSlash(rel))
	s := &AgentToolSession{ProjectID: "workbench", workspaceRunID: "workspace"}
	const turn = "读取目标仓库，然后告诉我当前分支"
	render := func(known string, count int) (string, string) {
		t.Helper()
		if err := writeConversationAttachments(root, assistantRequest{}); err != nil {
			t.Fatal(err)
		}
		prompt, next, err := h.workspaceRepositoryPrompt(s, root, known, len(turn))
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != count || strings.Contains(string(data), "signature=") {
			t.Fatalf("staged catalog contains %d entries or an unsanitized URL", len(lines))
		}
		for _, line := range lines {
			var hint workspaceRepositoryHint
			if err := json.Unmarshal([]byte(line), &hint); err != nil || hint.CloneURL == "" {
				t.Fatalf("invalid staged catalog entry: %v", err)
			}
		}
		return prompt, next
	}

	prompt, first := render("", 1000)
	if first == "" || !strings.Contains(prompt, path) {
		t.Fatal("first entry did not provide the staged catalog")
	}
	binary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{runtime.AgentCLICursor, runtime.AgentCLIPi, runtime.AgentCLIAntigravity} {
		command := runtime.AgentCLICommand(kind, runtime.AgentCLICommandOptions{
			Binary: binary, Prompt: prompt + turn, Chat: true, Workspace: true,
		})
		if err := exec.Command(command.Argv[0], command.Argv[1:]...).Run(); err != nil {
			t.Fatalf("%s could not start with a file-backed catalog: %v", kind, err)
		}
	}
	files, err := conversationInputFiles(root)
	if err != nil || len(files) != 1 || files[0].Path != rel || files[0].Rewrite {
		t.Fatalf("catalog not transferable as a runner attachment: %v", err)
	}
	if prompt, hash := render(first, 1000); prompt != "" || hash != "" {
		t.Fatal("an unchanged catalog was sent again")
	}
	if err := h.repo.PutRepository(domain.Repository{
		ID: "new-repo", Name: "New repo", ProjectID: "workbench", CloneURL: "https://git.example.test/team/new.git",
	}); err != nil {
		t.Fatal(err)
	}
	prompt, refreshed := render(first, 1001)
	if refreshed == "" || refreshed == first || !strings.Contains(prompt, path) {
		t.Fatal("catalog refresh did not provide the updated file")
	}
	if prompt, rebuilt := render("", 1001); rebuilt != refreshed || !strings.Contains(prompt, path) {
		t.Fatal("native-session rebuild did not resend the file reference")
	}
}

func TestWorkspaceCatalogAndCallRejectPlatformCapabilities(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, ProjectID: conv.ProjectID, RuntimeMode: domain.AssistantModeWorkspace, ExpiresAt: time.Now().Add(time.Minute), ctx: context.Background()}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := &agentToolGateway{deps: h.deps}
	list := g.handleRPC(withAgentToolToken(context.Background(), s.Token), s, agentToolRPCRequest{ID: 1, Method: "tools/list"})
	data, _ := json.Marshal(list)
	for _, name := range []string{"agents_save", "workflows_save", "submit_assistant_reply", "workbench_workspace_write", "workbench_workspace_command", "workbench_workspace_open", "workbench_workspace_tree", "workbench_workspace_submit_mr", "workbench_github_pr_create", "workbench_attachment_read", "workbench_workspace_attach", "workbench_delivery_register"} {
		if strings.Contains(string(data), `"name":"`+name+`"`) {
			t.Fatalf("workspace leaked %s", name)
		}
		params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
		out := g.handleRPC(withAgentToolToken(context.Background(), s.Token), s, agentToolRPCRequest{ID: 2, Method: "tools/call", Params: params})
		raw, _ := json.Marshal(out)
		if !strings.Contains(string(raw), `"isError":true`) && out.Error == nil {
			t.Fatalf("legacy tool callable: %s", raw)
		}
	}
	for _, name := range []string{"workbench_platform_mode", "workbench_publish_artifact"} {
		if !strings.Contains(string(data), `"name":"`+name+`"`) {
			t.Fatalf("missing integration: %s", name)
		}
	}
	old := s.Token
	if err := s.rotateMode(domain.AssistantModePlatform); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"tools/list", "tools/call", "agentflow/nativeApproval"} {
		params := json.RawMessage(`{"name":"agents_save","arguments":{}}`)
		out := g.handleRPC(withAgentToolToken(context.Background(), old), s, agentToolRPCRequest{ID: 3, Method: method, Params: params})
		raw, _ := json.Marshal(out)
		if out.Error == nil && !strings.Contains(string(raw), `"isError":true`) {
			t.Fatalf("old token accepted after rotate: %s %s", method, raw)
		}
	}
}

func TestEncodedCLIArgumentsPreserveGatewayRestrictions(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, ProjectID: conv.ProjectID, RuntimeMode: domain.AssistantModePlatform, ExpiresAt: time.Now().Add(time.Minute), ctx: context.Background()}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g := &agentToolGateway{deps: h.deps}
	ctx := withAgentToolToken(context.Background(), s.Token)
	params := json.RawMessage(`{"name":"workbench_tool_schema","arguments":"{\"name\":\"issues_list\"}"}`)
	if result, err := g.callTool(ctx, s, params); err != nil || result == nil {
		t.Fatalf("encoded CLI argument object was not accepted: %v", err)
	}
	for _, invalid := range []string{`"[]"`, `"null"`, `"\"nested\""`, `"{malformed}"`} {
		raw := json.RawMessage(`{"name":"workbench_tool_schema","arguments":` + invalid + `}`)
		if _, err := g.callTool(ctx, s, raw); err == nil {
			t.Fatalf("invalid encoded argument accepted: %s", invalid)
		}
	}
	if err := s.rotateMode(domain.AssistantModeWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := g.callTool(ctx, s, params); err == nil {
		t.Fatal("old token accepted an encoded call after handoff")
	}
	ctx = withAgentToolToken(context.Background(), s.Token)
	if _, err := g.callTool(ctx, s, params); err == nil {
		t.Fatal("encoded argument bypassed workspace catalog restriction")
	}
}

func TestNativeApprovalAllowsPlatformReadsAndRejectsEffects(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	g := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModePlatform, ctx: context.Background()}
	if result := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"Read","input":{"path":"SKILL.md"}}`)); !result.Allow {
		t.Fatal("platform skill read denied")
	}
	if result := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"ReadMediaFile","input":{"path":"attachments/example/image.png"}}`)); !result.Allow {
		t.Fatalf("platform media read denied: %s", result.Reason)
	}
	if result := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"Bash","input":{"command":"echo unsafe"}}`)); result.Allow {
		t.Fatal("platform native shell permitted")
	}
}

func TestWorkspaceNativeToolsRunWithoutReviewInEveryApprovalMode(t *testing.T) {
	h, actor, _, _, conv := sharedWorkbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	generation := domain.AssistantGeneration{ID: "native-generation", UserID: actor.ID, SessionID: conv.ID, ActorID: actor.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(generation); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), assistantControlScopeKey{}, assistantControlScope{GenerationID: generation.ID}))
	defer cancel()
	s := &AgentToolSession{Token: "native-phase", UserID: actor.ID, User: actor, ChatOwnerID: actor.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModeWorkspace, ctx: ctx,
		approvalReviewer: func(context.Context, string, string, map[string]any) assistantApprovalReview {
			t.Fatal("native development invoked AI review")
			return assistantApprovalReview{}
		},
	}
	for _, mode := range []string{AssistantApprovalConfirm, AssistantApprovalAssisted, AssistantApprovalAuto} {
		s.ExecMode = assistantExecModeForApproval(mode)
		for _, input := range []string{
			`{"id":"read","name":"Read","input":{"path":"note.txt"}}`,
			`{"id":"write","name":"Write","input":{"path":"note.txt","content":"edit"}}`,
			`{"id":"patch","name":"apply_patch","input":{"patch":"*** Begin Patch"}}`,
			`{"id":"shell","name":"Bash","input":{"command":"pwd && npm test"}}`,
		} {
			if result := gateway.approveNativeTool(ctx, s, json.RawMessage(input)); !result.Allow || result.Pending {
				t.Fatalf("%s: %s: %+v", mode, input, result)
			}
		}
	}
	if items := h.repo.PendingNativeApprovals(actor.ID, conv.ID); len(items) != 0 {
		t.Fatalf("native development created approvals: %+v", items)
	}
	input := json.RawMessage(`{"name":"Bash","input":{"command":"pwd"}}`)
	s.nativePhaseClosed.Store(true)
	if result := gateway.approveNativeTool(ctx, s, input); result.Allow {
		t.Fatal("exited phase retained native access")
	}
	s.nativePhaseClosed.Store(false)
	if err := h.repo.CancelAssistantGeneration(actor.ID, conv.ID, generation.ID); err != nil {
		t.Fatal(err)
	}
	if result := gateway.approveNativeTool(ctx, s, input); result.Allow {
		t.Fatal("persisted cancellation retained native access")
	}
	cancel()
	if result := gateway.approveNativeTool(context.Background(), s, input); result.Allow {
		t.Fatal("cancelled turn retained native access")
	}
}

// Commands that write outside the sandbox follow the approval mode: auto runs
// them, assisted asks the reviewer first, and confirm waits for the speaker.
func TestWorkspaceExternalWritesFollowApprovalMode(t *testing.T) {
	h, actor, _, _, conv := sharedWorkbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	generation := domain.AssistantGeneration{ID: "external-generation", UserID: actor.ID, SessionID: conv.ID, ActorID: actor.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(generation); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), assistantControlScopeKey{}, assistantControlScope{GenerationID: generation.ID})
	reviews := 0
	verdict := assistantApprovalReview{Outcome: "allow", RiskLevel: "low"}
	s := &AgentToolSession{Token: "external-phase", UserID: actor.ID, User: actor, ChatOwnerID: actor.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModeWorkspace, ctx: ctx,
		approvalReviewer: func(context.Context, string, string, map[string]any) assistantApprovalReview {
			reviews++
			return verdict
		},
	}
	approve := func(id, command string) nativeToolApprovalResult {
		raw, _ := json.Marshal(map[string]any{"id": id, "name": "Bash", "input": map[string]any{"command": command}})
		return gateway.approveNativeTool(ctx, s, raw)
	}

	s.ExecMode = assistantExecModeForApproval(AssistantApprovalAuto)
	if result := approve("auto", "git push origin fix"); !result.Allow || reviews != 0 {
		t.Fatalf("auto: %+v reviews=%d", result, reviews)
	}

	s.ExecMode = assistantExecModeForApproval(AssistantApprovalAssisted)
	if result := approve("assisted", "gh pr create --fill"); !result.Allow || reviews != 1 {
		t.Fatalf("assisted allow: %+v reviews=%d", result, reviews)
	}
	verdict = assistantApprovalReview{Outcome: "deny", RiskLevel: "high", Rationale: "未授权合并"}
	if result := approve("assisted-refused", "gh pr merge 3"); result.Allow || result.Pending || !strings.Contains(result.Reason, "未授权合并") {
		t.Fatalf("a refused review must deny, not ask the speaker: %+v", result)
	}
	verdict = assistantApprovalReview{Outcome: "unavailable", Rationale: "自动审批已取消或超时，请手动确认后执行"}
	if result := approve("assisted-unavailable", "git push origin fix"); result.Allow || result.Pending || strings.Contains(result.Reason, "手动确认") {
		t.Fatalf("an unavailable review must deny without a manual step: %+v", result)
	}
	if result := approve("assisted-refused", "gh pr merge 3"); result.Allow || result.Pending || reviews != 3 {
		t.Fatalf("a repeated poll must keep the verdict without another review: %+v reviews=%d", result, reviews)
	}
	if result := approve("assisted-refused", "gh pr merge 4"); result.Allow || !strings.Contains(result.Reason, "其他操作") {
		t.Fatalf("a reused invocation ID must not review different input: %+v", result)
	}
	if items := h.repo.PendingNativeApprovals(actor.ID, conv.ID); len(items) != 0 {
		t.Fatalf("assisted mode created approval cards: %+v", items)
	}

	s.ExecMode = assistantExecModeForApproval(AssistantApprovalConfirm)
	if result := approve("confirm", "git -C web push -u origin fix"); result.Allow || !result.Pending {
		t.Fatalf("confirm must wait: %+v", result)
	}
	if result := approve("confirm", "git -C web push -u origin fix"); !result.Pending {
		t.Fatalf("a poll must keep waiting: %+v", result)
	}
	pending := h.repo.PendingNativeApprovals(actor.ID, conv.ID)
	if len(pending) != 1 || pending[0].Summary != "git push" || pending[0].CreatedAt.IsZero() {
		t.Fatalf("only the confirm-mode command waits for the speaker: %+v", pending)
	}
	if err := h.repo.DecideNativeApproval(actor.ID, actor.ID, conv.ID, pending[0].ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if result := approve("confirm", "git -C web push -u origin fix"); !result.Allow {
		t.Fatalf("approved command: %+v", result)
	}
	if result := approve("confirm-denied", "curl -X POST https://api.github.com/repos/a/b/issues"); !result.Pending {
		t.Fatalf("HTTP write must wait: %+v", result)
	}
	denied := h.repo.PendingNativeApprovals(actor.ID, conv.ID)
	if err := h.repo.DecideNativeApproval(actor.ID, actor.ID, conv.ID, denied[len(denied)-1].ID, "deny"); err != nil {
		t.Fatal(err)
	}
	if result := approve("confirm-denied", "curl -X POST https://api.github.com/repos/a/b/issues"); result.Allow || result.Pending || !strings.Contains(result.Reason, "拒绝") {
		t.Fatalf("denied command: %+v", result)
	}
	if result := approve("local", "docker rm -f old-db && git commit -am fix && curl -s https://example.com"); !result.Allow || result.Pending {
		t.Fatalf("sandbox-internal commands need no approval: %+v", result)
	}
}

func TestNativeApprovalCardInputStaysValidJSONWhenRedacted(t *testing.T) {
	command := `curl -sS -X POST -H "Authorization: token $GH_TOKEN" -H 'Accept: application/vnd.github+json' -d '{"head":"fix","base":"main"}' https://api.github.com/repos/a/b/pulls`
	raw, _ := json.Marshal(map[string]any{"command": command, "timeout": 120000})
	if json.Valid([]byte(redactAgentToolResult(string(raw), domain.User{}))) {
		t.Fatal("fixture no longer breaks text redaction; keep it representative")
	}
	got := redactNativeApprovalInput(raw, domain.User{})
	var input struct {
		Command string      `json:"command"`
		Timeout json.Number `json:"timeout"`
	}
	if err := json.Unmarshal(got, &input); err != nil {
		t.Fatalf("card input must stay JSON: %v %s", err, got)
	}
	if strings.Contains(input.Command, "$GH_TOKEN") || !strings.Contains(input.Command, `"Authorization: [redacted]"`) || !strings.Contains(input.Command, "api.github.com/repos/a/b/pulls") || input.Timeout != "120000" {
		t.Fatalf("redacted input = %s", got)
	}
	if got := redactNativeApprovalInput(json.RawMessage(`{"command":`), domain.User{}); string(got) != `"[redacted]"` {
		t.Fatalf("undecodable input must be hidden: %s", got)
	}
}

func TestNativeExternalWriteClassifier(t *testing.T) {
	for input, want := range map[string]string{
		`{"command":"git push"}`: "git push",
		`{"command":"cd web && git -c core.x=1 push --force origin fix"}`: "git push",
		`{"command":["bash","-lc","gh pr create --title x"]}`:             "GitHub CLI 写操作",
		`{"cmd":"gh api repos/a/b/pulls -f title=x"}`:                     "GitHub API 写请求",
		`{"command":"gh api -X DELETE repos/a/b/git/refs/heads/x"}`:       "GitHub API 写请求",
		`{"command":"glab mr create --fill"}`:                             "GitLab CLI 写操作",
		`{"command":"glab api -X PUT projects/1/merge_requests/2/merge"}`: "GitLab API 写请求",
		`{"command":"tea pulls create --title x"}`:                        "Gitea CLI 写操作",
		`{"command":"tea comment 3 'lgtm'"}`:                              "Gitea CLI 写操作",
		`{"command":"glab mr view 2 && tea pulls list"}`:                  "",
		`{"command":"curl -sS --data '{}' https://api.example.com"}`:      "HTTP 写请求",
		`{"command":"npm publish --access public"}`:                       "发布软件包",
		`{"command":"docker push ghcr.io/a/b:1"}`:                         "推送镜像",
		`{"command":"kubectl apply -f deploy.yaml"}`:                      "集群或基础设施变更",
		`{"command":"git status && git log --oneline"}`:                   "",
		`{"command":"gh pr view 3 && gh api repos/a/b/pulls"}`:            "",
		`{"command":"curl -s https://example.com"}`:                       "",
		`{"command":"docker run --rm -v $PWD:/m alpine ls /m"}`:           "",
		`{"path":"notes.md","content":"git push origin main"}`:            "",
	} {
		got, ok := nativeExternalWrite(json.RawMessage(input))
		if got != want || ok != (want != "") {
			t.Errorf("%s = %q %v, want %q", input, got, ok, want)
		}
	}
}

func TestRuntimeModeHandoffPublishesToLiveGeneration(t *testing.T) {
	recorder := newAssistantGenerationRecorder(domain.AssistantGeneration{ID: "mode-stream", Status: "running"})
	defer recorder.close()
	recorder.sink("runtime_mode", map[string]string{"mode": domain.AssistantModeWorkspace})
	if got := recorder.snapshot(); got.RuntimeMode != domain.AssistantModeWorkspace {
		t.Fatal("handoff mode not published")
	}
	recorder.sink("runtime_mode", map[string]string{"mode": "invalid"})
	if got := recorder.snapshot(); got.RuntimeMode != domain.AssistantModeWorkspace {
		t.Fatal("invalid mode accepted")
	}
}

// Exercise the names recorded by real CLI hooks, including discovery before
// tools/call. A legitimate wrapper must reach the gateway without granting
// arbitrary native commands or another MCP server the same exception.
func TestNativeApprovalRecognizesCLIGatewayAliases(t *testing.T) {
	h, a, _, _, conv := sharedWorkbenchFixture(t)
	g := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{Mode: AgentToolAssistant, UserID: a.ID, User: a, ChatOwnerID: a.ID, ChatSessionID: conv.ID, ProjectID: conv.ProjectID, RuntimeMode: domain.AssistantModePlatform, ExpiresAt: time.Now().Add(time.Minute), ctx: context.Background()}
	if err := agentToolSessions.create(s); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, raw := range []string{
		`{"name":"mcp__agentflow_session__workbench_workspace_open","input":{}}`,
		`{"name":"agentflow_session_workbench_workspace_open","input":{}}`,
		`{"name":"call_mcp_tool","input":{"ServerName":"agentflow_session","ToolName":"workbench_workspace_open","Arguments":"{}"}}`,
		`{"name":"GetMcpTools","input":{"server":"agentflow_session"}}`,
		`{"name":"GetDynamicTools","input":{"namespace":"agentflow_session","toolName":"workbench_workspace_open"}}`,
		`{"name":"CallDynamicTool","input":{"namespace":"agentflow_session","toolName":"workbench_workspace_open","arguments":{}}}`,
		`{"name":"CallMcpTool","input":{"server":"agentflow_session","toolName":"workbench_workspace_open","arguments":{}}}`,
		`{"name":"MCP:workbench_workspace_open","input":{}}`,
	} {
		if result := g.approveNativeTool(context.Background(), s, json.RawMessage(raw)); !result.Allow {
			t.Fatalf("gateway alias blocked: %s: %s", raw, result.Reason)
		}
	}
	for _, raw := range []string{
		`{"name":"call_mcp_tool","input":{"ServerName":"foreign","ToolName":"Bash"}}`,
		`{"name":"call_mcp_tool","input":{"ServerName":"agentflow_session"}}`,
		`{"name":"CallDynamicTool","input":{"namespace":"foreign","toolName":"Bash"}}`,
		`{"name":"GetDynamicTools","input":{"namespace":"foreign"}}`,
		`{"name":"CallDynamicTool","input":{"namespace":"foreign","server":"agentflow_session","toolName":"Bash"}}`,
		`{"name":"MCP:Bash","input":{}}`,
		`{"name":"Bash","input":{"command":"touch effect.txt"}}`,
		`{"name":"run_command","input":{"command":"touch effect.txt"}}`,
		`{"name":"Write","input":{"path":"effect.txt","content":"unsafe"}}`,
	} {
		if result := g.approveNativeTool(context.Background(), s, json.RawMessage(raw)); result.Allow {
			t.Fatalf("non-gateway action allowed: %s", raw)
		}
	}
	if err := s.rotateMode(domain.AssistantModeWorkspace); err != nil {
		t.Fatal(err)
	}
	// A persisted MCP alias can pass the native hook, but an old platform tool
	// must still be rejected by the current gateway at the actual call entry.
	if result := g.approveNativeTool(context.Background(), s, json.RawMessage(`{"name":"agentflow_session_agents_save","input":{}}`)); !result.Allow {
		t.Fatal("gateway transport blocked")
	}
	if _, err := g.callTool(withAgentToolToken(context.Background(), s.Token), s, json.RawMessage(`{"name":"agents_save","arguments":{}}`)); err == nil {
		t.Fatal("native gateway exception bypassed workspace restrictions")
	}
}

// A pending external write waits for the speaker: reading the generation from
// an instance that does not run it must not decide it.
// A review slower than one hook request answers pending; the next poll of
// the same invocation reads the verdict instead of reviewing again.
func TestAssistedNativeReviewOutlivesOneHookRequest(t *testing.T) {
	h, actor, _, _, conv := sharedWorkbenchFixture(t)
	gateway := &agentToolGateway{deps: h.deps}
	generation := domain.AssistantGeneration{ID: "slow-review", UserID: actor.ID, SessionID: conv.ID, ActorID: actor.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(generation); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), assistantControlScopeKey{}, assistantControlScope{GenerationID: generation.ID})
	release := make(chan struct{})
	var reviews atomic.Int32
	s := &AgentToolSession{Token: "slow-phase", UserID: actor.ID, User: actor, ChatOwnerID: actor.ID, ChatSessionID: conv.ID, RuntimeMode: domain.AssistantModeWorkspace, ctx: ctx,
		ExecMode: assistantExecModeForApproval(AssistantApprovalAssisted),
		approvalReviewer: func(reviewCtx context.Context, _, _ string, _ map[string]any) assistantApprovalReview {
			reviews.Add(1)
			select {
			case <-release:
			case <-reviewCtx.Done():
				return assistantApprovalReview{Outcome: "unavailable"}
			}
			return assistantApprovalReview{Outcome: "allow", RiskLevel: "low", Rationale: "用户要求推送"}
		},
	}
	raw := json.RawMessage(`{"id":"push","name":"Bash","input":{"command":"git push origin fix"}}`)
	poll, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if result := gateway.approveNativeTool(poll, s, raw); result.Allow || !result.Pending {
		t.Fatalf("a running review must answer pending: %+v", result)
	}
	close(release)
	if result := gateway.approveNativeTool(ctx, s, raw); !result.Allow || result.Reason != "用户要求推送" {
		t.Fatalf("the next poll must read the verdict: %+v", result)
	}
	if n := reviews.Load(); n != 1 {
		t.Fatalf("reviews = %d", n)
	}
	if items := h.repo.PendingNativeApprovals(actor.ID, conv.ID); len(items) != 0 {
		t.Fatalf("assisted mode created approval cards: %+v", items)
	}
}

func TestPendingNativeApprovalWaitsForTheSpeaker(t *testing.T) {
	h, actor, _, _, session := sharedWorkbenchFixture(t)
	g := domain.AssistantGeneration{ID: "other-worker", SessionID: session.ID, UserID: actor.ID, ActorID: actor.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(g); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetAssistantRuntimeMode(actor.ID, session.ID, session.ProjectID, domain.AssistantModeWorkspace, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.CreateNativeApproval(actor.ID, session.ID, "phase", domain.AssistantNativeApproval{ID: "push", GenerationID: g.ID, ActorID: actor.ID, Tool: "Bash", Input: json.RawMessage(`{"command":"git push"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.currentGeneration(actor.ID, session.ID); !ok {
		t.Fatal("generation missing")
	}
	if item, ok := h.repo.NativeApproval(actor.ID, session.ID, "push"); !ok || item.Decision != "pending" {
		t.Fatalf("pending approval decided without the speaker: %+v", item)
	}
}
