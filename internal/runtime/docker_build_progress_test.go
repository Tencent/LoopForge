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
	script := "#!/bin/sh\ncase \"$1\" in\n  inspect|image) exit 1 ;;\n  build)\n" + buildScript + "\n  ;;\nesac\nexit 0\n"
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
