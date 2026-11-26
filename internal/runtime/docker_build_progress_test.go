package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/procwatch"
)

// installFakeDockerBuild makes the image missing and runs buildScript for
// `docker build`.
func installFakeDockerBuild(t *testing.T, buildScript string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  inspect) echo \"No such container\" >&2; exit 1 ;;\n  image) exit 1 ;;\n  build)\n" + buildScript + "\n  ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func slowDockerfile() domain.DockerfileDefinition {
	return domain.DockerfileDefinition{ID: "slow", Name: "backend", Content: "FROM debian:bookworm\nRUN apt-get update\n"}
}

type buildEvents struct {
	mu     sync.Mutex
	events []procwatch.Event
}

func (b *buildEvents) observe(e procwatch.Event) {
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
}

func (b *buildEvents) snapshot() []procwatch.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]procwatch.Event(nil), b.events...)
}

// Issue #124: a slow docker build on the first command streamed nothing and,
// when killed, put every line it had printed into the error.
func TestDockerBuildReportsProgressAndKeepsOnlyTheTail(t *testing.T) {
	installFakeDockerBuild(t, `    i=0
    while :; do
      i=$((i+1))
      echo "#5 [2/3] RUN apt-get update: Get:$i http://deb.debian.org/debian bookworm InRelease 20.0 kB/s"
      sleep 0.05
    done`)
	sb := &dockerSandbox{id: "slow-build", name: "slow-build-box", workspace: t.TempDir(), dockerfile: slowDockerfile()}
	rec := &buildEvents{}
	ctx, cancel := context.WithTimeout(procwatch.WithObserver(context.Background(), rec.observe), 30*time.Second)
	defer cancel()
	// Progress must be visible while the build is still running; once it is,
	// stop the build the way a closed turn would.
	during := make(chan []procwatch.Event, 1)
	go func() {
		for ctx.Err() == nil {
			if events := rec.snapshot(); len(events) >= 2 {
				time.Sleep(500 * time.Millisecond) // let a few more lines pile up for the tail check
				during <- events
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		during <- nil
	}()

	_, err := sb.Exec(ctx, CommandSpec{Argv: []string{"true"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "docker build") {
		t.Fatalf("err = %v", err)
	}
	events := <-during
	if len(events) < 2 || events[0].Stage != "image" || events[0].Kind != procwatch.Started || events[1].Kind != procwatch.Progress ||
		!strings.Contains(events[1].Message, "构建沙箱镜像 backend：#5 [2/3] RUN apt-get update") {
		t.Fatalf("no visible progress while building: %+v", events)
	}
	// More lines were printed than the error keeps; it carries the last 20 at most.
	if n := strings.Count(err.Error(), "RUN apt-get update"); n == 0 || n > 20 {
		t.Fatalf("error carries %d build lines", n)
	}
	if last := rec.snapshot(); last[len(last)-1].Kind != procwatch.Failed {
		t.Fatalf("failure not reported: %+v", last[len(last)-1])
	}
}

func TestDockerBuildStopsWhenTheBuildStallsOrRunsTooLong(t *testing.T) {
	t.Run("stall", func(t *testing.T) {
		t.Setenv(dockerBuildStallEnv, "300ms")
		installFakeDockerBuild(t, `    echo "#5 [2/3] RUN apt-get update"
    exec sleep 60`)
		sb := &dockerSandbox{id: "stalled-build", name: "stalled-build-box", workspace: t.TempDir(), dockerfile: slowDockerfile()}
		started := time.Now()
		_, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"true"}}, nil)
		if time.Since(started) > 5*time.Second || err == nil || !procwatch.Stopped(err) || !strings.Contains(err.Error(), dockerBuildStallEnv) {
			t.Fatalf("after %s: %v", time.Since(started), err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		t.Setenv(dockerBuildTimeoutEnv, "300ms")
		installFakeDockerBuild(t, `    while :; do echo step; sleep 0.05; done`)
		sb := &dockerSandbox{id: "long-build", name: "long-build-box", workspace: t.TempDir(), dockerfile: slowDockerfile()}
		_, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{"true"}}, nil)
		if err == nil || !procwatch.Stopped(err) || !strings.Contains(err.Error(), dockerBuildTimeoutEnv) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDockerBuildReportsStepsBeforeQuietBuildCompletes(t *testing.T) {
	t.Setenv(dockerBuildStallEnv, "")
	t.Setenv(dockerBuildTimeoutEnv, "")
	if watch := dockerBuildWatch(slowDockerfile()); watch.Stall != 0 || watch.Timeout != 0 {
		t.Fatalf("default build limits: %+v", watch)
	}
	gate := filepath.Join(t.TempDir(), "continue")
	t.Setenv("TEST_DOCKER_BUILD_CONTINUE", gate)
	installFakeDockerBuild(t, `
		printf '%s\n' \
		  '#1 [internal] load metadata for private-registry' \
		  '#4 [compile 1/2] FROM private-registry/base:latest' \
		  '#5 [compile 2/2] RUN hidden-arguments' \
		  '#5 0.1 hidden-output' \
		  '#6 [package 2/2] COPY --from=compile hidden-arguments'
		until [ -f "$TEST_DOCKER_BUILD_CONTINUE" ]; do sleep 0.05; done
		printf '%s\n' '#3 exporting to image'
	`)
	progress := make(chan domain.WorkspaceImageBuildProgress, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = WithDockerBuildProgress(ctx, func(update domain.WorkspaceImageBuildProgress) { progress <- update })
	done := make(chan error, 1)
	workspace := t.TempDir()
	go func() {
		_, err := ensureDockerImage(ctx, workspace, slowDockerfile())
		done <- err
	}()
	next := func() domain.WorkspaceImageBuildProgress {
		t.Helper()
		select {
		case update := <-progress:
			return update
		case <-ctx.Done():
			t.Fatal("build progress was not reported")
			return domain.WorkspaceImageBuildProgress{}
		}
	}
	started := next()
	if started.ID == "" || started.Step != "启动构建" || started.Done {
		t.Fatalf("start = %+v", started)
	}
	for _, step := range []string{
		"读取基础镜像信息",
		"准备 Dockerfile 阶段 compile 的基础镜像",
		"执行 Dockerfile 阶段 compile 的第 2/2 步",
		"执行 Dockerfile 阶段 package 的第 2/2 步",
	} {
		if update := next(); update.ID != started.ID || update.Step != step || update.Done {
			t.Fatalf("progress = %+v, want %q", update, step)
		}
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if update := next(); update.Step != "导出镜像" || update.Done {
		t.Fatalf("export = %+v", update)
	}
	if update := next(); update.ID != started.ID || !update.Done || update.Failed {
		t.Fatalf("completion = %+v", update)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
