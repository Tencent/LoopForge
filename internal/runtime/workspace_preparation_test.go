package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"agentflow/internal/domain"
)

func preparationFixture(t *testing.T) (Sandbox, domain.WorkflowDefinition) {
	t.Helper()
	root := t.TempDir()
	sb := &localSandbox{id: "preparation-test", workspace: root}
	env := domain.WorkflowNode{ID: "env", Name: "准备环境", Type: "script", RunOnWorkspaceRebuild: true, Script: "mkdir -p .agentflow/prep; printf 'run\\n' >> .agentflow/prep/environment-count; if [ ! -f source.txt ]; then printf initial > source.txt; fi; printf environment-ready"}
	index := domain.WorkflowNode{ID: "index", Name: "建立索引", Type: "script", RunOnWorkspaceRebuild: true, Script: "mkdir -p .agentflow/prep; printf 'run\\n' >> .agentflow/prep/index-count; cat source.txt > .agentflow/prep/index; printf index-ready"}
	return sb, domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{index, env}, Edges: []domain.WorkflowEdge{{Source: "env", Target: "index"}}}
}
func preparationCount(t *testing.T, sb Sandbox, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sb.Workspace(), ".agentflow", "prep", name+"-count"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "run\n")
}
func TestPreparationReusesWarmEnvironmentDespiteSourceEdits(t *testing.T) {
	sb, wf := preparationFixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if state, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil || state.Status != "ready" {
			t.Fatal(state, err)
		}
	}
	if err := os.WriteFile(filepath.Join(sb.Workspace(), "source.txt"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sb.Workspace(), "untracked.txt"), []byte("new file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if preparationCount(t, sb, "environment") != 1 || preparationCount(t, sb, "index") != 1 {
		t.Fatal("warm source edits reran rebuild scripts")
	}
}
func TestPreparationNormalScriptReceiptAvoidsImmediateReplay(t *testing.T) {
	sb, wf := preparationFixture(t)
	ctx := context.Background()
	for _, node := range PreparationNodes(wf) {
		output, err := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-lc", node.Script}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = RecordWorkspacePreparation(ctx, sb, wf, nil, node.ID, output); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if preparationCount(t, sb, "environment") != 1 || preparationCount(t, sb, "index") != 1 {
		t.Fatal("normally executed scripts replayed in same environment")
	}
}
func TestPreparationSnapshotRebuildReplaysWithoutOverwritingSource(t *testing.T) {
	sb, wf := preparationFixture(t)
	ctx := context.Background()
	if _, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sb.Workspace(), "source.txt"), []byte("preserve this edit"), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateWorkspaceCheckpoint(sb.Workspace())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "restored")
	if err = RestoreWorkspaceCheckpoint(root, archive); err != nil {
		t.Fatal(err)
	}
	restored := &localSandbox{id: "restored", workspace: root}
	if _, err = EnsureWorkspacePreparation(ctx, restored, wf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if preparationCount(t, restored, "environment") != 1 || preparationCount(t, restored, "index") != 1 {
		t.Fatal("reconstructed environment was not prepared")
	}
	raw, _ := os.ReadFile(filepath.Join(root, "source.txt"))
	if string(raw) != "preserve this edit" {
		t.Fatal("source edit overwritten", string(raw))
	}
}
func TestPreparationFailureRemainsVisibleAndFilesRemainReadable(t *testing.T) {
	sb, wf := preparationFixture(t)
	wf.Nodes[0].Script = "exit 7"
	state, err := EnsureWorkspacePreparation(context.Background(), sb, wf, nil, nil)
	if err == nil || state.Status != "failed" || state.Steps[len(state.Steps)-1].NodeID != "index" {
		t.Fatal(state, err)
	}
	if _, err = os.ReadFile(filepath.Join(sb.Workspace(), "source.txt")); err != nil {
		t.Fatal(err)
	}
	stored := ReadWorkspacePreparationStatus(sb.Workspace())
	if stored.Status != "failed" {
		t.Fatal(stored)
	}
}

func TestPreparationFailureIncludesBoundedOutputTailAndPreservesExitError(t *testing.T) {
	sb, wf := preparationFixture(t)
	const detail = "Could not delete /workspace/repo/vendor/phpstan/phpstan/CLAUDE.md"
	logPath := filepath.Join(t.TempDir(), "installer.log")
	log := strings.Repeat("Downloading dependency...\n", 2000) + "\nIn Filesystem.php line 303:\n\n  " + detail + "\n\n" +
		"update" + strings.Repeat(" [--dependency-option VALUE]", 120) + strings.Repeat("\n", 12)
	if err := os.WriteFile(logPath, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	wf.Nodes[0].Script = "cat -- " + preparationQuote(logPath) + "; exit 7"
	state, err := EnsureWorkspacePreparation(context.Background(), sb, wf, nil, nil)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("lost original command failure: %v", err)
	}
	if !strings.Contains(err.Error(), "恢复工作区 建立索引：exit status 7") || !strings.Contains(err.Error(), detail) {
		t.Fatalf("missing actionable failure detail: %v", err)
	}
	if !strings.Contains(err.Error(), "In Filesystem.php line 303:") || strings.Contains(err.Error(), "--dependency-option") {
		t.Fatalf("CLI usage displaced the failure context: %v", err)
	}
	if len(err.Error()) > (2<<10)+128 || strings.Count(err.Error(), "\n") > 8 {
		t.Fatalf("public error contains too much output: %d bytes", len(err.Error()))
	}
	step := state.Steps[len(state.Steps)-1]
	if len(step.Output) > 32<<10 || !strings.Contains(step.Output, detail) || step.Error != err.Error() {
		t.Fatalf("failure receipt did not retain bounded tail: %+v", step)
	}
	public := PublicWorkspacePreparationStatus(state)
	if public.Error != err.Error() || public.Steps[len(public.Steps)-1].Output != "" {
		t.Fatalf("public status lost summary or exposed raw output: %+v", public)
	}
}

func TestPreparationFailureOutputRedactsCredentialsBeforeTruncation(t *testing.T) {
	sb := &managedSandbox{Sandbox: &localSandbox{environment: map[string]string{"DB_PASSWORD": "db-secret-value"}}, credentials: RunCredentials{
		GitToken: "owned-secret",
		MCPServers: []domain.MCPServerDefinition{{
			Headers:     map[string]string{"Authorization": "Bearer header-owned-secret"},
			Environment: map[string]string{"API_TOKEN": "mcp-owned-secret", "ENABLED": "1"},
		}},
	}}
	raw := "\x1b[31mfailed\x1b[0m: owned-secret header-owned-secret mcp-owned-secret db-secret-value\n" +
		`{"password":"json-password", "API_KEY":"json-api-key"}` + "\n" +
		"AGENTFLOW_CONTROL_TOKEN=control-value Authorization: Bearer auth-value\n" +
		"https://private:url-password@example.test/repo\nexit status 1\n"
	redacted := redactPreparationOutput(sb, raw)
	for _, secret := range []string{"owned-secret", "header-owned-secret", "mcp-owned-secret", "header-[redacted]", "mcp-[redacted]", "db-secret-value", "json-password", "json-api-key", "control-value", "auth-value", "url-password", "\x1b"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("failure output exposed %q: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "exit status 1") || !strings.Contains(redacted, "[redacted]") {
		t.Fatalf("redaction removed useful diagnostic text: %s", redacted)
	}
	bounded := preparationFailureDetail(strings.Repeat("错误", 1000) + "\n" + redacted)
	if len(bounded) > 2<<10 || !utf8.ValidString(bounded) || !strings.HasSuffix(bounded, "exit status 1") {
		t.Fatalf("invalid bounded UTF-8 diagnostic: %q", bounded)
	}
	if detail := preparationFailureDetail("  \n"); detail != "" {
		t.Fatalf("empty output became error detail: %q", detail)
	}
}
func TestPreparationFlagOffRunsNothingAndAllowsBusinessDependencies(t *testing.T) {
	sb, wf := preparationFixture(t)
	for i := range wf.Nodes {
		wf.Nodes[i].RunOnWorkspaceRebuild = false
	}
	wf.Nodes = append(wf.Nodes, domain.WorkflowNode{ID: "business", Type: "agent"})
	wf.Edges = append(wf.Edges, domain.WorkflowEdge{Source: "business", Target: "env"})
	if _, err := EnsureWorkspacePreparation(context.Background(), sb, wf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sb.Workspace(), "source.txt")); !os.IsNotExist(err) {
		t.Fatal("flag-off script executed")
	}
}
func TestPreparationGroupUsesOriginalFrozenDependencyOrder(t *testing.T) {
	sb, wf := preparationFixture(t)
	group := wf
	group.CollaborationMode = "group"
	group.SOPCheckpoint = &domain.SOPCheckpoint{Workflow: wf}
	group.Edges = nil
	if _, err := EnsureWorkspacePreparation(context.Background(), sb, group, nil, nil); err != nil {
		t.Fatal(err)
	}
	if preparationCount(t, sb, "index") != 1 {
		t.Fatal("lost original dependency graph")
	}
}
func TestPreparationPublicStatusDoesNotMutateOrExposeInternalData(t *testing.T) {
	private := WorkspacePreparationStatus{Status: "ready", Environment: "secret epoch", Steps: []WorkspacePreparationStep{{Output: "private output", Fingerprint: "private stamp"}}}
	public := PublicWorkspacePreparationStatus(private)
	if public.Environment != "" || public.Steps[0].Output != "" || public.Steps[0].Fingerprint != "" {
		t.Fatal(public)
	}
	if private.Steps[0].Output != "private output" || private.Steps[0].Fingerprint != "private stamp" {
		t.Fatal("public projection mutated stored state")
	}
}
func TestPreparationRetainsOtherSuccessfulReceiptsWhenPlanIsTemporarilySmaller(t *testing.T) {
	sb, wf := preparationFixture(t)
	ctx := context.Background()
	for _, node := range PreparationNodes(wf) {
		output, err := sb.Exec(ctx, CommandSpec{Argv: []string{"sh", "-lc", node.Script}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = RecordWorkspacePreparation(ctx, sb, wf, nil, node.ID, output); err != nil {
			t.Fatal(err)
		}
	}
	partial := wf
	partial.Nodes = append([]domain.WorkflowNode(nil), wf.Nodes...)
	partial.Nodes[0].RunOnWorkspaceRebuild = false
	if _, err := EnsureWorkspacePreparation(ctx, sb, partial, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if preparationCount(t, sb, "environment") != 1 || preparationCount(t, sb, "index") != 1 {
		t.Fatal("a filtered plan dropped successful execution receipts")
	}
}
func TestPreparationMissingSameEnvironmentReceiptBlocksWithoutReplaying(t *testing.T) {
	sb, wf := preparationFixture(t)
	ctx := context.Background()
	empty := wf
	empty.Nodes = append([]domain.WorkflowNode(nil), wf.Nodes...)
	for i := range empty.Nodes {
		empty.Nodes[i].RunOnWorkspaceRebuild = false
	}
	if _, err := EnsureWorkspacePreparation(ctx, sb, empty, nil, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		state, err := EnsureWorkspacePreparation(ctx, sb, wf, nil, nil)
		if err == nil || !state.ReplayBlocked || !strings.Contains(err.Error(), "执行记录") {
			t.Fatal(state, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sb.Workspace(), "source.txt")); !os.IsNotExist(err) {
		t.Fatal("missing proof caused duplicate script execution")
	}
}
