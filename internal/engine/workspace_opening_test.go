package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/procwatch"
	"agentflow/internal/workspaceprep"
)

func TestWorkspaceOpeningPersistsProgressLogsAndFrozenDockerfileWarning(t *testing.T) {
	e, store := newRecoveryEngine(t)
	frozen := domain.DockerfileDefinition{ID: "opening-dockerfile", Name: "Opening test", Content: "FROM old\n"}
	if err := store.PutDockerfile(domain.DockerfileDefinition{ID: frozen.ID, Name: frozen.Name, Content: "FROM new\n"}); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "opening-run", Kind: domain.RunKindWorkspace, Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{SandboxSnapshot: &domain.RunnerSandboxSpec{Dockerfile: &frozen}}, Nodes: map[string]*domain.NodeRun{}}
	if err := store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	e.RecordWorkspaceOpening(run.ID, domain.WorkspaceOpening{State: "running", Phase: "clone", Message: "正在克隆仓库", Output: "Receiving objects: 50 KiB/s"})
	view, ok := store.RunView(run.ID)
	if !ok || view.LatestWorkspaceOpening() == nil || !strings.Contains(view.LatestWorkspaceOpening().Output, "50 KiB/s") {
		t.Fatalf("API view lost live progress: %+v", view.Events)
	}
	start := view.LatestWorkspaceOpening().StartedAt
	e.RecordWorkspaceOpening(run.ID, domain.WorkspaceOpening{State: "failed", Message: "git clone https://user:secret@example.com/r: timed out"})
	view, _ = store.Run(run.ID)
	progress := view.LatestWorkspaceOpening()
	if progress.State != "failed" || progress.Phase != "clone" || !progress.StartedAt.Equal(start) || progress.Warning == "" || strings.Contains(progress.Message, "secret") {
		t.Fatalf("incorrect failure: %+v", progress)
	}
	if len(view.Logs) != 2 || view.Logs[1].Level != "error" || view.Status != "waiting" {
		t.Fatalf("logs/status: %+v, %s", view.Logs, view.Status)
	}
	if view.WorkflowSnapshot.SandboxSnapshot.Dockerfile.Content != frozen.Content {
		t.Fatal("frozen environment was silently replaced")
	}
	e.RecordWorkspaceOpening(run.ID, domain.WorkspaceOpening{State: "ready", Message: "ready"})
	view, _ = store.RunView(run.ID)
	if len(view.Events) != 1 || view.LatestWorkspaceOpening().State != "ready" {
		t.Fatal("retry did not replace failure")
	}
}

func TestWorkspaceOpeningReferenceFallbackStopsAfterStall(t *testing.T) {
	remote := newBranchRemote(t)
	cache := t.TempDir()
	mustGit(t, "", "clone", remote, cache)
	attempts := filepath.Join(t.TempDir(), "attempts")
	t.Setenv("CLONE_ATTEMPTS_FILE", attempts)
	t.Setenv(gitwork.CloneStallEnv, "250ms")
	installFakeClone(t, `    echo attempt >> "$CLONE_ATTEMPTS_FILE"
    case " $* " in
      *" --reference "*) echo 'bad reference cache' >&2; exit 1 ;;
    esac
    echo connecting >&2
    sleep 30 & wait
    exit 1`)
	rc := domain.RepositoryContext{RepositoryID: "repo", Name: "web", LocalPath: cache, CloneURL: "https://git.example.invalid/team/web.git", BaseBranch: "main"}
	var updates []domain.WorkspaceOpening
	ctx := workspaceprep.WithSink(context.Background(), func(p domain.WorkspaceOpening) { updates = append(updates, p) })
	started := time.Now()
	err := prepareRepository(ctx, t.TempDir(), rc)
	if !procwatch.Stopped(err) || !strings.Contains(err.Error(), gitwork.CloneStallEnv) || time.Since(started) > 5*time.Second {
		t.Fatalf("fallback did not retain stall detection: %v", err)
	}
	data, readErr := os.ReadFile(attempts)
	if readErr != nil || strings.Count(string(data), "attempt") != 2 {
		t.Fatalf("unexpected clone retries: %q %v", data, readErr)
	}
	if len(updates) == 0 || !strings.Contains(updates[len(updates)-1].Output, "connecting") {
		t.Fatalf("fallback did not report progress: %+v", updates)
	}
}

func TestWorkspaceOpeningReferenceRepackCanBeSilent(t *testing.T) {
	remote := newBranchRemote(t)
	cache := t.TempDir()
	mustGit(t, "", "clone", remote, cache)
	t.Setenv(gitwork.CloneStallEnv, "100ms")
	installFakeClone(t, `    case " $* " in
      *" --reference "*) sleep 0.3 ;;
      *) echo 'expected reference cache' >&2; exit 1 ;;
    esac
    break`)
	ctx := workspaceprep.WithSink(context.Background(), func(domain.WorkspaceOpening) {})
	rc := domain.RepositoryContext{RepositoryID: "repo", Name: "web", LocalPath: cache, CloneURL: "file://" + remote, BaseBranch: "master"}
	if err := prepareRepository(ctx, t.TempDir(), rc); err != nil {
		t.Fatalf("silent reference repack was killed: %v", err)
	}
}

func TestWorkspaceOpeningRetriesBranchWithoutRecloningOrLosingEdits(t *testing.T) {
	remote := newBranchRemote(t)
	workspace := filepath.Join(t.TempDir(), "checkout")
	var phases []string
	ctx := workspaceprep.WithSink(context.Background(), func(p domain.WorkspaceOpening) { phases = append(phases, p.Phase) })
	rc := domain.RepositoryContext{RepositoryID: "repo", Name: "sample", CloneURL: remote, BaseBranch: "master", DefaultBranch: "master", WorkBranch: "feature/existing", CreateNewWorkBranch: true}
	if err := prepareRepository(ctx, workspace, rc); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("expected branch conflict: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("keep my notes"), 0600); err != nil {
		t.Fatal(err)
	}
	// Fix the remote conflict and retry the same checkout. The baseline marker
	// was never written, so the new-branch guard must run again.
	mustGit(t, remote, "branch", "-D", "feature/existing")
	phases = nil
	if err := prepareRepository(ctx, workspace, rc); err != nil {
		t.Fatal(err)
	}
	for _, phase := range phases {
		if phase == "clone" {
			t.Fatal("downloaded completed checkout again")
		}
	}
	if content, err := os.ReadFile(filepath.Join(workspace, "notes.txt")); err != nil || string(content) != "keep my notes" {
		t.Fatalf("lost work: %q %v", content, err)
	}
	if branch, _ := gitC(ctx, workspace, "branch", "--show-current"); branch != rc.WorkBranch {
		t.Fatalf("wrong branch: %s", branch)
	}
}

func TestWorkspaceOpeningRecoversEmptyInterruptedClone(t *testing.T) {
	workspace := t.TempDir()
	mustGit(t, workspace, "init", "-q", "-b", "master")
	ctx := workspaceprep.WithSink(context.Background(), func(domain.WorkspaceOpening) {})
	rc := domain.RepositoryContext{RepositoryID: "repo", CloneURL: newBranchRemote(t), BaseBranch: "master", WorkBranch: "feature/new", CreateNewWorkBranch: true}
	if err := prepareRepository(ctx, workspace, rc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "README.md")); err != nil {
		t.Fatal("clone did not recover", err)
	}
}
