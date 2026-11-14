package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
)

// installFakeClone puts a git first on PATH whose `clone` runs cloneScript;
// every other git command goes to the real git.
func installFakeClone(t *testing.T, cloneScript string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = clone ]; then\n" + cloneScript + "\n  fi\ndone\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func putCloningWorkspace(t *testing.T) (*Engine, domain.Run) {
	t.Helper()
	e, s := newRecoveryEngine(t)
	run := domain.Run{
		ID: "slow-clone-chat", Kind: domain.RunKindWorkspace, Status: "waiting", Nodes: map[string]*domain.NodeRun{},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "local"},
		ExecutionContext: domain.TelemetryContext{ProjectID: "platform"},
		Repositories:     []domain.RepositoryContext{{RepositoryID: "web", Name: "web", CloneURL: "https://git.example.invalid/team/web.git", BaseBranch: "main", WorkBranch: "feature/chat", WorkspaceDir: "web"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return e, run
}

func runEvents(e *Engine, id, prefix string) []domain.RunEvent {
	r, _ := e.repo.RunState(id)
	var out []domain.RunEvent
	for _, ev := range r.Events {
		if strings.HasPrefix(ev.Type, prefix) {
			out = append(out, ev)
		}
	}
	return out
}

// Issue #124: a slow clone while opening a conversation workspace used to leave
// the run at waiting with no events until it was killed, and then still none.
func TestMaterializeConversationWorkspaceReportsSlowCloneProgress(t *testing.T) {
	installFakeClone(t, `    i=0
    while :; do
      i=$((i+1))
      printf 'Receiving objects: %d%% (%d/1000), %d KiB | 30.00 KiB/s\r' $((i%100)) $i $((i*15)) >&2
      sleep 0.2
    done`)
	e, run := putCloningWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Progress must be on the run while the clone is still going; once it is,
	// stop the clone the way a closed turn would.
	during := make(chan []domain.RunEvent, 1)
	go func() {
		for ctx.Err() == nil {
			if events := runEvents(e, run.ID, "workspace.clone."); len(events) >= 2 {
				view, _ := e.repo.RunView(run.ID)
				opening := view.LatestWorkspaceOpening()
				if opening == nil || !strings.Contains(opening.Output, "KiB/s") {
					time.Sleep(20 * time.Millisecond)
					continue
				}
				during <- events
				time.Sleep(300 * time.Millisecond) // let the counter redraw a few times
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		during <- nil
	}()

	err := e.MaterializeConversationWorkspace(ctx, run.ID)
	if err == nil {
		t.Fatal("clone outlived its deadline")
	}
	events := <-during
	if len(events) < 2 || events[0].Type != "workspace.clone.started" || events[1].Type != "workspace.clone.progress" ||
		!strings.Contains(events[1].Message, "克隆仓库 web：Receiving objects") || !strings.Contains(events[1].Message, "KiB/s") {
		t.Fatalf("no visible progress while cloning: %+v", events)
	}
	after := runEvents(e, run.ID, "workspace.clone.")
	if last := after[len(after)-1]; last.Type != "workspace.clone.failed" || !strings.Contains(last.Message, "克隆仓库 web 失败") {
		t.Fatalf("failure not recorded on the run: %+v", last)
	}
	// Redrawn counters collapse to the newest, so the error stays one line of
	// progress instead of every redraw.
	if n := strings.Count(err.Error(), "Receiving objects"); n != 1 {
		t.Fatalf("error repeats the progress counter %d times: %v", n, err)
	}
	view, _ := e.repo.RunView(run.ID)
	if opening := view.LatestWorkspaceOpening(); opening == nil || opening.State != "failed" || opening.Phase != "clone" {
		t.Fatalf("opening failure lost: %+v", opening)
	}
}

func TestMaterializeConversationWorkspaceStopsAStalledClone(t *testing.T) {
	t.Setenv(gitwork.CloneStallEnv, "2s")
	installFakeClone(t, `    echo "Cloning into '.'..." >&2
    exec sleep 60`)
	e, run := putCloningWorkspace(t)
	started := time.Now()
	err := e.MaterializeConversationWorkspace(context.Background(), run.ID)
	// One stall window, not one per retry: retrying the same clone on the
	// same network would only repeat the wait. The window leaves a loaded CI
	// host time to start the fake git and print its first line.
	if elapsed := time.Since(started); elapsed > 3500*time.Millisecond {
		t.Fatalf("stalled clone held the open for %s", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "没有任何输出") || !strings.Contains(err.Error(), gitwork.CloneStallEnv) {
		t.Fatalf("stall not explained: %v", err)
	}
	events := runEvents(e, run.ID, "workspace.clone.failed")
	if len(events) == 0 || !strings.Contains(events[len(events)-1].Message, "Cloning into") {
		t.Fatalf("failed event lacks git's last output: %+v", events)
	}
}
