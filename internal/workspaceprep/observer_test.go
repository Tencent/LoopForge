package workspaceprep

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/procwatch"
)

func TestObserverPreservesLifecycleAndFinalTail(t *testing.T) {
	var events []procwatch.Event
	var updates []domain.WorkspaceOpening
	ctx := procwatch.WithObserver(context.Background(), func(e procwatch.Event) { events = append(events, e) })
	ctx = WithSink(ctx, func(p domain.WorkspaceOpening) { updates = append(updates, p) })
	err := Do(ctx, "build", "正在构建镜像", func(ctx context.Context) error {
		_, err := procwatch.Run(Observe(ctx), func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "printf 'first line\\nlast line'")
		}, procwatch.Options{Stage: "image", Label: "构建镜像"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Kind != procwatch.Started || events[1].Kind != procwatch.Progress || events[2].Kind != procwatch.Completed {
		t.Fatalf("existing observer was replaced: %+v", events)
	}
	last := updates[len(updates)-1]
	if last.Output != "first line\nlast line" || last.Phase != "build" || last.Message != "正在构建镜像" || last.State != "running" {
		t.Fatalf("lost final tail or marked entire workspace ready too soon: %+v", last)
	}
	Result(ctx, nil)
	if updates[len(updates)-1].State != "ready" {
		t.Fatal("successful opening was not marked ready")
	}
}

func TestObserverKeepsStallDetection(t *testing.T) {
	var updates []domain.WorkspaceOpening
	ctx := WithSink(context.Background(), func(p domain.WorkspaceOpening) { updates = append(updates, p) })
	err := Do(ctx, "clone", "正在克隆仓库", func(ctx context.Context) error {
		_, err := procwatch.Run(Observe(ctx), func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "echo connecting; sleep 30 & wait")
		}, procwatch.Options{Stage: "clone", Label: "克隆仓库", Stall: 250 * time.Millisecond})
		return err
	})
	Result(ctx, err)
	if !procwatch.Stopped(err) || !strings.Contains(err.Error(), "没有任何输出") {
		t.Fatalf("stall lost through phase wrapper: %v", err)
	}
	if len(updates) < 3 || updates[1].Output != "connecting" || updates[len(updates)-1].State != "failed" {
		t.Fatalf("stall progress/failure not reported: %+v", updates)
	}
}
